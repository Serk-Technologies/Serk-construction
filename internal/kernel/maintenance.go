// Fleet maintenance (FLT-03; migration 0056, ADR 0023).
//
// A work order is prepared, authorised by a plant manager, reported complete
// by plant staff and closed by a certifier who did not report it. A machine
// that has been under maintenance returns to service only after a work order
// for it has been closed (the database enforces that on the machine itself).
// Parts are ordinary stock issues linked to the work order; preventive plans
// say what is due from the meter and the calendar. Nothing here posts.
package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/fleet"
)

const (
	woType  = "maintenance_work_order"
	woTable = "maintenance_work_orders"
)

// WorkOrderInput prepares a maintenance work order.
type WorkOrderInput struct {
	EquipmentID string `json:"equipment_id"`
	PlanID      string `json:"plan_id,omitempty"` // a preventive plan this order carries out
	Kind        string `json:"kind"`              // PREVENTIVE | CORRECTIVE | BREAKDOWN | INSPECTION
	Description string `json:"description"`
}

// workOrderPerm is the permission under which p prepares or submits a work
// order: plant staff for any kind; site staff who operate equipment only for
// a breakdown, and only on a machine at one of their projects.
func (s *Service) workOrderPerm(p auth.Principal) auth.Permission {
	if s.authz.Has(p, auth.PermFleetManage) {
		return auth.PermFleetManage
	}
	return auth.PermFleetOperate
}

// CreateWorkOrder prepares a DRAFT work order.
func (s *Service) CreateWorkOrder(ctx context.Context, p auth.Principal, in WorkOrderInput) (string, error) {
	in.Kind, in.Description = strings.ToUpper(strings.TrimSpace(in.Kind)), strings.TrimSpace(in.Description)
	if !oneOf(in.Kind, "PREVENTIVE", "CORRECTIVE", "BREAKDOWN", "INSPECTION") || len(in.Description) < 10 {
		return "", fmt.Errorf("%w: a work order states its kind (PREVENTIVE, CORRECTIVE, BREAKDOWN or INSPECTION) and what is wrong or to be done (10+ characters)", ErrInvalidInput)
	}
	if in.PlanID != "" && in.Kind != "PREVENTIVE" {
		return "", fmt.Errorf("%w: only a preventive work order carries out a plan", ErrInvalidInput)
	}
	perm := s.workOrderPerm(p)
	if perm == auth.PermFleetOperate && in.Kind != "BREAKDOWN" {
		if err := s.authz.Require(p, auth.PermFleetManage, p.TenantID); err != nil {
			return "", err
		}
	}
	var id string
	err := s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		var project *string
		if err := tx.QueryRow(ctx, `SELECT current_project_id::text FROM equipment WHERE id = $1::uuid`, in.EquipmentID).Scan(&project); err != nil {
			return mapNoRows(err)
		}
		if sc := s.authz.ScopeOf(p); !sc.TenantWide && (project == nil || !contains(sc.Projects, *project)) {
			return ErrNotFound // site staff report breakdowns of machines at their own projects
		}
		num, err := postgres.NextNumber(ctx, tx, "MWO")
		if err != nil {
			return err
		}
		var plan any
		if in.PlanID != "" {
			plan = in.PlanID
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO maintenance_work_orders (equipment_id, plan_id, number, kind, description, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6) RETURNING id::text`, in.EquipmentID, plan, num, in.Kind, in.Description, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.maintenance.create", woType, id, "maintenance.work_order_prepared", map[string]any{"id": id, "number": num, "equipment_id": in.EquipmentID, "kind": in.Kind})
	})
	return id, err
}

// SubmitWorkOrder submits the preparer's draft for authorisation.
func (s *Service) SubmitWorkOrder(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, s.workOrderPerm(p), woType, woTable, id, "fleet.maintenance.submit", "maintenance.work_order_submitted")
}

// DecideWorkOrder authorises the work or rejects it (fleet.approve, fresh MFA, not the preparer).
func (s *Service) DecideWorkOrder(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermFleetApprove, woType, woTable, id, "fleet.maintenance.decide", "work order", approve, reason, nil)
}

// CancelWorkOrder withdraws the preparer's own draft.
func (s *Service) CancelWorkOrder(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	perm := s.workOrderPerm(p)
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, woType, id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the preparer cancels a work order", auth.ErrForbidden)
		}
		if err := s.authorize(ctx, tx, p, "fleet.maintenance.cancel", woType, id, ""); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE maintenance_work_orders SET status = 'CANCELLED', decision_reason = $2 WHERE id = $1::uuid`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.maintenance.cancel", woType, id, "maintenance.work_order_cancelled", map[string]any{"id": id, "reason": reason})
	})
}

