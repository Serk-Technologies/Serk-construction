// Workforce, equipment and HSE in the field (WP-3.3, WP-3.4, WP-3.7; SRS HCM-02,
// HCM-03, HCM-07, EQP-01..07, HSE-01..06). Migration: 0064_workforce_field.sql.
//
// Timesheets and equipment usage are recorded by the site and approved by a
// different person; approved hours feed payroll, approved usage feeds the
// monthly hire charge journal. HSE records incidents, corrective actions,
// permits to work, inductions, toolbox talks and inspections, with the
// separation of duties enforced by the database.
package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
	"github.com/serk-group/serk-construction/pkg/money"
)

// rowsAsMaps runs a query and returns every row as a JSON-shaped map.
func rowsAsMaps(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (`+sql+`) r`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		m, err := jsonMap(raw)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// scopeArgs are the arguments of the project filter "$1 OR project_id = ANY($2)".
func (s *Service) scopeArgs(p auth.Principal) (bool, []string) {
	sc := s.authz.ScopeOf(p)
	if sc.Projects == nil {
		return sc.TenantWide, []string{}
	}
	return sc.TenantWide, sc.Projects
}

// decideSimple approves or rejects a SUBMITTED document by a different
// person, without a fresh-MFA requirement (field approvals).
func (s *Service) decideSimple(ctx context.Context, p auth.Principal, perm auth.Permission, docType, table, id, action, what string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	}
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, docType, id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, what); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, action, docType, id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE `+table+` SET status = $4, approved_by = $2, decision_reason = NULLIF($3, '') WHERE id = $1::uuid`, id, p.UserID, reason, outcome); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, docType, id, docType+"."+strings.ToLower(outcome), map[string]any{"id": id, "reason": reason})
	})
}

// cancelDraft cancels a DRAFT document of its author.
func (s *Service) cancelDraft(ctx context.Context, p auth.Principal, perm auth.Permission, docType, table, id string) error {
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, docType, id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the author cancels a draft", auth.ErrForbidden)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE `+table+` SET status = 'CANCELLED' WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, docType+".cancel", docType, id, docType+".cancelled", map[string]any{"id": id})
	})
}

// ------------------------------------------------------------ timesheets ---

// TimesheetLineIn is one person's hours.
type TimesheetLineIn struct {
	EmployeeID  string          `json:"employee_id"`
	CostCodeID  string          `json:"cost_code_id"`
	HoursNormal decimal.Decimal `json:"hours_normal"`
	HoursOT     decimal.Decimal `json:"hours_ot"`
}

// TimesheetIn records a project's day.
type TimesheetIn struct {
	ProjectID string            `json:"project_id"`
	WorkDate  string            `json:"work_date"`
	Notes     string            `json:"notes"`
	Lines     []TimesheetLineIn `json:"lines"`
}

