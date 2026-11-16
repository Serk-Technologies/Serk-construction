package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/serk-group/serk-construction/internal/kernel"
)

// TestAPIPinLogin (0095): the e-mail + PIN sign-in over HTTP. An administrator
// issues a one-time PIN (shown once, never stored readable); the first
// sign-in must replace it; the session carries no second factor unless the
// authenticator is on; wrong PINs count down to a 15-minute lock (423 with
// Retry-After); the trail is visible to the administrator.
func TestAPIPinLogin(t *testing.T) {
	e := newEnv(t)
	pool, err := pgxpool.New(e.db.Ctx, e.db.LoginRole(t, "serk_abuse"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := New(Config{Verifier: testVerifier, Kernel: e.svc, Store: e.db.Router, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RatePerMinute: 1000,
		Abuse:    AbuseConfig{AuthFailuresPerMinute: 1000, PreAuthPerMinute: 1000},
		PinLogin: &PinLogin{Store: kernel.PoolLoginStore{Pool: pool}, Secrets: kernel.PinSecrets{Pepper: bytes.Repeat([]byte{0x33}, 32), Issuer: "SERK test"}, Signer: testVerifier}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	e.srv = srv
	admin := e.tok("u-api-admin", nil, true, "security_admin")

	if st, out := e.do("GET", "/auth/methods", "", nil); st != 200 || out["pin"] != true {
		t.Fatalf("methods: %d %v", st, out)
	}
	if st, out := e.do("POST", "/v1/people", admin, map[string]any{"user_id": "u-meron", "display_name": "Meron Alemu", "email": "meron@example.com",
		"roles": []string{"accountant"}, "reason": "new hire, HR ticket 90"}); st != 201 && st != 200 {
		t.Fatalf("add person: %d %v", st, out)
	}
	st, out := e.do("POST", "/v1/people/u-meron/login", admin, map[string]any{"email": "meron@example.com"})
	pin, _ := out["pin"].(string)
	if st != 200 || len(pin) != 6 || out["must_change"] != true {
		t.Fatalf("issue: %d %v", st, out)
	}
	login := func(body map[string]any) (int, map[string]any) { return e.do("POST", "/auth/login", "", body) }
	if st, out := login(map[string]any{"email": "meron@example.com", "pin": pin}); st != http.StatusConflict || out["code"] != "pin_change_required" {
		t.Fatalf("first sign-in without a new PIN: %d %v", st, out)
	}
	if st, out := login(map[string]any{"email": "meron@example.com", "pin": pin, "new_pin": "123456"}); st != http.StatusUnprocessableEntity {
		t.Fatalf("weak new PIN: %d %v", st, out)
	}
	st, out = login(map[string]any{"email": "meron@example.com", "pin": pin, "new_pin": "582047"})
	tok, _ := out["token"].(string)
	if st != 200 || tok == "" || out["mfa"] != false {
		t.Fatalf("first sign-in: %d %v", st, out)
	}
	if st, me := e.do("GET", "/v1/me/login", tok, nil); st != 200 || me["email"] != "meron@example.com" || me["must_change"] != false {
		t.Fatalf("my sign-in: %d %v", st, me)
	}
	// The session's roles come from the directory: the accountant reads journals.
	if st, _ := e.do("GET", "/v1/journals?status=DRAFT", tok, nil); st != 200 {
		t.Fatalf("an accountant signed in by PIN cannot read journals: %d", st)
	}
	// Changing access needs a second factor: a PIN-only session is refused.
	if st, _ := e.do("POST", "/v1/people/u-api-admin/login", tok, map[string]any{"email": "x@example.com"}); st == 200 {
		t.Fatal("a PIN-only accountant session issued a sign-in")
	}

	for i := 14; i >= 1; i-- {
		st, out := login(map[string]any{"email": "meron@example.com", "pin": "730518"})
		if st != 401 || out["attempts_left"] != float64(i) {
			t.Fatalf("wrong PIN (%d left): %d %v", i, st, out)
		}
	}
	req, _ := http.NewRequest("POST", srv.URL+"/auth/login", bytes.NewBufferString(`{"email":"meron@example.com","pin":"730518"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusLocked || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("15th wrong PIN: %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if st, out := login(map[string]any{"email": "meron@example.com", "pin": "582047"}); st != http.StatusLocked {
		t.Fatalf("the right PIN during the lock: %d %v", st, out)
	}
	if st, out := e.do("POST", "/v1/people/u-meron/login/unlock", admin, nil); st != 200 {
		t.Fatalf("unlock: %d %v", st, out)
	}
	if st, _ := login(map[string]any{"email": "meron@example.com", "pin": "582047"}); st != 200 {
		t.Fatalf("after unlock: %d", st)
	}
	st, trail := e.do("GET", "/v1/login-trail?user=u-meron&limit=100", admin, nil)
	if st != 200 || len(trail["items"].([]any)) < 18 {
		t.Fatalf("trail: %d %v", st, trail)
	}
	// The PIN is never returned again by any read.
	if st, logins := e.do("GET", "/v1/logins", admin, nil); st != 200 || bytes.Contains([]byte(jsonString(logins)), []byte(pin)) {
		t.Fatalf("logins list: %d (or it contains the PIN)", st)
	}
	if st, _ := login(map[string]any{"email": "nobody@example.com", "pin": "582047"}); st != 401 {
		t.Fatalf("unknown e-mail: %d", st)
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
