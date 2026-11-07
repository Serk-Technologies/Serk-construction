# WP-1.4 (financial control dashboard with drill-down): verification run 2026-11-07

Environment: Go 1.27.2, PostgreSQL 16, Chrome 157 via SERK_CHROME. Migrations 0001-0040 (no new migration: tiles read existing reports).

## Full suite after the last code change: go test -race -count=1 -p 1 ./...
```
ok  	github.com/serk-group/serk-construction/internal/config	1.034s
ok  	github.com/serk-group/serk-construction/internal/export	2.524s
ok  	github.com/serk-group/serk-construction/internal/httpapi	207.843s
ok  	github.com/serk-group/serk-construction/internal/kernel	292.096s
ok  	github.com/serk-group/serk-construction/internal/obs	1.025s
ok  	github.com/serk-group/serk-construction/internal/relay	2.167s
ok  	github.com/serk-group/serk-construction/pkg/attest	1.023s
ok  	github.com/serk-group/serk-construction/pkg/auth	1.032s
ok  	github.com/serk-group/serk-construction/pkg/automation	1.028s
ok  	github.com/serk-group/serk-construction/pkg/datakey	1.022s
ok  	github.com/serk-group/serk-construction/pkg/evm	1.023s
ok  	github.com/serk-group/serk-construction/pkg/fx	1.031s
ok  	github.com/serk-group/serk-construction/pkg/ledger	1.025s
ok  	github.com/serk-group/serk-construction/pkg/legacy	1.034s
ok  	github.com/serk-group/serk-construction/pkg/money	1.028s
ok  	github.com/serk-group/serk-construction/pkg/schedule	1.022s
ok  	github.com/serk-group/serk-construction/pkg/tax	1.022s
ok  	github.com/serk-group/serk-construction/pkg/workflow	1.042s
EXIT 0
```

## New tests
- kernel: TestDashboard_TilesEqualTheirReports (K-BUD-01/02/03, K-LED-01, K-CSH-03 equal independent computations from the cost report, trial balance and aging), TestDashboard_ScopeAndPermissions, TestDashboard_RefusesBadInput (unknown preset, entity of another company indistinguishable from absent), TestKPICatalogueDocumentIsCurrent (docs/reports/KPI_CATALOGUE.md is generated from the code)
- http: TestAPI_Dashboard

## Real-browser check (live demo stack)
CFO: 10 tiles, every tile has a formula panel and a drill-down link, drill opens #/cost, Project view 4 tiles, Finance view 7 tiles. Project manager: 4 of 10 tiles and a list of what is hidden and why. Auditor: finance tiles. No horizontal scroll at 390 px. No script or CSP errors. ALL PASS.

## Note
The demo database has no approved budget, so budget tiles show 0 or 'No approved budget yet'; figures are exercised against real data by the kernel tie-out test.
