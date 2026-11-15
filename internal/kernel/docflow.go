// Transmittals and submittals on the controlled documents (0077; rebuilt from new-version 0066, ADR 0025).
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
	// ResubmissionOf names the submittal reviewed C or D that this one answers (0090).
	ResubmissionOf string `json:"resubmission_of,omitempty"`
}

// SubmitSubmittal records a submittal for review.
func (s *Service) SubmitSubmittal(ctx context.Context, p auth.Principal, in SubmittalInput) (string, error) {
	var id string
	err := s.documentTx(ctx, p, auth.PermDocumentManage, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "SBM")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO submittals (project_id, number, spec_section, title, created_by, resubmission_of) VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, '')::uuid) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.SpecSection), strings.TrimSpace(in.Title), p.UserID, in.ResubmissionOf)
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

// SubmittalCycle lists the review cycle a submittal belongs to, first
// submission first: each entry is answered by the next (0090).
func (s *Service) SubmittalCycle(ctx context.Context, p auth.Principal, id string) ([]map[string]any, error) {
	var project string
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT project_id::text FROM submittals WHERE id = $1::uuid`, id).Scan(&project))
	}); err != nil {
		return nil, err
	}
	var out []map[string]any
	err := s.documentTx(ctx, p, auth.PermP2PRead, project, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = rowsAsMaps(ctx, tx, `WITH RECURSIVE up AS (
			  SELECT x.* FROM submittals x WHERE x.id = $1::uuid
			  UNION ALL SELECT p.* FROM submittals p JOIN up ON up.resubmission_of = p.id),
			first AS (SELECT id FROM up WHERE resubmission_of IS NULL),
			down AS (
			  SELECT x.* FROM submittals x WHERE x.id = (SELECT id FROM first)
			  UNION ALL SELECT n.* FROM submittals n JOIN down ON n.resubmission_of = down.id)
			SELECT id::text AS id, number, cycle, status, COALESCE(review_code, '') AS review_code, COALESCE(review_note, '') AS review_note, title, created_by
			  FROM down ORDER BY cycle`, id)
		return err
	})
	return out, err
}

// RFIImpact is the cost and time an RFI caused, through the change events
// raised from it (0090): computed from their lines, never typed.
func (s *Service) RFIImpact(ctx context.Context, p auth.Principal, id string) (map[string]any, error) {
	var project string
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT project_id::text FROM rfis WHERE id = $1::uuid`, id).Scan(&project))
	}); err != nil {
		return nil, err
	}
	out := map[string]any{"rfi_id": id}
	err := s.documentTx(ctx, p, auth.PermP2PRead, project, func(ctx context.Context, tx pgx.Tx) error {
		events, err := rowsAsMaps(ctx, tx, `SELECT change_event_id::text AS id, number, status, cost_impact, schedule_impact_days, revenue_impact
			FROM rfi_impact WHERE rfi_id = $1::uuid ORDER BY number`, id)
		if err != nil {
			return err
		}
		out["change_events"] = events
		var cost, revenue decimal.Decimal
		var days int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(cost_impact), 0), COALESCE(sum(schedule_impact_days), 0), COALESCE(sum(revenue_impact), 0)
			FROM rfi_impact WHERE rfi_id = $1::uuid AND status <> 'REJECTED'`, id).Scan(&cost, &days, &revenue); err != nil {
			return err
		}
		out["cost_impact"], out["schedule_impact_days"], out["revenue_impact"] = cost, days, revenue
		return nil
	})
	return out, err
}
