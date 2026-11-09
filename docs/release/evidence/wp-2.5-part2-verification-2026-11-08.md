# WP-2.5 part 2 (paying certificates, subcontractor documents): verification, 2026-11-08

SRS: SUB-06, SUB-07, TRS-01. Migration `0056_subcontract_payments.sql`; `internal/kernel/subpayment.go`; routes
`POST /v1/subcontractor-documents`, `GET /v1/suppliers/{id}/subcontractor-documents`, `POST /v1/subcontract-payments`
and `/{id}/submit|approve|reject|release|cancel`.

## Controls proven (TestSubcontract_PaymentComplianceAndPayWhenCertified)

| Control | Result |
|---|---|
| A payment above what is open on a certificate | refused |
| A quantity surveyor prepares a payment | refused (payment.prepare) |
| Approval while a required insurance has expired | refused by the database, naming the document |
| Pay when certified, before the client certified the period | refused by the database |
| Same, after a client IPC for a later period is certified | approved |
| The preparer releases | refused (three different people) |
| Release journal | Dr 2050 subcontractor payable 139,500 / Cr 1050 payments in transit 139,500; approved and posted by finance |
| Paying the same certificate again | refused |
| Payee shown to readers | masked from the approved bank account (****6789); no keyed digest, no full number |

## Browser check (demo on :8080)

`u-buyer` added an Awash Bank account for the subcontractor and `u-finance-manager` approved it (normal supplier-bank
flow). `u-treasury` prepared SPY-2026-000001 for the open 122,400 of SPA-2026-000001 from the subcontract page,
`u-finance-manager` approved it, `u-treasury-2` released it; the release created its draft journal. `u-qs` registered an
insurance document on the subcontractor. No console errors. Fixed during the check: the company-bank list asked for
status APPROVED (company accounts are ACTIVE); the payee panel showed a mask of the keyed digest (`****824b`) instead of
the account's real last four digits; there is now a projection for subcontract payments like supplier payments; and
a subcontractor without an approved bank account now gets an explanation instead of an empty list.

## Demo data added

An approved bank account for Safari Building Supplies (synthetic); payment SPY-2026-000001 (released, journal draft);
insurance document CAR-2026-0042.

## Still pending in WP-2.5

Settling subcontract payments from the bank statement, subcontract variations and their change-event links, the
subcontractor portal, the certificate PDF.

## Full suite

`go vet ./...` and `go test ./... -count=1` with PostgreSQL 16 (300 connections) and the Chrome browser journeys: all 18
packages with tests `ok`, 0 FAIL, exit 0.
