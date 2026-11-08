// Change events (WP-2.4; SRS PCC-07, CON-03).
//
// A change event records a change to the money or time of a project before it
// is agreed: its expected cost per cost code, its revenue impact and its
// schedule impact. One person (change.manage) prepares and submits it; a
// different person (change.approve, fresh MFA) decides it. Approval drafts a
// budget REVISION for the cost impact; that draft is still submitted and
// approved under budget control by other people (see budget.go). Migration:
// 0045_change_events.sql.
package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// ChangeEventLine is the expected cost impact on one cost code (signed: more cost is positive).
type ChangeEventLine struct {
	CostCodeID string          `json:"cost_code_id"`
	CostCode   string          `json:"cost_code,omitempty"`
	CostImpact decimal.Decimal `json:"cost_impact"`
	Note       string          `json:"note,omitempty"`
}

// ChangeEventInput creates a change event.
type ChangeEventInput struct {
	ProjectID    string            `json:"project_id"`
	Title        string            `json:"title"`
	Source       string            `json:"source"`
	Description  string            `json:"description"`
	RevenueImpct decimal.Decimal   `json:"revenue_impact"`
	ScheduleDays int               `json:"schedule_impact_days"`
	Lines        []ChangeEventLine `json:"lines"`
}

// ChangeEvent is a change event with its lines, totals and the budget draft it raised.
type ChangeEvent struct {
	ID            string            `json:"id"`
	Number        string            `json:"number"`
	ProjectID     string            `json:"project_id"`
	Title         string            `json:"title"`
	Source        string            `json:"source"`
	Description   string            `json:"description"`
	Status        string            `json:"status"`
	CreatedBy     string            `json:"created_by"`
	ApprovedBy    string            `json:"approved_by,omitempty"`
	Reason        string            `json:"decision_reason,omitempty"`
	RevenueImpact decimal.Decimal   `json:"revenue_impact"`
	ScheduleDays  int               `json:"schedule_impact_days"`
	CostImpact    decimal.Decimal   `json:"total_cost_impact"`
	Margin        decimal.Decimal   `json:"margin_impact"` // revenue impact - cost impact
	BudgetDraftID string            `json:"budget_version_id,omitempty"`
	Lines         []ChangeEventLine `json:"lines"`
}

var changeSources = map[string]bool{"RFI": true, "SITE_INSTRUCTION": true, "CLIENT": true, "DESIGN": true, "INTERNAL": true}

// CreateChangeEvent opens a DRAFT change event.
func (s *Service) CreateChangeEvent(ctx context.Context, p auth.Principal, in ChangeEventInput) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermChangeManage, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	in.Source = strings.ToUpper(strings.TrimSpace(in.Source))
	if !changeSources[in.Source] {
		return "", fmt.Errorf("%w: source must be RFI, SITE_INSTRUCTION, CLIENT, DESIGN or INTERNAL", ErrInvalidInput)
	}
	if len(strings.TrimSpace(in.Title)) < 3 || len(strings.TrimSpace(in.Description)) < 5 {
		return "", fmt.Errorf("%w: a title (3+ characters) and a description (5+ characters) are required", ErrInvalidInput)
	}
	seen := map[string]bool{}
	for _, l := range in.Lines {
		if l.CostImpact.IsZero() {
			return "", fmt.Errorf("%w: a cost line with no amount", ErrInvalidInput)
		}
		if seen[l.CostCodeID] {
			return "", fmt.Errorf("%w: a cost code appears twice", ErrInvalidInput)
		}
		seen[l.CostCodeID] = true
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "CE")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO change_events (project_id, number, title, source, description, revenue_impact, schedule_impact_days, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.Title), in.Source, strings.TrimSpace(in.Description), in.RevenueImpct, in.ScheduleDays, p.UserID)
		if err != nil {
			return err
		}
		for _, l := range in.Lines {
			if _, err := tx.Exec(ctx, `INSERT INTO change_event_lines (event_id, cost_code_id, cost_impact, note) VALUES ($1::uuid, $2::uuid, $3, NULLIF($4, ''))`,
				id, l.CostCodeID, l.CostImpact, l.Note); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "change.create", "change_event", id, "change.created", map[string]any{"change_event_id": id, "number": num, "project_id": in.ProjectID})
	})
	return id, err
}

