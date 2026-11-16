// E-mail + PIN sign-in with an optional authenticator (migration 0095).
//
// Sign-in itself runs before any tenant is known, through LoginStore (the
// pre-authentication database login; it may only look an e-mail up and
// record the attempt). Everything after sign-in runs in the person's own
// tenant session. The kernel never stores or returns a PIN except the one
// time an administrator issues an initial PIN, which must be changed at the
// first sign-in.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/pinauth"
)

// PinSecrets are the server-side secrets of the PIN sign-in.
type PinSecrets struct {
	Pepper []byte // >= 32 bytes; never stored in the database
	Issuer string // the name shown in authenticator apps
}

// LoginRow is what the pre-authentication lookup returns for an e-mail.
type LoginRow struct {
	TenantID, UserID string
	Salt, Hash       []byte
	Params           string
	MustChange       bool
	TOTPEnabled      bool
	TOTPSealed       []byte
	TOTPLastStep     int64
	LockedUntil      *time.Time
	Disabled         bool
}

// LoginRecord is one attempt to record.
type LoginRecord struct {
	Email, Outcome   string // SUCCESS, WRONG_PIN, WRONG_CODE, UNKNOWN_EMAIL
	IPHash           []byte
	TOTPStep         *int64
	NewSalt, NewHash []byte
	NewParams        string
}

// LoginOutcome is what the database decided.
type LoginOutcome struct {
	Outcome     string
	LockedUntil *time.Time
	Failures    int
}

// LoginStore is the pre-authentication access to the credentials.
type LoginStore interface {
	Lookup(ctx context.Context, email string) (LoginRow, bool, error)
	Record(ctx context.Context, r LoginRecord) (LoginOutcome, error)
}

// Sign-in refusals. The API maps them to responses; none reveals a PIN.
var (
	ErrSignIn            = errors.New("the e-mail address or PIN is not right")
	ErrSignInLocked      = errors.New("this sign-in is locked after 15 wrong attempts")
	ErrSignInDisabled    = errors.New("this sign-in is turned off; ask your system administrator")
	ErrCodeRequired      = errors.New("enter the 6-digit code from your authenticator app")
	ErrCodeWrong         = errors.New("the authenticator code is not right")
	ErrPINChangeRequired = errors.New("choose a new 6-digit PIN before you continue")
	ErrPINUnchanged      = errors.New("the new PIN must differ from the current one")
)

// SignInLock carries when a locked sign-in opens again.
type SignInLock struct{ Until time.Time }

func (e SignInLock) Error() string { return ErrSignInLocked.Error() }
func (e SignInLock) Unwrap() error { return ErrSignInLocked }

// SignInWrong carries how many attempts are left before the lock.
type SignInWrong struct {
	Err  error
	Left int
}

func (e SignInWrong) Error() string { return e.Err.Error() }
func (e SignInWrong) Unwrap() error { return e.Err }

// SignInInput is one sign-in attempt.
type SignInInput struct {
	Email, PIN, Code, NewPIN string
	IPHash                   []byte
}

// SignInResult names who signed in.
type SignInResult struct {
	TenantID, UserID string
	MFA              bool // an authenticator code was verified
}

