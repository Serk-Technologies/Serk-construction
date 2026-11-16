package kernel

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/pinauth"
)

var testPin = PinSecrets{Pepper: bytes.Repeat([]byte{0x5a}, 32), Issuer: "SERK test"}

// TestPinLogin_LockoutChangeAuthenticatorTrail (0095). E-mail + 6-digit PIN:
// an administrator issues a one-time PIN (never readable again) that must be
// changed at the first sign-in; 15 consecutive failures lock the sign-in for
// 15 minutes, during which even the right PIN is refused; the optional
// authenticator, once on, is required and each code works once; another
// company cannot see or change the sign-in; every attempt is in the trail.
func TestPinLogin_LockoutChangeAuthenticatorTrail(t *testing.T) {
	f := newFixture(t)
	pool, err := pgxpool.New(f.ctx, f.db.LoginRole(t, "serk_abuse"))
	must(t, "pre-auth pool", err)
	defer pool.Close()
	st := PoolLoginStore{Pool: pool}
	now0 := time.Now()
	fresh := func(p auth.Principal) auth.Principal {
		p.Kind, p.MFA, p.AuthTime, p.IssuedAt = auth.KindHuman, true, now0, now0.Add(-time.Minute)
		return p
	}
	admin := fresh(auth.Principal{UserID: "u-secadmin", TenantID: f.tenant, Roles: []string{"security_admin"}})
	_, err = f.svc.AddPerson(f.ctx, admin, PersonInput{UserID: "u-hanna", DisplayName: "Hanna Tesfaye", Email: "hanna@example.com",
		JobTitle: "Accountant", Roles: []string{"accountant"}, Reason: "new hire, HR ticket 77"})
	must(t, "add person", err)

	// The administrator's privilege, second factor and separation of duties.
	if _, err := f.svc.IssueLogin(f.ctx, fresh(auth.Principal{UserID: "u-pm", TenantID: f.tenant, Roles: []string{"project_manager"}}), testPin, "u-hanna", "hanna@example.com"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager issued a sign-in: %v", err)
	}
	stale := admin
	stale.MFA = false
	if _, err := f.svc.IssueLogin(f.ctx, stale, testPin, "u-hanna", "hanna@example.com"); err == nil {
		t.Fatal("a sign-in was issued without a fresh second factor")
	}
	if _, err := f.svc.IssueLogin(f.ctx, admin, testPin, "u-secadmin", "me@example.com"); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("an administrator issued their own sign-in: %v", err)
	}
	pin, err := f.svc.IssueLogin(f.ctx, admin, testPin, "u-hanna", " Hanna@Example.com ")
	must(t, "issue", err)
	if pinauth.CheckPIN(pin) != nil {
		t.Fatalf("issued PIN %q breaks the PIN rules", pin)
	}
	// Nothing readable gives the PIN back: the table is closed to the tenant and the pre-auth role.
	for _, role := range []string{"tenant", "pre-auth"} {
		var e error
		if role == "tenant" {
			e = f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `SELECT pin_hash FROM login_credentials`)
				return err
			})
		} else {
			_, e = pool.Exec(f.ctx, `SELECT pin_hash FROM login_credentials`)
		}
		if e == nil {
			t.Fatalf("the %s role read login_credentials directly", role)
		}
	}

	sign := func(email, p, code, next string) (SignInResult, error) {
		return f.svc.SignInWithPIN(f.ctx, st, testPin, SignInInput{Email: email, PIN: p, Code: code, NewPIN: next})
	}
	// The issued PIN must be changed in the same step; a weak or unchanged one is refused.
	if _, err := sign("hanna@example.com", pin, "", ""); !errors.Is(err, ErrPINChangeRequired) {
		t.Fatalf("first sign-in without a new PIN: %v", err)
	}
	for _, weak := range []string{"123456", "111111", pin} {
		if _, err := sign("hanna@example.com", pin, "", weak); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("new PIN %q: %v", weak, err)
		}
	}
	res, err := sign("HANNA@example.com", pin, "", "482915")
	must(t, "first sign-in with a new PIN", err)
	if res.UserID != "u-hanna" || res.TenantID != f.tenant || res.MFA {
		t.Fatalf("sign-in result: %+v", res)
	}
	if _, err := sign("hanna@example.com", pin, "", ""); err == nil {
		t.Fatal("the issued PIN still works after the change")
	}
	if _, err := sign("hanna@example.com", "482915", "", ""); err != nil {
		t.Fatalf("the new PIN: %v", err)
	}

	// Unknown e-mail: the same refusal as a wrong PIN.
	if _, err := sign("nobody@example.com", "482915", "", ""); !errors.Is(err, ErrSignIn) {
		t.Fatalf("unknown e-mail: %v", err)
	}

	// 14 wrong PINs leave one attempt; the 15th locks; then even the right PIN is refused.
	for i := 1; i <= 14; i++ {
		_, err := sign("hanna@example.com", "730518", "", "")
		var w SignInWrong
		if !errors.As(err, &w) || w.Left != 15-i {
			t.Fatalf("wrong PIN %d: %v", i, err)
		}
	}
	_, err = sign("hanna@example.com", "730518", "", "")
	var lock SignInLock
	if !errors.As(err, &lock) || lock.Until.Before(time.Now().Add(14*time.Minute)) || lock.Until.After(time.Now().Add(16*time.Minute)) {
		t.Fatalf("15th wrong PIN: %v", err)
	}
	if _, err := sign("hanna@example.com", "482915", "", ""); !errors.Is(err, ErrSignInLocked) {
		t.Fatalf("the right PIN during the lock: %v", err)
	}
	hanna := fresh(auth.Principal{UserID: "u-hanna", TenantID: f.tenant, Roles: []string{"accountant"}})
	if err := f.svc.LoginAction(f.ctx, admin, "u-hanna", "UNLOCK", ""); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, err := sign("hanna@example.com", "482915", "", ""); err != nil {
		t.Fatalf("after unlock: %v", err)
	}

	// Self-service PIN change checks the current PIN.
	if err := f.svc.ChangeMyPIN(f.ctx, hanna, testPin, "000001", "593017"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("change with a wrong current PIN: %v", err)
	}
	must(t, "change PIN", f.svc.ChangeMyPIN(f.ctx, hanna, testPin, "482915", "593017"))

	// Authenticator: optional; once on, required; each code once.
	setup, err := f.svc.BeginAuthenticator(f.ctx, hanna, testPin)
	must(t, "begin authenticator", err)
	if err := f.svc.ConfirmAuthenticator(f.ctx, hanna, testPin, setup.Secret, "000000"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("confirm with a wrong code: %v", err)
	}
	prev := time.Now().Add(-30*time.Second).Unix() / 30
	code, _ := pinauth.TOTPCode(setup.Secret, prev)
	must(t, "confirm authenticator", f.svc.ConfirmAuthenticator(f.ctx, hanna, testPin, setup.Secret, code))
	if _, err := sign("hanna@example.com", "593017", "", ""); !errors.Is(err, ErrCodeRequired) {
		t.Fatalf("sign-in without the code: %v", err)
	}
	if _, err := sign("hanna@example.com", "593017", code, ""); err == nil {
		t.Fatal("the code used to turn the authenticator on worked again")
	}
	now, _ := pinauth.TOTPCode(setup.Secret, time.Now().Unix()/30)
	res, err = sign("hanna@example.com", "593017", now, "")
	must(t, "sign-in with the code", err)
	if !res.MFA {
		t.Fatal("a sign-in with the authenticator is not marked multi-factor")
	}
	if _, err := sign("hanna@example.com", "593017", now, ""); err == nil {
		t.Fatal("an authenticator code was accepted twice")
	}

	// Another company sees nothing and changes nothing.
	other := fresh(auth.Principal{UserID: "u-other-admin", TenantID: f.tenant2, Roles: []string{"security_admin"}})
	accts, err := f.svc.LoginAccounts(f.ctx, other)
	must(t, "other company's accounts", err)
	if len(accts) != 0 {
		t.Fatalf("another company sees %d sign-ins", len(accts))
	}
	if err := f.svc.LoginAction(f.ctx, other, "u-hanna", "UNLOCK", ""); err == nil {
		t.Fatal("another company's administrator unlocked the sign-in")
	}

	// A person turned off in the directory cannot sign in, whatever the PIN.
	must(t, "disable person", errOnly(f.svc.SetPersonStatus(f.ctx, admin, "u-hanna", false, "left the company")))
	later, _ := pinauth.TOTPCode(setup.Secret, time.Now().Unix()/30+1)
	if _, err := sign("hanna@example.com", "593017", later, ""); !errors.Is(err, ErrSignInDisabled) {
		t.Fatalf("a disabled person signed in: %v", err)
	}

	// The trail holds every kind of event, newest first, for this company only.
	trail, err := f.svc.LoginTrail(f.ctx, admin, "u-hanna", 500)
	must(t, "trail", err)
	seen := map[string]int{}
	for _, e := range trail {
		seen[e.Outcome]++
	}
	for _, want := range []string{"PIN_ISSUED", "PIN_CHANGED", "SUCCESS", "WRONG_PIN", "NOW_LOCKED", "LOCKED", "UNLOCKED", "AUTHENTICATOR_ON", "CODE_REPLAYED", "DISABLED"} {
		if seen[want] == 0 {
			t.Errorf("trail has no %s event (have %v)", want, seen)
		}
	}
	if seen["WRONG_PIN"] != 15 { // 14 in the lockout run + the issued PIN tried after the change
		t.Errorf("trail: %d wrong-PIN events, want 15", seen["WRONG_PIN"])
	}
	if trail[0].At.Before(trail[len(trail)-1].At) {
		t.Error("trail is not newest first")
	}
	rawRefusedWith(t, f, "a sign-in event rewritten", "permission denied", `UPDATE login_events SET outcome = 'SUCCESS'`)
}