// CompleteWorkOrder reports an authorised work order as done (fleet.manage).
func (s *Service) CompleteWorkOrder(ctx context.Context, p auth.Principal, id, workDone string, labourHours decimal.Decimal) error {
	workDone = strings.TrimSpace(workDone)
	if len(workDone) < 10 || labourHours.IsNegative() || labourHours.GreaterThan(decimal.NewFromInt(100000)) {
		return fmt.Errorf("%w: a completion report states what was done (10+ characters) and the labour hours (0 or more)", ErrInvalidInput)
	}
	return s.p2pTx(ctx, p, auth.PermFleetManage, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermFleetManage, woType, id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "APPROVED"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "fleet.maintenance.complete", woType, id, ""); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE maintenance_work_orders SET status = 'COMPLETED', work_done = $2, labour_hours = $3, completed_by = $4 WHERE id = $1::uuid`,
			id, workDone, labourHours.Round(2), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.maintenance.complete", woType, id, "maintenance.work_order_completed", map[string]any{"id": id, "labour_hours": labourHours.String()})
	})
}

// CloseWorkOrder is the certifier's acceptance of completed work (accept) or
// its return to the workshop with a reason. fleet.release, fresh MFA, and not
// the person who reported the work complete.
func (s *Service) CloseWorkOrder(ctx context.Context, p auth.Principal, id string, accept bool, reason string) error {
	if !accept {
		if err := requireReason(reason); err != nil {
			return err
		}
	}
	return s.p2pTx(ctx, p, auth.PermFleetRelease, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermFleetRelease, woType, id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "COMPLETED"); err != nil {
			return err
		}
		var row struct {
			CompletedBy string `json:"completed_by"`
		}
		if err := json.Unmarshal(d.Detail, &row); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, row.CompletedBy, "completed work"); err != nil {
			return err
		}
		if err := s.authz.RequireStepUp(p); err != nil {
			return err
		}
		outcome, event := "CLOSED", "maintenance.work_order_closed"
		if !accept {
			outcome, event = "SENT_BACK", "maintenance.work_order_sent_back"
		}
		if err := s.authorize(ctx, tx, p, "fleet.maintenance.close", woType, id, outcome); err != nil {
			return err
		}
		if accept {
			err = postgres.Exec1(ctx, tx, `UPDATE maintenance_work_orders SET status = 'CLOSED', closed_by = $2 WHERE id = $1::uuid`, id, p.UserID)
		} else {
			err = postgres.Exec1(ctx, tx, `UPDATE maintenance_work_orders SET status = 'APPROVED', decision_reason = $3, returned_by = $2,
				work_done = NULL, labour_hours = NULL, completed_by = NULL, completed_at = NULL WHERE id = $1::uuid`, id, p.UserID, strings.TrimSpace(reason))
		}
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.maintenance.close", woType, id, event, map[string]any{"id": id, "outcome": outcome, "reason": reason})
	})
}

// WorkOrderPartInput requests a part for a work order from stock.
type WorkOrderPartInput struct {
	WarehouseID      string          `json:"warehouse_id"`
	StockItemID      string          `json:"stock_item_id"`
	ProjectID        string          `json:"project_id,omitempty"` // only for a machine that is not at a project
	CostCodeID       string          `json:"cost_code_id"`
	ExpenseAccountID string          `json:"expense_account_id"`
	Quantity         decimal.Decimal `json:"quantity"`
	Note             string          `json:"note,omitempty"`
}

// WorkOrderPart is a part requested for a work order with the state of its stock issue.
type WorkOrderPart struct {
	StockIssueID string           `json:"stock_issue_id"`
	IssueNumber  string           `json:"issue_number"`
	IssueStatus  string           `json:"issue_status"`
	ItemCode     string           `json:"item_code"`
	ItemName     string           `json:"item_name"`
	Quantity     decimal.Decimal  `json:"quantity"`
	Unit         string           `json:"unit"`
	Amount       *decimal.Decimal `json:"amount"` // from the stock ledger at approval
	Currency     string           `json:"currency"`
}

// RequestWorkOrderPart prepares a stock issue for a part and links it to an
// authorised work order, in one transaction. The issue is approved by a second
// person like any other; a machine at a project charges that project.
func (s *Service) RequestWorkOrderPart(ctx context.Context, p auth.Principal, workOrderID string, in WorkOrderPartInput) (string, error) {
	if !in.Quantity.IsPositive() {
		return "", fmt.Errorf("%w: quantity must be above 0", ErrInvalidInput)
	}
	var issueID string
	err := s.p2pTx(ctx, p, auth.PermStockIssue, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		var project *string
		if err := tx.QueryRow(ctx, `SELECT w.status, e.current_project_id::text FROM maintenance_work_orders w JOIN equipment e ON e.id = w.equipment_id WHERE w.id = $1::uuid`, workOrderID).Scan(&status, &project); err != nil {
			return mapNoRows(err)
		}
		if status != "APPROVED" {
			return fmt.Errorf("%w: parts are requested for an authorised work order that is not yet reported complete (this one is %s)", ErrInvalidInput, status)
		}
		charge := in.ProjectID
		if project != nil {
			charge = *project
		}
		if charge == "" {
			return fmt.Errorf("%w: the machine is not at a project; name the project the part is charged to", ErrInvalidInput)
		}
		if err := s.authz.RequireProject(p, auth.PermStockIssue, p.TenantID, charge); err != nil {
			return ErrNotFound
		}
		var num string
		var err error
		issueID, num, err = s.insertStockIssue(ctx, tx, p, StockIssueInput{WarehouseID: in.WarehouseID, StockItemID: in.StockItemID, ProjectID: charge, CostCodeID: in.CostCodeID,
			ExpenseAccountID: in.ExpenseAccountID, Quantity: in.Quantity, IssueDate: time.Now().UTC().Truncate(24 * time.Hour), Note: in.Note})
		if err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "fleet.maintenance.part.create", "stock_issue", issueID, workOrderID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO maintenance_parts (work_order_id, stock_issue_id, created_by) VALUES ($1::uuid, $2::uuid, $3)`, workOrderID, issueID, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.maintenance.part.create", woType, workOrderID, "maintenance.part_requested", map[string]any{"work_order_id": workOrderID, "stock_issue_id": issueID, "number": num})
	})
	return issueID, err
}

