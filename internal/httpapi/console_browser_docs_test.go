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

// TestConsoleBrowserDocs drives document control, an RFI and a quality plan through the web console in a real
// Chromium (migrations 0065 and 0066): upload, publish by a second person, download, RFI raised, answered and
// closed, an inspection plan approved. Skipped when node or playwright is missing.
func TestConsoleBrowserDocs(t *testing.T) {
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
	script, err := filepath.Abs("../../e2e/console_docs.mjs")
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
	tokens := map[string]string{"dc": a.tok("u-doc-controller", nil, false, "document_controller"), "pm": a.tok("u-pm", P, false, "project_manager"), "eng": a.tok("u-site-eng", P, false, "site_engineer"),
		"quality": a.tok("u-quality", nil, false, "quality_engineer"), "engPhone": a.tok("u-site-eng", P, false, "site_engineer")}
	dir := t.TempDir()
	fx, _ := json.Marshal(map[string]any{"tokens": tokens, "entity": a.entity, "project": a.project})
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, fx, 0o600); err != nil {
		t.Fatal(err)
	}
	shots := os.Getenv("SERK_E2E_SHOTS_DOCS")
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
	t.Logf("browser docs journey (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser docs journey failed: %v (screenshots in %s)", err, shots)
	}
	v := a.call(200, "GET", "/v1/documents", a.tok("u-doc-controller", nil, false, "document_controller"), nil)
	docs := v["documents"].([]any)
	if len(docs) != 1 || docs[0].(map[string]any)["revisions"].([]any)[0].(map[string]any)["status"] != "PUBLISHED" {
		t.Errorf("documents after the UI journey: %v", docs)
	}
	if rf := v["rfis"].([]any); len(rf) != 1 || rf[0].(map[string]any)["status"] != "CLOSED" {
		t.Errorf("rfis after the UI journey: %v", rf)
	}
}
