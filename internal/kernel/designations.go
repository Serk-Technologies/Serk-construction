package kernel

// Approval of tax and intercompany account designations (DEF-015; migration
// 0081). A designation decides what a tax return or a consolidation run is
// computed from; a person who did not record it approves it, attested.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Service) approveDesignation(ctx context.Context, p auth.Principal, perm auth.Permission, kind, table, id string,
	txFn func(context.Context, auth.Principal, auth.Permission, func(context.Context, pgx.Tx) error) error) error {
	return txFn(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		var creator, status string
		if err := mapNoRows(tx.QueryRow(ctx, `SELECT created_by, status FROM `+table+` WHERE id = $1::uuid`, id).Scan(&creator, &status)); err != nil {
			return err
		}
		if status != "PENDING" {
			return fmt.Errorf("%w: the designation is already approved", ErrWorkflowState)
		}
		if err := auth.CheckMakerChecker(p, creator, "designation"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, kind+".approve", kind, id, "APPROVED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE `+table+` SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, id, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, kind+".approve", kind, id, kind+".approved", map[string]any{"id": id})
	})
}

// ApproveTaxAccount approves a tax account designation (tax.approve, not the person who recorded it).
func (s *Service) ApproveTaxAccount(ctx context.Context, p auth.Principal, id string) error {
	return s.approveDesignation(ctx, p, auth.PermTaxApprove, "tax_account", "tax_accounts", id, s.taxTx)
}

// ApproveIntercompany approves an intercompany designation (consolidation.approve, not the person who recorded it).
func (s *Service) ApproveIntercompany(ctx context.Context, p auth.Principal, id string) error {
	return s.approveDesignation(ctx, p, auth.PermConsolidationApprove, "intercompany_account", "intercompany_accounts", id, s.consTx)
}
