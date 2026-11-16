package kernel

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolLoginStore is the pre-authentication access to e-mail + PIN credentials
// (migration 0095). Pool is the API's pre-authentication login (role
// serk_abuse), which may execute login_lookup and login_record and nothing
// else; it never reads a table.
type PoolLoginStore struct{ Pool *pgxpool.Pool }

// Lookup implements LoginStore.
func (s PoolLoginStore) Lookup(ctx context.Context, email string) (LoginRow, bool, error) {
	var r LoginRow
	err := s.Pool.QueryRow(ctx, `SELECT tenant_id::text, user_id, pin_salt, pin_hash, pin_params, must_change, totp_enabled,
		totp_secret_enc, totp_last_step, locked_until, disabled FROM login_lookup($1)`, email).
		Scan(&r.TenantID, &r.UserID, &r.Salt, &r.Hash, &r.Params, &r.MustChange, &r.TOTPEnabled, &r.TOTPSealed, &r.TOTPLastStep, &r.LockedUntil, &r.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// Record implements LoginStore.
func (s PoolLoginStore) Record(ctx context.Context, rec LoginRecord) (LoginOutcome, error) {
	var o LoginOutcome
	var until *time.Time
	var params *string
	if rec.NewParams != "" {
		params = &rec.NewParams
	}
	err := s.Pool.QueryRow(ctx, `SELECT outcome, locked_until, failures FROM login_record($1, $2, $3, $4, $5, $6, $7)`,
		rec.Email, rec.Outcome, rec.IPHash, rec.TOTPStep, rec.NewSalt, rec.NewHash, params).Scan(&o.Outcome, &until, &o.Failures)
	o.LockedUntil = until
	return o, err
}
