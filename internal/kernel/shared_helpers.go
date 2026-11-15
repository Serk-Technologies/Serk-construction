// Shared helpers carried from new-version's field.go (7ac2a6c) when its
// finance, quality and credit work was integrated (ADR 0025). The field
// module itself (timesheets, equipment usage, HSE) is rebuilt on this
// branch's equipment table separately; these helpers have no field logic.
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

// rowsAsMaps runs a query and returns every row as a JSON-shaped map.
func rowsAsMaps(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM (`+sql+`) r`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		m, err := jsonMap(raw)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// scopeArgs are the arguments of the project filter "$1 OR project_id = ANY($2)".
func (s *Service) scopeArgs(p auth.Principal) (bool, []string) {
	sc := s.authz.ScopeOf(p)
	if sc.Projects == nil {
		return sc.TenantWide, []string{}
	}
	return sc.TenantWide, sc.Projects
}

// decideSimple approves or rejects a SUBMITTED document by a different
// person, without a fresh-MFA requirement (field approvals).
func (s *Service) decideSimple(ctx context.Context, p auth.Principal, perm auth.Permission, docType, table, id, action, what string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	}
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, docType, id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, what); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, action, docType, id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE `+table+` SET status = $4, approved_by = $2, decision_reason = NULLIF($3, '') WHERE id = $1::uuid`, id, p.UserID, reason, outcome); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, docType, id, docType+"."+strings.ToLower(outcome), map[string]any{"id": id, "reason": reason})
	})
}

// cancelDraft cancels a DRAFT document of its author.
func (s *Service) cancelDraft(ctx context.Context, p auth.Principal, perm auth.Permission, docType, table, id string) error {
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, docType, id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the author cancels a draft", auth.ErrForbidden)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE `+table+` SET status = 'CANCELLED' WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return s.record(ctx, tx, p, docType+".cancel", docType, id, docType+".cancelled", map[string]any{"id": id})
	})
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func parseTime(field, v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		if d, e2 := time.Parse("2006-01-02T15:04", strings.TrimSpace(v)); e2 == nil {
			return d.UTC(), nil
		}
		return time.Time{}, fmt.Errorf("%w: %s is a date and time", ErrInvalidInput, field)
	}
	return t, nil
}

// ReportIncident records an incident; a major or critical one notifies the

// cancelAttested records the attested cancellation of a document in the
// transaction that cancels it (DEF-017, migration 0084): the database refuses
// the CANCELLED status without this row, written for the person cancelling.
func (s *Service) cancelAttested(ctx context.Context, tx pgx.Tx, p auth.Principal, action, subjectType, id, reason string) error {
	if err := s.authorize(ctx, tx, p, action, subjectType, id, "CANCELLED"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO document_cancellations (subject_type, subject_id, cancelled_by, reason) VALUES ($1, $2::uuid, $3, $4)`,
		subjectType, id, p.UserID, strings.TrimSpace(reason))
	return err
}
