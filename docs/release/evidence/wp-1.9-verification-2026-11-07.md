# WP-1.9 (release 1 hardening and documentation): verification run 2026-11-07

## Full suite: go test -race -count=1 -p 1 ./...
```
ok  	github.com/serk-group/serk-construction/internal/config	1.049s
ok  	github.com/serk-group/serk-construction/internal/export	2.178s
ok  	github.com/serk-group/serk-construction/internal/httpapi	201.608s
ok  	github.com/serk-group/serk-construction/internal/kernel	535.721s
ok  	github.com/serk-group/serk-construction/internal/obs	1.042s
ok  	github.com/serk-group/serk-construction/internal/relay	3.675s
ok  	github.com/serk-group/serk-construction/pkg/attest	1.036s
ok  	github.com/serk-group/serk-construction/pkg/auth	1.038s
ok  	github.com/serk-group/serk-construction/pkg/automation	1.045s
ok  	github.com/serk-group/serk-construction/pkg/datakey	1.037s
ok  	github.com/serk-group/serk-construction/pkg/evm	1.050s
ok  	github.com/serk-group/serk-construction/pkg/fx	1.038s
ok  	github.com/serk-group/serk-construction/pkg/ledger	1.033s
ok  	github.com/serk-group/serk-construction/pkg/legacy	1.044s
ok  	github.com/serk-group/serk-construction/pkg/money	1.035s
ok  	github.com/serk-group/serk-construction/pkg/schedule	1.032s
ok  	github.com/serk-group/serk-construction/pkg/tax	1.041s
ok  	github.com/serk-group/serk-construction/pkg/workflow	1.070s
EXIT 0
```

## What was delivered and how it is kept true
- api/openapi.yaml describes all 238 served routes (was 28 at the start of the session). Hand-written entries kept; 152 generated stubs marked x-generated. TestOpenAPIMatchesRoutes fails if a route is added without an entry or an entry has no route. tools/gen_openapi.py adds stubs.
- docs/security/ROLE_PERMISSION_MATRIX.md is generated from the role catalogue (TestRoleMatrixDocumentIsCurrent), with the duty-conflict catalogue and the never-delegable list.
- docs/operations/ADMIN_RUNBOOK.md, docs/release/RELEASE_NOTES_R1.md, docs/release/UAT_PLAN_R1.md written; GO_NO_GO.md updated (decision unchanged: NO-GO).
- make gen-docs regenerates every generated document.

## Not done (cannot be done here)
Independent UAT by the area leads, the signed GO record, penetration test, load test, identity provider, staging, vault and HSM, observability. The release is not released.
