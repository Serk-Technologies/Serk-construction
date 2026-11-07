# WP-1.2 (authority matrix, delegation, duty-conflict exceptions): verification run 2026-11-07

Environment: Go 1.27.2, PostgreSQL 16 (alpine, trust auth, deploy/00-roles.sql applied), Chrome 157 via SERK_CHROME, Playwright 1.63. Migrations 0001-0040.

## Full suite after the last code change: go test -race -count=1 -p 1 ./...
```
ok  	github.com/serk-group/serk-construction/internal/config	1.031s
ok  	github.com/serk-group/serk-construction/internal/export	1.838s
ok  	github.com/serk-group/serk-construction/internal/httpapi	107.202s
ok  	github.com/serk-group/serk-construction/internal/kernel	283.787s
ok  	github.com/serk-group/serk-construction/internal/obs	1.023s
ok  	github.com/serk-group/serk-construction/internal/relay	2.120s
ok  	github.com/serk-group/serk-construction/pkg/attest	1.025s
ok  	github.com/serk-group/serk-construction/pkg/auth	1.021s
ok  	github.com/serk-group/serk-construction/pkg/automation	1.031s
ok  	github.com/serk-group/serk-construction/pkg/datakey	1.024s
ok  	github.com/serk-group/serk-construction/pkg/evm	1.030s
ok  	github.com/serk-group/serk-construction/pkg/fx	1.026s
ok  	github.com/serk-group/serk-construction/pkg/ledger	1.027s
ok  	github.com/serk-group/serk-construction/pkg/legacy	1.024s
ok  	github.com/serk-group/serk-construction/pkg/money	1.027s
ok  	github.com/serk-group/serk-construction/pkg/schedule	1.020s
ok  	github.com/serk-group/serk-construction/pkg/tax	1.024s
ok  	github.com/serk-group/serk-construction/pkg/workflow	1.037s
EXIT 0
```
(httpapi 107 s includes the five real-browser journeys A, B, C, planning, site.)

## New tests
- kernel: TestAuthority_MatrixGovernanceAndLimits, TestAuthority_DatabaseRefusesBypass (13 raw-SQL forgeries refused, tenant isolation), TestAuthority_DelegationLifecycle, TestAuthority_ConflictingDelegationIsDroppedUnlessExcepted, TestAuthority_MatrixLimitsPurchaseOrders
- http: TestAPI_AuthorityAndDelegationOverHTTP
- pkg/auth: TestDelegatedAuthority, TestDelegationAllowed, TestSoDViolations (and the role catalogue is now tested against auth.DefaultSoDConflicts, 48 controls)

## Real-browser check of the new console pages (live demo stack, migration 0040 applied)
Personas u-process-owner, u-cfo, u-commercial, u-security-admin, u-planner: matrix prepared through the form, submitted, approved by the CFO; Authority & duties page shows the matrix in force and no role conflicts; delegation created, approved by a third person, visible to the delegate, revoked by the delegator; the author never sees Approve on their own document; no script or CSP errors. ALL PASS.

## Defects found by this verification and fixed
1. Process owners could create a matrix but not open it (read permission was p2p.read for every document type). Reads now depend on the document type (kernel readPerm); regression asserted in TestAuthority_MatrixGovernanceAndLimits.
2. A test world's second WBS version retired the cost code a BOQ needs (test fixture, not product).
3. A refused request to the SoD report was logged by the browser for users without the permission; the page now asks only when allowed.

## Not verified / known limits
See docs/IMPLEMENTATION_PLAN.md WP-1.2 (signer trust for delegated permissions, kernel-only matrix check for requisitions/orders/invoices/payments, no matrix coverage yet for journals, stock and contracts).