// normEmail lower-cases and trims an e-mail address.
func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// SignInWithPIN verifies an e-mail, PIN and (when the person turned it on)
// authenticator code. 15 consecutive failures lock the sign-in for 15
// minutes; the database counts them. A PIN issued by an administrator must
// be replaced in the same step (NewPIN).
func (s *Service) SignInWithPIN(ctx context.Context, st LoginStore, sec PinSecrets, in SignInInput) (SignInResult, error) {
	email := normEmail(in.Email)
	if email == "" || len(email) > 254 {
		return SignInResult{}, ErrSignIn
	}
	row, ok, err := st.Lookup(ctx, email)
	if err != nil {
		return SignInResult{}, err
	}
	rec := LoginRecord{Email: email, IPHash: in.IPHash}
	finish := func(o LoginOutcome, wrong error) (SignInResult, error) {
		switch o.Outcome {
		case "LOCKED", "NOW_LOCKED":
			if o.LockedUntil != nil {
				return SignInResult{}, SignInLock{Until: *o.LockedUntil}
			}
			return SignInResult{}, ErrSignInLocked
		case "DISABLED":
			return SignInResult{}, ErrSignInDisabled
		case "UNKNOWN_EMAIL":
			return SignInResult{}, ErrSignIn
		}
		return SignInResult{}, SignInWrong{Err: wrong, Left: 15 - o.Failures}
	}
	if !ok {
		// Spend the same work as a real check, so timing does not reveal which e-mails exist.
		salt, _ := pinauth.NewSalt()
		_, _ = pinauth.Hash(sec.Pepper, in.PIN, salt, pinauth.Iterations)
		rec.Outcome = "UNKNOWN_EMAIL"
		o, err := st.Record(ctx, rec)
		if err != nil {
			return SignInResult{}, err
		}
		return finish(o, ErrSignIn)
	}
	if !pinauth.Verify(sec.Pepper, in.PIN, row.Salt, row.Hash, row.Params) {
		rec.Outcome = "WRONG_PIN"
		o, err := st.Record(ctx, rec)
		if err != nil {
			return SignInResult{}, err
		}
		return finish(o, ErrSignIn)
	}
	if row.LockedUntil != nil || row.Disabled {
		// The right PIN does not open a locked or turned-off sign-in; the database says which.
		rec.Outcome = "WRONG_PIN"
		o, err := st.Record(ctx, rec)
		if err != nil {
			return SignInResult{}, err
		}
		return finish(o, ErrSignIn)
	}
	if row.TOTPEnabled {
		if strings.TrimSpace(in.Code) == "" {
			return SignInResult{}, ErrCodeRequired
		}
		secret, err := pinauth.Open(sec.Pepper, row.TOTPSealed)
		if err != nil {
			return SignInResult{}, err
		}
		step, good := pinauth.TOTPVerify(secret, strings.TrimSpace(in.Code), time.Now())
		if !good {
			rec.Outcome = "WRONG_CODE"
			o, err := st.Record(ctx, rec)
			if err != nil {
				return SignInResult{}, err
			}
			return finish(o, ErrCodeWrong)
		}
		rec.TOTPStep = &step
	}
	if row.MustChange {
		if in.NewPIN == "" {
			return SignInResult{}, ErrPINChangeRequired
		}
		if err := pinauth.CheckPIN(in.NewPIN); err != nil {
			return SignInResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		if in.NewPIN == in.PIN {
			return SignInResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, ErrPINUnchanged)
		}
		salt, err := pinauth.NewSalt()
		if err != nil {
			return SignInResult{}, err
		}
		h, err := pinauth.Hash(sec.Pepper, in.NewPIN, salt, pinauth.Iterations)
		if err != nil {
			return SignInResult{}, err
		}
		rec.NewSalt, rec.NewHash, rec.NewParams = salt, h, pinauth.Params
	}
	// The person must still be active in the company's directory.
	if _, err := s.ApplyDirectory(ctx, auth.Principal{UserID: row.UserID, TenantID: row.TenantID, Kind: auth.KindHuman}); err != nil {
		rec.Outcome, rec.TOTPStep, rec.NewSalt, rec.NewHash, rec.NewParams = "DISABLED", nil, nil, nil, ""
		if _, rerr := st.Record(ctx, rec); rerr != nil {
			return SignInResult{}, rerr
		}
		return SignInResult{}, ErrSignInDisabled
	}
	rec.Outcome = "SUCCESS"
	o, err := st.Record(ctx, rec)
	if err != nil {
		return SignInResult{}, err
	}
	switch o.Outcome {
	case "SUCCESS":
		return SignInResult{TenantID: row.TenantID, UserID: row.UserID, MFA: rec.TOTPStep != nil}, nil
	case "CODE_REPLAYED":
		return SignInResult{}, SignInWrong{Err: fmt.Errorf("that authenticator code was already used; wait for the next one"), Left: 15 - o.Failures}
	}
	return finish(o, ErrSignIn)
}

