// Progress rules of credit and installed quantities (WP-3.9; SRS EVM-03..05).
// Migration: 0067_credit_rules.sql.
//
// A rule of credit splits an activity into weighted steps; the site records
// completed steps; the database computes the percent complete; the planner
// turns the percentages into a draft schedule progress record.
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

// CreditStep is one weighted step.
type CreditStep struct {
	Name   string          `json:"name"`
	Weight decimal.Decimal `json:"weight"`
}

// CreditRuleIn prepares a rule of credit.
type CreditRuleIn struct {
	ProjectID    string       `json:"project_id"`
	ActivityCode string       `json:"activity_code"`
	Name         string       `json:"name"`
	Steps        []CreditStep `json:"steps"`
}

// CreateCreditRule prepares a DRAFT rule.
func (s *Service) CreateCreditRule(ctx context.Context, p auth.Principal, in CreditRuleIn) (string, error) {
	if len(in.Steps) < 2 {
		return "", fmt.Errorf("%w: a rule has at least two steps", ErrInvalidInput)
	}
	if err := s.authz.RequireProject(p, auth.PermCreditManage, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO credit_rules (project_id, activity_code, name, created_by) VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
			in.ProjectID, strings.ToUpper(strings.TrimSpace(in.ActivityCode)), strings.TrimSpace(in.Name), p.UserID)
		if err != nil {
			return err
		}
		for i, st := range in.Steps {
			if _, err := tx.Exec(ctx, `INSERT INTO credit_steps (rule_id, seq, name, weight) VALUES ($1::uuid, $2, $3, $4)`, id, i+1, strings.TrimSpace(st.Name), st.Weight); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "credit.rule.create", "credit_rule", id, "credit.rule_created", map[string]any{"id": id})
	})
	return id, err
}

func (s *Service) creditProject(ctx context.Context, p auth.Principal, id string) (string, error) {
	var project string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT project_id::text FROM credit_rules WHERE id = $1::uuid`, id).Scan(&project))
	})
	return project, err
}

// ApproveCreditRule approves a rule (not its preparer); an earlier approved rule
// of the same activity is retired.
func (s *Service) ApproveCreditRule(ctx context.Context, p auth.Principal, id string) error {
	project, err := s.creditProject(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, auth.PermCreditApprove, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE credit_rules SET status = 'RETIRED' WHERE status = 'APPROVED' AND project_id = $2::uuid AND activity_code = (SELECT activity_code FROM credit_rules WHERE id = $1::uuid)`, id, project); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE credit_rules SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid AND status = 'DRAFT'`, id, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "credit.rule.approve", "credit_rule", id, "credit.rule_approved", map[string]any{"id": id})
	})
}

// CompleteCreditStep records a step as completed.
func (s *Service) CompleteCreditStep(ctx context.Context, p auth.Principal, rule string, seq int, on string) error {
	day, err := parseDay("completed_on", on)
	if err != nil {
		return err
	}
	project, err := s.creditProject(ctx, p, rule)
	if err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, auth.PermCreditRecord, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO credit_completions (rule_id, seq, completed_on, recorded_by) VALUES ($1::uuid, $2, $3, $4)`, rule, seq, day, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "credit.step.complete", "credit_rule", rule, "credit.step_completed", map[string]any{"id": rule, "seq": seq})
	})
}

// InstalledIn records an installed quantity.
type InstalledIn struct {
	ProjectID    string          `json:"project_id"`
	ActivityCode string          `json:"activity_code"`
	Description  string          `json:"description"`
	Quantity     decimal.Decimal `json:"quantity"`
	Unit         string          `json:"unit"`
	InstalledOn  string          `json:"installed_on"`
}

