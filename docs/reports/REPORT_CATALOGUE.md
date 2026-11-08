# Report catalogue

Generated from `internal/kernel/reportcat.go` (`reportDefs`); do not edit by hand. Regenerate with
`UPDATE_KPI_DOC=1 go test ./internal/kernel -run TestReportCatalogueDocumentIsCurrent`.

Every report is read-only, runs as the person asking (their permissions, project scope and row-level security),
shows the time its data is as of and the filters used, totals money in exact decimals, is limited to 5,000 rows
and exports to PDF, Word and Excel through `/v1/export` (never more than the screen).
API: `GET /v1/reports/catalogue` and `GET /v1/reports/catalogue/{id}?entity=&project=&subject=&from=&to=`.

| Report | Group | Needs | Scope | Filters | What it shows |
|---|---|---|---|---|---|
| `gl-detail` General ledger detail | Ledger | `journal.read` | company-wide (tenant-wide scope) | entity, subject, from, to (account) | Every posted journal line of the company, optionally for one account and a period. |
| `journal-register` Journal register | Ledger | `journal.read` | company-wide (tenant-wide scope) | entity, from, to | Every journal of the company with its status, total and who prepared, approved and posted it. |
| `vendor-statement` Supplier statement | Payables | `p2p.read` | company-wide (tenant-wide scope) | entity, subject, from, to (supplier) | Approved invoices and released payments of one supplier, with the running balance owed. |
| `customer-statement` Customer statement | Receivables | `p2p.read` | company-wide (tenant-wide scope) | entity, subject, from, to (customer) | Approved invoices and confirmed receipts of one customer, with the running balance owed to you. |
| `po-register` Purchase order register | Procurement | `p2p.read` | your projects | entity, project, from, to | Every purchase order of the projects you may see, with supplier, status and value. |
| `open-commitments` Open commitments | Procurement | `p2p.read` | your projects | entity, project | Approved purchase orders and how much of each is not yet invoiced: money you are committed to pay. |
| `grni` Goods received not invoiced | Procurement | `p2p.read` | your projects | entity, project | Value of goods confirmed as received on each order that has not yet been invoiced (the GRNI accrual). |
| `match-exceptions` Three-way match exceptions | Payables | `p2p.read` | your projects | entity, project, from, to | Supplier invoices whose price or quantity did not match the order and receipts, with the reason recorded. |
| `ipc-register` Payment certificate register | Revenue | `p2p.read` | your projects | entity, project, from, to | Interim payment certificates by contract: value of work, retention, advance recovery, VAT and the amount due. |
| `retention-ledger` Retention ledger | Revenue | `p2p.read` | your projects | entity, project | Retention withheld on approved customer invoices, released to date and still held, per contract. |
| `stock-valuation` Stock valuation by store | Stores | `p2p.read` | your projects | entity, project | Quantity and value of stock on hand per store and item (FIFO or weighted average, as set for each item). |
| `low-stock` Low stock and reorder suggestions | Stores | `p2p.read` | your projects | entity, project | Items whose available stock (on hand minus reserved) is below the minimum set in the reorder rules, with the quantity to order to reach the maximum. A suggestion only: nothing orders automatically. |
| `stock-movement` Stock movement history | Stores | `p2p.read` | your projects | entity, project, from, to | Receipts, issues, transfers and count differences of stock in date order: what moved, where and at what value. |
| `payment-register` Payment register | Payables | `p2p.read` | company-wide (tenant-wide scope) | entity, from, to | Every payment instruction with its status and who prepared, approved and released it. Bank details are never shown. |
| `budget-vs-actual` Budget against actual by month | Project controls | `p2p.read` | your projects | entity, project, from, to | Posted cost per cost code and month, cumulative cost, and what is left of the current budget. Needs one project. |
| `forecast-summary` Cost forecast against budget | Project controls | `p2p.read` | your projects | entity, project | The latest approved cost forecast of each project, by cost code: current budget, cost to date, estimate to complete, estimate at completion and the variance (budget - EAC; negative is a forecast overrun). |
| `wip-schedule` Work in progress: over and under billing | Revenue | `p2p.read` | your projects | entity, project | Cost-to-cost percent complete from the latest approved forecast (cost to date / estimate at completion), earned revenue (contract value x percent complete), work billed up to the forecast date and the difference. Positive = over-billed (a liability); negative = under-billed (an asset). A project whose estimate at completion exceeds its active contract value shows the total expected loss (none is shown for a project with no active contract yet). |
| `notice-register` Contract notices and time bars | Revenue | `p2p.read` | your projects | entity, project | Every contract notice with the date it must be sent by, the date it was sent and its standing: open, due soon (within 7 days), overdue (time-barred risk), sent on time or sent late. |
| `claims-register` Claims register | Revenue | `p2p.read` | your projects | entity, project | Claims for time and cost by contract: what was claimed, where each stands, and what the client agreed. |

Plus the core reports: trial balance, payables and receivables aging, reconciliation, project cost report, earned value, stock on hand and journals.
