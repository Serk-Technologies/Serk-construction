# WP-1.3 (budget control) and WP-0.8 (page guides): verification run 2026-11-07

Environment: Go 1.27.2, PostgreSQL 16 (alpine, trust auth, deploy/00-roles.sql applied), Chrome 157 via SERK_CHROME, Playwright 1.63.

## Full suite: go test -race -count=1 -p 1 ./...
```
ok  	github.com/serk-group/serk-construction/internal/config	1.028s
ok  	github.com/serk-group/serk-construction/internal/export	1.672s
ok  	github.com/serk-group/serk-construction/internal/httpapi	33.601s
ok  	github.com/serk-group/serk-construction/internal/kernel	477.040s
ok  	github.com/serk-group/serk-construction/internal/obs	1.024s
ok  	github.com/serk-group/serk-construction/internal/relay	2.084s
ok  	github.com/serk-group/serk-construction/pkg/attest	1.022s
ok  	github.com/serk-group/serk-construction/pkg/auth	1.025s
ok  	github.com/serk-group/serk-construction/pkg/automation	1.029s
ok  	github.com/serk-group/serk-construction/pkg/datakey	1.019s
ok  	github.com/serk-group/serk-construction/pkg/evm	1.026s
ok  	github.com/serk-group/serk-construction/pkg/fx	1.026s
ok  	github.com/serk-group/serk-construction/pkg/ledger	1.030s
ok  	github.com/serk-group/serk-construction/pkg/legacy	1.029s
ok  	github.com/serk-group/serk-construction/pkg/money	1.028s
ok  	github.com/serk-group/serk-construction/pkg/schedule	1.026s
ok  	github.com/serk-group/serk-construction/pkg/tax	1.023s
ok  	github.com/serk-group/serk-construction/pkg/workflow	1.034s
EXIT 0
```

## Browser journeys and new HTTP tests (after the last console edits)
```
TestConsoleBrowserJourneyA/B/C, TestConsoleBrowserPlanning, TestConsoleBrowserSite: PASS
TestAPI_MetaRolesCatalogue, TestAPI_BudgetRevisionOverHTTP: PASS
ok  github.com/serk-group/serk-construction/internal/httpapi 87.360s
```

## Budget tests (kernel)
TestBudget_ArithmeticAndControls, TestBudget_DatabaseRefusesBypass (7 raw-SQL forgeries refused; tenant isolation of project_budget_current), TestBudget_ConcurrentTransfersCannotOverspend: PASS

## Fresh-database check (separate compose project)
Migrations 0001-0039 applied on an empty database (migrate exit 0); serk-seed (attested maker/checker) succeeded; ORIGINAL budget, contingency REVISION and drawdown approved over HTTP; budget overview shows drawdowns in/out and the contingency marker; page guide present on special, list, document and form pages with roles from GET /v1/meta/roles; no script or CSP errors; no horizontal scroll at 390 px.