// CreateTimesheet records a DRAFT timesheet.
func (s *Service) CreateTimesheet(ctx context.Context, p auth.Principal, in TimesheetIn) (string, error) {
	day, err := parseDay("work_date", in.WorkDate)
	if err != nil {
		return "", err
	}
	if len(in.Lines) == 0 {
		return "", fmt.Errorf("%w: a timesheet has at least one person", ErrInvalidInput)
	}
	if err := s.authz.RequireProject(p, auth.PermTimesheetRecord, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "TS")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO timesheets (project_id, work_date, number, notes, created_by) VALUES ($1::uuid, $2, $3, NULLIF($4, ''), $5) RETURNING id::text`,
			in.ProjectID, day, num, strings.TrimSpace(in.Notes), p.UserID)
		if err != nil {
			return err
		}
		for _, l := range in.Lines {
			if _, err := tx.Exec(ctx, `INSERT INTO timesheet_lines (timesheet_id, employee_id, cost_code_id, hours_normal, hours_ot) VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5)`,
				id, l.EmployeeID, l.CostCodeID, l.HoursNormal, l.HoursOT); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "timesheet.create", "timesheet", id, "timesheet.created", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// SubmitTimesheet submits a draft (its recorder).
func (s *Service) SubmitTimesheet(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermTimesheetRecord, "timesheet", "timesheets", id, "timesheet.submit", "timesheet.submitted")
}

// DecideTimesheet approves or rejects (a different person; project scope).
func (s *Service) DecideTimesheet(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideSimple(ctx, p, auth.PermTimesheetApprove, "timesheet", "timesheets", id, "timesheet.decide", "timesheet", approve, reason)
}

// CancelTimesheet cancels a draft.
func (s *Service) CancelTimesheet(ctx context.Context, p auth.Principal, id string) error {
	return s.cancelDraft(ctx, p, auth.PermTimesheetRecord, "timesheet", "timesheets", id)
}

// Roster lists the people a project's site may record hours or equipment
// for: number, name and trade only (no pay data).
func (s *Service) Roster(ctx context.Context, p auth.Principal, project string) ([]map[string]any, error) {
	if !s.authz.Has(p, auth.PermTimesheetRecord) && !s.authz.Has(p, auth.PermTimesheetApprove) && !s.authz.Has(p, auth.PermHSEManage) && !s.authz.Has(p, auth.PermHSEReport) {
		return nil, auth.ErrForbidden
	}
	if !s.authz.ScopeOf(p).Allows(project) {
		return nil, ErrNotFound
	}
	var out []map[string]any
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = rowsAsMaps(ctx, tx, `SELECT e.id, e.number, e.name, e.trade FROM employees e JOIN projects pr ON pr.legal_entity_id = e.legal_entity_id
			WHERE pr.id = $1::uuid AND e.status = 'ACTIVE' ORDER BY e.number`, project)
		return err
	})
	return out, err
}

// TimesheetDetail returns a timesheet with its lines.
func (s *Service) TimesheetDetail(ctx context.Context, p auth.Principal, id string) (map[string]any, error) {
	var out map[string]any
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "timesheet", id, false); err != nil {
			return err
		}
		h, err := rowsAsMaps(ctx, tx, `SELECT t.*, (SELECT code FROM projects WHERE id = t.project_id) AS project FROM timesheets t WHERE t.id = $1::uuid`, id)
		if err != nil || len(h) == 0 {
			return ErrNotFound
		}
		out = h[0]
		out["lines"], err = rowsAsMaps(ctx, tx, `SELECT e.number, e.name, e.trade, c.code AS cost_code, l.hours_normal, l.hours_ot FROM timesheet_lines l JOIN employees e ON e.id = l.employee_id
			LEFT JOIN cost_codes c ON c.id = l.cost_code_id WHERE l.timesheet_id = $1::uuid ORDER BY e.number`, id)
		return err
	})
	return out, err
}

// ImportTimesheetsToPayRun fills a draft pay run's inputs from approved
// timesheets: daily-rate people get their days, hourly people their normal
// hours, everyone their overtime; the project with most hours is charged.
// People already on the run are left as they are.
func (s *Service) ImportTimesheetsToPayRun(ctx context.Context, p auth.Principal, run string) (int, error) {
	var n int
	err := s.payrollTx(ctx, p, auth.PermPayrollPrepare, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM pay_runs WHERE id = $1::uuid`, run).Scan(&status); err != nil {
			return mapNoRows(err)
		}
		if status != "DRAFT" {
			return fmt.Errorf("%w: hours are imported into a draft run (this one is %s)", ErrInvalidInput, status)
		}
		return tx.QueryRow(ctx, `WITH r AS (SELECT legal_entity_id, period_start, period_end FROM pay_runs WHERE id = $1::uuid),
			h AS (SELECT tl.employee_id, t.project_id, tl.cost_code_id, t.work_date, tl.hours_normal, tl.hours_ot FROM r
			        JOIN timesheets t ON t.status = 'APPROVED' AND t.work_date BETWEEN r.period_start AND r.period_end
			        JOIN timesheet_lines tl ON tl.timesheet_id = t.id
			        JOIN employees e ON e.id = tl.employee_id AND e.legal_entity_id = r.legal_entity_id),
			agg AS (SELECT employee_id, count(DISTINCT work_date) AS days, sum(hours_normal) AS hn, sum(hours_ot) AS ho FROM h GROUP BY employee_id),
			main AS (SELECT DISTINCT ON (employee_id) employee_id, project_id, cost_code_id FROM (SELECT employee_id, project_id, cost_code_id, sum(hours_normal + hours_ot) AS hrs FROM h GROUP BY 1, 2, 3) q ORDER BY employee_id, hrs DESC),
			ins AS (INSERT INTO pay_inputs (run_id, employee_id, units, overtime_hours, allowances, project_id, cost_code_id)
			        SELECT $1::uuid, a.employee_id, CASE e.kind WHEN 'SALARIED' THEN 1 WHEN 'DAILY' THEN a.days ELSE a.hn END, a.ho, 0, m.project_id, m.cost_code_id
			          FROM agg a JOIN employees e ON e.id = a.employee_id JOIN main m ON m.employee_id = a.employee_id
			         WHERE NOT EXISTS (SELECT 1 FROM pay_inputs x WHERE x.run_id = $1::uuid AND x.employee_id = a.employee_id) AND (CASE e.kind WHEN 'SALARIED' THEN 1 WHEN 'DAILY' THEN a.days ELSE a.hn END) > 0
			        RETURNING 1) SELECT count(*) FROM ins`, run).Scan(&n)
	})
	return n, err
}

// ------------------------------------------------------------- equipment ---

// EquipmentIn registers a piece of equipment.
type EquipmentIn struct {
	LegalEntityID        string          `json:"legal_entity_id"`
	Code                 string          `json:"code"`
	Name                 string          `json:"name"`
	Class                string          `json:"class"`
	Ownership            string          `json:"ownership"`
	AssetID              string          `json:"asset_id"`
	HireRate             decimal.Decimal `json:"hire_rate"`
	RateUnit             string          `json:"rate_unit"`
	FuelNormLPH          decimal.Decimal `json:"fuel_norm_lph"`
	ServiceIntervalHours decimal.Decimal `json:"service_interval_hours"`
	Location             string          `json:"location"`
}

