package kernel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestCertifications_VerifyExpireRevoke (PPL-05.1, 0086): a certificate or
// training is recorded from the document issued and counts only once someone
// else holding hse.manage verified it (attested; the tenant login cannot forge
// it); the register shows what is expiring or expired; certification_valid is
// the one rule for "holds a valid X on that day"; a verified record is revoked
// with a reason; nothing is deleted.
func TestCertifications_VerifyExpireRevoke(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	now := time.Now()
	person := func(id, role string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: f.tenant, Roles: []string{role}, ProjectIDs: []string{f.project}, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman}
	}
	eng, hse, hse2 := c.siteEng, person("u-hse", "hse_officer"), person("u-hse-2", "hse_officer")
	payroll := person("u-payroll", "payroll_officer")
	payroll.ProjectIDs = nil
	emp, err := f.svc.RegisterEmployee(f.ctx, payroll, EmployeeInput{LegalEntityID: f.entity, Number: "E-101", Name: "Meseret Tadesse", Kind: "DAILY", BaseRate: dec("450"), StartsOn: "2026-01-01"})
	must(t, "employee", err)
	day := func(d int) string { return now.AddDate(0, 0, d).Format("2006-01-02") }

	height := CertificationIn{EmployeeID: emp, Category: "training", Kind: "working at height", Title: "Working at height, level 2", Issuer: "Ethiopian Construction Works Corporation training centre",
		CertificateNo: "WAH-2026-0412", IssuedOn: day(-300), ExpiresOn: day(20)}
	if _, err := f.svc.RecordCertification(f.ctx, c.buyer, height); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer recorded a certification: %v", err)
	}
	bad := height
	bad.ExpiresOn = day(-400)
	if _, err := f.svc.RecordCertification(f.ctx, eng, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an expiry before the issue date was accepted: %v", err)
	}
	wah, err := f.svc.RecordCertification(f.ctx, eng, height)
	must(t, "working at height", err)
	crane, err := f.svc.RecordCertification(f.ctx, hse, CertificationIn{EmployeeID: emp, Category: "CERTIFICATE", Kind: "CRANE_OPERATOR", Title: "Mobile crane operator",
		Issuer: "Federal TVET Agency", IssuedOn: day(-800), ExpiresOn: day(-5)})
	must(t, "crane (expired)", err)
	aid, err := f.svc.RecordCertification(f.ctx, eng, CertificationIn{EmployeeID: emp, Category: "TRAINING", Kind: "FIRST_AID", Title: "First aid at work",
		Issuer: "Ethiopian Red Cross Society", IssuedOn: day(-10)})
	must(t, "first aid (no expiry)", err)

	valid := func(kind string, d int) bool {
		t.Helper()
		var ok bool
		must(t, "valid", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT certification_valid($1::uuid, $2, $3::date)`, emp, kind, day(d)).Scan(&ok)
		}))
		return ok
	}
	if valid("WORKING_AT_HEIGHT", 0) {
		t.Fatal("an unverified training counts")
	}
	// Verification: not by the recorder, not forged by the tenant login, not by a reporter.
	if err := f.svc.VerifyCertification(f.ctx, eng, wah, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the site engineer verified a certification: %v", err)
	}
	if err := f.svc.VerifyCertification(f.ctx, hse, crane, true, ""); err == nil {
		t.Fatal("the recorder verified their own record")
	}
	rawRefusedWith(t, f, "verify by a forged update", "approval attestation",
		`UPDATE certifications SET status = 'VERIFIED', verified_by = 'u-hse' WHERE id = $1::uuid`, wah)
	rawRefusedWith(t, f, "the recorder verifies by a direct update", "segregation of duties",
		`UPDATE certifications SET status = 'VERIFIED', verified_by = 'u-site-engineer' WHERE id = $1::uuid`, wah)
	if err := f.svc.VerifyCertification(f.ctx, hse, aid, false, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a rejection without a reason: %v", err)
	}
	must(t, "verify height", f.svc.VerifyCertification(f.ctx, hse, wah, true, ""))
	must(t, "verify crane", f.svc.VerifyCertification(f.ctx, hse2, crane, true, ""))
	must(t, "reject first aid", f.svc.VerifyCertification(f.ctx, hse, aid, false, "The certificate copy is illegible"))
	if err := f.svc.VerifyCertification(f.ctx, hse, wah, true, ""); !errors.Is(err, ErrWorkflowState) {
		t.Fatalf("verified twice: %v", err)
	}
	if !valid("WORKING_AT_HEIGHT", 0) || !valid("WORKING_AT_HEIGHT", 20) || valid("WORKING_AT_HEIGHT", 21) || valid("WORKING_AT_HEIGHT", -301) {
		t.Fatal("working at height: valid exactly from issue to expiry")
	}
	if valid("CRANE_OPERATOR", 0) || !valid("CRANE_OPERATOR", -6) || valid("FIRST_AID", 0) {
		t.Fatal("an expired or rejected record counts today")
	}

	reg, err := f.svc.CertificationRegister(f.ctx, eng, 30)
	must(t, "register", err)
	standing := map[string]string{}
	for _, r := range reg {
		standing[r["id"].(string)] = r["standing"].(string)
	}
	if standing[wah] != "EXPIRING" || standing[crane] != "EXPIRED" || standing[aid] != "REJECTED" {
		t.Fatalf("register standings: %v", standing)
	}
	if _, err := f.svc.CertificationRegister(f.ctx, c.buyer, 30); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer read the certification register: %v", err)
	}

	// Revocation: a reason, attested; the record then no longer counts; nothing is deleted.
	if err := f.svc.RevokeCertification(f.ctx, hse2, wah, "no"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a revocation without a reason: %v", err)
	}
	rawRefusedWith(t, f, "revoke by a forged update", "approval attestation",
		`UPDATE certifications SET status = 'REVOKED', revoked_by = 'u-hse-2', decision_note = 'forged revocation' WHERE id = $1::uuid`, wah)
	must(t, "revoke", f.svc.RevokeCertification(f.ctx, hse2, wah, "Training provider withdrew the certificate"))
	if valid("WORKING_AT_HEIGHT", 0) {
		t.Fatal("a revoked training still counts")
	}
	rawFails(t, f, "delete a certification", `DELETE FROM certifications WHERE id = '`+aid+`'`)
	rawRefusedWith(t, f, "record a certification already verified", "recorded unverified",
		`INSERT INTO certifications (employee_id, number, category, kind, title, issuer, issued_on, status, verified_by, created_by)
		 VALUES ($1::uuid, 'CRT-X1', 'TRAINING', 'CONFINED_SPACE', 'Confined space entry', 'Somebody', current_date, 'VERIFIED', 'u-hse', 'u-site-engineer')`, emp)
}
