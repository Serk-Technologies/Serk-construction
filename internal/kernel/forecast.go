// Cost forecasting (WP-2.1; SRS PCC-04, PCC-05).
//
// Each month a project manager forecasts the cost at completion of every cost
// code and a different person approves it. A forecast is a CYCLE (one project,
// one month end) holding one LINE per cost code. The line's figures (current
// budget, committed, received, cost to date) are computed and frozen by the
// database from the real cost position; the person chooses only the METHOD
// that sets the estimate to complete (ETC) and, for MANUAL, the amount. See
// migration 0044_forecast.sql for the guards.
package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// ForecastLine is one cost code of a forecast cycle. Money fields are computed
// by the database; Method, ManualETC and Note are the preparer's input.
type ForecastLine struct {
	CostCodeID string           `json:"cost_code_id"`
	CostCode   string           `json:"cost_code"`
	Method     string           `json:"method"`
	ManualETC  *decimal.Decimal `json:"manual_etc,omitempty"`
	Note       string           `json:"note,omitempty"`
	Budget     decimal.Decimal  `json:"budget"`
	Committed  decimal.Decimal  `json:"committed"`
	Reserved   decimal.Decimal  `json:"reserved"`
	Received   decimal.Decimal  `json:"received"`
	CostToDate decimal.Decimal  `json:"cost_to_date"`
	ETC        decimal.Decimal  `json:"etc"`
	EAC        decimal.Decimal  `json:"eac"`
	VAC        decimal.Decimal  `json:"vac"`
}

// ForecastCycle is a forecast with its lines and totals.
type ForecastCycle struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"project_id"`
	PeriodEnd  string          `json:"period_end"`
	Status     string          `json:"status"`
	Note       string          `json:"note,omitempty"`
	CreatedBy  string          `json:"created_by"`
	ApprovedBy string          `json:"approved_by,omitempty"`
	Reason     string          `json:"decision_reason,omitempty"`
	Lines      []ForecastLine  `json:"lines"`
	Budget     decimal.Decimal `json:"total_budget"`
	CostToDate decimal.Decimal `json:"total_cost_to_date"`
	ETC        decimal.Decimal `json:"total_etc"`
	EAC        decimal.Decimal `json:"total_eac"`
	VAC        decimal.Decimal `json:"total_vac"`
}

// CreateForecast opens a DRAFT cycle for the project at periodEnd, with one
// REMAINING_BUDGET line per cost code that has any money on it. The preparer
// then adjusts the lines.
func (s *Service) CreateForecast(ctx context.Context, p auth.Principal, projectID, periodEnd, note string) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermForecastPrepare, p.TenantID, projectID); err != nil {
		return "", err
	}
	pe, err := time.Parse("2006-01-02", strings.TrimSpace(periodEnd))
	if err != nil {
		return "", fmt.Errorf("%w: period_end must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Plain-language refusals for the two rules people meet first.
		var open string
		err := tx.QueryRow(ctx, `SELECT 'FC-' || to_char(period_end, 'YYYY-MM') || ' (' || lower(status) || ', by ' || created_by || ')' FROM forecast_cycles
			WHERE project_id = $1::uuid AND status IN ('DRAFT', 'SUBMITTED')`, projectID).Scan(&open)
		if err == nil {
			return fmt.Errorf("%w: this project already has an open forecast %s; it must be submitted and decided, or cancelled by its author, before another is prepared", ErrInvalidInput, open)
		}
		var lines int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM p2p_project_cost_report($1::uuid) WHERE "current" <> 0 OR committed <> 0 OR actual_posted <> 0 OR reserved <> 0`, projectID).Scan(&lines); err != nil {
			return err
		}
		if lines == 0 {
			return fmt.Errorf("%w: this project has no budget, commitment or cost on any cost code yet, so there is nothing to forecast; approve its budget first", ErrInvalidInput)
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO forecast_cycles (project_id, period_end, note, created_by)
			VALUES ($1::uuid, $2, NULLIF($3, ''), $4) RETURNING id::text`, projectID, pe, note, p.UserID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO forecast_lines (cycle_id, cost_code_id)
			SELECT $1::uuid, cost_code_id FROM p2p_project_cost_report($2::uuid)
			 WHERE "current" <> 0 OR committed <> 0 OR actual_posted <> 0 OR reserved <> 0`, id, projectID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "forecast.create", "forecast_cycle", id, "forecast.created", map[string]any{"forecast_id": id, "project_id": projectID, "period_end": periodEnd})
	})
	return id, err
}

