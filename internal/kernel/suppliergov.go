// Supplier qualification, conflict declarations and performance scores (SUP-03.3; migration 0093).
package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// SupplierQualificationInput records a qualification from the document issued.
type SupplierQualificationInput struct {
	SupplierID string `json:"supplier_id"`
	Category   string `json:"category"`
	Reference  string `json:"reference"`
	ValidFrom  string `json:"valid_from"`
	ValidTo    string `json:"valid_to"`
}

// RecordSupplierQualification records a PENDING qualification (supplier.manage).
func (s *Service) RecordSupplierQualification(ctx context.Context, p auth.Principal, in SupplierQualificationInput) (string, error) {
	from, err := parseDay("valid_from", in.ValidFrom)
	if err != nil {
		return "", err
	}
	to, err := parseDay("valid_to", in.ValidTo)
	if err != nil {
		return "", err
	}
	if !to.After(from) {
		return "", fmt.Errorf("%w: a qualification ends after it starts", ErrInvalidInput)
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermSupplierManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermSupplierManage, "supplier", in.SupplierID); err != nil {
			return err
		}
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO supplier_qualifications (supplier_id, category, reference, valid_from, valid_to, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`, in.SupplierID, strings.TrimSpace(in.Category), strings.TrimSpace(in.Reference), from, to, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "supplier_qualification.record", "supplier_qualification", id, "supplier.qualification_recorded", map[string]any{"id": id, "supplier_id": in.SupplierID})
	})
	return id, err
}

// DecideSupplierQualification approves or rejects a qualification (supplier.approve, not its recorder; attested).
func (s *Service) DecideSupplierQualification(ctx context.Context, p auth.Principal, id string, approve bool, note string) error {
	outcome := "APPROVED"
	if !approve {
		outcome = "REJECTED"
		if len(strings.TrimSpace(note)) < 5 {
			return fmt.Errorf("%w: rejecting needs a reason", ErrInvalidInput)
		}
	}
	return s.p2pTx(ctx, p, auth.PermSupplierApprove, func(ctx context.Context, tx pgx.Tx) error {
		var creator, status string
		if err := mapNoRows(tx.QueryRow(ctx, `SELECT created_by, status FROM supplier_qualifications WHERE id = $1::uuid FOR UPDATE`, id).Scan(&creator, &status)); err != nil {
			return err
		}
		if status != "PENDING" {
			return fmt.Errorf("%w: the qualification was already decided", ErrWorkflowState)
		}
		if err := auth.CheckMakerChecker(p, creator, "supplier qualification"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "supplier_qualification.decide", "supplier_qualification", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE supplier_qualifications SET status = $2, approved_by = $3, decision_note = NULLIF($4, '') WHERE id = $1::uuid`,
			id, outcome, p.UserID, strings.TrimSpace(note)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "supplier_qualification.decide", "supplier_qualification", id, "supplier.qualification_"+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// DeclareRFQConflict records, for the caller, either no conflict of interest on
// an RFQ (supplierID empty) or a conflict with one of its suppliers. It is
// never changed or withdrawn.
func (s *Service) DeclareRFQConflict(ctx context.Context, p auth.Principal, rfqID, supplierID, relationship string) error {
	if p.Kind != auth.KindHuman {
		return auth.ErrForbidden
	}
	if (supplierID == "") != (strings.TrimSpace(relationship) == "") {
		return fmt.Errorf("%w: a conflict names the supplier and the relationship; no conflict names neither", ErrInvalidInput)
	}
	return s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermP2PRead, "rfq", rfqID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO rfq_conflict_declarations (rfq_id, declared_by, supplier_id, relationship) VALUES ($1::uuid, $2, NULLIF($3, '')::uuid, NULLIF($4, ''))`,
			rfqID, p.UserID, supplierID, strings.TrimSpace(relationship)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rfq.declare", "rfq", rfqID, "rfq.conflict_declared", map[string]any{"rfq_id": rfqID, "supplier_id": supplierID})
	})
}

// RFQDeclarations lists the conflict declarations made on an RFQ.
func (s *Service) RFQDeclarations(ctx context.Context, p auth.Principal, rfqID string) ([]map[string]any, error) {
	var out []map[string]any
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermP2PRead, "rfq", rfqID); err != nil {
			return err
		}
		var err error
		out, err = rowsAsMaps(ctx, tx, `SELECT d.declared_by, COALESCE(s.code || ' ' || s.name, '') AS supplier, COALESCE(d.relationship, '') AS relationship, d.declared_at
			FROM rfq_conflict_declarations d LEFT JOIN suppliers s ON s.id = d.supplier_id WHERE d.rfq_id = $1::uuid ORDER BY d.declared_at`, rfqID)
		return err
	})
	return out, err
}

// ScoreSupplier scores an order's supplier once, after receipt (not by the
// order's approver): quality, delivery and safety from 1 to 5.
func (s *Service) ScoreSupplier(ctx context.Context, p auth.Principal, poID string, quality, delivery, safety int, note string) error {
	for _, v := range []int{quality, delivery, safety} {
		if v < 1 || v > 5 {
			return fmt.Errorf("%w: scores run from 1 to 5", ErrInvalidInput)
		}
	}
	return s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if !s.authz.Has(p, auth.PermGRNConfirm) && !s.authz.Has(p, auth.PermPOManage) && !s.authz.Has(p, auth.PermPOApprove) {
			return auth.ErrForbidden
		}
		if _, err := s.loadDoc(ctx, tx, p, auth.PermP2PRead, "purchase_order", poID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO supplier_scores (po_id, supplier_id, quality, delivery, safety, note, scored_by)
			SELECT id, supplier_id, $2, $3, $4, NULLIF($5, ''), $6 FROM purchase_orders WHERE id = $1::uuid`, poID, quality, delivery, safety, strings.TrimSpace(note), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "supplier.score", "purchase_order", poID, "supplier.scored", map[string]any{"po_id": poID, "quality": quality, "delivery": delivery, "safety": safety})
	})
}

