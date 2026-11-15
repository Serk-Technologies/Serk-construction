// Tax returns and e-invoicing (WP-2.11; SRS FIN-07, FIN-14, RPT-08).
// Migration: 0063_tax.sql.
//
// Tax accounts are designated per entity. A VAT or withholding return for a
// period holds lines the database computes from posted journals; the preparer
// prepares, the CFO approves (fresh MFA, not the preparer), the preparer
// files with the authority's reference. E-invoices go through an adapter:
// a fake until the authority's format is confirmed (decision D-06).
package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// EInvoice is what is sent to the e-invoicing service for an approved invoice.
type EInvoice struct {
	Number      string `json:"number"`
	Date        string `json:"date"`
	Currency    string `json:"currency"`
	CustomerTIN string `json:"customer_tin"`
	Customer    string `json:"customer"`
	Gross       string `json:"gross"`
	VAT         string `json:"vat"`
	Total       string `json:"total"`
}

// EInvoiceAdapter submits an invoice to an authority's service.
type EInvoiceAdapter interface {
	Name() string
	Submit(ctx context.Context, inv EInvoice, payloadHash string) (reference string, accepted bool, response string, err error)
}

// FakeEInvoice accepts every invoice with a reference derived from the hash;
// a customer without a TIN is rejected, as a real service would.
type FakeEInvoice struct{}

func (FakeEInvoice) Name() string { return "FAKE" }
func (FakeEInvoice) Submit(_ context.Context, inv EInvoice, hash string) (string, bool, string, error) {
	if strings.TrimSpace(inv.CustomerTIN) == "" {
		return "", false, "customer TIN missing", nil
	}
	return "FAKE-" + strings.ToUpper(hash[:12]), true, "accepted by the fake adapter", nil
}

// EInvoiceAdapterFor is the adapter in use; replaced when the format is confirmed (D-06).
var EInvoiceAdapterFor = func() EInvoiceAdapter { return FakeEInvoice{} }

func (s *Service) taxTx(ctx context.Context, p auth.Principal, perm auth.Permission, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if !s.tenantWide(p) {
		return auth.ErrForbidden
	}
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, fn)
}