// ---------------------------------------------------------------- the signed-in person

// LoginAccount is one person's sign-in state (never a PIN or secret).
type LoginAccount struct {
	UserID        string     `json:"user_id"`
	Email         string     `json:"email"`
	MustChange    bool       `json:"must_change"`
	Authenticator bool       `json:"authenticator"`
	Failures      int        `json:"failed_attempts"`
	LockedUntil   *time.Time `json:"locked_until,omitempty"`
	Disabled      bool       `json:"disabled"`
	LastSuccess   *time.Time `json:"last_success_at,omitempty"`
	LastFailure   *time.Time `json:"last_failure_at,omitempty"`
	PINSetBy      string     `json:"pin_set_by"`
	PINSetAt      time.Time  `json:"pin_set_at"`
}

// LoginEvent is one line of the sign-in trail.
type LoginEvent struct {
	At      time.Time `json:"at"`
	UserID  string    `json:"user_id,omitempty"`
	Email   string    `json:"email"`
	Outcome string    `json:"outcome"`
	Actor   string    `json:"actor,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

func (s *Service) ownTx(ctx context.Context, p auth.Principal, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if p.Kind != auth.KindHuman || p.UserID == "" {
		return auth.ErrForbidden
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkSessionTx(ctx, tx, p); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

type ownCredential struct {
	email      string
	salt, hash []byte
	params     string
	totp       bool
	sealed     []byte
	lastStep   int64
}

func loadOwn(ctx context.Context, tx pgx.Tx, userID string) (ownCredential, error) {
	var c ownCredential
	err := tx.QueryRow(ctx, `SELECT email, pin_salt, pin_hash, pin_params, totp_enabled, totp_secret_enc, totp_last_step FROM login_own_credential($1)`, userID).
		Scan(&c.email, &c.salt, &c.hash, &c.params, &c.totp, &c.sealed, &c.lastStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fmt.Errorf("%w: you have no e-mail and PIN sign-in", ErrNotFound)
	}
	return c, err
}

// MyLogin returns the signed-in person's own sign-in state.
func (s *Service) MyLogin(ctx context.Context, p auth.Principal) (LoginAccount, error) {
	var out LoginAccount
	err := s.ownTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		accts, err := loginAccounts(ctx, tx)
		if err != nil {
			return err
		}
		for _, a := range accts {
			if a.UserID == p.UserID {
				out = a
				return nil
			}
		}
		return fmt.Errorf("%w: you have no e-mail and PIN sign-in", ErrNotFound)
	})
	return out, err
}

// ChangeMyPIN replaces the person's PIN after checking the current one.
func (s *Service) ChangeMyPIN(ctx context.Context, p auth.Principal, sec PinSecrets, current, next string) error {
	if err := pinauth.CheckPIN(next); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if next == current {
		return fmt.Errorf("%w: %v", ErrInvalidInput, ErrPINUnchanged)
	}
	return s.ownTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		c, err := loadOwn(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		if !pinauth.Verify(sec.Pepper, current, c.salt, c.hash, c.params) {
			return fmt.Errorf("%w: the current PIN is not right", ErrInvalidInput)
		}
		salt, err := pinauth.NewSalt()
		if err != nil {
			return err
		}
		h, err := pinauth.Hash(sec.Pepper, next, salt, pinauth.Iterations)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT login_issue($1, $2, $3, $4, $5, false, $1)`, p.UserID, c.email, salt, h, pinauth.Params); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "login.pin_change", "person", p.UserID, "login.pin_changed", map[string]any{"user_id": p.UserID})
	})
}