// RegisterEquipment adds a piece of equipment to the register.
func (s *Service) RegisterEquipment(ctx context.Context, p auth.Principal, in EquipmentIn) (string, error) {
	var id string
	err := s.payrollTxPerm(ctx, p, auth.PermEquipmentManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment (legal_entity_id, code, name, class, ownership, asset_id, hire_rate, rate_unit, fuel_norm_lph, service_interval_hours, location, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, '')::uuid, $7, $8, NULLIF($9, 0), NULLIF($10, 0), NULLIF($11, ''), $12) RETURNING id::text`,
			in.LegalEntityID, strings.ToUpper(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Name), strings.TrimSpace(in.Class), strings.ToUpper(strings.TrimSpace(in.Ownership)), in.AssetID,
			in.HireRate, strings.ToUpper(strings.TrimSpace(in.RateUnit)), in.FuelNormLPH, in.ServiceIntervalHours, strings.TrimSpace(in.Location), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "equipment.register", "equipment", id, "equipment.registered", map[string]any{"id": id, "code": in.Code})
	})
	return id, err
}

// payrollTxPerm is an entity-level transaction for a permission (tenant-wide scope).
func (s *Service) payrollTxPerm(ctx context.Context, p auth.Principal, perm auth.Permission, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if !s.tenantWide(p) {
		return auth.ErrForbidden
	}
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, fn)
}

// SetEquipmentStatus activates or retires a piece of equipment (and moves it).
func (s *Service) SetEquipmentStatus(ctx context.Context, p auth.Principal, id, status, location string) error {
	status = strings.ToUpper(strings.TrimSpace(status))
	return s.payrollTxPerm(ctx, p, auth.PermEquipmentManage, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE equipment SET status = $2, location = COALESCE(NULLIF($3, ''), location) WHERE id = $1::uuid`, id, status, strings.TrimSpace(location)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "equipment.update", "equipment", id, "equipment.updated", map[string]any{"id": id, "status": status, "location": location})
	})
}

// UsageIn records one day of an equipment's use on a project.
type UsageIn struct {
	EquipmentID    string          `json:"equipment_id"`
	ProjectID      string          `json:"project_id"`
	CostCodeID     string          `json:"cost_code_id"`
	WorkDate       string          `json:"work_date"`
	HoursWork      decimal.Decimal `json:"hours_work"`
	HoursIdle      decimal.Decimal `json:"hours_idle"`
	HoursBreakdown decimal.Decimal `json:"hours_breakdown"`
	LitresFuel     decimal.Decimal `json:"litres_fuel"`
	Operator       string          `json:"operator_name"`
}

// RecordUsage records a DRAFT usage record.
func (s *Service) RecordUsage(ctx context.Context, p auth.Principal, in UsageIn) (string, error) {
	day, err := parseDay("work_date", in.WorkDate)
	if err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermEquipmentRecord, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "EQU")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_usage (equipment_id, project_id, cost_code_id, work_date, number, hours_work, hours_idle, hours_breakdown, litres_fuel, operator_name, created_by)
			VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11) RETURNING id::text`,
			in.EquipmentID, in.ProjectID, in.CostCodeID, day, num, in.HoursWork, in.HoursIdle, in.HoursBreakdown, in.LitresFuel, strings.TrimSpace(in.Operator), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "equipment.usage.create", "equipment_usage", id, "equipment_usage.created", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// SubmitUsage, DecideUsage and CancelUsage move a usage record.
func (s *Service) SubmitUsage(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermEquipmentRecord, "equipment_usage", "equipment_usage", id, "equipment.submit", "equipment_usage.submitted")
}
func (s *Service) DecideUsage(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideSimple(ctx, p, auth.PermEquipmentApprove, "equipment_usage", "equipment_usage", id, "equipment.decide", "usage record", approve, reason)
}
func (s *Service) CancelUsage(ctx context.Context, p auth.Principal, id string) error {
	return s.cancelDraft(ctx, p, auth.PermEquipmentRecord, "equipment_usage", "equipment_usage", id)
}

// WorkOrderIn opens a service or repair.
type WorkOrderIn struct {
	EquipmentID string `json:"equipment_id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// OpenWorkOrder opens a work order.
func (s *Service) OpenWorkOrder(ctx context.Context, p auth.Principal, in WorkOrderIn) (string, error) {
	var id string
	err := s.payrollTxPerm(ctx, p, auth.PermEquipmentManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "WO")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_work_orders (equipment_id, number, kind, description, created_by) VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`,
			in.EquipmentID, num, strings.ToUpper(strings.TrimSpace(in.Kind)), strings.TrimSpace(in.Description), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "equipment.workorder.open", "equipment_work_order", id, "equipment_work_order.opened", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// CompleteWorkOrder completes a work order with its cost; the equipment's
// approved hours to date are recorded for the service interval.
func (s *Service) CompleteWorkOrder(ctx context.Context, p auth.Principal, id string, cost decimal.Decimal) error {
	return s.payrollTxPerm(ctx, p, auth.PermEquipmentManage, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE equipment_work_orders w SET status = 'DONE', done_on = current_date, cost = $2, closed_by = $3,
			hours_at_done = COALESCE((SELECT sum(hours_work) FROM equipment_usage u WHERE u.equipment_id = w.equipment_id AND u.status = 'APPROVED'), 0) WHERE w.id = $1::uuid AND w.status = 'OPEN'`, id, cost, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "equipment.workorder.done", "equipment_work_order", id, "equipment_work_order.done", map[string]any{"id": id})
	})
}

