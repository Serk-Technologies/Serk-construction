# WP-2.7 Guarantees and price adjustment: verification, 2026-11-08

SRS: CON-05 (guarantees and bonds against a contract, with expiry tracking), CON-07 (contract price
adjustment by published indices), TRS-04 (guarantees as treasury instruments).

## Delivered

| Layer | What |
|---|---|
| Database | `0050_bonds_escalation.sql`: `guarantees`, `price_indices`, `price_index_values`, `escalation_formulas`, `escalation_terms`, `price_adjustments`, all with row-level security, column grants and guards; `price_adjustment_factor()` computes Pn = a + Σ b·(Ln/Lo), rounded half-even to 6 decimals, from approved values only; `escalation_formula_check()` requires a + Σb = 1 exactly and approved base values. Attestation subject text and project scope extended to the four new documents. |
| Authorisation | `bond.manage` (treasury officer), `bond.approve` (commercial manager), `escalation.manage` (quantity surveyor), `escalation.approve` (commercial manager); two new duty-conflict pairs (Guarantees; Price adjustment); plain-language labels; role matrix regenerated. |
| Kernel | `internal/kernel/bonds.go`: RegisterGuarantee, ChangeGuarantee, CloseGuarantee, Guarantees (standing, totals, advance cover); CreatePriceIndex, ProposeIndexValue, Submit/DecideIndexValue, PriceIndices; CreateFormula, Submit/DecideFormula; PreviewAdjustment, PrepareAdjustment, Submit/DecideAdjustment; Escalation. |
| API | `GET/POST /v1/guarantees`, `POST /v1/guarantees/{id}/change|close`, `GET/POST /v1/price-indices`, `POST /v1/price-index-values` (+ submit/approve/reject), `POST /v1/escalation/formulas` (+ submit/approve/reject), `GET /v1/escalation?contract=`, `GET /v1/price-adjustments/preview`, `POST /v1/price-adjustments` (+ submit/approve/reject). OpenAPI regenerated (20 routes). |
| Console | **Commercial & revenue → Guarantees & price adjustment**. Three tabs: **Guarantees** (summary tiles, advance payment cover bars, cards showing elapsed term, filters, register, reduce or extend, release or call); **Price indices** (cards with latest value, monthly change and trend line, approval queue); **Price adjustment** (the formula with its weights bar, a composer that checks a + Σb = 1 as you type, certificates with *Compute*, a breakdown by index and *Prepare and submit*). The four documents also appear in lists, document pages and the My work queues. Page guide added. |

## Controls proven

| Control | Where enforced | Test |
|---|---|---|
| A guarantee's value only goes down and its expiry only later, each with a reason; other fields never change | kernel and database guard | TestBonds_GuaranteeRegister (raw updates refused) |
| Release or call: `bond.approve`, not the registrar, fresh MFA, attestation consumed by the database | kernel + `approval_use` in the guard | same; raw release without attestation refused |
| A call never exceeds the guarantee's value; an expired guarantee cannot be called; a closed one never changes | database CHECK and guard | same |
| Standing (in force, expires within 30 days, expired) and advance cover (advance received − recovered vs advance guarantees in force) | kernel query | same |
| Index values: maker/checker, fresh MFA, never for a future month, one live value per month, approved never changes | kernel + database guard + unique index | TestBonds_PriceAdjustment, TestAPIBondsAndIndices |
| A formula adds up to exactly 1, needs approved base values, one per contract, indices only while draft | kernel + database | TestBonds_PriceAdjustment |
| The adjustment is computed by the database for a certified IPC, from approved values only, between the base month and the IPC month, once per IPC, and recomputed at approval; the amount cannot be edited | database guard | same (golden case 1.1625 × 350,000 → 56,875) |
| Project scope; an outsider sees nothing | kernel | both kernel tests |

## Browser check (demo on :8080)

`u-treasury` registered an advance guarantee (1,500,000, 25 days left: *Expires soon*) and a performance guarantee
(1,675,000); a negative amount was refused in the sheet; extending the advance guarantee returned it to *In force*.
`u-qs` added CEM and FUEL and proposed five monthly values; `u-commercial` approved four and rejected one.
Submitting the formula was then **refused by the database** ("no approved base value (2026-07) for: FUEL"): the
walk-through had rejected the FUEL base value. After the value was proposed again and approved, the formula (0.30 +
0.45 CEM + 0.25 FUEL, base Jul 2026) was submitted and approved; the composer showed 0.950000 in red before the
weights added up. *Compute* on IPC-2026-000001 (work 1,830,000) gave factor **1.071102** and adjustment
**130,116.66 ETB**, matching the hand calculation; it was prepared, submitted, found in *Price adjustments to approve*
and approved. `u-commercial` sees the release form, not the change form. Night theme and 390 px (no horizontal
overflow) checked; no console errors.

One defect fixed during the check: the approval queue's buttons all read "Approve". They now carry the index and
month in their accessible names ("Approve FUEL Jul 2026"), and the queue is sorted by index and month.

## Demo data added

A synthetic contract `CTR-2026-000001` (Mekelle ring road lot 2, 16,750,000; advance 1,500,000) with a verified
measurement and certified IPC-2026-000001 (1,830,000). Guarantees BG-2026-000001 (advance, extended to 31 Mar 2027)
and BG-2026-000002 (performance). Indices CEM and FUEL with approved values (CEM Jul, Oct 2026; FUEL Jul, Sep, Oct
2026; one rejected FUEL Jul value), formula ESC-2026-000001 approved, adjustment PA-2026-000001 approved.

## Not in this package

Billing an approved adjustment to the client (a customer invoice and its journal) is not built; the IPC, its invoice
and journals are unchanged by this package. The approved total is shown per contract.

## Full suite

`go vet ./...` and `go test ./... -count=1` with PostgreSQL 16 and the Chrome browser journeys: every package `ok`,
0 FAIL, exit 0 (18 packages with tests), including TestBonds_GuaranteeRegister, TestBonds_PriceAdjustment,
TestAPIBondsAndIndices, the OpenAPI contract test and the privilege catalogue invariants.