// AuthenticatorSetup is what an authenticator app needs to be added.
type AuthenticatorSetup struct {
	Secret string `json:"secret"`
	URI    string `json:"otpauth_uri"`
}

// BeginAuthenticator returns a new secret for the person to add to an app.
// Nothing is stored until ConfirmAuthenticator receives a code from it.
func (s *Service) BeginAuthenticator(ctx context.Context, p auth.Principal, sec PinSecrets) (AuthenticatorSetup, error) {
	var out AuthenticatorSetup
	err := s.ownTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		c, err := loadOwn(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		secret, err := pinauth.NewTOTPSecret()
		if err != nil {
			return err
		}
		issuer := sec.Issuer
		if issuer == "" {
			issuer = "SERK Construction"
		}
		out = AuthenticatorSetup{Secret: secret, URI: pinauth.TOTPURI(issuer, c.email, secret)}
		return nil
	})
	return out, err
}

// ConfirmAuthenticator turns the authenticator on when the code matches the secret.
func (s *Service) ConfirmAuthenticator(ctx context.Context, p auth.Principal, sec PinSecrets, secret, code string) error {
	step, ok := pinauth.TOTPVerify(secret, strings.TrimSpace(code), time.Now())
	if !ok {
		return fmt.Errorf("%w: the code does not match; check the time on your phone and try the next code", ErrInvalidInput)
	}
	sealed, err := pinauth.Seal(sec.Pepper, secret)
	if err != nil {
		return err
	}
	return s.ownTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT login_change($1, 'AUTHENTICATOR_ON', $2, $3, $1, '')`, p.UserID, sealed, step); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "login.authenticator", "person", p.UserID, "login.authenticator_on", map[string]any{"user_id": p.UserID})
	})
}

// TurnOffMyAuthenticator turns the authenticator off after checking the PIN.
func (s *Service) TurnOffMyAuthenticator(ctx context.Context, p auth.Principal, sec PinSecrets, pin string) error {
	return s.ownTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		c, err := loadOwn(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		if !pinauth.Verify(sec.Pepper, pin, c.salt, c.hash, c.params) {
			return fmt.Errorf("%w: the PIN is not right", ErrInvalidInput)
		}
		if _, err := tx.Exec(ctx, `SELECT login_change($1, 'AUTHENTICATOR_OFF', NULL, NULL, $1, '')`, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "login.authenticator", "person", p.UserID, "login.authenticator_off", map[string]any{"user_id": p.UserID})
	})
}

// ---------------------------------------------------------------- administrators

// IssueLogin gives a person an e-mail sign-in with a new random PIN, returned
// once; the person must change it at the first sign-in. Re-issuing replaces
// the PIN and clears a lock. user.manage, fresh second factor, never oneself.
func (s *Service) IssueLogin(ctx context.Context, p auth.Principal, sec PinSecrets, userID, email string) (string, error) {
	email = normEmail(email)
	if err := notSelf(p, userID); err != nil {
		return "", err
	}
	pin, err := pinauth.NewPIN()
	if err != nil {
		return "", err
	}
	salt, err := pinauth.NewSalt()
	if err != nil {
		return "", err
	}
	h, err := pinauth.Hash(sec.Pepper, pin, salt, pinauth.Iterations)
	if err != nil {
		return "", err
	}
	err = s.peopleTx(ctx, p, auth.PermUserManage, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.issueLoginTx(ctx, tx, userID, email, salt, h, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "login.issue", "person", userID, "login.issued", map[string]any{"user_id": userID, "email": email})
	})
	if err != nil {
		return "", err
	}
	return pin, nil
}

func (s *Service) issueLoginTx(ctx context.Context, tx pgx.Tx, userID, email string, salt, hash []byte, by string) error {
	var managed bool
	if err := tx.QueryRow(ctx, `SELECT managed FROM directory_users WHERE user_id = $1 AND status = 'ACTIVE'`, userID).Scan(&managed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no active person %s in this company", ErrNotFound, userID)
		}
		return err
	}
	if !managed {
		return fmt.Errorf("%w: manage this person's roles in SERK first, so the sign-in carries them", ErrInvalidInput)
	}
	if _, err := tx.Exec(ctx, `UPDATE directory_users SET email = $2 WHERE user_id = $1 AND email IS DISTINCT FROM $2`, userID, email); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT login_issue($1, $2, $3, $4, $5, true, $6)`, userID, email, salt, hash, pinauth.Params, by)
	return err
}