// EquipmentView lists the register with hours, service due and fuel anomalies,
// recent usage and work orders.
func (s *Service) EquipmentView(ctx context.Context, p auth.Principal) (map[string]any, error) {
	out := map[string]any{}
	if !s.authz.Has(p, auth.PermEquipmentManage) && !s.authz.Has(p, auth.PermEquipmentRecord) && !s.authz.Has(p, auth.PermEquipmentApprove) && !s.authz.Has(p, auth.PermEquipmentCharge) {
		return out, auth.ErrForbidden
	}
	wide, projects := s.scopeArgs(p)
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out["entities"], err = rowsAsMaps(ctx, tx, `SELECT id, name, functional_currency FROM legal_entities ORDER BY name`); err != nil {
			return err
		}
		if out["projects"], err = rowsAsMaps(ctx, tx, `SELECT id, code, name FROM projects WHERE $1 OR id = ANY($2::uuid[]) ORDER BY code`, wide, projects); err != nil {
			return err
		}
		if out["equipment"], err = rowsAsMaps(ctx, tx, `SELECT e.id, e.code, e.name, e.class, e.ownership, e.hire_rate, e.rate_unit, e.fuel_norm_lph, e.service_interval_hours, e.location, e.status,
			COALESCE(h.hours, 0) AS hours_total, COALESCE(h.hours, 0) - COALESCE((SELECT max(hours_at_done) FROM equipment_work_orders w WHERE w.equipment_id = e.id AND w.kind = 'SERVICE' AND w.status = 'DONE'), 0) AS hours_since_service,
			(e.service_interval_hours IS NOT NULL AND COALESCE(h.hours, 0) - COALESCE((SELECT max(hours_at_done) FROM equipment_work_orders w WHERE w.equipment_id = e.id AND w.kind = 'SERVICE' AND w.status = 'DONE'), 0) >= e.service_interval_hours) AS service_due,
			COALESCE(f.lph, 0) AS fuel_lph_30d, (e.fuel_norm_lph IS NOT NULL AND COALESCE(f.lph, 0) > e.fuel_norm_lph * 1.2) AS fuel_anomaly
			FROM equipment e
			LEFT JOIN (SELECT equipment_id, sum(hours_work) AS hours FROM equipment_usage WHERE status = 'APPROVED' GROUP BY equipment_id) h ON h.equipment_id = e.id
			LEFT JOIN (SELECT equipment_id, sum(litres_fuel) / NULLIF(sum(hours_work), 0) AS lph FROM equipment_usage WHERE status = 'APPROVED' AND work_date > current_date - 30 GROUP BY equipment_id) f ON f.equipment_id = e.id
			ORDER BY e.code`); err != nil {
			return err
		}
		if out["usage"], err = rowsAsMaps(ctx, tx, `SELECT u.id, u.number, e.code AS equipment, pr.code AS project, u.work_date, u.hours_work, u.hours_idle, u.hours_breakdown, u.litres_fuel, u.charge, u.status, u.created_by, u.hire_run_id IS NOT NULL AS charged
			FROM equipment_usage u JOIN equipment e ON e.id = u.equipment_id JOIN projects pr ON pr.id = u.project_id WHERE ($1 OR u.project_id = ANY($2::uuid[])) ORDER BY u.work_date DESC, u.number DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["work_orders"], err = rowsAsMaps(ctx, tx, `SELECT w.id, w.number, e.code AS equipment, w.kind, w.description, w.status, w.opened_on, w.done_on, w.cost FROM equipment_work_orders w JOIN equipment e ON e.id = w.equipment_id ORDER BY w.opened_on DESC, w.number DESC LIMIT 100`); err != nil {
			return err
		}
		out["hire_runs"], err = rowsAsMaps(ctx, tx, `SELECT r.id, r.number, r.period_start, r.period_end, (SELECT sum(amount) FROM equipment_hire_lines l WHERE l.run_id = r.id) AS total,
			(SELECT count(*) FROM equipment_hire_lines l WHERE l.run_id = r.id) AS usage_records FROM equipment_hire_runs r ORDER BY r.period_end DESC`)
		return err
	})
	return out, err
}

// PrepareHireRun charges approved, uncharged usage of the period to the
// projects: one draft journal per project (Dr cost / Cr recovery) for
// finance to approve and post.
func (s *Service) PrepareHireRun(ctx context.Context, p auth.Principal, entity, start, end, costAcct, recoveryAcct string) (string, []string, error) {
	ps, err := parseDay("period_start", start)
	if err != nil {
		return "", nil, err
	}
	pe, err := parseDay("period_end", end)
	if err != nil {
		return "", nil, err
	}
	type line struct {
		usage, project, cc string
		amount             decimal.Decimal
	}
	var lines []line
	var ccy string
	if !s.tenantWide(p) {
		return "", nil, auth.ErrForbidden
	}
	if err := s.authz.Require(p, auth.PermEquipmentCharge, p.TenantID); err != nil {
		return "", nil, err
	}
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT functional_currency FROM legal_entities WHERE id = $1::uuid`, entity).Scan(&ccy); err != nil {
			return mapNoRows(err)
		}
		rows, err := tx.Query(ctx, `SELECT u.id::text, u.project_id::text, COALESCE(u.cost_code_id::text, ''), u.charge FROM equipment_usage u JOIN equipment e ON e.id = u.equipment_id
			WHERE e.legal_entity_id = $1::uuid AND u.status = 'APPROVED' AND u.hire_run_id IS NULL AND u.charge > 0 AND u.work_date BETWEEN $2 AND $3 ORDER BY u.project_id, u.work_date`, entity, ps, pe)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l line
			if err := rows.Scan(&l.usage, &l.project, &l.cc, &l.amount); err != nil {
				return err
			}
			lines = append(lines, l)
		}
		return rows.Err()
	}); err != nil {
		return "", nil, err
	}
	if len(lines) == 0 {
		return "", nil, fmt.Errorf("%w: no approved, uncharged usage in the period", ErrInvalidInput)
	}
	runID := uuid.NewString()
	// One draft journal per project (a journal belongs to one project).
	byProject := map[string]map[string]decimal.Decimal{}
	order := []string{}
	for _, l := range lines {
		if byProject[l.project] == nil {
			byProject[l.project] = map[string]decimal.Decimal{}
			order = append(order, l.project)
		}
		byProject[l.project][l.cc] = byProject[l.project][l.cc].Add(l.amount)
	}
	journals := map[string]string{}
	for _, proj := range order {
		var jl []ledger.Line
		total := decimal.Zero
		ccs := make([]string, 0, len(byProject[proj]))
		for cc := range byProject[proj] {
			ccs = append(ccs, cc)
		}
		sortStrings(ccs)
		for _, cc := range ccs {
			amt := byProject[proj][cc]
			total = total.Add(amt)
			jl = append(jl, ledger.Line{LineNo: len(jl) + 1, AccountID: costAcct, Side: ledger.Debit, Amount: money.MustNew(amt.StringFixed(2), money.Currency(ccy)), ProjectID: proj, CostCodeID: cc})
		}
		jl = append(jl, ledger.Line{LineNo: len(jl) + 1, AccountID: recoveryAcct, Side: ledger.Credit, Amount: money.MustNew(total.StringFixed(2), money.Currency(ccy)), ProjectID: proj})
		res, err := s.CreateJournal(ctx, p, ledger.Journal{LegalEntityID: entity, ProjectID: proj, FunctionalCurrency: money.Currency(ccy), BusinessEventAt: time.Date(pe.Year(), pe.Month(), pe.Day(), 12, 0, 0, 0, time.UTC),
			Description: "Equipment hire charge " + ps.Format("2006-01-02") + " to " + pe.Format("2006-01-02"), SourceType: "MANUAL", SourceRef: runID, IdempotencyKey: "eqp-hire:" + runID + ":" + proj, Lines: jl})
		if err != nil {
			return "", nil, err
		}
		journals[proj] = res.JournalID
	}
	var jids []string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "EHR")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO equipment_hire_runs (id, legal_entity_id, number, period_start, period_end, cost_account_id, recovery_account_id, created_by) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7::uuid, $8)`,
			runID, entity, num, ps, pe, costAcct, recoveryAcct, p.UserID); err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(ctx, `INSERT INTO equipment_hire_lines (run_id, usage_id, project_id, cost_code_id, amount) VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4, '')::uuid, $5)`, runID, l.usage, l.project, l.cc, l.amount); err != nil {
				return err
			}
			if err := postgres.Exec1(ctx, tx, `UPDATE equipment_usage SET hire_run_id = $2::uuid WHERE id = $1::uuid`, l.usage, runID); err != nil {
				return err
			}
		}
		for proj, jid := range journals {
			if _, err := tx.Exec(ctx, `INSERT INTO equipment_hire_journals (run_id, project_id, journal_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`, runID, proj, jid); err != nil {
				return err
			}
			jids = append(jids, jid)
		}
		return s.record(ctx, tx, p, "equipment.hirerun", "equipment_hire_run", runID, "equipment_hire_run.prepared", map[string]any{"id": runID, "number": num, "journals": len(journals)})
	})
	return runID, jids, err
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// ------------------------------------------------------------------- HSE ---

// IncidentIn reports an incident.
type IncidentIn struct {
	ProjectID      string `json:"project_id"`
	Kind           string `json:"kind"`
	Severity       string `json:"severity"`
	OccurredAt     string `json:"occurred_at"` // RFC 3339
	Location       string `json:"location"`
	Description    string `json:"description"`
	PeopleInvolved int    `json:"people_involved"`
	LostDays       int    `json:"lost_days"`
}

func parseTime(field, v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		if d, e2 := time.Parse("2006-01-02T15:04", strings.TrimSpace(v)); e2 == nil {
			return d.UTC(), nil
		}
		return time.Time{}, fmt.Errorf("%w: %s is a date and time", ErrInvalidInput, field)
	}
	return t, nil
}

// ReportIncident records an incident; a major or critical one notifies the
// HSE officers and project managers at once (not configurable).
func (s *Service) ReportIncident(ctx context.Context, p auth.Principal, in IncidentIn) (string, error) {
	at, err := parseTime("occurred_at", in.OccurredAt)
	if err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermHSEReport, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	in.Kind, in.Severity = strings.ToUpper(strings.TrimSpace(in.Kind)), strings.ToUpper(strings.TrimSpace(in.Severity))
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "INC")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO incidents (project_id, number, kind, severity, occurred_at, location, description, people_involved, lost_days, reported_by)
			VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10) RETURNING id::text`, in.ProjectID, num, in.Kind, in.Severity, at, strings.TrimSpace(in.Location), strings.TrimSpace(in.Description), in.PeopleInvolved, in.LostDays, p.UserID)
		if err != nil {
			return err
		}
		if in.Severity == "MAJOR" || in.Severity == "CRITICAL" {
			for _, role := range []string{"hse_officer", "project_manager"} {
				if err := s.notify(ctx, tx, postgres.Notification{RecipientRole: role, Kind: "hse.incident", Severity: "critical", Title: fmt.Sprintf("%s incident %s reported", strings.ToLower(in.Severity), num),
					Body: strings.TrimSpace(in.Description), SubjectType: "incident", SubjectID: id, ProjectID: in.ProjectID, Href: "#/hse/incidents", DedupeKey: "hse.incident:" + id + ":" + role, CreatedBy: p.UserID}); err != nil {
					return err
				}
			}
		}
		return s.record(ctx, tx, p, "hse.incident.report", "incident", id, "hse.incident_reported", map[string]any{"id": id, "number": num, "severity": in.Severity})
	})
	return id, err
}

