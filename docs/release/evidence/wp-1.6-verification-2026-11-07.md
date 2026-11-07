# WP-1.6 (units of measure, reservations, reorder points): verification run 2026-11-07

Environment: Go 1.27.2, PostgreSQL 16, Chrome 157 via SERK_CHROME. Migrations 0001-0041.

## Full suite after the last code change: go test -race -count=1 -p 1 ./...
```
ok  	github.com/serk-group/serk-construction/internal/config	1.107s
ok  	github.com/serk-group/serk-construction/internal/export	2.968s
ok  	github.com/serk-group/serk-construction/internal/httpapi	192.747s
ok  	github.com/serk-group/serk-construction/internal/kernel	293.547s
ok  	github.com/serk-group/serk-construction/internal/obs	1.024s
ok  	github.com/serk-group/serk-construction/internal/relay	2.436s
ok  	github.com/serk-group/serk-construction/pkg/attest	1.027s
ok  	github.com/serk-group/serk-construction/pkg/auth	1.023s
ok  	github.com/serk-group/serk-construction/pkg/automation	1.030s
ok  	github.com/serk-group/serk-construction/pkg/datakey	1.021s
ok  	github.com/serk-group/serk-construction/pkg/evm	1.024s
ok  	github.com/serk-group/serk-construction/pkg/fx	1.023s
ok  	github.com/serk-group/serk-construction/pkg/ledger	1.034s
ok  	github.com/serk-group/serk-construction/pkg/legacy	1.043s
ok  	github.com/serk-group/serk-construction/pkg/money	1.024s
ok  	github.com/serk-group/serk-construction/pkg/schedule	1.022s
ok  	github.com/serk-group/serk-construction/pkg/tax	1.025s
ok  	github.com/serk-group/serk-construction/pkg/workflow	1.037s
EXIT 0
```

## New tests
- kernel: TestStockPlus_ConversionsAreControlled (maker/checker, attested, one approved factor per pair, item conversions must target the item's unit, raw-SQL forgeries refused, an issue counted in bundles is stored in tonnes), TestStockPlus_ReservationsIssuesAndReorder (reserve beyond unreserved refused; two racing reservations for the last units: exactly one wins; an issue cannot use reserved stock; an issue against a reservation consumes it and cannot exceed it or reuse it; release rules; forgery probes; reorder rule and low-stock suggestion)
- http: TestAPI_StockPlus
- reports: low-stock added (15 catalogue reports), catalogue document regenerated

## Real-browser check (live demo stack, migration 0041 applied)
u-buyer prepares a conversion through the form, the author sees no Approve, u-finance-manager approves, the list shows it; reservations page, reservation form, low-stock report and the stock-issue form's unit and reservation fields render with guides; the estimator can read reservations but is not offered Reserve stock. No script or CSP errors. ALL PASS.

## Design note
Reservations and issue approvals lock the stock item row before reading the balance and the reservations, so two people cannot both take the last units (write skew). Tested with concurrent reservations; the issue-versus-reservation race relies on the same lock.
