# WP-1.7 (project master, organisation structure, calendar): verification run 2026-11-07

Environment: Go 1.27.2, PostgreSQL 16, Chrome 157 via SERK_CHROME. Migrations 0001-0042.

## Full suite (before the last small change, which added the creator-sees-own-project rule and re-ran TestOrg): go test -race -count=1 -p 1 ./...
```
ok  	github.com/serk-group/serk-construction/internal/config	1.034s
ok  	github.com/serk-group/serk-construction/internal/export	2.510s
ok  	github.com/serk-group/serk-construction/internal/httpapi	216.892s
ok  	github.com/serk-group/serk-construction/internal/kernel	320.755s
ok  	github.com/serk-group/serk-construction/internal/obs	1.029s
ok  	github.com/serk-group/serk-construction/internal/relay	2.230s
ok  	github.com/serk-group/serk-construction/pkg/attest	1.021s
ok  	github.com/serk-group/serk-construction/pkg/auth	1.023s
ok  	github.com/serk-group/serk-construction/pkg/automation	1.025s
ok  	github.com/serk-group/serk-construction/pkg/datakey	1.021s
ok  	github.com/serk-group/serk-construction/pkg/evm	1.025s
ok  	github.com/serk-group/serk-construction/pkg/fx	1.027s
ok  	github.com/serk-group/serk-construction/pkg/ledger	1.023s
ok  	github.com/serk-group/serk-construction/pkg/legacy	1.062s
ok  	github.com/serk-group/serk-construction/pkg/money	1.030s
ok  	github.com/serk-group/serk-construction/pkg/schedule	1.024s
ok  	github.com/serk-group/serk-construction/pkg/tax	1.024s
ok  	github.com/serk-group/serk-construction/pkg/workflow	1.038s
EXIT 0
```

## New tests
- kernel: TestOrg_ProjectLifecycleThroughGates (PLANNED/BID on creation; raw SQL cannot insert an ACTIVE project, skip the attestation, move the phase or rename; gates cannot skip phases, one open gate, author cannot decide, stale MFA refused, approval moves the project atomically, rejection leaves it, full life to CLOSED), TestOrg_MasterDataRegionsAndRollups (dates, coordinates, scope, regions, business units, dashboard roll-up by region and unit, calendar)
- http: TestAPI_ProjectMasterAndGates

## Defects found by this verification and fixed
1. SECURITY GAP CLOSED: tenant logins could insert projects directly (only UPDATE had been revoked), creating ACTIVE projects without any control. Tenant-created projects are now PLANNED/BID with an attested creation; the operator's tooling is unchanged. TestSC10 relied on the gap and now creates its second project as the operator does.
2. The calendar used DELETE, which the platform's privilege catalogue forbids for application roles; entries are now removed (kept, marked removed, audited), never deleted.
3. A project's creator could not see it afterwards (visibility follows the identity-provider token, which does not list a new project): the creator now sees projects they created.
4. Tenant projects.tenant_id gained the same DEFAULT current_tenant() as every other table.

## Real-browser check (live demo stack, migration 0042 applied)
Controller adds a region and a calendar day; commercial manager creates a project through the form, it is listed in phase Bid and the manager is not offered Edit; the CFO sees it; the dashboard offers a region filter; Stage gates is in the sidebar; no script or CSP errors. ALL PASS.
