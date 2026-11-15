// Transmittals and submittals on the controlled documents (0077; rebuilt from new-version 0066, ADR 0025).
package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// TransmittalInput sends the approved revisions of documents to a party.
type TransmittalInput struct {
	ProjectID   string   `json:"project_id"`
	Subject     string   `json:"subject"`
	ToParty     string   `json:"to_party"`
	Purpose     string   `json:"purpose"`
	DocumentIDs []string `json:"document_ids"`
}

// SendTransmittal records the current approved revision of each document as
// sent. The transmittal and its items never change.
func (s *Service) SendTransmittal(ctx context.Context, p auth.Principal, in TransmittalInput) (string, error) {
	if len(in.DocumentIDs) == 0 || len(in.DocumentIDs) > 200 {
		return "", fmt.Errorf("%w: choose between 1 and 200 documents", ErrInvalidInput)
	}
	var id string
	err := s.documentTx(ctx, p, auth.PermDocumentManage, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "TRM")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO transmittals (project_id, number, subject, to_party, purpose, created_by) VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.Subject), strings.TrimSpace(in.ToParty), strings.ToUpper(strings.TrimSpace(in.Purpose)), p.UserID)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, d := range in.DocumentIDs {
			if seen[d] {
				continue
			}
			seen[d] = true
			var rev string
			if err := tx.QueryRow(ctx, `SELECT r.id::text FROM document_revisions r JOIN documents x ON x.id = r.document_id
				WHERE r.document_id = $1::uuid AND x.project_id = $2::uuid AND r.status = 'APPROVED'`, d, in.ProjectID).Scan(&rev); err != nil {
				return fmt.Errorf("%w: a chosen document has no approved revision on this project", ErrInvalidInput)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO transmittal_items (transmittal_id, revision_id) VALUES ($1::uuid, $2::uuid)`, id, rev); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "transmittal.send", "transmittal", id, "transmittal.sent", map[string]any{"id": id, "number": num, "documents": len(seen)})
	})
	return id, err
}

// SubmittalInput is a contractor submittal (shop drawings, samples, data) for review.
type SubmittalInput struct {
	ProjectID   string `json:"project_id"`
	SpecSection string `json:"spec_section"`
	Title       string `json:"title"`
}

// SubmitSubmittal records a submittal for review.
func (s *Service) SubmitSubmittal(ctx context.Context, p auth.Principal, in SubmittalInput) (string, error) {
	var id string
	err := s.documentTx(ctx, p, auth.PermDocumentManage, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "SBM")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO submittals (project_id, number, spec_section, title, created_by) VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.SpecSection), strings.TrimSpace(in.Title), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "submittal.submit", "submittal", id, "submittal.submitted", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// ReviewSubmittal records the review code (A approved, B approved as noted, C
// revise and resubmit, D rejected) by someone other than the submitter; the
// decision is attested and spent by the database.
func (s *Service) ReviewSubmittal(ctx context.Context, p auth.Principal, id, code, note string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !oneOf(code, "A", "B", "C", "D") {
		return fmt.Errorf("%w: the review code is A, B, C or D", ErrInvalidInput)
	}
	if (code == "C" || code == "D") && len(strings.TrimSpace(note)) < 5 {
		return fmt.Errorf("%w: code C or D needs a note", ErrInvalidInput)
	}
	var project, creator string
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT project_id::text, created_by FROM submittals WHERE id = $1::uuid`, id).Scan(&project, &creator))
	}); err != nil {
		return err
	}
	if err := auth.CheckMakerChecker(p, creator, "submittal"); err != nil {
		return err
	}
	return s.documentTx(ctx, p, auth.PermDocumentApprove, project, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.authorize(ctx, tx, p, "submittal.review", "submittal", id, code); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE submittals SET status = 'REVIEWED', review_code = $2, review_note = NULLIF($3, ''), reviewed_by = $4 WHERE id = $1::uuid AND status = 'SUBMITTED'`,
			id, code, strings.TrimSpace(note), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "submittal.review", "submittal", id, "submittal.reviewed", map[string]any{"id": id, "code": code})
	})
}
