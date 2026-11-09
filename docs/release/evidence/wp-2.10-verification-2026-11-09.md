# WP-2.10 Consolidation and intercompany: verification, 2026-11-09

SRS: FIN-08, FIN-09. Migration `0062_consolidation.sql`; `internal/kernel/consolidation.go`;
`internal/httpapi/consolidation_handlers.go`; console **Accounting → Consolidation** (Runs, Groups, Intercompany accounts).
Permissions `consolidation.manage` (controller) and `consolidation.approve` (CFO); duty-conflict pair "Consolidation".

## Controls
Group versions are maker/checker (CFO, fresh MFA); the parent must be a member at 100% FULL; at least two members.
Intercompany accounts are typed (receivable asset, payable liability, revenue, expense) with a different counterparty.
A run needs an approved group and a closing and average rate for each foreign functional currency; the database
computes every line (`consolidation_compute`), checks lines against it at commit and requires the run to balance;
one live run per group and period; approved runs never change; cancelling needs a reason. Nothing is posted to any ledger.

## Golden fixture (TestConsolidation_TranslateEliminateApprove)
Parent ETB; Kenyan subsidiary KES 80% FULL (closing 0.45, average 0.44); associate ETB 25% EQUITY.
Proven: cash 1,270,000 (1,000,000 + 270,000); share capital -1,225,000; retained earnings brought forward -45,000;
translation reserve +2,000; non-controlling interest -36,000 / +36,000; investment in associates 100,000;
intercompany balances eliminated; fee income and expense differ by 2,000 (average-rate translation) and stay visible as an
intercompany difference; assets 1,370,000, equity 1,368,000, profit 0. At a single rate nothing is unmatched.
Also: wrong parent refused; proposer cannot approve; forged line refused; no rate refused; second live run refused; no
approval without fresh MFA; accountant refused; approved run cannot be cancelled.

## Not built
Equity at historical rates; consolidation journals; automatic intercompany matching by reference; goodwill and
step acquisitions; a consolidated cash-flow statement. No browser journey for this page yet.
