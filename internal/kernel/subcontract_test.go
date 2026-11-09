package kernel

// Tests for subcontracts (WP-2.5 part 1, migration 0055, kernel/subcontract.go).
// Arithmetic fixture (hand-checked):
//   SOV: line 1 excavation 10,000 m3 x 120 = 1,200,000; line 2 formwork 2,000 m2 x 400 = 800,000.
//   Retention 10% capped at 100,000; advance 200,000 recovered at 20%; withholding 2%.
//   Certificate 1: 4,000 / 500 -> gross 680,000; retention 68,000; advance 136,000;
//     withholding 13,600; net 462,400.
//   Back-charge 50,000.
//   Certificate 2: 9,000 / 2,000 -> cumulative 1,880,000; this 1,200,000; retention 32,000
//     (cap reached); advance 64,000 (fully recovered); back-charge 50,000; withholding 24,000;
//     net 1,030,000.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

func TestSubcontract_CertificatesAndJournal(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	qs, cm := w.ar.qs, c.commercial
	must(t, "accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, name, typ string }{{"2050", "Subcontractor payable", "LIABILITY"}, {"2060", "Subcontract retention payable", "LIABILITY"},
			{"1170", "Advances to subcontractors", "ASSET"}, {"2310", "Withholding tax payable", "LIABILITY"}, {"5300", "Back-charge recoveries", "EXPENSE"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			w.acct[a.code] = id
		}
		return nil
	}))
	pol, err := f.svc.ProposeSubcontractPolicy(f.ctx, c.controller, SubcontractPolicyInput{LegalEntityID: f.entity, Payable: w.acct["2050"], Retention: w.acct["2060"],
		Advance: w.acct["1170"], WHT: w.acct["2310"], Backcharge: w.acct["5300"]})
	must(t, "policy", err)
	must(t, "policy approve", f.svc.DecideSubcontractPolicy(f.ctx, w.mfa(c.cfo), pol, true, ""))

	committed := func() decimal.Decimal {
		var v decimal.Decimal
		must(t, "position", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT committed FROM p2p_cost_position($1::uuid, NULL, NULL)`, w.costCode).Scan(&v)
		}))
		return v
	}
	before := committed()
	in := SubcontractInput{ProjectID: f.project, SupplierID: w.sup[1], Title: "Earthworks and formwork", RetentionPct: dec("0.10"), RetentionCap: dec("100000"),
		AdvanceAmount: dec("200000"), AdvanceRecoveryPct: dec("0.20"), WHTRate: dec("0.02"),
		Lines: []SubcontractLine{{1, "Excavation", "m3", dec("10000"), dec("120"), w.costCode, w.acct["5000"]}, {2, "Formwork", "m2", dec("2000"), dec("400"), w.costCode, w.acct["5000"]}}}
	if _, err := f.svc.CreateSubcontract(f.ctx, w.ar.arClerk, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an AR clerk prepared a subcontract: %v", err)
	}
	bad := in
	bad.Lines = []SubcontractLine{{1, "Excavation", "m3", dec("10"), dec("1"), w.costCode, w.acct["2050"]}}
	if _, err := f.svc.CreateSubcontract(f.ctx, qs, bad); err == nil {
		t.Fatal("a line on a liability account was accepted")
	}
	sub, err := f.svc.CreateSubcontract(f.ctx, qs, in)
	must(t, "subcontract", err)
	must(t, "submit", f.svc.SubmitSubcontract(f.ctx, qs, sub))
	if err := f.svc.DecideSubcontract(f.ctx, w.mfa(qs), sub, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the QS approved the subcontract: %v", err)
	}
	must(t, "approve", f.svc.DecideSubcontract(f.ctx, w.mfa(cm), sub, true, ""))
	if got := committed().Sub(before); !got.Equal(dec("2000000")) {
		t.Fatalf("the approved subcontract committed %s, want 2,000,000", got)
	}

	// Certificate 1.
	earlier := today().AddDate(0, 0, -10).Format("2006-01-02")
	app1, err := f.svc.CreateApplication(f.ctx, qs, sub, earlier, []ApplicationLine{{1, dec("5000")}, {2, dec("500")}})
	must(t, "application 1", err)
	if _, err := f.svc.CreateApplication(f.ctx, qs, sub, today().Format("2006-01-02"), []ApplicationLine{{1, dec("1")}}); err == nil {
		t.Fatal("two open applications")
	}
	must(t, "submit 1", f.svc.SubmitApplication(f.ctx, qs, app1))
	if _, err := f.svc.CertifyApplication(f.ctx, w.mfa(qs), app1, []ApplicationLine{{1, dec("4000")}, {2, dec("500")}}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the QS certified: %v", err)
	}
	if _, err := f.svc.CertifyApplication(f.ctx, w.mfa(cm), app1, []ApplicationLine{{1, dec("10001")}, {2, dec("500")}}); err == nil {
		t.Fatal("a quantity above the subcontract was certified")
	}
	j1, err := f.svc.CertifyApplication(f.ctx, w.mfa(cm), app1, []ApplicationLine{{1, dec("4000")}, {2, dec("500")}})
	must(t, "certify 1", err)
	cert := func(id string) [6]string {
		var r [6]decimal.Decimal
		must(t, "cert", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT this_gross, retention, advance_recovery, backcharges, wht, net_payable FROM subcontract_applications WHERE id = $1::uuid`, id).
				Scan(&r[0], &r[1], &r[2], &r[3], &r[4], &r[5])
		}))
		var out [6]string
		for i, x := range r {
			out[i] = x.StringFixed(2)
		}
		return out
	}
	if got := cert(app1); got != [6]string{"680000.00", "68000.00", "136000.00", "0.00", "13600.00", "462400.00"} {
		t.Fatalf("certificate 1: %v", got)
	}
	var lines string
	must(t, "journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT string_agg(a.code || ' ' || l.side || ' ' || l.amount::numeric(24,2), ', ' ORDER BY a.code) FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = $1::uuid`, j1).Scan(&lines)
	}))
	if lines != "1170 CREDIT 136000.00, 2050 CREDIT 462400.00, 2060 CREDIT 68000.00, 2310 CREDIT 13600.00, 5000 DEBIT 680000.00" {
		t.Fatalf("journal: %s", lines)
	}
	w.financePost(t, j1)
	rawFails(t, f, "edit a certificate", `UPDATE subcontract_applications SET net_payable = 1 WHERE id = '`+app1+`'`)

	// Back-charge, then certificate 2: retention cap and advance fully recovered.
	bc, err := f.svc.CreateBackcharge(f.ctx, qs, sub, "Clean-up of spoil left on site", "Site photo set 14", dec("50000"))
	must(t, "backcharge", err)
	must(t, "bc submit", f.svc.SubmitBackcharge(f.ctx, qs, bc))
	must(t, "bc approve", f.svc.DecideBackcharge(f.ctx, w.mfa(cm), bc, true, ""))
	if _, err := f.svc.CreateApplication(f.ctx, qs, sub, earlier, []ApplicationLine{{1, dec("9000")}}); err == nil {
		t.Fatal("an application for an already certified period was accepted")
	}
	app2, err := f.svc.CreateApplication(f.ctx, qs, sub, today().Format("2006-01-02"), []ApplicationLine{{1, dec("9000")}, {2, dec("2000")}})
	must(t, "application 2", err)
	must(t, "submit 2", f.svc.SubmitApplication(f.ctx, qs, app2))
	j2, err := f.svc.CertifyApplication(f.ctx, w.mfa(cm), app2, []ApplicationLine{{1, dec("9000")}, {2, dec("2000")}})
	must(t, "certify 2", err)
	if got := cert(app2); got != [6]string{"1200000.00", "32000.00", "64000.00", "50000.00", "24000.00", "1030000.00"} {
		t.Fatalf("certificate 2: %v", got)
	}
	var deducted string
	must(t, "deducted", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(deducted_in::text, '') FROM subcontract_backcharges WHERE id = $1::uuid`, bc).Scan(&deducted)
	}))
	if deducted != app2 {
		t.Fatalf("back-charge deducted in %q, want %s", deducted, app2)
	}
	w.financePost(t, j2)
	if got := committed().Sub(before); !got.Equal(dec("2000000")) {
		t.Fatalf("after certificates the commitment is %s, want 2,000,000 (certified work stays inside it)", got)
	}

	// A third application must certify every line again, and cannot go down.
	app3, err := f.svc.CreateApplication(f.ctx, qs, sub, today().Format("2006-01-02"), []ApplicationLine{{2, dec("2000")}})
	if err == nil {
		t.Fatal("an application for the period of the last certificate was accepted")
	}
	_ = app3
	view, err := f.svc.GetSubcontract(f.ctx, cm, sub)
	must(t, "view", err)
	if len(view.Applications) != 2 || len(view.Backcharges) != 1 || view.Position["certified"] == nil {
		t.Fatalf("view: %+v", view)
	}
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-qs-out", TenantID: f.tenant, Roles: []string{"quantity_surveyor"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}}
	if _, err := f.svc.GetSubcontract(f.ctx, outsider, sub); err == nil {
		t.Fatal("an outsider read the subcontract")
	}
}

// Paying certificates (0056): documents, pay when certified, three people, journal.
func TestSubcontract_PaymentComplianceAndPayWhenCertified(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	qs, cm := w.ar.qs, c.commercial
	must(t, "accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, name, typ string }{{"2050", "Subcontractor payable", "LIABILITY"}, {"2060", "Subcontract retention payable", "LIABILITY"},
			{"1170", "Advances to subcontractors", "ASSET"}, {"2310", "Withholding tax payable", "LIABILITY"}, {"5300", "Back-charge recoveries", "EXPENSE"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			w.acct[a.code] = id
		}
		return nil
	}))
	pol, err := f.svc.ProposeSubcontractPolicy(f.ctx, c.controller, SubcontractPolicyInput{LegalEntityID: f.entity, Payable: w.acct["2050"], Retention: w.acct["2060"],
		Advance: w.acct["1170"], WHT: w.acct["2310"], Backcharge: w.acct["5300"]})
	must(t, "policy", err)
	must(t, "policy approve", f.svc.DecideSubcontractPolicy(f.ctx, w.mfa(c.cfo), pol, true, ""))
	sub, err := f.svc.CreateSubcontract(f.ctx, qs, SubcontractInput{ProjectID: f.project, SupplierID: w.sup[0], Title: "Drainage", RetentionPct: dec("0.05"),
		WHTRate: dec("0.02"), PayWhenCertified: true, Lines: []SubcontractLine{{1, "Culverts", "m", dec("400"), dec("1500"), w.costCode, w.acct["5000"]}}})
	must(t, "subcontract", err)
	must(t, "submit", f.svc.SubmitSubcontract(f.ctx, qs, sub))
	must(t, "approve", f.svc.DecideSubcontract(f.ctx, w.mfa(cm), sub, true, ""))
	app, err := f.svc.CreateApplication(f.ctx, qs, sub, "2026-10-05", []ApplicationLine{{1, dec("100")}})
	must(t, "application", err)
	must(t, "app submit", f.svc.SubmitApplication(f.ctx, qs, app))
	cj, err := f.svc.CertifyApplication(f.ctx, w.mfa(cm), app, []ApplicationLine{{1, dec("100")}})
	must(t, "certify", err)
	w.financePost(t, cj)
	// 150,000 gross; retention 7,500; withholding 3,000; net 139,500.
	net := dec("139500")

	// An expired required insurance.
	_, err = f.svc.RegisterSubDocument(f.ctx, qs, SubDocumentInput{SupplierID: w.sup[0], DocType: "insurance", Reference: "CAR-2025-77", IssuedOn: "2025-01-01", ExpiresOn: "2026-01-01", Required: true})
	must(t, "expired doc", err)

	in := SubPaymentInput{SubcontractID: sub, SupplierBankID: w.supBank, CompanyBankID: w.bank, ValueDate: today().Format("2006-01-02")}
	in.Allocations = append(in.Allocations, struct {
		ApplicationID string          `json:"application_id"`
		Amount        decimal.Decimal `json:"amount"`
	}{app, net.Add(dec("1"))})
	if _, err := f.svc.PrepareSubPayment(f.ctx, c.treasury, in); err == nil {
		t.Fatal("a payment above the open certificate was accepted")
	}
	in.Allocations[0].Amount = net
	if _, err := f.svc.PrepareSubPayment(f.ctx, qs, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a QS prepared a payment: %v", err)
	}
	pay, err := f.svc.PrepareSubPayment(f.ctx, c.treasury, in)
	must(t, "payment", err)
	must(t, "pay submit", f.svc.SubmitSubPayment(f.ctx, c.treasury, pay))
	if err := f.svc.DecideSubPayment(f.ctx, w.mfa(c.finMgr), pay, true, ""); err == nil {
		t.Fatal("a payment was approved while the subcontractor's insurance had expired")
	}
	_, err = f.svc.RegisterSubDocument(f.ctx, qs, SubDocumentInput{SupplierID: w.sup[0], DocType: "INSURANCE", Reference: "CAR-2026-12", IssuedOn: "2026-01-02", ExpiresOn: "2027-01-01", Required: true})
	must(t, "renewed doc", err)
	if err := f.svc.DecideSubPayment(f.ctx, w.mfa(c.finMgr), pay, true, ""); err == nil {
		t.Fatal("pay when certified: approved before the client certified the period")
	}
	w.activeContract(t)
	w.measure(t, 10, "1000", "0", "0")
	w.certifiedIPC(t, 10, "ERA-IPC-SUB")
	must(t, "approve payment", f.svc.DecideSubPayment(f.ctx, w.mfa(c.finMgr), pay, true, ""))
	if _, err := f.svc.ReleaseSubPayment(f.ctx, w.mfa(c.treasury), pay); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the preparer released the payment: %v", err)
	}
	jid, err := f.svc.ReleaseSubPayment(f.ctx, w.mfa(c.treasury2), pay)
	must(t, "release", err)
	var lines string
	must(t, "journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT string_agg(a.code || ' ' || l.side || ' ' || l.amount::numeric(24,2), ', ' ORDER BY a.code) FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = $1::uuid`, jid).Scan(&lines)
	}))
	if lines != "1050 CREDIT 139500.00, 2050 DEBIT 139500.00" {
		t.Fatalf("payment journal: %s", lines)
	}
	w.financePost(t, jid)
	if _, err := f.svc.PrepareSubPayment(f.ctx, c.treasury, in); err == nil {
		t.Fatal("a certificate was paid twice")
	}
	pd, err := f.svc.GetP2PDoc(f.ctx, c.treasury, "subcontract_payment", pay)
	must(t, "payment detail", err)
	if raw := string(pd.Detail); !strings.Contains(raw, `"account_number_masked":"****6789"`) || strings.Contains(raw, "account_digest") || strings.Contains(raw, "1000123456789") {
		t.Fatalf("payee projection: %s", raw)
	}
	docs, err := f.svc.SubDocuments(f.ctx, c.treasury, w.sup[0])
	must(t, "documents", err)
	if len(docs) != 2 {
		t.Fatalf("documents: %v", docs)
	}
}
