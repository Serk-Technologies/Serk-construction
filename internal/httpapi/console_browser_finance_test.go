package httpapi

import (
	"context"
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

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

// TestConsoleBrowserFinance drives the tax page (0063) end to end and opens the
// consolidation page (0062) with a prepared run, in a real Chromium. Skipped
// when node or playwright is not installed.
func TestConsoleBrowserFinance(t *testing.T) {
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
	script, err := filepath.Abs("../../e2e/console_finance.mjs")
	if err != nil {
		t.Fatal(err)
	}
	a := newP2PAPI(t)
	var sub string
	if err := a.db.AdminStore.WithTenant(a.db.Ctx, a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if sub, err = postgres.CreateLegalEntity(ctx, tx, a.tenant, "SERK KENYA LTD", "KE", "KES", "KES"); err != nil {
			return err
		}
		for _, x := range []struct{ code, typ string }{{"2300", "LIABILITY"}, {"1300x", "ASSET"}} {
			code := strings.TrimSuffix(x.code, "x")
			if code == "1300" {
				code = "1310"
			}
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: a.tenant, LegalEntityID: a.entity, Code: code, Name: code, Type: ledger.AccountType(x.typ), Active: true})
			if err != nil {
				return err
			}
			a.accounts[code] = id
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := New(Config{Verifier: testVerifier, Kernel: a.svc, Store: a.db.Router, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		RatePerMinute: 100000, Abuse: AbuseConfig{PreAuthPerMinute: 1000000, MaxInFlight: 1024}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	a.srv = srv
	ctrl := a.tok("u-controller", nil, true, "controller")
	cfo := a.tok("u-cfo-seed", nil, true, "cfo")
	grp := a.id("POST", "/v1/consolidation-groups", ctrl, map[string]any{"code": "SERK", "name": "Serk Group", "presentation_currency": "ETB", "parent_entity_id": a.entity,
		"members": []map[string]any{{"legal_entity_id": a.entity, "ownership_pct": "1", "method": "FULL"}, {"legal_entity_id": sub, "ownership_pct": "0.8", "method": "FULL"}}})
	a.call(200, "POST", "/v1/consolidation-groups/"+grp+"/approve", cfo, nil)
	a.id("POST", "/v1/consolidation-runs", ctrl, map[string]any{"group_id": grp, "period_start": "2026-10-01", "period_end": "2026-12-31",
		"rates": map[string]any{"KES": map[string]any{"closing": "0.45", "average": "0.44"}}, "rate_source": "NBE test rates"})

	tokens := map[string]string{"accountant": a.tok("u-accountant", nil, false, "accountant"), "cfo": a.tok("u-cfo", nil, true, "cfo"), "ctrl": a.tok("u-controller-ui", nil, true, "controller"),
		"ctrlPhone": a.tok("u-accountant", nil, false, "accountant")}
	dir := t.TempDir()
	fx, _ := json.Marshal(map[string]any{"tokens": tokens, "entity": a.entity, "project": a.project})
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, fx, 0o600); err != nil {
		t.Fatal(err)
	}
	shots := os.Getenv("SERK_E2E_SHOTS_FINANCE")
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
	t.Logf("browser finance journey (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser finance journey failed: %v (screenshots in %s)", err, shots)
	}
	v := a.call(200, "GET", "/v1/tax", cfo, nil)
	rets := v["returns"].([]any)
	if len(rets) != 1 || rets[0].(map[string]any)["status"] != "FILED" {
		t.Errorf("tax returns after the UI journey: %v", rets)
	}
}
