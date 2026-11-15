// Fleet fuel (FLT-02; migration 0055, ADR 0022).
//
// Fuel is ordinary stock: bought, received and counted through the existing
// stock ledger, with a tank or bowser as a warehouse. Filling a machine is a
// stock issue, prepared here together with the record of which machine took
// it; a second person approves the issue (the existing rule), which values it,
// removes the stock and generates the STK_ISSUE journal that carries the cost
// to the project. This file adds no quantity, value or posting of its own.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/fleet"
)

// FuelTolerance is the share of the larger figure by which dispensed, logged
// and expected fuel may differ before the difference is listed for
// explanation. It is a reporting threshold proposed by engineering, not a
// policy or a statutory figure; the reconciliation states it.
var FuelTolerance = decimal.RequireFromString("0.10")

// DesignateFuelItem marks an active stock item as fuel (fleet.approve; attested; permanent).
func (s *Service) DesignateFuelItem(ctx context.Context, p auth.Principal, stockItemID string) error {
	return s.p2pTx(ctx, p, auth.PermFleetApprove, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.authorize(ctx, tx, p, "fleet.fuel.item.designate", "stock_item", stockItemID, ""); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO fuel_items (stock_item_id, designated_by) VALUES ($1::uuid, $2)`, stockItemID, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.fuel.item.designate", "stock_item", stockItemID, "fuel.item_designated", map[string]any{"stock_item_id": stockItemID})
	})
}

// FuelItem is a stock item designated as fuel.
type FuelItem struct {
	StockItemID  string `json:"stock_item_id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Unit         string `json:"unit"`
	DesignatedBy string `json:"designated_by"`
}

// ListFuelItems returns the tenant's fuel items.
func (s *Service) ListFuelItems(ctx context.Context, p auth.Principal) ([]FuelItem, error) {
	out := []FuelItem{}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT f.stock_item_id::text, i.code, i.name, i.unit, f.designated_by FROM fuel_items f JOIN stock_items i ON i.id = f.stock_item_id ORDER BY i.code LIMIT 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x FuelItem
			if err := rows.Scan(&x.StockItemID, &x.Code, &x.Name, &x.Unit, &x.DesignatedBy); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// FuelStandardInput sets the expected litres per metered unit of a machine from a date.
type FuelStandardInput struct {
	EquipmentID   string          `json:"equipment_id"`
	LitresPerUnit decimal.Decimal `json:"litres_per_unit"`
	EffectiveFrom string          `json:"effective_from"`
	Basis         string          `json:"basis"` // where the figure comes from: maker's data, a measured trial, ...
}

// SetFuelStandard records a fuel standard (fleet.approve; attested; append-only).
func (s *Service) SetFuelStandard(ctx context.Context, p auth.Principal, in FuelStandardInput) (string, error) {
	from, err := time.Parse("2006-01-02", strings.TrimSpace(in.EffectiveFrom))
	if err != nil {
		return "", fmt.Errorf("%w: effective_from must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	rate := in.LitresPerUnit.Round(4)
	if !rate.IsPositive() || rate.GreaterThan(decimal.NewFromInt(10000)) || len(strings.TrimSpace(in.Basis)) < 5 {
		return "", fmt.Errorf("%w: a fuel standard states litres per metered unit (above 0) and what the figure is based on (5+ characters)", ErrInvalidInput)
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermFleetApprove, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "equipment", in.EquipmentID, false); err != nil {
			return err
		}
		// The attestation names the exact figure and date (the database compares them).
		if err := s.authorize(ctx, tx, p, "fleet.fuel.standard.set", "equipment", in.EquipmentID, rate.StringFixed(4)+"|"+from.Format("2006-01-02")); err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_fuel_standards (equipment_id, litres_per_unit, effective_from, basis, set_by)
			VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`, in.EquipmentID, rate, from, strings.TrimSpace(in.Basis), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.fuel.standard.set", "equipment", in.EquipmentID, "fuel.standard_set", map[string]any{"equipment_id": in.EquipmentID, "litres_per_unit": rate.String(), "effective_from": in.EffectiveFrom})
	})
	return id, err
}

// FuelDispenseInput is one filling of a machine from a tank or bowser.
type FuelDispenseInput struct {
	EquipmentID      string           `json:"equipment_id"`
	WarehouseID      string           `json:"warehouse_id"` // the tank or bowser
	StockItemID      string           `json:"stock_item_id"`
	CostCodeID       string           `json:"cost_code_id"`
	ExpenseAccountID string           `json:"expense_account_id"`
	Litres           decimal.Decimal  `json:"litres"`
	MeterReading     *decimal.Decimal `json:"meter_reading,omitempty"`
	Operator         string           `json:"operator"`
	DispensedOn      time.Time        `json:"-"`
	ClientRef        string           `json:"client_ref,omitempty"` // makes a retried request (offline device) record once
	Note             string           `json:"note,omitempty"`
}

// FuelDispense is a recorded filling with the state of its stock issue.
type FuelDispense struct {
	ID            string           `json:"id"`
	StockIssueID  string           `json:"stock_issue_id"`
	IssueNumber   string           `json:"issue_number"`
	IssueStatus   string           `json:"issue_status"`
	EquipmentID   string           `json:"equipment_id"`
	EquipmentNo   string           `json:"equipment_number"`
	ProjectID     string           `json:"project_id"`
	DispensedOn   string           `json:"dispensed_on"`
	Litres        decimal.Decimal  `json:"litres"`
	MeterReading  *decimal.Decimal `json:"meter_reading"`
	Operator      string           `json:"operator"`
	Amount        *decimal.Decimal `json:"amount"` // value at approval, from the stock ledger; null until approved
	Currency      string           `json:"currency"`
	CreatedBy     string           `json:"created_by"`
	AlreadyExists bool             `json:"already_recorded,omitempty"`
}

// DispenseFuel prepares the stock issue of a filling and records which machine
// took it, in one transaction. The machine's current project is the project
// charged. The issue is a DRAFT until a second person approves it
// (ApproveStockIssue): only then does stock leave the tank and cost reach the
// project. A repeated client_ref returns the first record.
func (s *Service) DispenseFuel(ctx context.Context, p auth.Principal, in FuelDispenseInput) (FuelDispense, error) {
	var out FuelDispense
	in.Litres = in.Litres.Round(4)
	in.Operator, in.ClientRef = strings.TrimSpace(in.Operator), strings.TrimSpace(in.ClientRef)
	if !in.Litres.IsPositive() || in.Operator == "" {
		return out, fmt.Errorf("%w: a fuel dispense states the litres (above 0) and the operator", ErrInvalidInput)
	}
	if in.MeterReading != nil && in.MeterReading.IsNegative() {
		return out, fmt.Errorf("%w: the meter reading cannot be negative", ErrInvalidInput)
	}
	if err := stockDate(in.DispensedOn); err != nil {
		return out, err
	}
	if err := s.authz.Require(p, auth.PermStockIssue, p.TenantID); err != nil {
		return out, err
	}
	var id string
	err := s.p2pTx(ctx, p, auth.PermStockIssue, func(ctx context.Context, tx pgx.Tx) error {
		if in.ClientRef != "" {
			err := tx.QueryRow(ctx, `SELECT id::text FROM fuel_dispenses WHERE created_by = $1 AND client_ref = $2`, p.UserID, in.ClientRef).Scan(&id)
			if err == nil {
				out.AlreadyExists = true
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var project *string
		if err := tx.QueryRow(ctx, `SELECT current_project_id::text FROM equipment WHERE id = $1::uuid`, in.EquipmentID).Scan(&project); err != nil {
			return mapNoRows(err)
		}
		if project == nil {
			return fmt.Errorf("%w: the equipment is not at a project; fuel is charged to the project the machine is working on", ErrInvalidInput)
		}
		// The caller must hold stock.issue on that project; otherwise the machine is not theirs to see.
		if err := s.authz.RequireProject(p, auth.PermStockIssue, p.TenantID, *project); err != nil {
			return ErrNotFound
		}
		var isFuel bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fuel_items WHERE stock_item_id = $1::uuid)`, in.StockItemID).Scan(&isFuel); err != nil {
			return err
		}
		if !isFuel {
			return fmt.Errorf("%w: that stock item is not designated as fuel (a plant manager designates fuel items)", ErrInvalidInput)
		}
		issueID, num, err := s.insertStockIssue(ctx, tx, p, StockIssueInput{WarehouseID: in.WarehouseID, StockItemID: in.StockItemID, ProjectID: *project,
			CostCodeID: in.CostCodeID, ExpenseAccountID: in.ExpenseAccountID, Quantity: in.Litres, IssueDate: in.DispensedOn, Note: in.Note})
		if err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "fleet.fuel.dispense.create", "stock_issue", issueID, in.EquipmentID); err != nil {
			return err
		}
		var ref any
		if in.ClientRef != "" {
			ref = in.ClientRef
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO fuel_dispenses (stock_issue_id, equipment_id, project_id, dispensed_on, litres, meter_serial, meter_reading, operator, client_ref, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, 0, $6, $7, $8, $9) RETURNING id::text`,
			issueID, in.EquipmentID, *project, in.DispensedOn, in.Litres, in.MeterReading, in.Operator, ref, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.fuel.dispense.create", "stock_issue", issueID, "fuel.dispensed", map[string]any{
			"fuel_dispense_id": id, "stock_issue_id": issueID, "number": num, "equipment_id": in.EquipmentID, "litres": in.Litres.String()})
	})
	if err != nil && in.ClientRef != "" {
		// Two deliveries of the same offline request can race; the loser's
		// transaction is rolled back whole (the unique index), and the record
		// that won is the answer for both.
		if prior, lerr := s.listFuelDispenses(ctx, p, `f.created_by = $1 AND f.client_ref = $2`, p.UserID, in.ClientRef); lerr == nil && len(prior) == 1 {
			prior[0].AlreadyExists = true
			return prior[0], nil
		}
	}
	if err != nil {
		return out, err
	}
	exists := out.AlreadyExists
	list, err := s.listFuelDispenses(ctx, p, `f.id = $1::uuid`, id)
	if err != nil {
		return out, err
	}
	if len(list) != 1 {
		return out, ErrNotFound
	}
	out = list[0]
	out.AlreadyExists = exists
	return out, nil
}

// ListFuelDispenses returns the latest fillings of one machine (or of the caller's projects when equipmentID is empty).
func (s *Service) ListFuelDispenses(ctx context.Context, p auth.Principal, equipmentID string) ([]FuelDispense, error) {
	if equipmentID == "" {
		return s.listFuelDispenses(ctx, p, `true`)
	}
	return s.listFuelDispenses(ctx, p, `f.equipment_id = $1::uuid`, equipmentID)
}

func (s *Service) listFuelDispenses(ctx context.Context, p auth.Principal, where string, args ...any) ([]FuelDispense, error) {
	out := []FuelDispense{}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		sc := s.authz.ScopeOf(p)
		n := len(args)
		args = append(args, sc.TenantWide, sc.Projects)
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT f.id::text, f.stock_issue_id::text, i.number, i.status, f.equipment_id::text, e.number, f.project_id::text, f.dispensed_on::text,
				f.litres, f.meter_reading, f.operator, i.amount, i.currency, f.created_by
			FROM fuel_dispenses f JOIN stock_issues i ON i.id = f.stock_issue_id JOIN equipment e ON e.id = f.equipment_id
			WHERE %s AND ($%d OR f.project_id = ANY($%d::uuid[])) ORDER BY f.dispensed_on DESC, f.created_at DESC LIMIT 200`, where, n+1, n+2), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x FuelDispense
			if err := rows.Scan(&x.ID, &x.StockIssueID, &x.IssueNumber, &x.IssueStatus, &x.EquipmentID, &x.EquipmentNo, &x.ProjectID, &x.DispensedOn,
				&x.Litres, &x.MeterReading, &x.Operator, &x.Amount, &x.Currency, &x.CreatedBy); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// FuelLine is the fuel reconciliation of one machine for a period.
type FuelLine struct {
	EquipmentID     string           `json:"equipment_id"`
	Number          string           `json:"number"`
	Name            string           `json:"name"`
	MeterKind       string           `json:"meter_kind"`
	Dispensed       decimal.Decimal  `json:"dispensed_litres"` // approved stock issues into the machine
	Dispenses       int              `json:"dispenses"`
	Pending         decimal.Decimal  `json:"pending_litres"` // prepared, not yet approved: still in the tank
	Cost            decimal.Decimal  `json:"fuel_cost"`      // value of the approved issues, from the stock ledger
	Logged          decimal.Decimal  `json:"logged_litres"`  // approved daily logs
	MeteredUnits    decimal.Decimal  `json:"metered_units"`
	LitresPerUnit   *decimal.Decimal `json:"standard_litres_per_unit"`
	StandardFrom    string           `json:"standard_from,omitempty"`
	Expected        *decimal.Decimal `json:"expected_litres"`
	ActualPerUnit   *decimal.Decimal `json:"actual_litres_per_unit"`
	DispensedLogged decimal.Decimal  `json:"dispensed_minus_logged"`
	Flags           []fleet.FuelFlag `json:"flags"`
}

// FuelReconciliation is the comparison of dispensed, logged and expected fuel.
type FuelReconciliation struct {
	From      string     `json:"from"`
	To        string     `json:"to"`
	Tolerance string     `json:"tolerance"`
	Basis     string     `json:"basis"`
	Lines     []FuelLine `json:"lines"`
	Flagged   int        `json:"flagged"`
}

// FuelReconciliationOf compares, per machine, the fuel issued from stock, the
// fuel written in approved daily logs and the fuel the standard predicts. A
// flag is a question for a person; nothing here decides or posts anything.
func (s *Service) FuelReconciliationOf(ctx context.Context, p auth.Principal, from, to, projectID string) (FuelReconciliation, error) {
	out := FuelReconciliation{From: from, To: to, Tolerance: FuelTolerance.String(), Lines: []FuelLine{},
		Basis: "Dispensed: approved stock issues of fuel items into the machine, dated in the period. Pending: issues prepared and not yet approved. Cost: the stock ledger's value of the approved issues. " +
			"Logged: fuel written in approved daily logs. Metered units: meter movement in those logs. Expected: metered units x the fuel standard in force at the end of the period. " +
			"A line is flagged when two of these differ by more than the tolerance (a reporting threshold, not a policy) and by more than one litre. A flag asks for an explanation; it is not a finding."}
	a, err := time.Parse("2006-01-02", from)
	if err != nil {
		return out, fmt.Errorf("%w: from must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	b, err := time.Parse("2006-01-02", to)
	if err != nil || b.Before(a) || b.Sub(a) > 366*24*time.Hour {
		return out, fmt.Errorf("%w: to must be a date on or after from, at most 366 days later", ErrInvalidInput)
	}
	err = s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		sc := s.authz.ScopeOf(p)
		var projects any // nil = every project (tenant-wide readers only)
		switch {
		case projectID != "":
			if !sc.TenantWide && !contains(sc.Projects, projectID) {
				return ErrNotFound
			}
			projects = []string{projectID}
		case !sc.TenantWide:
			projects = sc.Projects
		}
		rows, err := tx.Query(ctx, `SELECT e.id::text, e.number, e.name, e.meter_kind, s.dispensed_litres, s.dispenses, s.pending_litres, s.fuel_cost, s.logged_litres, s.metered_units,
				s.litres_per_unit, COALESCE(s.standard_from::text, '')
			FROM equipment_fuel_summary($1::date, $2::date, $3::uuid[]) s JOIN equipment e ON e.id = s.equipment_id
			ORDER BY e.number COLLATE "C" LIMIT 2000`, a, b, projects)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l FuelLine
			if err := rows.Scan(&l.EquipmentID, &l.Number, &l.Name, &l.MeterKind, &l.Dispensed, &l.Dispenses, &l.Pending, &l.Cost, &l.Logged, &l.MeteredUnits, &l.LitresPerUnit, &l.StandardFrom); err != nil {
				return err
			}
			f := fleet.FuelFigures{Dispensed: l.Dispensed, Logged: l.Logged, MeteredUnits: l.MeteredUnits, LitresPerUnit: l.LitresPerUnit}
			l.Expected = f.Expected()
			if l.MeteredUnits.IsPositive() {
				v := l.Dispensed.Div(l.MeteredUnits).Round(4)
				l.ActualPerUnit = &v
			}
			l.DispensedLogged = l.Dispensed.Sub(l.Logged)
			l.Flags = fleet.FuelFlags(f, FuelTolerance)
			if len(l.Flags) > 0 {
				out.Flagged++
			}
			out.Lines = append(out.Lines, l)
		}
		return rows.Err()
	})
	return out, err
}
