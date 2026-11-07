// Budget control (WP-1.3; SRS PCC-01, PCC-02, EST-07; invariant R8).
//
// A project's budget changes only through three controlled documents:
//
//	budget version   ORIGINAL baseline or a REVISION (+/- per cost code)
//	budget transfer  money moved from one cost code to another
//	contingency drawdown  money moved out of a cost code that holds contingency
//
// Each is created DRAFT by its author, submitted by the same author, and
// approved or rejected by a different person holding budget.approve. Every
// step is attested (signed) and the database re-checks it: see migration
// 0039_budget_revisions.sql for the guards. The current budget is never
// stored; it is computed by the project_budget_current view and read by the
// cost report and by every requisition and purchase-order budget check.
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

// BudgetLine represents one cost code line in a budget version.
type BudgetLine struct {
	CostCodeID      string          `json:"cost_code_id"`
	Amount          decimal.Decimal `json:"amount"`
	ContingencyFlag bool            `json:"contingency_flag"`
}

// BudgetTransferRequest holds fields to request an inter-cost-code budget transfer.
type BudgetTransferRequest struct {
	ProjectID    string          `json:"project_id"`
	FromCostCode string          `json:"from_cost_code"`
	ToCostCode   string          `json:"to_cost_code"`
	Amount       decimal.Decimal `json:"amount"`
	Reason       string          `json:"reason"`
}

// ContingencyDrawdownRequest moves money out of a contingency cost code.
// FromCostCode may be empty when the project has exactly one contingency code.
type ContingencyDrawdownRequest struct {
	ProjectID    string          `json:"project_id"`
	FromCostCode string          `json:"from_cost_code,omitempty"`
	ToCostCode   string          `json:"to_cost_code"`
	Amount       decimal.Decimal `json:"amount"`
	Reason       string          `json:"reason"`
}

// BudgetCurrentPosition is one row of project_budget_current: how a cost
// code's current budget is made up.
type BudgetCurrentPosition struct {
	CostCodeID        string          `json:"cost_code_id"`
	CostCode          string          `json:"cost_code"`
	Original          decimal.Decimal `json:"original"`
	ApprovedRevisions decimal.Decimal `json:"approved_revisions"`
	TransfersIn       decimal.Decimal `json:"transfers_in"`
	TransfersOut      decimal.Decimal `json:"transfers_out"`
	DrawdownsIn       decimal.Decimal `json:"drawdowns_in"`
	DrawdownsOut      decimal.Decimal `json:"drawdowns_out"`
	Current           decimal.Decimal `json:"current"`
	IsContingency     bool            `json:"is_contingency"` // the code holds contingency; drawdowns come from it
}

