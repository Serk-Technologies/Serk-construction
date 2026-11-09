# WP-2.6 completion (damages, extension of time, obligations, alerts): verification, 2026-11-08

SRS: CON-09 (liquidated damages), CON-04 (time bars and notices), SCH-07 (contract time against the schedule).
The first part of WP-2.6 (notices, claims, the client's answer) was verified earlier (`0046_claims.sql`).

## Delivered

| Layer | What |
|---|---|
| Database | `0051_contract_time.sql`: `contract_ld_terms` (time for completion, damages per day, cap; maker/checker with attestation; one set per contract; cap ≤ contract value, completion after signing, cap ≥ one day), `contract_time()` (approved completion + days agreed on claims), `contract_obligations` (raised open, closed once, a waiver needs a reason, no future close date), row-level security and column grants; attestation subject text extended. |
| Kernel | `internal/kernel/contracttime.go`: CreateLDTerms, Submit/DecideLDTerms, ContractTimePosition (revised completion, agreed extensions, baseline schedule finish, days late, damages to date and forecast, capped), Obligations, AddObligation, CloseObligation; `LDAmount`. |
| Alerts | Rule kinds `notice_due`, `obligation_due`, `guarantee_expiring` (`pkg/automation`); the finding key is the alert stage (`DeadlineBucket`: 14, 7, 2 days, late), so an activated rule warns again at each stage. |
| API | `POST /v1/ld-terms` (+ submit/approve/reject), `GET /v1/contracts/{id}/time`, `GET/POST /v1/obligations`, `POST /v1/obligations/{id}/close`. OpenAPI regenerated. |
| Console | *Time for completion and liquidated damages* panel on every active contract (terms form, tiles, cap bar, extensions table, obligations); **Commercial & revenue → Contract obligations** (tiles, filters, add and close sheets); *Damages terms to approve* queue; alert rule form takes the new kinds; page guide. |

## Controls proven

| Control | Test |
|---|---|
| Terms prepared under `claim.manage`, approved by another person under `claim.approve` with fresh MFA; one set per contract; approved terms never change | TestContractTime_DamagesExtensionsObligations |
| Cap above the contract value and completion before signing refused by the database | same |
| Days agreed by the client on a claim move the revised completion (20 Oct + 8 = 28 Oct) and the damages | same |
| Damages = days late × rate, never negative, never above the cap | same (`LDAmount`) |
| Obligations: standing, closed once, waiver reason, no future date, never reopened (raw update refused), project scope | same |
| Deadline alerts find late and near items with stage keys | same, TestDeadlineBucket |

## Browser check (demo on :8080)

On `CTR-2026-000001`, `u-qs` set damages terms (complete by 1 Nov 2026, 15,000 a day, cap 1,675,000) and submitted
them; `u-commercial` found them in *Damages terms to approve* and approved them. The panel then showed 7 days late
and **105,000 ETB** damages to date (7 × 15,000), no baseline schedule (so no forecast), no extension yet. `u-qs`
added two obligations (one from the contract panel, one from the register): *Due within 7 days* and *Overdue*.
Night theme checked; no console errors. Two defects fixed: a stray "null" under the obligations table (a DOM
`append(null)`), and dates wrapping in the tiles.

## Demo data added

Damages terms LD-2026-000001 (approved) and obligations OBL-2026-000001 and OBL-2026-000002 on `CTR-2026-000001`.

## Full suite

`go vet ./...` and `go test ./... -count=1` with PostgreSQL 16 and the Chrome browser journeys: all 18 packages
with tests `ok`, 0 FAIL, exit 0 (after the console fixes above).