// InvestigateIncident records the investigation and moves the incident to INVESTIGATING.
func (s *Service) InvestigateIncident(ctx context.Context, p auth.Principal, id, rootCause, findings string) error {
	return s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO incident_investigations (incident_id, root_cause, findings, created_by) VALUES ($1::uuid, $2, $3, $4)`, id, strings.TrimSpace(rootCause), strings.TrimSpace(findings), p.UserID); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE incidents SET status = 'INVESTIGATING' WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.incident.investigate", "incident", id, "hse.incident_investigated", map[string]any{"id": id})
	})
}

// RaiseCAPA raises a corrective action on an open incident.
func (s *Service) RaiseCAPA(ctx context.Context, p auth.Principal, incident, description, owner, dueOn string) (string, error) {
	due, err := parseDay("due_on", dueOn)
	if err != nil {
		return "", err
	}
	var id string
	err = s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO capas (incident_id, description, owner_name, due_on, created_by) VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`,
			incident, strings.TrimSpace(description), strings.TrimSpace(owner), due, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.capa.raise", "capa", id, "hse.capa_raised", map[string]any{"id": id, "incident_id": incident})
	})
	return id, err
}

// CompleteCAPA marks a corrective action done; VerifyCAPA is a different person.
func (s *Service) CompleteCAPA(ctx context.Context, p auth.Principal, id string) error {
	return s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE capas SET status = 'DONE', done_by = $2, done_on = current_date WHERE id = $1::uuid`, id, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.capa.done", "capa", id, "hse.capa_done", map[string]any{"id": id})
	})
}
func (s *Service) VerifyCAPA(ctx context.Context, p auth.Principal, id string) error {
	return s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE capas SET status = 'VERIFIED', verified_by = $2 WHERE id = $1::uuid`, id, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.capa.verify", "capa", id, "hse.capa_verified", map[string]any{"id": id})
	})
}

