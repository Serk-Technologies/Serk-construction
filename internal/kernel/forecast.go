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
	// PrevEAC is the same code's EAC in the last approved forecast (0052).
	PrevEAC *decimal.Decimal `json:"prev_eac,omitempty"`
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
	// PrevEAC totals the lines that were in the last approved forecast.
	PrevEAC *decimal.Decimal `json:"total_prev_eac,omitempty"`
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
	case "REMAINING_BUDGET", "OPEN_COMMITMENTS", "CPI":
		manualETC = nil
	case "MANUAL":
		if manualETC == nil || manualETC.IsNegative() {
			return fmt.Errorf("%w: a MANUAL line needs a non-negative estimate to complete", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: method must be REMAINING_BUDGET, OPEN_COMMITMENTS, CPI or MANUAL", ErrInvalidInput)
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
			l.budget, l.committed, l.reserved, l.received, l.cost_to_date, l.etc, l.eac, l.vac, l.prev_eac
			FROM forecast_lines l JOIN cost_codes cc ON cc.id = l.cost_code_id WHERE l.cycle_id = $1::uuid ORDER BY cc.code`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l ForecastLine
			if err := rows.Scan(&l.CostCodeID, &l.CostCode, &l.Method, &l.ManualETC, &l.Note, &l.Budget, &l.Committed, &l.Reserved, &l.Received, &l.CostToDate, &l.ETC, &l.EAC, &l.VAC, &l.PrevEAC); err != nil {
				return err
			}
			c.Budget, c.CostToDate, c.ETC = c.Budget.Add(l.Budget), c.CostToDate.Add(l.CostToDate), c.ETC.Add(l.ETC)
			c.EAC, c.VAC = c.EAC.Add(l.EAC), c.VAC.Add(l.VAC)
			if l.PrevEAC != nil {
				t := l.PrevEAC.Add(decimal.Zero)
				if c.PrevEAC != nil {
					t = t.Add(*c.PrevEAC)
				}
				c.PrevEAC = &t
			}
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

// SCurvePoint is one month end of a project's S-curve.
type SCurvePoint struct {
	Month string           `json:"month"` // YYYY-MM
	PV    decimal.Decimal  `json:"pv"`
	EV    *decimal.Decimal `json:"ev,omitempty"` // only up to today
	AC    *decimal.Decimal `json:"ac,omitempty"`
}

// SCurve is the planned, earned and actual cost of a project by month, with
// the estimate at completion of each approved forecast.
type SCurve struct {
	ProjectID  string           `json:"project_id"`
	Baseline   bool             `json:"baseline"`
	BAC        decimal.Decimal  `json:"bac"`
	Start      string           `json:"start,omitempty"`
	Finish     string           `json:"finish,omitempty"`
	Points     []SCurvePoint    `json:"points"`
	Forecasts  []map[string]any `json:"forecasts"`
	LatestEAC  *decimal.Decimal `json:"latest_eac,omitempty"`
	LatestCPI  *decimal.Decimal `json:"cpi,omitempty"`
	LatestSPI  *decimal.Decimal `json:"spi,omitempty"`
	StatusDate string           `json:"status_date"`
}

// ProjectSCurve returns the project's S-curve: planned value over the
// baseline schedule by month end, earned value and actual cost up to today,
// and the EAC of each approved forecast (at most 120 months).
func (s *Service) ProjectSCurve(ctx context.Context, p auth.Principal, projectID string) (SCurve, error) {
	out := SCurve{ProjectID: projectID, Points: []SCurvePoint{}, Forecasts: []map[string]any{}}
	if err := s.authz.RequireProject(p, auth.PermP2PRead, p.TenantID, projectID); err != nil {
		return out, err
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	out.StatusDate = today.Format("2006-01-02")
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '10s'`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT period_end::text, COALESCE((SELECT sum(eac) FROM forecast_lines l WHERE l.cycle_id = f.id), 0)
			FROM forecast_cycles f WHERE f.project_id = $1::uuid AND f.status = 'APPROVED' ORDER BY period_end, approved_at`, projectID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pe string
			var eac decimal.Decimal
			if err := rows.Scan(&pe, &eac); err != nil {
				rows.Close()
				return err
			}
			e := eac
			out.LatestEAC = &e
			out.Forecasts = append(out.Forecasts, map[string]any{"period_end": pe, "eac": eac})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var start time.Time
		var finish *time.Time
		err = tx.QueryRow(ctx, `SELECT start_date, finish_date FROM schedule_versions WHERE project_id = $1::uuid AND status = 'BASELINE'`, projectID).Scan(&start, &finish)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		out.Baseline = true
		out.Start = start.Format("2006-01-02")
		end := start
		if finish != nil {
			end = *finish
			out.Finish = finish.Format("2006-01-02")
		}
		if today.After(end) {
			end = today
		}
		m := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < 120 && !m.After(end); i++ {
			monthEnd := m.AddDate(0, 1, -1)
			at := monthEnd
			if monthEnd.After(today) && !m.After(today) {
				at = today // the current month is measured today
			}
			var r postgres.EVMReport
			if r, err = postgres.ScheduleEVM(ctx, tx, projectID, at); err != nil {
				return err
			}
			pt := SCurvePoint{Month: m.Format("2006-01"), PV: r.PV}
			if !at.After(today) {
				ev, ac := r.EV, r.AC
				pt.EV, pt.AC = &ev, &ac
				out.LatestCPI, out.LatestSPI = r.CPI, r.SPI
			}
			out.BAC = r.BAC
			out.Points = append(out.Points, pt)
			m = m.AddDate(0, 1, 0)
		}
		return nil
	})
	return out, err
}
