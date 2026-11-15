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

	"context"
	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestConsoleBrowserForms (XCT-FORM) drives the cost-code-dependent forms in a
// real Chromium across two projects with different cost codes: an older answer
// released (by an explicit request barrier) after a newer one changes nothing;
// a line's cost code that the new project does not offer is marked unresolved,
// the typed line stays and the form will not send until it is chosen again;
// submission waits for loading choices; a stale failure is not reported; and
// leaving a form with typed input asks first. Skipped (never passed) without
// node and Playwright; the CI gate rejects any skip.
func TestConsoleBrowserForms(t *testing.T) {
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
	script, err := filepath.Abs("../../e2e/console_forms.mjs")
	if err != nil {
		t.Fatal(err)
	}
	a := newP2PAPI(t)
	// A second project whose only cost code is 02-LAB.
	var p2 string
	if err := a.db.AdminStore.WithTenant(a.db.Ctx, a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		p2, err = postgres.CreateProject(ctx, tx, a.tenant, a.entity, "P-TWO", "Bahir Dar Bridge", "ETB")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pm := auth.Principal{Kind: auth.KindHuman, UserID: "u-wbs", TenantID: a.tenant, Roles: []string{"project_manager"}, ProjectIDs: []string{p2}}
	v, _, err := a.svc.ProposeWBSVersion(a.db.Ctx, pm, p2, "Bridge WBS", []postgres.WBSNode{{Code: "02", Name: "Labour"}},
		[]postgres.CostCode{{NodeCode: "02", Code: "02-LAB", Name: "Site labour", CostType: "LABOUR", ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.svc.DecideWBSVersion(a.db.Ctx, auth.Principal{Kind: auth.KindHuman, UserID: "u-wbs-approver", TenantID: a.tenant, Roles: []string{"finance_manager"}}, v.ID, true, "approved"); err != nil {
		t.Fatal(err)
	}
	h := New(Config{Verifier: testVerifier, Kernel: a.svc, Store: a.db.Router, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		RatePerMinute: 100000, Abuse: AbuseConfig{PreAuthPerMinute: 1000000, MaxInFlight: 1024}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	a.srv = srv
	P := []string{a.project, p2}
	tokens := map[string]string{"siteEng": a.tok("u-se", P, false, "site_engineer"), "pm": a.tok("u-pm", P, false, "project_manager"),
		"estimator": a.tok("u-est", P, false, "estimator"), "planner": a.tok("u-planner", P, false, "planner")}
	dir := t.TempDir()
	fx, _ := json.Marshal(map[string]any{"tokens": tokens})
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, fx, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script)
	cmd.Env = append(os.Environ(), "SERK_URL="+srv.URL, "SERK_FIXTURE="+fixture, "SERK_SHOTS="+filepath.Join(dir, "shots"), "SERK_PLAYWRIGHT="+pw)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	t.Logf("browser forms journey (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser forms journey failed: %v", err)
	}
}
