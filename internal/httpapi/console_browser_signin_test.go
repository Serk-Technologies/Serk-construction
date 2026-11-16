package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/serk-group/serk-construction/internal/kernel"
)

// TestConsoleBrowserSignin (BRS-03, XCT-UI) drives the e-mail + PIN sign-in
// page, the branding editor and the workflow map in a real Chromium: the
// two-panel page shows the company's words; a wrong PIN reports the attempts
// left; the one-time PIN must be replaced (a weak PIN is refused) before the
// session opens; the system administrator edits the motto and colours with a
// live preview and contrast warnings, uploads a logo, and the sign-in page then
// shows them; the workflow map lists who prepares and approves; my profile
// offers the PIN change and the authenticator; the people sheet shows the
// sign-in and its trail. Skipped (never passed) without node and Playwright;
// the CI gate rejects any skip.
func TestConsoleBrowserSignin(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the browser journey was not run")
	}
	pw := os.Getenv("SERK_PLAYWRIGHT")
	if pw == "" {
		if out, err := exec.Command("npm", "root", "-g").Output(); err == nil {
			pw = filepath.Join(strings.TrimSpace(string(out)), "playwright")
		}
	}
	if _, err := os.Stat(filepath.Join(pw, "package.json")); err != nil {
		t.Skipf("playwright module not found at %q: the browser journey was not run", pw)
	}
	script, err := filepath.Abs("../../e2e/console_signin.mjs")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	pool, err := pgxpool.New(e.db.Ctx, e.db.LoginRole(t, "serk_abuse"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := New(Config{Verifier: testVerifier, Kernel: e.svc, Store: e.db.Router, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RatePerMinute: 100000,
		Abuse:       AbuseConfig{AuthFailuresPerMinute: 1000, PreAuthPerMinute: 1000000, MaxInFlight: 1024},
		BrandTenant: e.tenant,
		PinLogin:    &PinLogin{Store: kernel.PoolLoginStore{Pool: pool}, Secrets: kernel.PinSecrets{Pepper: bytes.Repeat([]byte{0x5a}, 32), Issuer: "SERK test"}, Signer: testVerifier}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	e.srv = srv
	admin := e.tok("u-sec-admin", nil, true, "security_admin")
	// Sign-in e-mails are unique across companies: one per run.
	email := "selam." + uuid.NewString()[:8] + "@example.com"
	must := func(what string, ok bool, st int, out map[string]any) {
		t.Helper()
		if !ok {
			t.Fatalf("fixture, %s: %d %v", what, st, out)
		}
	}
	st, out := e.do("POST", "/v1/people", admin, map[string]any{"user_id": "u-sysadmin-b", "display_name": "Selam Haile", "email": email,
		"job_title": "System administrator", "roles": []string{"system_admin"}, "reason": "IT lead, ticket 7"})
	must("add the system administrator", st == 201 || st == 200, st, out)
	st, out = e.do("POST", "/v1/people/u-sysadmin-b/login", admin, map[string]any{"email": email})
	pin, _ := out["pin"].(string)
	must("issue the sign-in", st == 200 && len(pin) == 6, st, out)

	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 122, G: 31, B: uint8(x * 3), A: 255})
		}
	}
	var logo bytes.Buffer
	if err := png.Encode(&logo, img); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fx, _ := json.Marshal(map[string]any{"email": email, "pin": pin, "logo": base64.StdEncoding.EncodeToString(logo.Bytes())})
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, fx, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script)
	cmd.Env = append(os.Environ(), "SERK_URL="+srv.URL, "SERK_FIXTURE="+fixture, "SERK_SHOTS="+filepath.Join(dir, "shots"), "SERK_PLAYWRIGHT="+pw)
	start := time.Now()
	res, err := cmd.CombinedOutput()
	t.Logf("browser sign-in journey (%s):\n%s", time.Since(start).Round(time.Second), res)
	if err != nil {
		t.Fatalf("browser sign-in journey failed: %v", err)
	}
	// What the browser saved is what the server holds.
	st, out = e.do("GET", "/branding", "", nil)
	if st != 200 || out["motto"] != "Built to last." || out["logo"] == nil {
		t.Fatalf("public branding after the journey: %d %v", st, out)
	}
	if th, _ := out["theme"].(map[string]any); th == nil || th["light"].(map[string]any)["accent"] != "#7a1f1f" {
		t.Fatalf("theme after the journey: %v", out["theme"])
	}
}