// CloseIncident closes an investigated incident (not its reporter).
func (s *Service) CloseIncident(ctx context.Context, p auth.Principal, id, note string) error {
	return s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE incidents SET status = 'CLOSED', closed_by = $2, close_note = $3 WHERE id = $1::uuid`, id, p.UserID, strings.TrimSpace(note)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.incident.close", "incident", id, "hse.incident_closed", map[string]any{"id": id})
	})
}

// PermitIn requests a permit to work.
type PermitIn struct {
	ProjectID   string `json:"project_id"`
	Kind        string `json:"kind"`
	Location    string `json:"location"`
	Description string `json:"description"`
	ValidFrom   string `json:"valid_from"`
	ValidTo     string `json:"valid_to"`
}

// RequestPermit requests a permit to work.
func (s *Service) RequestPermit(ctx context.Context, p auth.Principal, in PermitIn) (string, error) {
	from, err := parseTime("valid_from", in.ValidFrom)
	if err != nil {
		return "", err
	}
	to, err := parseTime("valid_to", in.ValidTo)
	if err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermHSEReport, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "PTW")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO permits_to_work (project_id, number, kind, location, description, valid_from, valid_to, requested_by) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
			in.ProjectID, num, strings.ToUpper(strings.TrimSpace(in.Kind)), strings.TrimSpace(in.Location), strings.TrimSpace(in.Description), from, to, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.permit.request", "permit", id, "hse.permit_requested", map[string]any{"id": id, "number": num})
	})
	return id, err
}

func (s *Service) permitStep(ctx context.Context, p auth.Principal, perm auth.Permission, id, sql, action string, args ...any) error {
	var project string
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT project_id::text FROM permits_to_work WHERE id = $1::uuid`, id).Scan(&project))
	}); err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, perm, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, sql, append([]any{id}, args...)...); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, "permit", id, strings.Replace(action, "hse.permit.", "hse.permit_", 1), map[string]any{"id": id})
	})
}

