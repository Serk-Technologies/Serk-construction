package config

import (
	"strings"
	"testing"
)

// The e-mail + PIN sign-in (0095) issues HS256 sessions, so like the staging
// sign-in it is refused in production; it needs the pre-authentication
// database login and a pepper of its own.
func TestPinLoginConfig(t *testing.T) {
	staging := func() map[string]string {
		return map[string]string{"SERK_ENV": "staging", "SERK_DB_BASE_DSN": "postgres://db:5432/serk?sslmode=prefer",
			"SERK_TENANT_DB_SECRET": goodSecret, "SERK_SIGNER_SOCKET": "/tmp/serk/signer.sock", "SERK_TOKEN_SECRET": goodSecret2,
			"SERK_HEALTH_DSN": "postgres://serk_health@db:5432/serk?sslmode=prefer",
			"SERK_ABUSE_DSN":  "postgres://serk_ratelimit@db:5432/serk?sslmode=prefer", "SERK_ABUSE_KEY": goodSecret3,
			"SERK_LOGIN_PEPPER": "a-login-pepper-of-at-least-32-characters"}
	}
	c, err := Load(src(staging(), nil))
	if err != nil {
		t.Fatalf("staging with the PIN sign-in refused: %v", err)
	}
	if string(c.LoginPepper) != "a-login-pepper-of-at-least-32-characters" {
		t.Fatalf("parsed %q", c.LoginPepper)
	}
	for name, mod := range map[string]func(m map[string]string){
		"short pepper":       func(m map[string]string) { m["SERK_LOGIN_PEPPER"] = "short" },
		"no pre-auth login":  func(m map[string]string) { delete(m, "SERK_ABUSE_DSN"); delete(m, "SERK_ABUSE_KEY") },
		"pepper = token key": func(m map[string]string) { m["SERK_LOGIN_PEPPER"] = goodSecret2 },
		"pepper = abuse key": func(m map[string]string) { m["SERK_LOGIN_PEPPER"] = goodSecret3 },
	} {
		m := staging()
		mod(m)
		if _, err := Load(src(m, nil)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p := prodEnv()
	p["SERK_LOGIN_PEPPER"] = "a-login-pepper-of-at-least-32-characters"
	if _, err := Load(src(p, nil)); err == nil || !strings.Contains(err.Error(), "refused in production") {
		t.Errorf("production with SERK_LOGIN_PEPPER: %v", err)
	}
}
