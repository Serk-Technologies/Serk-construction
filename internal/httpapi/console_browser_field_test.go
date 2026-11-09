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

// TestConsoleBrowserField drives timesheets, equipment usage and HSE (migration 0064) through the web
// console in a real Chromium: the site engineer records, the project manager approves, an incident is
// reported and investigated, a permit requested and issued. Skipped when node or playwright is missing.
func TestConsoleBrowserField(t *testing.T) {
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
	script, err := filepath.Abs("../../e2e/console_field.mjs")
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
	payroll := a.tok("u-payroll", nil, false, "payroll_officer")
	for i, n := range []string{"Dawit Haile", "Tigist Alemu"} {
		a.id("POST", "/v1/employees", payroll, map[string]any{"legal_entity_id": a.entity, "number": "E-00" + string(rune('1'+i)), "name": n, "kind": "HOURLY", "base_rate": "50", "starts_on": "2026-01-01"})
	}
	a.id("POST", "/v1/equipment", a.tok("u-plant", nil, false, "plant_manager"), map[string]any{"legal_entity_id": a.entity, "code": "EXC-01", "name": "Excavator CAT 320", "class": "Excavator", "ownership": "OWNED",
		"hire_rate": "800", "rate_unit": "HOUR", "fuel_norm_lph": "18", "service_interval_hours": "100"})
	tokens := map[string]string{"eng": a.tok("u-site-eng", P, false, "site_engineer"), "pm": a.tok("u-pm", P, false, "project_manager"), "plant": a.tok("u-plant", nil, false, "plant_manager"),
		"hse": a.tok("u-hse", nil, false, "hse_officer"), "engPhone": a.tok("u-site-eng", P, false, "site_engineer")}
	dir := t.TempDir()
	fx, _ := json.Marshal(map[string]any{"tokens": tokens, "entity": a.entity, "project": a.project})
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, fx, 0o600); err != nil {
		t.Fatal(err)
	}
	shots := os.Getenv("SERK_E2E_SHOTS_FIELD")
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
	t.Logf("browser field journey (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser field journey failed: %v (screenshots in %s)", err, shots)
	}
	if l := a.call(200, "GET", "/v1/p2p/timesheet?status=APPROVED", a.tok("u-pm-check", P, false, "project_manager"), nil); len(l["items"].([]any)) != 1 {
		t.Errorf("approved timesheets after the UI journey: %v", l)
	}
	hv := a.call(200, "GET", "/v1/hse", a.tok("u-hse", nil, false, "hse_officer"), nil)
	if inc := hv["incidents"].([]any); len(inc) != 1 || inc[0].(map[string]any)["status"] != "INVESTIGATING" {
		t.Errorf("incidents after the UI journey: %v", hv["incidents"])
	}
	if pm := hv["permits"].([]any); len(pm) != 1 || pm[0].(map[string]any)["status"] != "ISSUED" {
		t.Errorf("permits after the UI journey: %v", hv["permits"])
	}
}