// IssuePermit issues a requested permit (HSE officer, not the requester); the
// conditions are what the issuer sets for the work.
func (s *Service) IssuePermit(ctx context.Context, p auth.Principal, id, conditions string) error {
	return s.permitStep(ctx, p, auth.PermHSEManage, id, `UPDATE permits_to_work SET status = 'ISSUED', issued_by = $2, conditions = NULLIF($3, '') WHERE id = $1::uuid`, "hse.permit.issue", p.UserID, strings.TrimSpace(conditions))
}

// AcceptPermit accepts an issued permit (the person who will do the work, not the issuer).
func (s *Service) AcceptPermit(ctx context.Context, p auth.Principal, id string) error {
	return s.permitStep(ctx, p, auth.PermHSEReport, id, `UPDATE permits_to_work SET status = 'ACCEPTED', accepted_by = $2 WHERE id = $1::uuid`, "hse.permit.accept", p.UserID)
}

// ClosePermit closes an accepted permit when the work is finished.
func (s *Service) ClosePermit(ctx context.Context, p auth.Principal, id string) error {
	return s.permitStep(ctx, p, auth.PermHSEReport, id, `UPDATE permits_to_work SET status = 'CLOSED', closed_by = $2 WHERE id = $1::uuid`, "hse.permit.close", p.UserID)
}

// CancelPermit cancels a requested or issued permit.
func (s *Service) CancelPermit(ctx context.Context, p auth.Principal, id string) error {
	return s.permitStep(ctx, p, auth.PermHSEReport, id, `UPDATE permits_to_work SET status = 'CANCELLED' WHERE id = $1::uuid`, "hse.permit.cancel")
}

// RecordInduction records a person's site induction.
func (s *Service) RecordInduction(ctx context.Context, p auth.Principal, employee, project, inductedOn, validTo, topics string) error {
	on, err := parseDay("inducted_on", inductedOn)
	if err != nil {
		return err
	}
	to, err := parseDay("valid_to", validTo)
	if err != nil {
		return err
	}
	return s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO inductions (employee_id, project_id, inducted_on, valid_to, topics, created_by) VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5, ''), $6)`, employee, project, on, to, strings.TrimSpace(topics), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.induction.record", "induction", employee, "hse.induction_recorded", map[string]any{"employee_id": employee, "project_id": project})
	})
}

// RecordToolboxTalk and RecordSafetyInspection are insert-only records.
func (s *Service) RecordToolboxTalk(ctx context.Context, p auth.Principal, project, heldOn, topic string, attendees int, notes string) error {
	on, err := parseDay("held_on", heldOn)
	if err != nil {
		return err
	}
	return s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO toolbox_talks (project_id, held_on, topic, attendees, notes, created_by) VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''), $6)`, project, on, strings.TrimSpace(topic), attendees, strings.TrimSpace(notes), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.toolbox.record", "toolbox_talk", project, "hse.toolbox_recorded", map[string]any{"project_id": project, "topic": topic})
	})
}
func (s *Service) RecordSafetyInspection(ctx context.Context, p auth.Principal, project, on, area string, score int, findings string) error {
	d, err := parseDay("inspected_on", on)
	if err != nil {
		return err
	}
	return s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO safety_inspections (project_id, inspected_on, area, score, findings, created_by) VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''), $6)`, project, d, strings.TrimSpace(area), score, strings.TrimSpace(findings), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.inspection.record", "safety_inspection", project, "hse.inspection_recorded", map[string]any{"project_id": project, "area": area})
	})
}

// SetInductionGate switches the induction gate on timesheets.
func (s *Service) SetInductionGate(ctx context.Context, p auth.Principal, on bool) error {
	return s.payrollTxPerm(ctx, p, auth.PermHSEManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO hse_settings (induction_gate, updated_by) VALUES ($1, $2) ON CONFLICT (tenant_id) DO UPDATE SET induction_gate = EXCLUDED.induction_gate, updated_by = EXCLUDED.updated_by, updated_at = now()`, on, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "hse.settings.gate", "hse_settings", p.TenantID, "hse.induction_gate_set", map[string]any{"induction_gate": on})
	})
}

