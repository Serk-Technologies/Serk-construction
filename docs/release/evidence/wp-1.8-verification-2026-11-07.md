# WP-1.8 (company setup checklist and staged imports): verification run 2026-11-07

Environment: Go 1.27.2, PostgreSQL 16, Chrome 157 via SERK_CHROME. Migrations 0001-0043.

## Full suite: go test -race -count=1 -p 1 ./... (run before the two small console and persona edits below, which were then re-verified in the browser and by TestStaging)
```
ok  	github.com/serk-group/serk-construction/internal/config	1.056s
ok  	github.com/serk-group/serk-construction/internal/export	3.725s
ok  	github.com/serk-group/serk-construction/internal/httpapi	319.753s
ok  	github.com/serk-group/serk-construction/internal/kernel	536.494s
ok  	github.com/serk-group/serk-construction/internal/obs	1.041s
ok  	github.com/serk-group/serk-construction/internal/relay	4.203s
ok  	github.com/serk-group/serk-construction/pkg/attest	1.046s
ok  	github.com/serk-group/serk-construction/pkg/auth	1.058s
ok  	github.com/serk-group/serk-construction/pkg/automation	1.066s
ok  	github.com/serk-group/serk-construction/pkg/datakey	1.033s
ok  	github.com/serk-group/serk-construction/pkg/evm	1.037s
ok  	github.com/serk-group/serk-construction/pkg/fx	1.037s
ok  	github.com/serk-group/serk-construction/pkg/ledger	1.045s
ok  	github.com/serk-group/serk-construction/pkg/legacy	1.042s
ok  	github.com/serk-group/serk-construction/pkg/money	1.043s
ok  	github.com/serk-group/serk-construction/pkg/schedule	1.032s
ok  	github.com/serk-group/serk-construction/pkg/tax	1.039s
ok  	github.com/serk-group/serk-construction/pkg/workflow	1.112s
EXIT 0
```

## New tests
- kernel: TestImports_StageReviewCommit (validation messages per row; staging creates nothing; the stager cannot commit; a wrong confirmed row count is refused; valid rows created through the ordinary command and stay PENDING; second commit never duplicates; the importer cannot approve what they imported; 9 raw-SQL forgeries refused; tenant isolation), TestImports_StockItemsAndCustomers, TestSetup_ChecklistReadsRealState
- http: TestAPI_ImportsAndSetup

## Defects found by this verification and fixed
1. The progress bar used an inline style, which the strict content-security policy blocks (it would have rendered empty): now the native progress element.
2. The demo had only one procurement officer and one AR clerk, so no second person could commit an import: personas u-buyer-2 and u-ar-clerk-2 added (28 personas).
3. A test-data row meant to have a too-short name did not: corrected.

## Real-browser check (live demo stack, migration 0043)
CFO: Company setup lists 16 steps with progress and ticked steps from real records; u-buyer stages a supplier CSV (4 rows: 2 valid, 2 with reasons), cannot commit it; u-buyer-2 is refused a wrong row count, then commits: partial, 2 created; the supplier is listed waiting for approval. No script or CSP errors. ALL PASS.
