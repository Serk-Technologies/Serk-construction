# KPI catalogue: financial control dashboard

Generated from `internal/kernel/dashboard.go` (`KPICatalogue`); do not edit by hand. Regenerate with
`UPDATE_KPI_DOC=1 go test ./internal/kernel -run TestKPICatalogueDocumentIsCurrent`.

Every number on the dashboard is computed by the server from posted and approved records at the moment
the page is opened (nothing is cached), within the caller's permissions and project scope. Each tile
links to the page holding the rows behind it (drill-down) and lists its largest contributors, which
link to their documents. A tile a role may not read is shown as hidden, with the permission it needs.

| ID | KPI | Formula | Source | Thresholds |
|---|---|---|---|---|
| K-BUD-01 | Budget consumed | (committed + reserved + other actual cost) / current budget, summed over the projects of the legal entity that you may see. Committed = approved purchase orders (or what was received, if more); reserved = approved requisitions not yet ordered; other actual cost = posted cost not already inside commitments. | cost report (p2p_project_cost_report) per project | ok below 85 %; watch from 85 % to 100 %; alert above 100 % |
| K-BUD-02 | Over-budget cost codes | Number of cost codes whose available budget is below zero. Available = current budget - committed - reserved - other actual cost. | cost report per project | ok at 0; alert above 0 |
| K-BUD-03 | Current budget | Sum of the current budget over the same projects: original + approved revisions + transfers in - out + contingency drawdowns in - out. The tile also shows the original budget and the difference. | project_budget_current via the cost report | informational |
| K-RSK-01 | Projects at high risk | Number of projects in your scope whose overall assessment is high, out of all projects assessed. The assessment is rule-based (schedule, cost, procurement, cash, controls); see Risks for each rule. | risk assessment (risk-rules/1) | ok at 0; watch if any medium; alert if any high |
| K-CSH-01 | Cash held | Sum of the ledger balances of the company's active bank accounts that are linked to a ledger account. | general ledger, company bank accounts | informational; unknown without a linked bank account |
| K-CSH-02 | Net 90-day position | Cash held + receivables due within 90 days (and overdue) - payables due within 90 days (and overdue), from approved invoices only. | cash outlook | alert if negative; watch if below a quarter of payables; otherwise ok |
| K-CSH-03 | Overdue payables | Total of approved supplier invoices past their due date and not fully paid. | payables aging | ok at 0; watch above 0 |
| K-CSH-04 | Overdue receivables | Total of approved customer invoices past their due date and not fully received. | receivables aging | ok at 0; watch above 0 |
| K-LED-01 | Ledger balance | Total posted debits minus total posted credits at today's date for the legal entity. It is always zero if the books are sound. | trial balance | ok at 0.00; alert otherwise |
| K-CTL-01 | Control findings to review | Number of transactions flagged for a second look: duplicate supplier invoice numbers and payments just below the dual-approval threshold. | control review | ok at 0; watch above 0; alert if any is high |

## Role views (presets)

A preset only chooses which tiles to show; it never widens what a person may read.

| Preset | Tiles |
|---|---|
| executive | K-BUD-01, K-BUD-02, K-BUD-03, K-RSK-01, K-CSH-01, K-CSH-02, K-CSH-03, K-CSH-04, K-LED-01, K-CTL-01 |
| project | K-BUD-01, K-BUD-02, K-BUD-03, K-RSK-01 |
| finance | K-CSH-01, K-CSH-02, K-CSH-03, K-CSH-04, K-LED-01, K-CTL-01, K-BUD-01 |
| procurement | K-BUD-01, K-BUD-02, K-CTL-01 |