// SetForecastLine changes one line's method (and manual ETC / note) while the
// cycle is DRAFT. Only the preparer works on a draft.
func (s *Service) SetForecastLine(ctx context.Context, p auth.Principal, cycleID, costCodeID, method string, manualETC *decimal.Decimal, note string) error {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case "REMAINING_BUDGET", "OPEN_COMMITMENTS":
		manualETC = nil
	case "MANUAL":
		if manualETC == nil || manualETC.IsNegative() {
			return fmt.Errorf("%w: a MANUAL line needs a non-negative estimate to complete", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: method must be REMAINING_BUDGET, OPEN_COMMITMENTS or MANUAL", ErrInvalidInput)
	}
	return s.p2pTx(ctx, p, auth.PermForecastPrepare, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermForecastPrepare, "forecast_cycle", cycleID)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the preparer edits a draft forecast", auth.ErrForbidden)
		}
		tag, err := tx.Exec(ctx, `UPDATE forecast_lines SET method = $3, manual_etc = $4, note = NULLIF($5, '')
			 WHERE cycle_id = $1::uuid AND cost_code_id = $2::uuid`, cycleID, costCodeID, method, manualETC, note)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return s.record(ctx, tx, p, "forecast.line", "forecast_cycle", cycleID, "forecast.line_changed", map[string]any{"forecast_id": cycleID, "cost_code_id": costCodeID, "method": method})
	})
}

// CancelForecast abandons a DRAFT forecast (its author only, with a reason), so
// that the project can have a new one.
func (s *Service) CancelForecast(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, auth.PermForecastPrepare, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermForecastPrepare, "forecast_cycle", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the preparer cancels a draft forecast", auth.ErrForbidden)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE forecast_cycles SET status = 'CANCELLED', decision_reason = $2 WHERE id = $1::uuid`, id, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "forecast.cancel", "forecast_cycle", id, "forecast.cancelled", map[string]any{"forecast_id": id, "reason": reason})
	})
}

// SubmitForecast submits a draft forecast (by its preparer).
func (s *Service) SubmitForecast(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermForecastPrepare, "forecast_cycle", "forecast_cycles", id, "forecast.submit", "forecast.submitted")
}

// DecideForecast approves or rejects a submitted forecast (fresh MFA; not the preparer).
func (s *Service) DecideForecast(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermForecastApprove, "forecast_cycle", "forecast_cycles", id, "forecast.decide", "cost forecast", approve, reason, nil)
}

// GetForecast returns a cycle with its lines and totals.
func (s *Service) GetForecast(ctx context.Context, p auth.Principal, id string) (ForecastCycle, error) {
	c := ForecastCycle{Lines: []ForecastLine{}} // an empty forecast is [], not null
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "forecast_cycle", id, false); err != nil {
			return err
		}
		var pe time.Time
		var note, by, reason *string
		if err := tx.QueryRow(ctx, `SELECT id::text, project_id::text, period_end, status, note, created_by, approved_by, decision_reason
			FROM forecast_cycles WHERE id = $1::uuid`, id).Scan(&c.ID, &c.ProjectID, &pe, &c.Status, &note, &c.CreatedBy, &by, &reason); err != nil {
			return mapNoRows(err)
		}
		c.PeriodEnd = pe.Format("2006-01-02")
		c.Note, c.ApprovedBy, c.Reason = deref(note), deref(by), deref(reason)
		rows, err := tx.Query(ctx, `SELECT l.cost_code_id::text, cc.code, l.method, l.manual_etc, COALESCE(l.note, ''),
			l.budget, l.committed, l.reserved, l.received, l.cost_to_date, l.etc, l.eac, l.vac
			FROM forecast_lines l JOIN cost_codes cc ON cc.id = l.cost_code_id WHERE l.cycle_id = $1::uuid ORDER BY cc.code`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l ForecastLine
			if err := rows.Scan(&l.CostCodeID, &l.CostCode, &l.Method, &l.ManualETC, &l.Note, &l.Budget, &l.Committed, &l.Reserved, &l.Received, &l.CostToDate, &l.ETC, &l.EAC, &l.VAC); err != nil {
				return err
			}
			c.Budget, c.CostToDate, c.ETC = c.Budget.Add(l.Budget), c.CostToDate.Add(l.CostToDate), c.ETC.Add(l.ETC)
			c.EAC, c.VAC = c.EAC.Add(l.EAC), c.VAC.Add(l.VAC)
			c.Lines = append(c.Lines, l)
		}
		return rows.Err()
	})
	return c, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