// WorkOrderParts lists the parts requested for a work order.
func (s *Service) WorkOrderParts(ctx context.Context, p auth.Principal, workOrderID string) ([]WorkOrderPart, error) {
	out := []WorkOrderPart{}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, woType, workOrderID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT i.id::text, i.number, i.status, s.code, s.name, i.quantity, s.unit, i.amount, i.currency
			FROM maintenance_parts mp JOIN stock_issues i ON i.id = mp.stock_issue_id JOIN stock_items s ON s.id = i.stock_item_id
			WHERE mp.work_order_id = $1::uuid ORDER BY mp.created_at LIMIT 500`, workOrderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x WorkOrderPart
			if err := rows.Scan(&x.StockIssueID, &x.IssueNumber, &x.IssueStatus, &x.ItemCode, &x.ItemName, &x.Quantity, &x.Unit, &x.Amount, &x.Currency); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// MaintenancePlanInput is a preventive maintenance plan of one machine.
type MaintenancePlanInput struct {
	EquipmentID string           `json:"equipment_id"`
	Title       string           `json:"title"`
	EveryUnits  *decimal.Decimal `json:"every_units,omitempty"` // metered hours or km between services
	EveryDays   *int             `json:"every_days,omitempty"`
}

// CreateMaintenancePlan records a plan; its interval counts from the machine's present meter and today.
func (s *Service) CreateMaintenancePlan(ctx context.Context, p auth.Principal, in MaintenancePlanInput) (string, error) {
	in.Title = strings.TrimSpace(in.Title)
	if len(in.Title) < 3 || (in.EveryUnits == nil && in.EveryDays == nil) || (in.EveryUnits != nil && !in.EveryUnits.IsPositive()) || (in.EveryDays != nil && (*in.EveryDays < 1 || *in.EveryDays > 3650)) {
		return "", fmt.Errorf("%w: a plan has a title and an interval in metered units, in days (1 to 3650), or both", ErrInvalidInput)
	}
	var id string
	err := s.p2pTx(ctx, p, auth.PermFleetManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "equipment", in.EquipmentID, false); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "fleet.maintenance.plan.create", "equipment", in.EquipmentID, in.Title); err != nil {
			return err
		}
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO maintenance_plans (equipment_id, title, every_units, every_days, created_by) VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`,
			in.EquipmentID, in.Title, in.EveryUnits, in.EveryDays, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.maintenance.plan.create", "equipment", in.EquipmentID, "maintenance.plan_created", map[string]any{"plan_id": id, "equipment_id": in.EquipmentID, "title": in.Title})
	})
	return id, err
}

