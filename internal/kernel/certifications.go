// Certifications and safety training with expiry (PPL-05.1; migration 0086).
package kernel

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// CertificationIn records a certificate or a training from the document issued.
type CertificationIn struct {
	EmployeeID    string `json:"employee_id"`
	Category      string `json:"category"` // CERTIFICATE or TRAINING
	Kind          string `json:"kind"`     // e.g. WORKING_AT_HEIGHT, CRANE_OPERATOR, FIRST_AID
	Title         string `json:"title"`
	Issuer        string `json:"issuer"`
	CertificateNo string `json:"certificate_no,omitempty"`
	IssuedOn      string `json:"issued_on"`
	ExpiresOn     string `json:"expires_on,omitempty"`
}

var certKind = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,40}$`)

// RecordCertification records a certificate or training (hse.report); it
// counts only after someone else verifies it.
func (s *Service) RecordCertification(ctx context.Context, p auth.Principal, in CertificationIn) (string, error) {
	cat := strings.ToUpper(strings.TrimSpace(in.Category))
	kind := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(in.Kind), " ", "_"))
	if !oneOf(cat, "CERTIFICATE", "TRAINING") {
		return "", fmt.Errorf("%w: the category is CERTIFICATE or TRAINING", ErrInvalidInput)
	}
	if !certKind.MatchString(kind) {
		return "", fmt.Errorf("%w: the kind is a short code such as WORKING_AT_HEIGHT", ErrInvalidInput)
	}
	issued, err := parseDay("issued_on", in.IssuedOn)
	if err != nil {
		return "", err
	}
	var expires any
	if strings.TrimSpace(in.ExpiresOn) != "" {
		e, err := parseDay("expires_on", in.ExpiresOn)
		if err != nil {
			return "", err
		}
		if !e.After(issued) {
			return "", fmt.Errorf("%w: the expiry is after the issue date", ErrInvalidInput)
		}
		expires = e
	}
	if err := s.authz.Require(p, auth.PermHSEReport, p.TenantID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "CRT")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO certifications (employee_id, number, category, kind, title, issuer, certificate_no, issued_on, expires_on, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9::date, $10) RETURNING id::text`,
			in.EmployeeID, num, cat, kind, strings.TrimSpace(in.Title), strings.TrimSpace(in.Issuer), strings.TrimSpace(in.CertificateNo), issued, expires, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "certification.record", "certification", id, "certification.recorded", map[string]any{"id": id, "number": num, "kind": kind})
	})
	return id, err
}

// VerifyCertification accepts or rejects a recorded certification against
// the original (hse.manage, not the recorder; attested).
func (s *Service) VerifyCertification(ctx context.Context, p auth.Principal, id string, accept bool, note string) error {
	outcome := "VERIFIED"
	if !accept {
		outcome = "REJECTED"
		if len(strings.TrimSpace(note)) < 5 {
			return fmt.Errorf("%w: rejecting needs a reason", ErrInvalidInput)
		}
	}
	return s.certTx(ctx, p, id, func(ctx context.Context, tx pgx.Tx, status, creator string) error {
		if status != "RECORDED" {
			return fmt.Errorf("%w: the certification was already decided", ErrWorkflowState)
		}
		if err := auth.CheckMakerChecker(p, creator, "certification"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "certification.verify", "certification", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE certifications SET status = $2, verified_by = $3, decision_note = NULLIF($4, '') WHERE id = $1::uuid`, id, outcome, p.UserID, strings.TrimSpace(note)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "certification.verify", "certification", id, "certification."+strings.ToLower(outcome), map[string]any{"id": id})
	})
}

// RevokeCertification withdraws a verified certification with a reason
// (hse.manage; attested).
func (s *Service) RevokeCertification(ctx context.Context, p auth.Principal, id, reason string) error {
	if len(strings.TrimSpace(reason)) < 5 {
		return fmt.Errorf("%w: revoking needs a reason", ErrInvalidInput)
	}
	return s.certTx(ctx, p, id, func(ctx context.Context, tx pgx.Tx, status, _ string) error {
		if status != "VERIFIED" {
			return fmt.Errorf("%w: only a verified certification is revoked", ErrWorkflowState)
		}
		if err := s.authorize(ctx, tx, p, "certification.revoke", "certification", id, "REVOKED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE certifications SET status = 'REVOKED', revoked_by = $2, decision_note = $3 WHERE id = $1::uuid`, id, p.UserID, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "certification.revoke", "certification", id, "certification.revoked", map[string]any{"id": id, "reason": reason})
	})
}

func (s *Service) certTx(ctx context.Context, p auth.Principal, id string, fn func(context.Context, pgx.Tx, string, string) error) error {
	if err := s.authz.Require(p, auth.PermHSEManage, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var status, creator string
		if err := mapNoRows(tx.QueryRow(ctx, `SELECT status, created_by FROM certifications WHERE id = $1::uuid FOR UPDATE`, id).Scan(&status, &creator)); err != nil {
			return err
		}
		return fn(ctx, tx, status, creator)
	})
}

// CertificationRegister lists certifications with their standing today:
// PENDING (not yet verified), VALID, EXPIRING (within the warning days),
// EXPIRED, REJECTED or REVOKED, soonest expiry first.
func (s *Service) CertificationRegister(ctx context.Context, p auth.Principal, warnDays int) ([]map[string]any, error) {
	if !s.authz.Has(p, auth.PermHSEManage) && !s.authz.Has(p, auth.PermHSEReport) {
		return nil, auth.ErrForbidden
	}
	if warnDays <= 0 || warnDays > 365 {
		warnDays = 30
	}
	var out []map[string]any
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = rowsAsMaps(ctx, tx, `SELECT c.id::text AS id, c.number, e.number AS employee_number, e.name AS employee, c.category, c.kind, c.title, c.issuer,
			COALESCE(c.certificate_no, '') AS certificate_no, c.issued_on::text AS issued_on, COALESCE(c.expires_on::text, '') AS expires_on, c.status,
			CASE WHEN c.status = 'RECORDED' THEN 'PENDING' WHEN c.status IN ('REJECTED', 'REVOKED') THEN c.status
			     WHEN c.expires_on < current_date THEN 'EXPIRED' WHEN c.expires_on <= current_date + $1::int THEN 'EXPIRING' ELSE 'VALID' END AS standing,
			c.created_by, COALESCE(c.verified_by, '') AS verified_by, COALESCE(c.decision_note, '') AS decision_note
			FROM certifications c JOIN employees e ON e.id = c.employee_id
			ORDER BY c.expires_on NULLS LAST, c.number`, warnDays)
		return err
	})
	return out, err
}