// HSEView returns the HSE register and its indicators.
func (s *Service) HSEView(ctx context.Context, p auth.Principal) (map[string]any, error) {
	out := map[string]any{}
	if !s.authz.Has(p, auth.PermHSEManage) && !s.authz.Has(p, auth.PermHSEReport) {
		return out, auth.ErrForbidden
	}
	wide, projects := s.scopeArgs(p)
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out["projects"], err = rowsAsMaps(ctx, tx, `SELECT id, code, name FROM projects WHERE $1 OR id = ANY($2::uuid[]) ORDER BY code`, wide, projects); err != nil {
			return err
		}
		if out["incidents"], err = rowsAsMaps(ctx, tx, `SELECT i.id, i.number, pr.code AS project, i.kind, i.severity, i.occurred_at, i.location, i.description, i.people_involved, i.lost_days, i.status, i.reported_by, i.closed_by,
			(SELECT count(*) FROM capas c WHERE c.incident_id = i.id) AS actions, (SELECT count(*) FROM capas c WHERE c.incident_id = i.id AND c.status <> 'VERIFIED') AS open_actions,
			(SELECT row_to_json(x) FROM (SELECT root_cause, findings FROM incident_investigations WHERE incident_id = i.id) x) AS investigation
			FROM incidents i JOIN projects pr ON pr.id = i.project_id WHERE ($1 OR i.project_id = ANY($2::uuid[])) ORDER BY i.occurred_at DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["actions"], err = rowsAsMaps(ctx, tx, `SELECT c.id, i.number AS incident, c.description, c.owner_name, c.due_on, c.status, c.done_by, c.verified_by, (c.status = 'OPEN' AND c.due_on < current_date) AS overdue
			FROM capas c JOIN incidents i ON i.id = c.incident_id WHERE ($1 OR i.project_id = ANY($2::uuid[])) ORDER BY c.due_on LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["permits"], err = rowsAsMaps(ctx, tx, `SELECT w.id, w.number, pr.code AS project, w.kind, w.location, w.description, w.conditions, w.valid_from, w.valid_to, w.status, w.requested_by, w.issued_by, w.accepted_by,
			(w.status = 'ACCEPTED' AND now() BETWEEN w.valid_from AND w.valid_to) AS in_force FROM permits_to_work w JOIN projects pr ON pr.id = w.project_id WHERE ($1 OR w.project_id = ANY($2::uuid[])) ORDER BY w.valid_from DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["inductions"], err = rowsAsMaps(ctx, tx, `SELECT i.id, e.number, e.name, pr.code AS project, i.inducted_on, i.valid_to, (i.valid_to >= current_date) AS valid FROM inductions i JOIN employees e ON e.id = i.employee_id JOIN projects pr ON pr.id = i.project_id
			WHERE ($1 OR i.project_id = ANY($2::uuid[])) ORDER BY i.inducted_on DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["talks"], err = rowsAsMaps(ctx, tx, `SELECT t.id, pr.code AS project, t.held_on, t.topic, t.attendees FROM toolbox_talks t JOIN projects pr ON pr.id = t.project_id WHERE ($1 OR t.project_id = ANY($2::uuid[])) ORDER BY t.held_on DESC LIMIT 100`, wide, projects); err != nil {
			return err
		}
		if out["inspections"], err = rowsAsMaps(ctx, tx, `SELECT t.id, pr.code AS project, t.inspected_on, t.area, t.score, t.findings FROM safety_inspections t JOIN projects pr ON pr.id = t.project_id WHERE ($1 OR t.project_id = ANY($2::uuid[])) ORDER BY t.inspected_on DESC LIMIT 100`, wide, projects); err != nil {
			return err
		}
		// LTIFR over the last 12 months: lost-time injuries x 1,000,000 / hours worked (approved timesheets).
		var lti int
		var hours decimal.Decimal
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM incidents i WHERE i.kind IN ('LOST_TIME', 'FATALITY') AND i.occurred_at > now() - interval '12 months' AND ($1 OR i.project_id = ANY($2::uuid[]))),
			COALESCE((SELECT sum(l.hours_normal + l.hours_ot) FROM timesheet_lines l JOIN timesheets t ON t.id = l.timesheet_id WHERE t.status = 'APPROVED' AND t.work_date > current_date - 365 AND ($1 OR t.project_id = ANY($2::uuid[]))), 0)`, wide, projects).Scan(&lti, &hours); err != nil {
			return err
		}
		ltifr := decimal.Zero
		if hours.IsPositive() {
			ltifr = decimal.NewFromInt(int64(lti)).Mul(decimal.NewFromInt(1000000)).Div(hours).Round(2)
		}
		var gate bool
		_ = tx.QueryRow(ctx, `SELECT COALESCE((SELECT induction_gate FROM hse_settings), false)`).Scan(&gate)
		out["kpi"] = map[string]any{"lost_time_injuries_12m": lti, "hours_worked_12m": hours, "ltifr": ltifr, "induction_gate": gate}
		return nil
	})
	return out, err
}
