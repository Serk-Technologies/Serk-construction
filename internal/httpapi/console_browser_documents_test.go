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

// TestConsoleBrowserDocuments drives controlled documents and RFIs (migration
// 0052) through the web console in a real Chromium: a drawing registered with
// revision A, published by the project manager from the worklist, superseded
// by revision B, downloaded and compared with the upload; an RFI raised,
// answered by someone else and closed by its author. The outcome is verified
// through the API. Skipped when node or playwright is not installed.
func TestConsoleBrowserDocuments(t *testing.T) {
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
	script, err := filepath.Abs("../../e2e/console_documents.mjs")
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
	tokens := map[string]string{"eng": a.tok("u-doc-eng", P, false, "site_engineer"), "pm": a.tok("u-doc-pm", P, true, "project_manager"),
		"planner": a.tok("u-doc-planner", P, false, "planner"), "pmPhone": a.tok("u-doc-pm", P, false, "project_manager")}
	dir := t.TempDir()
	fx, _ := json.Marshal(map[string]any{"tokens": tokens, "entity": a.entity, "project": a.project})
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, fx, 0o600); err != nil {
		t.Fatal(err)
	}
	shots := os.Getenv("SERK_E2E_SHOTS_DOCUMENTS")
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
	t.Logf("browser documents journey (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser documents journey failed: %v (screenshots in %s)", err, shots)
	}
	check := a.tok("u-doc-check", P, false, "planner")
	docs := a.call(200, "GET", "/v1/documents?project="+a.project, check, nil)["documents"].([]any)
	if len(docs) != 1 {
		t.Fatalf("documents after the UI journey: %v", docs)
	}
	d := docs[0].(map[string]any)
	cur, _ := d["current"].(map[string]any)
	if cur["revision"] != "B" || cur["approved_by"] != "u-doc-pm" || d["superseded_revisions"] != float64(1) || d["revisions_in_review"] != float64(0) {
		t.Errorf("register after the UI journey: %v", d)
	}
	rfis := a.call(200, "GET", "/v1/rfis?status=CLOSED", check, nil)["rfis"].([]any)
	if len(rfis) != 1 || rfis[0].(map[string]any)["answered_by"] != "u-doc-planner" || rfis[0].(map[string]any)["revision_id"] != cur["id"] {
		t.Errorf("RFIs after the UI journey: %v", rfis)
	}
}