// DuePlan is a preventive plan with how close it is to its next service.
type DuePlan struct {
	PlanID        string           `json:"plan_id"`
	EquipmentID   string           `json:"equipment_id"`
	Number        string           `json:"number"`
	Name          string           `json:"name"`
	Title         string           `json:"title"`
	EveryUnits    *decimal.Decimal `json:"every_units"`
	EveryDays     *int             `json:"every_days"`
	LastDoneMeter decimal.Decimal  `json:"last_done_meter"`
	LastDoneOn    string           `json:"last_done_on"`
	Meter         decimal.Decimal  `json:"meter"`
	UnitsLeft     *decimal.Decimal `json:"units_left"`
	DaysLeft      *int             `json:"days_left"`
	State         string           `json:"state"` // OK | DUE_SOON | OVERDUE
	OpenWorkOrder string           `json:"open_work_order,omitempty"`
}

// MaintenanceDue lists the preventive plans of the machines in the caller's
// scope, most urgent first. Site staff see the machines at their projects.
func (s *Service) MaintenanceDue(ctx context.Context, p auth.Principal) ([]DuePlan, error) {
	out := []DuePlan{}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		sc := s.authz.ScopeOf(p)
		rows, err := tx.Query(ctx, `SELECT d.plan_id::text, d.equipment_id::text, e.number, e.name, d.title, d.every_units, d.every_days, d.last_done_meter, d.last_done_on::text,
				d.meter, d.units_left, d.days_left, COALESCE(d.open_work_order::text, '')
			FROM maintenance_due($1::uuid[], $2) d JOIN equipment e ON e.id = d.equipment_id ORDER BY e.number COLLATE "C", d.title LIMIT 2000`, sc.Projects, sc.TenantWide)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x DuePlan
			if err := rows.Scan(&x.PlanID, &x.EquipmentID, &x.Number, &x.Name, &x.Title, &x.EveryUnits, &x.EveryDays, &x.LastDoneMeter, &x.LastDoneOn, &x.Meter, &x.UnitsLeft, &x.DaysLeft, &x.OpenWorkOrder); err != nil {
				return err
			}
			x.State = fleet.DueState(x.UnitsLeft, x.EveryUnits, x.DaysLeft, x.EveryDays)
			out = append(out, x)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	rank := map[string]int{fleet.DueOverdue: 0, fleet.DueSoon: 1, fleet.DueOK: 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].State] < rank[out[j].State] })
	return out, nil
}

