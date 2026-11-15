// Paying subcontract certificates and subcontractor documents (WP-2.5 part 2;
// SRS SUB-06, SUB-07, TRS-01). Migration: 0056_subcontract_payments.sql.
//
// A treasury officer prepares a payment allocated to certified certificates;
// finance approves it; a different treasury officer releases it, which
// generates the SUB_PAY journal. Expired required documents of the
// subcontractor, and "pay when certified" without a client certificate for
// the period, are refused by the database at approval and release.
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

// SubDocumentInput registers a subcontractor document.
type SubDocumentInput struct {
	SupplierID string `json:"supplier_id"`
	DocType    string `json:"doc_type"`
	Reference  string `json:"reference"`
	IssuedOn   string `json:"issued_on"`
	ExpiresOn  string `json:"expires_on"`
	Required   bool   `json:"required"`
}

// SubPaymentInput prepares a payment of certificates.
type SubPaymentInput struct {
	SubcontractID  string `json:"subcontract_id"`
	SupplierBankID string `json:"supplier_bank_id"`
	CompanyBankID  string `json:"company_bank_id"`
	ValueDate      string `json:"value_date"`
	Allocations    []struct {
		ApplicationID string          `json:"application_id"`
		Amount        decimal.Decimal `json:"amount"`
	} `json:"allocations"`
}

// RegisterSubDocument records a document of a subcontractor (subcontract.manage).
func (s *Service) RegisterSubDocument(ctx context.Context, p auth.Principal, in SubDocumentInput) (string, error) {
	in.DocType = strings.ToUpper(strings.TrimSpace(in.DocType))
	issued, err := parseDay("issued_on", in.IssuedOn)
	if err != nil {
		return "", err
	}
	expires, err := parseDay("expires_on", in.ExpiresOn)
	if err != nil {
		return "", err
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermSubcontractManage, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO subcontractor_documents (supplier_id, doc_type, reference, issued_on, expires_on, required, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7) RETURNING id::text`, in.SupplierID, in.DocType, strings.TrimSpace(in.Reference), issued, expires, in.Required, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "subdoc.create", "subcontractor_document", id, "subcontractor_document.registered", map[string]any{"id": id, "supplier_id": in.SupplierID, "doc_type": in.DocType})
	})
	return id, err
}

// SubDocuments lists a subcontractor's documents with their standing.
func (s *Service) SubDocuments(ctx context.Context, p auth.Principal, supplierID string) ([]map[string]any, error) {
	out := []map[string]any{}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (SELECT d.id, d.doc_type, d.reference, d.issued_on, d.expires_on, d.required, d.created_by,
			CASE WHEN d.expires_on < current_date THEN 'EXPIRED' WHEN d.expires_on - current_date <= 30 THEN 'EXPIRING' ELSE 'VALID' END AS standing,
			(d.expires_on - current_date) AS days_left
			FROM subcontractor_documents d WHERE d.supplier_id = $1::uuid ORDER BY d.doc_type, d.expires_on DESC) r`, supplierID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			m, err := jsonMap(raw)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// PrepareSubPayment prepares a DRAFT payment with its allocations.
func (s *Service) PrepareSubPayment(ctx context.Context, p auth.Principal, in SubPaymentInput) (string, error) {
	value, err := parseDay("value_date", in.ValueDate)
	if err != nil {
		return "", err
	}
	if len(in.Allocations) == 0 {
		return "", fmt.Errorf("%w: a payment pays at least one certificate", ErrInvalidInput)
	}
	var id string
	err = s.subTx(ctx, p, auth.PermPaymentPrepare, in.SubcontractID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "SPY")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO subcontract_payments (legal_entity_id, subcontract_id, number, supplier_bank_id, company_bank_id, currency, value_date, created_by)
			SELECT s.legal_entity_id, s.id, $2, $3::uuid, $4::uuid, s.currency, $5, $6 FROM subcontracts s WHERE s.id = $1::uuid RETURNING id::text`,
			in.SubcontractID, num, in.SupplierBankID, in.CompanyBankID, value, p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		for _, a := range in.Allocations {
			if _, err := tx.Exec(ctx, `INSERT INTO subcontract_payment_allocations (payment_id, application_id, amount) VALUES ($1::uuid, $2::uuid, $3)`, id, a.ApplicationID, a.Amount); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "subpay.create", "subcontract_payment", id, "subcontract_payment.prepared", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// SubmitSubPayment submits a draft payment (its preparer).
func (s *Service) SubmitSubPayment(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermPaymentPrepare, "subcontract_payment", "subcontract_payments", id, "subpay.submit", "subcontract_payment.submitted")
}

// DecideSubPayment approves or rejects a payment (finance; fresh MFA to approve).
func (s *Service) DecideSubPayment(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermPaymentApprove, "subcontract_payment", "subcontract_payments", id, "subpay.decide", "subcontract payment", approve, reason, nil)
}

// ReleaseSubPayment releases an approved payment (a third person, fresh MFA)
// and generates its SUB_PAY journal.
func (s *Service) ReleaseSubPayment(ctx context.Context, p auth.Principal, id string) (string, error) {
	if err := s.authz.RequireStepUp(p); err != nil {
		return "", err
	}
	var jid string
	err := s.p2pTx(ctx, p, auth.PermPaymentRelease, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermPaymentRelease, "subcontract_payment", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "APPROVED"); err != nil {
			return err
		}
		var approver, entity, ccy, num string
		var value time.Time
		if err := tx.QueryRow(ctx, `SELECT approved_by, legal_entity_id::text, currency, number, value_date FROM subcontract_payments WHERE id = $1::uuid`, id).Scan(&approver, &entity, &ccy, &num, &value); err != nil {
			return err
		}
		if p.UserID == d.CreatedBy || p.UserID == approver {
			return fmt.Errorf("%w: the releaser must differ from the preparer and the approver", auth.ErrSoD)
		}
		if err := s.authorize(ctx, tx, p, "subpay.release", "subcontract_payment", id, "RELEASED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE subcontract_payments SET status = 'RELEASED', released_by = $2 WHERE id = $1::uuid`, id, p.UserID); err != nil {
			return err
		}
		jid, err = s.subledgerJournal(ctx, tx, p, "SUB_PAY", id, entity, ccy, "Subcontract payment "+num, value)
		if err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE subcontract_payments SET release_journal_id = $2::uuid WHERE id = $1::uuid`, id, jid); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "subpay.release", "subcontract_payment", id, "subcontract_payment.released", map[string]any{"id": id, "journal_id": jid})
	})
	return jid, err
}

// CancelSubPayment cancels a payment before release (preparer or approver).
func (s *Service) CancelSubPayment(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	perm := auth.PermPaymentPrepare
	if !s.authz.Has(p, perm) {
		perm = auth.PermPaymentApprove
	}
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, "subcontract_payment", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT", "SUBMITTED", "APPROVED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE subcontract_payments SET status = 'CANCELLED', cancel_reason = $2 WHERE id = $1::uuid`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "subpay.cancel", "subcontract_payment", id, "subcontract_payment.cancelled", map[string]any{"id": id, "reason": reason})
	})
}
