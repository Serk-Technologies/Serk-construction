package kernel

// Project risk register (PLB-04.1; migration 0080). A risk has an owner, a
// cause and effect, a response and a history of assessments; its exposure is
// the latest probability times the latest cost impact. The register compares
// the project's open exposure with the contingency left in its budget.

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

// RiskAssessment is one judgement of a risk.
type RiskAssessment struct {
	Probability  decimal.Decimal `json:"probability"` // 0 < p <= 1
	CostImpact   decimal.Decimal `json:"cost_impact"`
	ScheduleDays int             `json:"schedule_days"`
	Note         string          `json:"note,omitempty"`
}

// RiskInput raises a risk with its first assessment.
type RiskInput struct {
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Category  string `json:"category"`
	Cause     string `json:"cause"`
	Effect    string `json:"effect"`
	Owner     string `json:"owner"`
	Response  string `json:"response"`
	RiskAssessment
}

func (a RiskAssessment) valid() error {
	one := decimal.NewFromInt(1)
	if !a.Probability.IsPositive() || a.Probability.GreaterThan(one) || a.CostImpact.IsNegative() || a.ScheduleDays < 0 || a.ScheduleDays > 3650 {
		return fmt.Errorf("%w: a probability above 0 and at most 1, a cost impact of 0 or more and 0 to 3650 days", ErrInvalidInput)
	}
	return nil
}

func (s *Service) riskProject(ctx context.Context, p auth.Principal, id string) (string, error) {
	var project string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT project_id::text FROM project_risks WHERE id = $1::uuid`, id).Scan(&project))
	})
	return project, err
}

// RaiseRisk records a risk on a project (change.manage on the project).
func (s *Service) RaiseRisk(ctx context.Context, p auth.Principal, in RiskInput) (string, error) {
	if err := in.RiskAssessment.valid(); err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermChangeManage, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "RSK")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO project_risks (project_id, number, title, category, cause, effect, owner, response, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id::text`, in.ProjectID, num, strings.TrimSpace(in.Title), strings.ToUpper(strings.TrimSpace(in.Category)),
			strings.TrimSpace(in.Cause), strings.TrimSpace(in.Effect), strings.TrimSpace(in.Owner), strings.TrimSpace(in.Response), p.UserID)
		if err != nil {
			return err
		}
		if err := insertAssessment(ctx, tx, p, id, in.RiskAssessment); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "risk.raise", "project_risk", id, "risk.raised", map[string]any{"id": id, "number": num})
	})
	return id, err
}

func insertAssessment(ctx context.Context, tx pgx.Tx, p auth.Principal, risk string, a RiskAssessment) error {
	_, err := tx.Exec(ctx, `INSERT INTO risk_assessments (risk_id, probability, cost_impact, schedule_days, note, assessed_by) VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''), $6)`,
		risk, a.Probability.Round(4), a.CostImpact.Round(6), a.ScheduleDays, strings.TrimSpace(a.Note), p.UserID)
	return err
}

// AssessRisk records a new assessment of an open risk; earlier ones are kept.
func (s *Service) AssessRisk(ctx context.Context, p auth.Principal, id string, a RiskAssessment) error {
	if err := a.valid(); err != nil {
		return err
	}
	project, err := s.riskProject(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, auth.PermChangeManage, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := insertAssessment(ctx, tx, p, id, a); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "risk.assess", "project_risk", id, "risk.assessed", map[string]any{"id": id, "probability": a.Probability.String(), "cost_impact": a.CostImpact.String()})
	})
}

// CloseRisk closes a risk with a note; a closed risk never changes.
func (s *Service) CloseRisk(ctx context.Context, p auth.Principal, id, note string) error {
	if err := requireReason(note); err != nil {
		return err
	}
	project, err := s.riskProject(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, auth.PermChangeManage, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE project_risks SET status = 'CLOSED', closed_by = $2, close_note = $3 WHERE id = $1::uuid AND status = 'OPEN'`, id, p.UserID, strings.TrimSpace(note)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "risk.close", "project_risk", id, "risk.closed", map[string]any{"id": id})
	})
}

// RiskRow is a risk at its latest assessment.
type RiskRow struct {
	ID           string          `json:"id"`
	Number       string          `json:"number"`
	Title        string          `json:"title"`
	Category     string          `json:"category"`
	Owner        string          `json:"owner"`
	Status       string          `json:"status"`
	Probability  decimal.Decimal `json:"probability"`
	CostImpact   decimal.Decimal `json:"cost_impact"`
	ScheduleDays int             `json:"schedule_days"`
	Exposure     decimal.Decimal `json:"exposure"`
	AssessedAt   time.Time       `json:"assessed_at"`
}

// RiskRegister is a project's risks with the comparison against contingency.
type RiskRegister struct {
	ProjectID            string          `json:"project_id"`
	Risks                []RiskRow       `json:"risks"`
	OpenExposure         decimal.Decimal `json:"open_exposure"`
	OpenScheduleDays     int             `json:"open_schedule_days_max"`
	ContingencyRemaining decimal.Decimal `json:"contingency_remaining"`
	Cover                decimal.Decimal `json:"cover"` // contingency remaining - open exposure
	Shortfall            bool            `json:"shortfall"`
}

// ProjectRiskRegister returns the register of a project the reader can see.
func (s *Service) ProjectRiskRegister(ctx context.Context, p auth.Principal, project string) (RiskRegister, error) {
	out := RiskRegister{ProjectID: project, Risks: []RiskRow{}}
	if err := s.authz.RequireProject(p, auth.PermP2PRead, p.TenantID, project); err != nil {
		return out, err
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT risk_id::text, number, title, category, owner, status, probability, cost_impact, schedule_days, exposure, assessed_at
			FROM project_risk_exposure WHERE project_id = $1::uuid ORDER BY status DESC, exposure DESC, number`, project)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RiskRow
			if err := rows.Scan(&r.ID, &r.Number, &r.Title, &r.Category, &r.Owner, &r.Status, &r.Probability, &r.CostImpact, &r.ScheduleDays, &r.Exposure, &r.AssessedAt); err != nil {
				return err
			}
			if r.Status == "OPEN" {
				out.OpenExposure = out.OpenExposure.Add(r.Exposure)
				if r.ScheduleDays > out.OpenScheduleDays {
					out.OpenScheduleDays = r.ScheduleDays
				}
			}
			out.Risks = append(out.Risks, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(sum(current), 0) FROM project_budget_current WHERE project_id = $1::uuid AND is_contingency`, project).Scan(&out.ContingencyRemaining)
	})
	out.Cover = out.ContingencyRemaining.Sub(out.OpenExposure)
	out.Shortfall = out.Cover.IsNegative()
	return out, err
}