// EquipmentWorkOrder is one line of a machine's maintenance history.
type EquipmentWorkOrder struct {
	ID          string           `json:"id"`
	Number      string           `json:"number"`
	Kind        string           `json:"kind"`
	Status      string           `json:"status"`
	Description string           `json:"description"`
	OpenedOn    string           `json:"opened_on"`
	ClosedOn    string           `json:"closed_on,omitempty"`
	LabourHours *decimal.Decimal `json:"labour_hours"`
	Parts       int              `json:"parts"`
	PartsCost   decimal.Decimal  `json:"parts_cost"` // approved stock issues, from the stock ledger
}

// EquipmentMaintenance is a machine's work orders (latest first, at most 200)
// and the hours it has spent out of service in each state, from its lifecycle history.
type EquipmentMaintenance struct {
	WorkOrders    []EquipmentWorkOrder       `json:"work_orders"`
	DownHours     map[string]decimal.Decimal `json:"out_of_service_hours"`
	DownHoursNote string                     `json:"out_of_service_basis"`
}

// EquipmentMaintenanceOf returns the maintenance history of one machine.
func (s *Service) EquipmentMaintenanceOf(ctx context.Context, p auth.Principal, equipmentID string) (EquipmentMaintenance, error) {
	out := EquipmentMaintenance{WorkOrders: []EquipmentWorkOrder{}, DownHours: map[string]decimal.Decimal{},
		DownHoursNote: "Elapsed hours between the machine entering BREAKDOWN, UNDER_MAINTENANCE, UNDER_INSPECTION or SAFETY_HOLD and its next change of state (or now), from the lifecycle history, over the machine's whole life."}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "equipment", equipmentID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT w.id::text, w.number, w.kind, w.status, w.description, w.created_at::date::text, COALESCE(w.closed_at::date::text, ''), w.labour_hours,
				(SELECT count(*) FROM maintenance_parts mp WHERE mp.work_order_id = w.id),
				COALESCE((SELECT sum(i.amount) FROM maintenance_parts mp JOIN stock_issues i ON i.id = mp.stock_issue_id WHERE mp.work_order_id = w.id AND i.status = 'APPROVED'), 0)
			FROM maintenance_work_orders w WHERE w.equipment_id = $1::uuid ORDER BY w.created_at DESC LIMIT 200`, equipmentID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x EquipmentWorkOrder
			if err := rows.Scan(&x.ID, &x.Number, &x.Kind, &x.Status, &x.Description, &x.OpenedOn, &x.ClosedOn, &x.LabourHours, &x.Parts, &x.PartsCost); err != nil {
				rows.Close()
				return err
			}
			out.WorkOrders = append(out.WorkOrders, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT to_status, round((sum(extract(epoch FROM COALESCE(until, now()) - occurred_at)) / 3600)::numeric, 2)
			FROM (SELECT to_status, occurred_at, lead(occurred_at) OVER (ORDER BY id) AS until FROM equipment_events WHERE equipment_id = $1::uuid) e
			WHERE to_status IN ('BREAKDOWN', 'UNDER_MAINTENANCE', 'UNDER_INSPECTION', 'SAFETY_HOLD') GROUP BY to_status`, equipmentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var st string
			var h decimal.Decimal
			if err := rows.Scan(&st, &h); err != nil {
				return err
			}
			out.DownHours[st] = h
		}
		return rows.Err()
	})
	return out, err
}