// RecordInstalled records what was installed (insert-only).
func (s *Service) RecordInstalled(ctx context.Context, p auth.Principal, in InstalledIn) error {
	day, err := parseDay("installed_on", in.InstalledOn)
	if err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, auth.PermCreditRecord, p.TenantID, in.ProjectID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO installed_quantities (project_id, activity_code, description, quantity, unit, installed_on, recorded_by) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
			in.ProjectID, strings.ToUpper(strings.TrimSpace(in.ActivityCode)), strings.TrimSpace(in.Description), in.Quantity, strings.TrimSpace(in.Unit), day, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "credit.installed.record", "installed_quantity", in.ProjectID, "credit.installed_recorded", map[string]any{"project_id": in.ProjectID, "activity": in.ActivityCode})
	})
}

// ProgressFromRules turns the completed steps into a DRAFT progress record.
func (s *Service) ProgressFromRules(ctx context.Context, p auth.Principal, project, statusDate string) (string, error) {
	day, err := parseDay("status_date", statusDate)
	if err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermCreditManage, p.TenantID, project); err != nil {
		return "", err
	}
	var lines []ProgressLine
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.activity_code, credit_percent(r.id), (SELECT min(completed_on) FROM credit_completions c WHERE c.rule_id = r.id), (SELECT max(completed_on) FROM credit_completions c WHERE c.rule_id = r.id)
			FROM credit_rules r WHERE r.project_id = $1::uuid AND r.status = 'APPROVED' ORDER BY r.activity_code`, project)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code string
			var pc decimal.Decimal
			var first, last *time.Time
			if err := rows.Scan(&code, &pc, &first, &last); err != nil {
				return err
			}
			if pc.IsZero() {
				continue
			}
			l := ProgressLine{ActivityCode: code, PercentComplete: pc, ActualStart: first}
			if pc.Equal(decimal.NewFromInt(100)) {
				l.ActualFinish = last
			}
			lines = append(lines, l)
		}
		return rows.Err()
	})
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%w: no step has been completed under an approved rule", ErrInvalidInput)
	}
	return s.RecordProgress(ctx, p, project, day, lines)
}

// CreditView lists rules with their steps and percent, and installed quantities.
func (s *Service) CreditView(ctx context.Context, p auth.Principal) (map[string]any, error) {
	out := map[string]any{}
	if !s.authz.Has(p, auth.PermCreditManage) && !s.authz.Has(p, auth.PermCreditApprove) && !s.authz.Has(p, auth.PermCreditRecord) {
		return out, auth.ErrForbidden
	}
	wide, projects := s.scopeArgs(p)
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out["projects"], err = rowsAsMaps(ctx, tx, `SELECT id, code, name FROM projects WHERE $1 OR id = ANY($2::uuid[]) ORDER BY code`, wide, projects); err != nil {
			return err
		}
		if out["rules"], err = rowsAsMaps(ctx, tx, `SELECT r.id, pr.code AS project, r.project_id, r.activity_code, r.name, r.status, r.created_by, r.approved_by, credit_percent(r.id) AS percent,
			(SELECT json_agg(json_build_object('seq', s.seq, 'name', s.name, 'weight', s.weight, 'completed_on', (SELECT completed_on FROM credit_completions c WHERE c.rule_id = s.rule_id AND c.seq = s.seq)) ORDER BY s.seq) FROM credit_steps s WHERE s.rule_id = r.id) AS steps
			FROM credit_rules r JOIN projects pr ON pr.id = r.project_id WHERE ($1 OR r.project_id = ANY($2::uuid[])) ORDER BY pr.code, r.activity_code, r.created_at DESC`, wide, projects); err != nil {
			return err
		}
		out["installed"], err = rowsAsMaps(ctx, tx, `SELECT pr.code AS project, i.activity_code, i.description, i.quantity, i.unit, i.installed_on, i.recorded_by,
			sum(i.quantity) OVER (PARTITION BY i.project_id, i.activity_code, i.unit ORDER BY i.installed_on, i.recorded_at) AS cumulative
			FROM installed_quantities i JOIN projects pr ON pr.id = i.project_id WHERE ($1 OR i.project_id = ANY($2::uuid[])) ORDER BY i.installed_on DESC, i.recorded_at DESC LIMIT 200`, wide, projects)
		return err
	})
	return out, err
}