// SeedLogin issues a sign-in with a given PIN, for the synthetic staging and
// development seed only (no principal; the caller is the seeding tool).
func (s *Service) SeedLogin(ctx context.Context, tenantID string, sec PinSecrets, userID, email, pin string) error {
	if err := pinauth.CheckPIN(pin); err != nil {
		return err
	}
	salt, err := pinauth.NewSalt()
	if err != nil {
		return err
	}
	h, err := pinauth.Hash(sec.Pepper, pin, salt, pinauth.Iterations)
	if err != nil {
		return err
	}
	return s.store.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return s.issueLoginTx(ctx, tx, userID, normEmail(email), salt, h, "serk-seed")
	})
}

// LoginAction is an administrator's change to someone's sign-in.
func (s *Service) LoginAction(ctx context.Context, p auth.Principal, userID, action, reason string) error {
	switch action {
	case "UNLOCK", "LOGIN_ENABLED":
	case "AUTHENTICATOR_RESET", "LOGIN_DISABLED":
		if len(strings.TrimSpace(reason)) < 5 {
			return fmt.Errorf("%w: say why (at least 5 characters)", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: unknown sign-in action", ErrInvalidInput)
	}
	if err := notSelf(p, userID); err != nil {
		return err
	}
	return s.peopleTx(ctx, p, auth.PermUserManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT login_change($1, $2, NULL, NULL, $3, $4)`, userID, action, p.UserID, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "login."+strings.ToLower(action), "person", userID, "login."+strings.ToLower(action), map[string]any{"user_id": userID, "reason": reason})
	})
}

func loginAccounts(ctx context.Context, tx pgx.Tx) ([]LoginAccount, error) {
	rows, err := tx.Query(ctx, `SELECT user_id, email, must_change, totp_enabled, failed_count, locked_until, disabled, last_success_at, last_failure_at, pin_set_by, pin_set_at FROM login_accounts()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LoginAccount{}
	for rows.Next() {
		var a LoginAccount
		if err := rows.Scan(&a.UserID, &a.Email, &a.MustChange, &a.Authenticator, &a.Failures, &a.LockedUntil, &a.Disabled, &a.LastSuccess, &a.LastFailure, &a.PINSetBy, &a.PINSetAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LoginAccounts lists the company's e-mail sign-ins (user.manage, role.manage or audit.read).
func (s *Service) LoginAccounts(ctx context.Context, p auth.Principal) ([]LoginAccount, error) {
	if err := s.canSeePeople(p); err != nil {
		return nil, err
	}
	var out []LoginAccount
	err := s.ownTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = loginAccounts(ctx, tx)
		return err
	})
	return out, err
}

// LoginTrail returns the company's sign-in trail, newest first (one person's
// when userID is given). Everyone sees their own; the whole company's needs
// user.manage, role.manage or audit.read.
func (s *Service) LoginTrail(ctx context.Context, p auth.Principal, userID string, limit int) ([]LoginEvent, error) {
	if userID != p.UserID {
		if err := s.canSeePeople(p); err != nil {
			return nil, err
		}
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	out := []LoginEvent{}
	err := s.ownTx(ctx, p, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT at, COALESCE(user_id, ''), email, outcome, COALESCE(actor, ''), COALESCE(detail, '') FROM login_trail($1, $2)`, userID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e LoginEvent
			if err := rows.Scan(&e.At, &e.UserID, &e.Email, &e.Outcome, &e.Actor, &e.Detail); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}