// SupplierStanding is a supplier's qualification today and its average scores.
type SupplierStanding struct {
	SupplierID     string           `json:"supplier_id"`
	QualifiedToday bool             `json:"qualified_today"`
	Qualifications []map[string]any `json:"qualifications"`
	Scores         int              `json:"scores"`
	Quality        *float64         `json:"quality"`
	Delivery       *float64         `json:"delivery"`
	Safety         *float64         `json:"safety"`
}

// SupplierStandingOf returns a supplier's qualifications and performance.
func (s *Service) SupplierStandingOf(ctx context.Context, p auth.Principal, supplierID string) (SupplierStanding, error) {
	out := SupplierStanding{SupplierID: supplierID, Qualifications: []map[string]any{}}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermP2PRead, "supplier", supplierID); err != nil {
			return err
		}
		var err error
		if out.Qualifications, err = rowsAsMaps(ctx, tx, `SELECT id::text AS id, category, reference, valid_from::text AS valid_from, valid_to::text AS valid_to, status,
			created_by, COALESCE(approved_by, '') AS approved_by, COALESCE(decision_note, '') AS decision_note,
			(status = 'APPROVED' AND current_date BETWEEN valid_from AND valid_to) AS valid_today
			FROM supplier_qualifications WHERE supplier_id = $1::uuid ORDER BY valid_to DESC`, supplierID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT supplier_qualified($1::uuid, $2::date), count(*), round(avg(quality), 2)::float8, round(avg(delivery), 2)::float8, round(avg(safety), 2)::float8
			FROM supplier_scores WHERE supplier_id = $1::uuid`, supplierID, time.Now().UTC()).Scan(&out.QualifiedToday, &out.Scores, &out.Quality, &out.Delivery, &out.Safety)
	})
	return out, err
}
