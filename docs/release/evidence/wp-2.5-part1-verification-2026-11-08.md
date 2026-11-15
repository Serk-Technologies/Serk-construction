# WP-2.5 part 1 (subcontracts, certificates): verification, 2026-11-08

SRS: SUB-01..05, PCC-02. Migration `0055_subcontracts.sql`; `internal/kernel/subcontract.go`;
`internal/httpapi/subcontract_handlers.go`; console menu **Subcontracts** (Subcontracts, Applications and certificates,
Back-charges, Subcontract accounts). Permissions: `subcontract.manage` and `subcontract.apply` (quantity surveyor),
`subcontract.approve` and `subcontract.certify` (commercial manager); two new duty-conflict pairs.

## Arithmetic fixture (TestSubcontract_CertificatesAndJournal)

| | Certificate 1 | Certificate 2 |
|---|---|---|
| Certified to date (10,000 m3 × 120; 2,000 m2 × 400) | 4,000 / 500 → 680,000 | 9,000 / 2,000 → 1,880,000 |
| This period | 680,000 | 1,200,000 |
| Retention 10%, cap 100,000 | 68,000 | 32,000 (cap reached) |
| Advance 200,000 at 20% | 136,000 | 64,000 (fully recovered) |
| Back-charge | 0 | 50,000 (deducted once) |
| Withholding 2% | 13,600 | 24,000 |
| Net payable | 462,400 | 1,030,000 |

Journal of certificate 1: Dr 5000 680,000 / Cr 2050 payable 462,400, Cr 2060 retention 68,000, Cr 1170 advance
136,000, Cr 2310 withholding 13,600; approved and posted by finance. The approved subcontract adds exactly 2,000,000
to the cost code's commitment, and certified work stays inside it. Also proven: an AR clerk cannot prepare; a line on a
liability account is refused; the QS cannot approve or certify; a quantity above the subcontract is refused; two open
applications, an application for an already certified period, and an edit of a certificate are refused; an outsider
cannot read the subcontract.

## Browser check (demo on :8080)

`u-controller` proposed the subcontract accounts (2050, 2060, 1170, 2310, 5300) and `u-cfo` approved them; `u-qs`
prepared *Culverts and drainage* (400 m × 1,500 = 600,000; retention 5% capped at 50,000; advance 100,000 at 25%;
withholding 2%) and submitted it; `u-commercial` approved it. `u-qs` entered an application of 150 m; `u-commercial`
certified 120 m: this period **180,000**, retention 9,000, advance 45,000, withholding 3,600, net **122,400**, all as
computed by hand. No console errors. A first walk-through stopped early because the script re-opened the page it
was already on (no reload); that run left an extra approved subcontract SUB-2026-000001 and a second subcontract-policy
proposal.

## Not in part 1

Paying certificates (payment instructions), pay-when-certified, compliance documents, subcontract variations and their
link to change events, the subcontractor portal, the certificate PDF.

## Full suite

`go vet ./...`, the role matrix regenerated, and `go test ./... -count=1` with PostgreSQL 16 and the Chrome browser
journeys: 17 of 18 packages `ok`; in `internal/kernel` one test, TestJourneyB_RetentionReleaseRace, failed at fixture
setup with "cannot open tenant database session" (exit 1). It passed three times alone and the whole kernel package passed
on rerun (480 s). Cause: the test PostgreSQL's 100-connection limit under the parallel suite (each test company opens its
own pool); the test container now runs with `max_connections=300` and `runlocal.md` says so. This run also covers
WP-2.4's `TestChange_LinkedVariations`.