// SubmitChangeEvent submits a draft (by its author).
func (s *Service) SubmitChangeEvent(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermChangeManage, "change_event", "change_events", id, "change.submit", "change.submitted")
}

// DecideChangeEvent approves or rejects a submitted change event (fresh MFA, not the author).
// Approving a change event with cost lines drafts the budget revision that carries them.
func (s *Service) DecideChangeEvent(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermChangeApprove, "change_event", "change_events", id, "change.decide", "change event", approve, reason,
		func(ctx context.Context, tx pgx.Tx) error {
			var project, number, title, author, currency string
			if err := tx.QueryRow(ctx, `SELECT e.project_id::text, e.number, e.title, e.created_by, le.functional_currency
				FROM change_events e JOIN projects pr ON pr.id = e.project_id JOIN legal_entities le ON le.id = pr.legal_entity_id WHERE e.id = $1::uuid`, id).
				Scan(&project, &number, &title, &author, &currency); err != nil {
				return mapNoRows(err)
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM change_event_lines WHERE event_id = $1::uuid`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return nil
			}
			reasonText := "Change event " + number + ": " + title
			if len(reasonText) > 500 {
				reasonText = reasonText[:500]
			}
			// Authored by the change's author, so that the author can submit it and a
			// different person approves it under budget control.
			bv, err := postgres.InsertReturningID(ctx, tx, `INSERT INTO budget_versions (project_id, kind, seq, reason, created_by)
				SELECT $1::uuid, 'REVISION', COALESCE((SELECT max(seq) FROM budget_versions WHERE project_id = $1::uuid), 0) + 1, $2, $3 RETURNING id::text`, project, reasonText, author)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO budget_lines (version_id, cost_code_id, amount, currency, contingency_flag)
				SELECT $1::uuid, cost_code_id, cost_impact, $3, false FROM change_event_lines WHERE event_id = $2::uuid`, bv, id, currency); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO change_event_budget (event_id, budget_version_id) VALUES ($1::uuid, $2::uuid)`, id, bv); err != nil {
				return err
			}
			return nil
		})
}

// GetChangeEvent returns a change event with its lines and totals.
func (s *Service) GetChangeEvent(ctx context.Context, p auth.Principal, id string) (ChangeEvent, error) {
	e := ChangeEvent{Lines: []ChangeEventLine{}}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "change_event", id, false); err != nil {
			return err
		}
		var by, reason, bv *string
		if err := tx.QueryRow(ctx, `SELECT e.id::text, e.number, e.project_id::text, e.title, e.source, e.description, e.status, e.created_by, e.approved_by, e.decision_reason,
			e.revenue_impact, e.schedule_impact_days, b.budget_version_id::text
			FROM change_events e LEFT JOIN change_event_budget b ON b.event_id = e.id WHERE e.id = $1::uuid`, id).
			Scan(&e.ID, &e.Number, &e.ProjectID, &e.Title, &e.Source, &e.Description, &e.Status, &e.CreatedBy, &by, &reason, &e.RevenueImpact, &e.ScheduleDays, &bv); err != nil {
			return mapNoRows(err)
		}
		e.ApprovedBy, e.Reason, e.BudgetDraftID = deref(by), deref(reason), deref(bv)
		rows, err := tx.Query(ctx, `SELECT l.cost_code_id::text, c.code, l.cost_impact, COALESCE(l.note, '') FROM change_event_lines l JOIN cost_codes c ON c.id = l.cost_code_id
			WHERE l.event_id = $1::uuid ORDER BY c.code`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l ChangeEventLine
			if err := rows.Scan(&l.CostCodeID, &l.CostCode, &l.CostImpact, &l.Note); err != nil {
				return err
			}
			e.CostImpact = e.CostImpact.Add(l.CostImpact)
			e.Lines = append(e.Lines, l)
		}
		e.Margin = e.RevenueImpact.Sub(e.CostImpact)
		return rows.Err()
	})
	return e, err
}