// DesignateTaxAccount marks an account as output VAT, input VAT or withholding payable.
func (s *Service) DesignateTaxAccount(ctx context.Context, p auth.Principal, account, kind string) (string, error) {
	var id string
	err := s.taxTx(ctx, p, auth.PermTaxPrepare, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO tax_accounts (account_id, kind, created_by) VALUES ($1::uuid, $2, $3) RETURNING id::text`, account, strings.ToUpper(strings.TrimSpace(kind)), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "taxaccount.designate", "tax_account", id, "tax_account.designated", map[string]any{"id": id, "kind": kind})
	})
	return id, err
}

// PrepareTaxReturn prepares a VAT or WHT return; the database computes its lines.
func (s *Service) PrepareTaxReturn(ctx context.Context, p auth.Principal, entity, kind, start, end string) (string, error) {
	ps, err := parseDay("period_start", start)
	if err != nil {
		return "", err
	}
	pe, err := parseDay("period_end", end)
	if err != nil {
		return "", err
	}
	var id string
	err = s.taxTx(ctx, p, auth.PermTaxPrepare, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "TAX")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO tax_returns (legal_entity_id, number, kind, period_start, period_end, created_by) VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`,
			entity, num, strings.ToUpper(strings.TrimSpace(kind)), ps, pe, p.UserID)
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `WITH ins AS (INSERT INTO tax_return_lines (return_id, account_code, account_name, tax_kind, amount)
			SELECT $1::uuid, c.account_code, c.account_name, c.tax_kind, c.amount FROM tax_return_compute($1::uuid) c RETURNING 1) SELECT count(*) FROM ins`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: designate the entity's tax accounts first", ErrInvalidInput)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE tax_returns SET sealed = true WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "taxreturn.prepare", "tax_return", id, "tax_return.prepared", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// DecideTaxReturn approves or rejects (tax.approve, not the preparer, fresh MFA).
func (s *Service) DecideTaxReturn(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	} else if err := s.authz.RequireStepUp(p); err != nil {
		return err
	}
	return s.taxTx(ctx, p, auth.PermTaxApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermTaxApprove, "tax_return", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "PREPARED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "tax return"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "taxreturn.decide", "tax_return", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE tax_returns SET status = $2, approved_by = $3, decision_reason = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "taxreturn.decide", "tax_return", id, "tax_return."+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// FileTaxReturn records the filing of an approved return with the authority's reference.
func (s *Service) FileTaxReturn(ctx context.Context, p auth.Principal, id, ref string) error {
	return s.taxTx(ctx, p, auth.PermTaxPrepare, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermTaxPrepare, "tax_return", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "APPROVED"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "taxreturn.file", "tax_return", id, "FILED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE tax_returns SET status = 'FILED', filing_ref = $2, filed_by = $3 WHERE id = $1::uuid`, id, strings.TrimSpace(ref), p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "taxreturn.file", "tax_return", id, "tax_return.filed", map[string]any{"id": id, "ref": ref})
	})
}

// CancelTaxReturn cancels a prepared return with a reason.
func (s *Service) CancelTaxReturn(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.taxTx(ctx, p, auth.PermTaxPrepare, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE tax_returns SET status = 'CANCELLED', decision_reason = $2 WHERE id = $1::uuid AND status = 'PREPARED'`, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "taxreturn.cancel", "tax_return", id, "tax_return.cancelled", map[string]any{"id": id, "reason": reason})
	})
}

// SubmitEInvoice sends an approved customer invoice through the adapter and records the outcome.
func (s *Service) SubmitEInvoice(ctx context.Context, p auth.Principal, invoiceID string) (map[string]any, error) {
	out := map[string]any{}
	err := s.taxTx(ctx, p, auth.PermTaxPrepare, func(ctx context.Context, tx pgx.Tx) error {
		var inv EInvoice
		var status string
		if err := tx.QueryRow(ctx, `SELECT i.number, i.invoice_date::text, i.currency, COALESCE(c.tax_id, ''), c.name, i.gross::text, i.vat_amount::text, i.total::text, i.status
			FROM customer_invoices i JOIN customers c ON c.id = i.customer_id AND c.legal_entity_id = i.legal_entity_id WHERE i.id = $1::uuid`, invoiceID).
			Scan(&inv.Number, &inv.Date, &inv.Currency, &inv.CustomerTIN, &inv.Customer, &inv.Gross, &inv.VAT, &inv.Total, &status); err != nil {
			return mapNoRows(err)
		}
		if status != "APPROVED" {
			return fmt.Errorf("%w: only an approved invoice is sent (this one is %s)", ErrInvalidInput, status)
		}
		raw, _ := json.Marshal(inv)
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])
		ad := EInvoiceAdapterFor()
		ref, ok, resp, err := ad.Submit(ctx, inv, hash)
		if err != nil {
			return err
		}
		st := map[bool]string{true: "ACCEPTED", false: "REJECTED"}[ok]
		id, err := postgres.InsertReturningID(ctx, tx, `INSERT INTO e_invoice_submissions (invoice_id, adapter, reference, payload_hash, status, response, created_by)
			VALUES ($1::uuid, $2, NULLIF($3, ''), $4, $5, $6, $7) RETURNING id::text`, invoiceID, ad.Name(), ref, hash, st, resp, p.UserID)
		if err != nil {
			return err
		}
		out["id"], out["status"], out["reference"], out["response"], out["adapter"] = id, st, ref, resp, ad.Name()
		return s.record(ctx, tx, p, "einvoice.submit", "e_invoice_submission", id, "e_invoice.submitted", map[string]any{"id": id, "invoice_id": invoiceID, "status": st})
	})
	return out, err
}

// TaxView lists the entity's tax accounts, returns and e-invoice submissions.
func (s *Service) TaxView(ctx context.Context, p auth.Principal, returnID string) (map[string]any, error) {
	out := map[string]any{}
	perm := auth.PermTaxApprove
	if s.authz.Has(p, auth.PermTaxPrepare) {
		perm = auth.PermTaxPrepare
	}
	err := s.taxTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		q := func(key, sql string, args ...any) error {
			rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (`+sql+`) r`, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			list := []map[string]any{}
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					return err
				}
				m, err := jsonMap(raw)
				if err != nil {
					return err
				}
				list = append(list, m)
			}
			out[key] = list
			return rows.Err()
		}
		if err := q("entities", `SELECT id, name, functional_currency FROM legal_entities ORDER BY name`); err != nil {
			return err
		}
		if err := q("accounts", `SELECT a.id, a.code, a.name, a.type, e.name AS entity FROM accounts a JOIN legal_entities e ON e.id = a.legal_entity_id WHERE a.active ORDER BY e.name, a.code`); err != nil {
			return err
		}
		if err := q("tax_accounts", `SELECT t.id, t.kind, a.code, a.name, e.name AS entity FROM tax_accounts t JOIN accounts a ON a.id = t.account_id JOIN legal_entities e ON e.id = a.legal_entity_id ORDER BY e.name, t.kind, a.code`); err != nil {
			return err
		}
		if err := q("returns", `SELECT r.id, r.number, r.kind, r.period_start, r.period_end, r.status, r.created_by, r.approved_by, r.filing_ref, r.filed_by, (SELECT name FROM legal_entities WHERE id = r.legal_entity_id) AS entity,
			COALESCE((SELECT sum(amount) FROM tax_return_lines l WHERE l.return_id = r.id), 0) AS net FROM tax_returns r ORDER BY r.period_end DESC, r.created_at DESC`); err != nil {
			return err
		}
		if returnID != "" {
			if err := q("lines", `SELECT account_code, account_name, tax_kind, amount FROM tax_return_lines WHERE return_id = $1::uuid ORDER BY tax_kind DESC, account_code`, returnID); err != nil {
				return err
			}
		}
		if err := q("invoices", `SELECT i.id, i.number, i.invoice_date, i.total, i.vat_amount, c.name AS customer, c.tax_id,
			(SELECT status FROM e_invoice_submissions s WHERE s.invoice_id = i.id ORDER BY s.created_at DESC LIMIT 1) AS e_status,
			(SELECT reference FROM e_invoice_submissions s WHERE s.invoice_id = i.id AND s.status = 'ACCEPTED') AS e_reference
			FROM customer_invoices i JOIN customers c ON c.id = i.customer_id AND c.legal_entity_id = i.legal_entity_id WHERE i.status = 'APPROVED' ORDER BY i.invoice_date DESC LIMIT 100`); err != nil {
			return err
		}
		return nil
	})
	return out, err
}
