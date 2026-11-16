// Correspondence register (CNX-05.1; migration 0085): letters, notices and
// e-mails exchanged with the project's parties, incoming and outgoing.
package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// CorrespondenceInput records one item. ReplyDueOn marks it as needing an
// answer; InReplyTo records it as the answer to an earlier item.
type CorrespondenceInput struct {
	ProjectID    string `json:"project_id"`
	ContractID   string `json:"contract_id,omitempty"`
	Direction    string `json:"direction"`
	Counterparty string `json:"counterparty"`
	Subject      string `json:"subject"`
	Reference    string `json:"reference,omitempty"`
	DatedOn      string `json:"dated_on"`
	ReplyDueOn   string `json:"reply_due_on,omitempty"`
	InReplyTo    string `json:"in_reply_to,omitempty"`
}

// RecordCorrespondence records an item (document.manage on its project). The
// item never changes afterwards; recording a reply marks the item it answers
// ANSWERED in the same transaction.
func (s *Service) RecordCorrespondence(ctx context.Context, p auth.Principal, in CorrespondenceInput) (string, error) {
	dir := strings.ToUpper(strings.TrimSpace(in.Direction))
	if !oneOf(dir, "IN", "OUT") {
		return "", fmt.Errorf("%w: direction is IN or OUT", ErrInvalidInput)
	}
	dated, err := parseDay("dated_on", in.DatedOn)
	if err != nil {
		return "", err
	}
	var due any
	if strings.TrimSpace(in.ReplyDueOn) != "" {
		d, err := parseDay("reply_due_on", in.ReplyDueOn)
		if err != nil {
			return "", err
		}
		if d.Before(dated) {
			return "", fmt.Errorf("%w: the reply is due on or after the item's date", ErrInvalidInput)
		}
		due = d
	}
	if len(strings.TrimSpace(in.Counterparty)) < 2 || len(strings.TrimSpace(in.Subject)) < 3 {
		return "", fmt.Errorf("%w: name the counterparty and the subject", ErrInvalidInput)
	}
	var id string
	err = s.documentTx(ctx, p, auth.PermDocumentManage, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		if in.InReplyTo != "" {
			var st string
			if err := mapNoRows(tx.QueryRow(ctx, `SELECT status FROM correspondence WHERE id = $1::uuid AND project_id = $2::uuid`, in.InReplyTo, in.ProjectID).Scan(&st)); err != nil {
				return err
			}
			if st != "AWAITING_REPLY" {
				return fmt.Errorf("%w: that item is not awaiting a reply", ErrWorkflowState)
			}
		}
		num, err := postgres.NextNumber(ctx, tx, "COR")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO correspondence (project_id, contract_id, number, direction, counterparty, subject, reference, dated_on, reply_due_on, in_reply_to, status, created_by)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6, NULLIF($7, ''), $8, $9::date, NULLIF($10, '')::uuid, CASE WHEN $9::date IS NULL THEN 'RECORDED' ELSE 'AWAITING_REPLY' END, $11) RETURNING id::text`,
			in.ProjectID, in.ContractID, num, dir, strings.TrimSpace(in.Counterparty), strings.TrimSpace(in.Subject), strings.TrimSpace(in.Reference), dated, due, in.InReplyTo, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "correspondence.record", "correspondence", id, "correspondence.recorded",
			map[string]any{"id": id, "number": num, "direction": dir, "in_reply_to": in.InReplyTo})
	})
	return id, err
}

// CorrespondenceRegister lists a project's correspondence, newest first, with
// what is overdue (an answer due before today and not yet recorded).
func (s *Service) CorrespondenceRegister(ctx context.Context, p auth.Principal, projectID string) ([]map[string]any, error) {
	var out []map[string]any
	err := s.documentTx(ctx, p, auth.PermP2PRead, projectID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = rowsAsMaps(ctx, tx, `SELECT c.id::text AS id, c.number, c.direction, c.counterparty, c.subject, COALESCE(c.reference, '') AS reference,
			c.dated_on::text AS dated_on, COALESCE(c.reply_due_on::text, '') AS reply_due_on, c.status,
			(c.status = 'AWAITING_REPLY' AND c.reply_due_on < current_date) AS overdue,
			COALESCE((SELECT number FROM contracts WHERE id = c.contract_id), '') AS contract,
			COALESCE((SELECT number FROM correspondence r WHERE r.id = c.in_reply_to), '') AS replies_to,
			COALESCE((SELECT number FROM correspondence r WHERE r.id = c.answered_by), '') AS answered_by, c.created_by
			FROM correspondence c WHERE c.project_id = $1::uuid ORDER BY c.dated_on DESC, c.number DESC`, projectID)
		return err
	})
	return out, err
}
