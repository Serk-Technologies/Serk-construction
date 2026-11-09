# Evidence: WP-2.1 forecasting, WP-2.2 WIP schedule, WP-2.4 change events, WP-2.6 notices and claims

Date: 2026-11-08. Branch: `new-version` (uncommitted). Status: built and tested; UAT by area leads pending.

## What was delivered

| Package | Migration | Kernel | Console | Tests |
|---|---|---|---|---|
| WP-2.1 Cost forecasting | `0044_forecast.sql` | `forecast.go` | Projects & planning → Cost forecasts | `TestForecast_MethodsAndGuards` |
| WP-2.2 WIP schedule | none (two catalogue reports) | `reportcat.go` | Reports → Cost forecast against budget; Work in progress | `TestForecast_WIPScheduleAndSummary` |
| WP-2.4 Change events | `0045_change_events.sql` | `change.go` | Projects & planning → Change events | `TestChange_EventToBudgetDraft` |
| WP-2.6 Notices and claims | `0046_claims.sql` | `claims.go` | Commercial & revenue → Contract notices, Claims; Reports → Contract notices and time bars, Claims register | `TestClaims_NoticesAndClaims` |

New permissions: `forecast.prepare`, `forecast.approve`, `change.manage`, `change.approve`, `claim.manage`, `claim.approve`
(each pair is a maker/checker segregation-of-duties conflict; the approve permissions need a fresh MFA sign-in).

## Controls proven by the tests

* The database, not the client, computes every forecast figure (budget, commitments, cost to date, ETC, EAC, VAC) and a notice deadline.
* A forecast above its budget cannot be submitted without an explanation; an approved forecast, change event or claim never changes.
* Preparer cannot decide; stale MFA refused; AI principals refused; a forged approval without an attestation is refused by the database.
* A change event's approval drafts a budget revision whose approver must differ from its author; the budget moves only then.
* A claim with a missing or late notice needs a stated reason; the client's answer is recorded once and never exceeds the claim.
* WIP: percent complete, earned revenue and over/under billing are recomputed from first principles in the test and equal the report.

## Browser check on the demo stack (http://127.0.0.1:8080, 2026-11-08)

Real Chrome, signed in through the staging token as `u-pm`, `u-commercial`, `u-qs`, `u-cfo`:

* All four new lists open (Cost forecasts, Change events, Contract notices, Claims); no script errors, no CSP violations.
* Forecast on P-001 (after an ORIGINAL budget of 11,500,000 was approved through the normal maker/checker flow):
  three lines appear; a MANUAL estimate of 5,000,000 on 01-MAT (budget 4,000,000) saves; **Submit** is refused
  with "cost code 01-MAT is forecast above its budget: explain why"; after a note it submits; `u-commercial`
  approves. Totals: budget 11,500,000, EAC 12,500,000, variance -1,000,000. The forecast summary report agrees.
* Change event (revenue 80,000, cost 50,000): submitted by `u-pm`, approved by `u-commercial`; the page shows
  **Effect on the budget** and a link to the drafted budget revision.
* Notices and claims forms open, but the demo has no ACTIVE contract, so the notice/claim flow could not be
  run on the demo; it is covered end to end by `TestClaims_NoticesAndClaims`.

Defects found by the browser check and fixed the same day:

1. An empty forecast returned `lines: null` and the page crashed ("c.lines is not iterable"): lists are now `[]`.
2. The Total row of a draft forecast was editable and produced "ids must be canonical UUIDs": the Total row is read-only.
3. An abandoned draft forecast blocked its project forever: migration 0047 adds author cancellation with a reason.
4. Refusals for a second open forecast and for a project with no budget quoted a database constraint name: now plain sentences.
5. The WIP schedule showed an "expected loss" equal to the whole EAC for a project with no active contract: now 0 until a contract exists.

## Full suite

Run after the last fix (2026-11-08): `go test ./... -count=1` against a fresh PostgreSQL 16 test database, browser journeys in Chrome: every package `ok`, 0 FAIL, exit 0.