// CreateBudgetRevision creates a DRAFT budget version (ORIGINAL or REVISION) with lines.
func (s *Service) CreateBudgetRevision(ctx context.Context, p auth.Principal, projectID, kind, reason string, lines []BudgetLine) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermBudgetRevise, p.TenantID, projectID); err != nil {
		return "", err
	}
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if kind != "ORIGINAL" && kind != "REVISION" {
		return "", fmt.Errorf("%w: kind must be ORIGINAL or REVISION", ErrInvalidInput)
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%w: budget revision requires lines", ErrInvalidInput)
	}
	if len(strings.TrimSpace(reason)) < 3 {
		return "", fmt.Errorf("%w: reason must be at least 3 characters", ErrInvalidInput)
	}

	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var currency string
		err := tx.QueryRow(ctx, `SELECT le.functional_currency FROM projects pr JOIN legal_entities le ON le.id = pr.legal_entity_id WHERE pr.id = $1::uuid`, projectID).Scan(&currency)
		if err != nil {
			return mapNoRows(err)
		}

		id, err = postgres.InsertReturningID(ctx, tx, `
			INSERT INTO budget_versions (project_id, kind, seq, reason, created_by)
			SELECT $1::uuid, $2, COALESCE((SELECT max(seq) FROM budget_versions WHERE project_id = $1::uuid), 0) + 1, $3, $4
			RETURNING id::text`,
			projectID, kind, reason, p.UserID)
		if err != nil {
			return err
		}

		for _, l := range lines {
			if _, err := tx.Exec(ctx, `
				INSERT INTO budget_lines (version_id, cost_code_id, amount, currency, contingency_flag)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
				id, l.CostCodeID, l.Amount, currency, l.ContingencyFlag); err != nil {
				return err
			}
		}

		return s.record(ctx, tx, p, "budget.revision.create", "budget_version", id, "budget.revision.created", map[string]any{
			"budget_version_id": id,
			"project_id":        projectID,
			"kind":              kind,
			"lines_count":       len(lines),
		})
	})
	return id, err
}

// SubmitBudgetRevision submits a draft budget revision for approval.
func (s *Service) SubmitBudgetRevision(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermBudgetRevise, "budget_version", "budget_versions", id, "budget.revision.submit", "budget.revision.submitted")
}

// DecideBudgetRevision approves or rejects a submitted budget revision.
func (s *Service) DecideBudgetRevision(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	}
	return s.p2pTx(ctx, p, auth.PermBudgetApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermBudgetApprove, "budget_version", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "budget revision"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		action := "budget.revision.decide"
		if err := s.authorize(ctx, tx, p, action, "budget_version", id, outcome); err != nil {
			return err
		}
		if approve {
			err = postgres.Exec1(ctx, tx, `UPDATE budget_versions SET status = 'APPROVED', approved_by = $2, decision_reason = NULLIF($3, '') WHERE id = $1::uuid`, id, p.UserID, reason)
		} else {
			err = postgres.Exec1(ctx, tx, `UPDATE budget_versions SET status = 'REJECTED', approved_by = $2, decision_reason = $3 WHERE id = $1::uuid`, id, p.UserID, reason)
		}
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, "budget_version", id, "budget.revision."+strings.ToLower(outcome), map[string]any{
			"budget_version_id": id,
			"reason":            reason,
		})
	})
}

// RequestBudgetTransfer creates a DRAFT budget transfer request.
func (s *Service) RequestBudgetTransfer(ctx context.Context, p auth.Principal, req BudgetTransferRequest) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermBudgetTransfer, p.TenantID, req.ProjectID); err != nil {
		return "", err
	}
	if req.FromCostCode == req.ToCostCode {
		return "", fmt.Errorf("%w: source and destination cost codes must be different", ErrInvalidInput)
	}
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return "", fmt.Errorf("%w: transfer amount must be positive", ErrInvalidInput)
	}
	if len(strings.TrimSpace(req.Reason)) < 3 {
		return "", fmt.Errorf("%w: reason must be at least 3 characters", ErrInvalidInput)
	}

	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var currency string
		err := tx.QueryRow(ctx, `SELECT le.functional_currency FROM projects pr JOIN legal_entities le ON le.id = pr.legal_entity_id WHERE pr.id = $1::uuid`, req.ProjectID).Scan(&currency)
		if err != nil {
			return mapNoRows(err)
		}

		id, err = postgres.InsertReturningID(ctx, tx, `
			INSERT INTO budget_transfers (project_id, from_cost_code, to_cost_code, amount, currency, reason, requested_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7)
			RETURNING id::text`,
			req.ProjectID, req.FromCostCode, req.ToCostCode, req.Amount, currency, req.Reason, p.UserID)
		if err != nil {
			return err
		}

		return s.record(ctx, tx, p, "budget.transfer.create", "budget_transfer", id, "budget.transfer.created", map[string]any{
			"budget_transfer_id": id,
			"project_id":         req.ProjectID,
			"amount":             req.Amount,
		})
	})
	return id, err
}

// SubmitBudgetTransfer submits a draft budget transfer for approval.
func (s *Service) SubmitBudgetTransfer(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermBudgetTransfer, "budget_transfer", "budget_transfers", id, "budget.transfer.submit", "budget.transfer.submitted")
}

// DecideBudgetTransfer approves or rejects a submitted budget transfer.
func (s *Service) DecideBudgetTransfer(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	}
	return s.p2pTx(ctx, p, auth.PermBudgetApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermBudgetApprove, "budget_transfer", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "budget transfer"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		action := "budget.transfer.decide"
		if err := s.authorize(ctx, tx, p, action, "budget_transfer", id, outcome); err != nil {
			return err
		}
		if approve {
			err = postgres.Exec1(ctx, tx, `UPDATE budget_transfers SET status = 'APPROVED', approved_by = $2, decision_reason = NULLIF($3, '') WHERE id = $1::uuid`, id, p.UserID, reason)
		} else {
			err = postgres.Exec1(ctx, tx, `UPDATE budget_transfers SET status = 'REJECTED', approved_by = $2, decision_reason = $3 WHERE id = $1::uuid`, id, p.UserID, reason)
		}
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, "budget_transfer", id, "budget.transfer."+strings.ToLower(outcome), map[string]any{
			"budget_transfer_id": id,
			"reason":             reason,
		})
	})
}

// RequestContingencyDrawdown creates a DRAFT contingency drawdown request.
func (s *Service) RequestContingencyDrawdown(ctx context.Context, p auth.Principal, req ContingencyDrawdownRequest) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermBudgetRevise, p.TenantID, req.ProjectID); err != nil {
		return "", err
	}
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return "", fmt.Errorf("%w: drawdown amount must be positive", ErrInvalidInput)
	}
	if req.FromCostCode != "" && req.FromCostCode == req.ToCostCode {
		return "", fmt.Errorf("%w: a drawdown moves money to a different cost code", ErrInvalidInput)
	}
	if len(strings.TrimSpace(req.Reason)) < 3 {
		return "", fmt.Errorf("%w: reason must be at least 3 characters", ErrInvalidInput)
	}

	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var currency string
		err := tx.QueryRow(ctx, `SELECT le.functional_currency FROM projects pr JOIN legal_entities le ON le.id = pr.legal_entity_id WHERE pr.id = $1::uuid`, req.ProjectID).Scan(&currency)
		if err != nil {
			return mapNoRows(err)
		}

		// Without an explicit source, use the project's only contingency code.
		if req.FromCostCode == "" {
			var codes []string
			rows, err := tx.Query(ctx, `SELECT cost_code_id::text FROM project_budget_current WHERE project_id = $1::uuid AND is_contingency`, req.ProjectID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var c string
				if err := rows.Scan(&c); err != nil {
					rows.Close()
					return err
				}
				codes = append(codes, c)
			}
			rows.Close()
			if len(codes) != 1 {
				return fmt.Errorf("%w: the project has %d contingency cost codes; name the one to draw from", ErrInvalidInput, len(codes))
			}
			req.FromCostCode = codes[0]
		}
		id, err = postgres.InsertReturningID(ctx, tx, `
			INSERT INTO contingency_drawdowns (project_id, from_cost_code, to_cost_code, amount, currency, reason, requested_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7)
			RETURNING id::text`,
			req.ProjectID, req.FromCostCode, req.ToCostCode, req.Amount, currency, req.Reason, p.UserID)
		if err != nil {
			return err
		}

		return s.record(ctx, tx, p, "contingency.drawdown.create", "contingency_drawdown", id, "contingency.drawdown.created", map[string]any{
			"contingency_drawdown_id": id,
			"project_id":              req.ProjectID,
			"amount":                  req.Amount,
		})
	})
	return id, err
}

// SubmitContingencyDrawdown submits a draft contingency drawdown for approval.
func (s *Service) SubmitContingencyDrawdown(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermBudgetRevise, "contingency_drawdown", "contingency_drawdowns", id, "contingency.drawdown.submit", "contingency.drawdown.submitted")
}

// DecideContingencyDrawdown approves or rejects a submitted contingency drawdown.
func (s *Service) DecideContingencyDrawdown(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	}
	return s.p2pTx(ctx, p, auth.PermBudgetApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermBudgetApprove, "contingency_drawdown", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "contingency drawdown"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		action := "contingency.drawdown.decide"
		if err := s.authorize(ctx, tx, p, action, "contingency_drawdown", id, outcome); err != nil {
			return err
		}
		if approve {
			err = postgres.Exec1(ctx, tx, `UPDATE contingency_drawdowns SET status = 'APPROVED', approved_by = $2, decision_reason = NULLIF($3, '') WHERE id = $1::uuid`, id, p.UserID, reason)
		} else {
			err = postgres.Exec1(ctx, tx, `UPDATE contingency_drawdowns SET status = 'REJECTED', approved_by = $2, decision_reason = $3 WHERE id = $1::uuid`, id, p.UserID, reason)
		}
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, "contingency_drawdown", id, "contingency.drawdown."+strings.ToLower(outcome), map[string]any{
			"contingency_drawdown_id": id,
			"reason":                  reason,
		})
	})
}

// GetProjectBudgetCurrent returns the current budget positions for a project.
func (s *Service) GetProjectBudgetCurrent(ctx context.Context, p auth.Principal, projectID string) ([]BudgetCurrentPosition, error) {
	if !s.canSee(p, "budget_version", projectID) {
		return nil, ErrNotFound
	}
	var out []BudgetCurrentPosition
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT pbc.cost_code_id::text, c.code, pbc.original, pbc.approved_revisions, pbc.transfers_in, pbc.transfers_out,
			       pbc.drawdowns_in, pbc.drawdowns_out, pbc.current, pbc.is_contingency
			  FROM project_budget_current pbc
			  JOIN cost_codes c ON c.id = pbc.cost_code_id
			 WHERE pbc.project_id = $1::uuid
			 ORDER BY c.code`, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pos BudgetCurrentPosition
			if err := rows.Scan(&pos.CostCodeID, &pos.CostCode, &pos.Original, &pos.ApprovedRevisions, &pos.TransfersIn, &pos.TransfersOut, &pos.DrawdownsIn, &pos.DrawdownsOut, &pos.Current, &pos.IsContingency); err != nil {
				return err
			}
			out = append(out, pos)
		}
		return rows.Err()
	})
	return out, err
}
