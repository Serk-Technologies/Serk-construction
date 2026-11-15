package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConsoleBrowserFleet drives SERK FLEET part 1 (migration 0054) through
// the web console in a real Chromium: register, commission, reserve, approve,
// mobilise, receive, safety hold (which site staff cannot release), certify,
// operator authorisation, a daily log and its approval, and the command
// centre's figures. The outcome is verified through the API. Skipped when
// node or playwright is not installed.
func TestConsoleBrowserFleet(t *testing.T) {
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
	script, err := filepath.Abs("../../e2e/console_fleet.mjs")
	if err != nil {
		t.Fatal(err)
	}
	a := newP2PAPI(t)
	h := New(Config{Verifier: testVerifier, Kernel: a.svc, Store: a.db.Router, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		RatePerMinute: 100000, Abuse: AbuseConfig{PreAuthPerMinute: 1000000, MaxInFlight: 1024}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	a.srv = srv
	P := []string{a.project}
	tokens := map[string]string{"officer": a.tok("u-fl-officer", nil, false, "plant_officer"), "manager": a.tok("u-fl-manager", nil, true, "plant_manager"),
		"inspector": a.tok("u-fl-inspector", nil, true, "plant_inspector"), "eng": a.tok("u-fl-eng", P, false, "site_engineer"),
		"pm": a.tok("u-fl-pm", P, false, "project_manager"), "engPhone": a.tok("u-fl-eng", P, false, "site_engineer")}
	dir := t.TempDir()
	fx, _ := json.Marshal(map[string]any{"tokens": tokens, "entity": a.entity, "project": a.project})
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, fx, 0o600); err != nil {
		t.Fatal(err)
	}
	shots := os.Getenv("SERK_E2E_SHOTS_FLEET")
	if shots == "" {
		shots = filepath.Join(dir, "shots")
	}
	if err := os.MkdirAll(shots, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script)
	cmd.Env = append(os.Environ(), "SERK_URL="+srv.URL, "SERK_FIXTURE="+fixture, "SERK_SHOTS="+shots, "SERK_PLAYWRIGHT="+pw)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	t.Logf("browser fleet journey (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser fleet journey failed: %v (screenshots in %s)", err, shots)
	}
	check := a.tok("u-fl-check", P, false, "project_manager")
	items := a.call(200, "GET", "/v1/equipment", check, nil)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("register after the UI journey: %v", items)
	}
	e := items[0].(map[string]any)
	if e["status"] != "ASSIGNED" || e["current_project_id"] != a.project || e["last_meter"] != "2008" || e["status_changed_by"] != "u-fl-inspector" {
		t.Errorf("equipment after the UI journey: %v", e)
	}
	if l := a.call(200, "GET", "/v1/p2p/equipment_log?status=APPROVED", check, nil)["items"].([]any); len(l) != 1 {
		t.Errorf("approved logs after the UI journey: %v", l)
	}
	if l := a.call(200, "GET", "/v1/p2p/equipment_reservation?status=APPROVED", check, nil)["items"].([]any); len(l) != 1 {
		t.Errorf("approved reservations after the UI journey: %v", l)
	}
}
