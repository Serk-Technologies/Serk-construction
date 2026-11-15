package kernel

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// rawRefusedWith runs a statement through the tenant's own database login
// (the kernel is bypassed on purpose) and requires the database to refuse it
// for the stated reason: any other error — a missing privilege, a typo, a
// duplicate key — does not count as the control holding (DEF-018).
func rawRefusedWith(t *testing.T, f *fixture, name, reason, q string, args ...any) {
	t.Helper()
	err := f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, args...)
		return err
	})
	if err == nil {
		t.Fatalf("database accepted: %s", name)
	}
	if !strings.Contains(err.Error(), reason) {
		t.Fatalf("%s: refused, but not for %q: %v", name, reason, err)
	}
}

// attestedWriteRefusedWith issues a genuine attestation for by (as the kernel
// would after its own checks, which are skipped here) and then writes the row:
// the database must still refuse for the stated reason.
func attestedWriteRefusedWith(t *testing.T, f *fixture, by auth.Principal, action, subjectType, id, actx, reason, q string, args ...any) {
	t.Helper()
	err := f.svc.store.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		if err := f.svc.authorize(ctx, tx, by, action, subjectType, id, actx); err != nil {
			t.Fatalf("issuing the attestation for the direct write: %v", err)
		}
		_, err := tx.Exec(ctx, q, args...)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("database let %s through %s (want refusal %q): %v", by.UserID, action, reason, err)
	}
}
