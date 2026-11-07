# WP-1.5 (report catalogue to 25): verification run 2026-11-07

Environment: Go 1.27.2, PostgreSQL 16, Chrome 157 via SERK_CHROME. No new migration (reports are read-only queries over existing tables).

## Full suite after the last code change: go test -race -count=1 -p 1 ./...
```
ok  	github.com/serk-group/serk-construction/internal/config	1.031s
ok  	github.com/serk-group/serk-construction/internal/export	2.321s
ok  	github.com/serk-group/serk-construction/internal/httpapi	199.345s
ok  	github.com/serk-group/serk-construction/internal/kernel	287.915s
ok  	github.com/serk-group/serk-construction/internal/obs	1.027s
ok  	github.com/serk-group/serk-construction/internal/relay	2.195s
ok  	github.com/serk-group/serk-construction/pkg/attest	1.023s
ok  	github.com/serk-group/serk-construction/pkg/auth	1.024s
ok  	github.com/serk-group/serk-construction/pkg/automation	1.028s
ok  	github.com/serk-group/serk-construction/pkg/datakey	1.022s
ok  	github.com/serk-group/serk-construction/pkg/evm	1.025s
ok  	github.com/serk-group/serk-construction/pkg/fx	1.026s
ok  	github.com/serk-group/serk-construction/pkg/ledger	1.022s
ok  	github.com/serk-group/serk-construction/pkg/legacy	1.029s
ok  	github.com/serk-group/serk-construction/pkg/money	1.023s
ok  	github.com/serk-group/serk-construction/pkg/schedule	1.018s
ok  	github.com/serk-group/serk-construction/pkg/tax	1.026s
ok  	github.com/serk-group/serk-construction/pkg/workflow	1.038s
EXIT 0
```

## New tests
- kernel: TestReportCatalogue_EveryReportRunsAndTiesOut (14 reports run on realistic data; GL detail = trial balance; open commitments = order - invoiced; payment register has no bank details; budget-vs-actual cumulative = cost report actual), TestReportCatalogue_ScopeAndInput, TestReportCatalogueDocumentIsCurrent
- journey B: customer statement closing balance = invoiced - received; certificate register; retention ledger = withheld - released
- journey C: stock valuation report = stock on hand; movement history has put-aways and issues
- http: TestAPI_ReportCatalogue (listing by scope, run, 403/404/422, PDF/Word/Excel export)

## Defect found by the tie-out and fixed
The first stock valuation query summed FIFO layers directly and disagreed with stock on hand (weighted-average items do not use layers). It now reads stock_on_hand(), the one definition, so it cannot drift.

## Real-browser check (live demo stack)
CFO: Reports hub lists 8 core + 14 catalogue reports in groups; all 11 no-input reports render with the data-as-of line; statements and budget-vs-actual ask for their subject first; an Excel export downloads. Project manager: company-wide reports are not offered. No horizontal scroll at 390 px; no script or CSP errors. ALL PASS.
