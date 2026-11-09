# SERK Construction OS: implementation plan

Status: v1.0, 2026-11-06. Companion to the SRS
([docs/srs/SERK_Construction_SRS_v1.0.pdf](srs/SERK_Construction_SRS_v1.0.pdf)).
Every work package (WP) below names the SRS requirements it delivers, so
progress can be traced requirement by requirement.

This is the working plan we follow up against. **How to use it:**

- Work is cut into work packages (`WP-<phase>.<n>`). One WP is one pull request,
  or a short series of them, that can be reviewed, tested and merged on its own.
- Each WP lists: goal, SRS IDs, dependencies, migration, data model, state
  machine, permissions and roles, database guards, code touch points, tests,
  acceptance criteria and an estimate in engineer-weeks (ew).
- Progress is tracked in the **status board** (section 9). Update the row when a
  WP starts, is in review, and is done (with the PR or commit hash and the
  evidence file).
- "Done" means the Definition of Done in section 3 is met. It does not mean the
  code exists.
- Estimates assume the team in section 4. They are planning numbers, not
  commitments, and will be re-baselined at the end of each phase.

---

## 1. Starting point (as of commit 5f94bc3 plus uncommitted dev work)

| Area | State | Evidence |
|---|---|---|
| Financial core: ledger, periods, FX, reversals, attestations, isolated signer | Built and tested | gate_a_*, review3-12 regression suites |
| Procure to pay (Journey A) | Built, kernel + HTTP + browser | journey_a_test, e2e/console_journey_a.mjs |
| Contract to cash (Journey B) | Built (no claims, EOT, LDs, bonds) | journey_b_test |
| Stock FIFO/WAC, transfers, counts (Journey C) | Built (no reservations, batches, UoM) | journey_c_test |
| Planning: CPM, baselines, progress, EVM | Partial (no constraints, resources, P6) | schedule_test, console_planning.mjs |
| Site diaries, devices, offline sync | Built (no mobile app) | site_offline_test |
| Platform: settings, notifications, automation, intelligence, exports | Built | platform_test, UI_EXPORTS_REPORTS.md |
| Identity | HS256 dev tokens and OIDC verifier; no IdP connected | GATEA-02 and GATEA-07 BLOCKED |
| Operations | PITR drill done; no staging with TLS or vault; no OTel collector; no CI | GO_NO_GO.md |
| Missing modules | Subcontracts, payroll, equipment, fixed assets, consolidation, document control, QHSE, portals, mobile app | BACKLOG.md |
| Test suite | 378 pass, 0 fail, 0 skip (recorded) | docs/release/evidence |
| Release decision | **NO-GO** | GO_NO_GO.md |

SRS baseline coverage: 57 of 253 requirements exist, 55 are partial and 141
are new.

---

## 2. Engineering principles (non-negotiable for every WP)

1. **Recipe per module** (from docs/architecture/MODULES.md): migration, then
   store repo (SQL only), then kernel command (permission, scope, `WithTenant`,
   audit + outbox), then HTTP handler, then a console `DOCS` entry or page,
   then tests, then seed, then a BACKLOG update.
2. **Controls twice.** Every maker/checker rule, state transition and amount
   rule is enforced in the kernel **and** by PostgreSQL (CHECK, trigger,
   attestation verification). A WP that adds a human decision must add its
   attested action to `approval_is_draft_action` handling and to `pkg/auth`.
3. **Additive migrations.** Never edit a committed migration. Numbers are
   reserved in this plan (section 8) to avoid collisions between parallel WPs.
4. **Exact money.** `pkg/money` and `numeric` only; rounding rules have tests.
5. **No AI or automation decides.** New permissions that approve, certify,
   post, pay, release, close or configure are human-only by default.
6. **Every table**: `tenant_id DEFAULT current_tenant()`, RLS with
   `tenant_isolation` and `owner_maintenance` policies, `REVOKE ALL FROM
   PUBLIC`, column-level grants to `serk_app`, a pinned `search_path` on every
   function, immutability triggers on posted or decided rows.
7. **Exports show no more than the screen.** New lists are added to
   `/v1/export` through the same read path.
8. **Accessibility and phones.** Every new screen passes the existing browser
   audit (no horizontal scroll at 390 px, labelled controls, no CSP
   violations).

---

## 3. Definition of Done (per WP)

- [ ] Migration applies on a fresh database and on a copy of the previous release.
- [ ] Kernel tests on a disposable database (`testdb.Setup`), including:
      the happy path, every refusal, maker = checker refused, cross-tenant refused,
      cross-project refused, AI/service principal refused for decisions.
- [ ] A **database regression probe** for each guard: the same violation attempted
      with raw SQL as the tenant login is refused.
- [ ] Concurrency test where two requests can race (decisions, stock, budget).
- [ ] API test (HTTP status codes, problem+json errors, idempotency key replay).
- [ ] Console: list, detail and actions; browser journey extended if the WP is
      on a journey path.
- [ ] Exports include the new list.
- [ ] `api/openapi.yaml` updated.
- [ ] Seed data in `cmd/serk-seed` so testers see the feature.
- [ ] `testmanual.md` gains test cases (format of the existing cases).
- [ ] `docs/BACKLOG.md` and the status board updated; evidence log in
      `docs/release/evidence/`.
- [ ] `go vet`, `gofmt`, `go test -race -count=1 -p 1 ./...` green in CI.
- [ ] Reviewed by two engineers for financial, approval, stock, payroll, contract
      or sync code.

---

## 4. Team and cadence

| Role | R0 | R1 | R2 | R3 | R4 |
|---|---|---|---|---|---|
| Tech lead / architect | 1 | 1 | 1 | 1 | 1 |
| Backend (Go/PostgreSQL) | 2 | 4 | 5 | 4 | 3 |
| Frontend (console, portals) | 1 | 2 | 2 | 2 | 2 |
| Mobile (field app) | 0 | 0 | 1 | 2 | 1 |
| QA automation | 1 | 1 | 2 | 2 | 1 |
| DevOps / SRE | 1 | 1 | 1 | 1 | 1 |
| Security engineer | 0.5 | 1 | 0.5 | 0.5 | 0.5 |
| Domain experts: QS, accountant, payroll, HSE (part time) | 0.5 | 1 | 2 | 1.5 | 0.5 |

Cadence: 2-week sprints; a demo every sprint with a domain expert; a phase gate
review at the end of each phase (section 7); an independent code review before
each release.

---

## 5. Roadmap

| Phase | Weeks (indicative) | Theme | Exit |
|---|---|---|---|
| **R0** Foundations | 1-6 | CI, console shell, design system, i18n, IdP, staging | CI green on every PR; staging with TLS and IdP |
| **R1** Financial core GA | 7-20 | Close Gate A; budget control; dashboards; reports; first paying customer | Gates R1-R5, R8, R9; pentest; signed GO |
| **R2** Project controls + commercial | 21-40 | Forecasting, WIP/IFRS 15, change events, subcontracts, claims, bonds, payroll, fixed assets, consolidation, tax | Finance and tax sign-off; payroll fixtures signed |
| **R3** Field, documents, QHSE | 41-60 | Mobile app, timesheets, equipment, CDE, RFIs, submittals, quality, HSE, P6 | Gate R6; field pilot on 2 live projects |
| **R4** Intelligence + ecosystem | 61-76 | Document AI, predictive risk, portals, BI, report builder, SCIM, scale | Gate R7; performance targets met |

Critical path: WP-0.2 CI, then WP-0.5 IdP, then WP-1.1 Gate A closure, then R1 GA.
Budget revisions (WP-1.3) unblock forecasting (WP-2.1), which unblocks WIP
(WP-2.2) and IFRS 15 (WP-2.3). Timesheets (WP-3.3) unblock payroll costing in
production (WP-2.8 runs on imported hours until then) and HSE indicators.

---

## 6. Work packages

### Phase R0: Foundations (weeks 1-6)

#### WP-0.1 Console navigation: scrollable, collapsible sidebar ✅ (delivered 2026-11-06)
- **SRS:** EI-UI-01, NFR-USB-01, NFR-USB-03.
- **Delivered:**
  - The sidebar sticks under the top band and scrolls independently (`nav`
    with `position: sticky`, `height: calc(100vh - var(--top-h))`,
    `overflow-y: auto`). `--top-h` follows the band's real height through a
    `ResizeObserver`.
  - Categories reorganised along the flow of work: Overview, Projects &
    planning, Procurement, Site, Stores, Commercial & revenue, Payables,
    Treasury & banking, Accounting, Governance.
  - Each category heading is a button (`aria-expanded`, `aria-controls`) with a
    count badge and a chevron. "Expand all" and "Collapse all" sit above the
    categories. The folded set is stored in `localStorage` (`serk.nav.folded`).
  - The category holding the current page always opens and the active link is
    scrolled into view.
  - A "Find a page" filter searches every category, folded or not. Enter opens
    the first match; Escape clears.
  - Phones keep the single "Menu" button; categories fold inside it.
- **Files:** `internal/webui/static/app.js` (`NAV`, `buildNav`, `markNav`),
  `internal/webui/static/app.css` (shell and sidebar section).
- **Remaining:** run the browser journeys (`console_browser_*_test.go`) in CI
  once WP-0.2 lands; add a screenshot to `docs/testing/evidence`.

#### WP-0.8 Self-explanatory pages ✅ (delivered 2026-11-07)
- **SRS:** EI-UI-01, NFR-USB-02, NFR-USB-06.
- **Delivered:** every console page shows a "How this page works" card under
  its title: what the page is for, how to use it, the document life cycle,
  who prepares and who decides, and the rules the system enforces.
  - Content: `internal/webui/static/guide.js` (hand-written guides for special
    pages, one purpose sentence per document type).
  - Life cycles and "who does it" are generated from the console's `DOCS`
    action metadata and the server's role catalogue, `GET /v1/meta/roles`
    (`Authorizer.Catalogue`), so they cannot drift from what is enforced.
  - The card folds; closing it is remembered per page. It is hidden in print.
  - Files carry header comments explaining their purpose (app.js file map,
    guide.js, budget.go, 0039).
- **Tests:** `TestAPI_MetaRolesCatalogue` (authenticated, no data leaks);
  browser check of the card on list, document, form and special pages.

#### WP-0.2 Continuous integration
- **SRS:** NFR-SEC-05, NFR-MNT-03, Section 12.
- **Tasks:**
  1. Add `.github/workflows/ci.yml` (or the equivalent for the chosen host):
     - job `lint`: `gofmt -l`, `go vet`, `staticcheck`;
     - job `test`: PostgreSQL 16 service container, `SERK_TEST_ADMIN_DSN`,
       `go test -race -count=1 -p 1 ./...`, upload the test log as an
       artefact;
     - job `browser`: Node and Playwright, the `console_browser_*` suites;
     - job `supply-chain`: `govulncheck`, `tools/verify_gosum_origin.py`
       against sum.golang.org, `tools/osv_scan.py`, SBOM (`cyclonedx-gomod`);
     - job `image`: build `deploy/Dockerfile` and sign it with cosign
       (keyless or KMS).
  2. Branch protection: two reviews on `internal/kernel`, `internal/store`,
     `pkg/auth`, `pkg/money`, `pkg/ledger` and migrations (CODEOWNERS).
  3. Have `tools/gate_status.py` run in CI and fail if evidence is missing.
- **Acceptance:** a PR with a failing race test, a vulnerable dependency or a
  gofmt error is blocked. GATEA-12 moves from BLOCKED to PASS.
- **Estimate:** 2 ew (DevOps).

#### WP-0.3 Commit and harden the containerised dev environment
- **SRS:** NFR-MNT-05.
- **Tasks:** commit `.dockerignore`, the `deploy/Dockerfile` devtools target,
  the compose changes, `scripts/dev-seed.sh`, `runlocal.md` and `testmanual.md`.
  Review the `/run/serk` directory in the production image (signer socket),
  and add a `make up`, `make seed` and `make reset` wrapper.
- **Acceptance:** a fresh clone reaches a seeded, signed-in console with three
  commands.
- **Estimate:** 0.5 ew.

#### WP-0.4 Console design system and i18n framework
- **SRS:** ADM-04, NFR-USB-04, NFR-USB-07, RPT-06.
- **Tasks:**
  1. Extract design tokens (colours, spacing, type) already in `app.css` into a
     documented token table in `docs/design/TOKENS.md`. Add components: page
     header with breadcrumbs, KPI tile, data table with sort, sticky header and
     column chooser, filter bar, empty state, side drawer, confirmation dialog
     with reason field, toast.
  2. i18n: wrap every user-visible string in `t('key')`, with message catalogues
     `static/i18n/en.json` and `am.json` (and `ti.json` and `om.json` later). The
     language comes from the company setting and the user preference. Numbers
     and dates use `Intl`. Canonical data is never translated.
  3. Ethiopian calendar display helper (`ethiopic.js`, a pure conversion with
     tests); storage stays UTC/Gregorian.
  4. Breadcrumbs and a "last updated" freshness stamp on every list.
- **Acceptance:** switching the language to Amharic changes every label without
  reload errors; all journeys pass in both languages; a catalogue lint fails
  on missing keys.
- **Estimate:** 5 ew (frontend), plus translator time.

#### WP-0.5 Identity provider integration (OIDC, step-up, revocation)
- **SRS:** IAM-02, IAM-04, IAM-11, NFR-SEC-04. Closes GATEA-02 and GATEA-07.
- **Tasks:**
  1. Keycloak in the dev and staging compose (realm export in
     `deploy/keycloak/`), with persona users matching the seed roles.
  2. API: the OIDC verifier is already in `internal/httpapi/oidc.go`. Map
     claims `pkind`, `amr`, `auth_time` and `acr`. Step-up is a re-auth with
     `max_age` and `acr_values`; the attestation binds the `auth_time`.
  3. Console: an authorization-code + PKCE login. Remove the token paste box
     outside development.
  4. Back-channel logout and session revocation feed `sessions.go`.
- **Acceptance:** approval without a fresh MFA in the last 10 minutes is refused
  with a prompt to re-authenticate; a user disabled in the IdP is refused
  within 60 s.
- **Estimate:** 4 ew.

#### WP-0.6 Staging environment with TLS, vault and KMS signer
- **SRS:** NFR-SEC-01, NFR-SEC-04, NFR-AVL-05. Closes GATEA-08.
- **Tasks:** staging on the chosen host (Railway config exists in
  `deploy/railway/`); Caddy or edge TLS; secrets from a vault (Doppler,
  Vault or the cloud KMS); the signer key in KMS with a documented key
  ceremony (`docs/operations/KEY_CUSTODY_RUNBOOK.md`); nightly encrypted
  backup to object-locked storage; the restore drill scheduled.
- **Acceptance:** staging passes `check-prod-config`; drill evidence in
  `docs/operations/evidence`.
- **Estimate:** 3 ew.

#### WP-0.7 Observability
- **SRS:** NFR-MNT-06. Closes GATEA-11.
- **Tasks:** OTel SDK (dependency review first); OTLP to a collector;
  Prometheus and Alertmanager using `deploy/observability/prometheus-alerts.yml`;
  Grafana dashboards (API, DB, outbox, workflow, sync); nightly full audit-chain
  verification job.
- **Estimate:** 2 ew.

---

### Phase R1: Financial core GA (weeks 7-20)

#### WP-1.1 Gate A closure and production readiness
- **SRS:** NFR-SEC-03, NFR-SEC-07, NFR-PERF-01..04, NFR-AVL-02.
- **Tasks:** an independent penetration test (external vendor) and remediation;
  a load and soak test (k6 scripts in `tools/load/`) at 2,000 users, 500
  projects and 50 million journal lines; an index review with `pg_stat_statements`;
  partition `journal_lines` and `audit_log` by tenant and month; edge rate limiting.
- **Acceptance:** NFR-PERF targets met; no open critical or high finding;
  `GATE_STATUS.json` Gate A = PASS.
- **Estimate:** 6 ew (plus vendor).

#### WP-1.2 Delegation of authority matrix and SoD analysis
- **SRS:** IAM-09, IAM-13, IAM-14.
- **Migration:** `0040_authority_matrix.sql`.
- **Data model:**
  - `authority_matrix(id, version, status DRAFT|APPROVED|RETIRED, created_by, approved_by)`;
  - `authority_rule(matrix_id, document_type, permission, role, entity_id NULL, project_id NULL, min_amount, max_amount, currency, requires_mfa, requires_second)`;
  - `delegation(id, delegator, delegate, permission_set, project_scope, valid_from, valid_to, reason, status, approved_by)`;
  - `sod_conflict_rule(id, perm_a, perm_b, severity)` and
    `sod_exception(user, rule_id, mitigating_control, approved_by, expires_at)`.
- **Guards:** the kernel and the DB resolve the approver's limit from the
  approved matrix version at decision time; a delegate's limits are no higher
  than the delegator's; a delegation never lets someone approve their own
  document; only one APPROVED matrix version per tenant.
- **Permissions:** `authority.manage` (process_owner), `authority.approve` (cfo),
  `delegation.request` (any human), `delegation.approve` (security_admin plus
  the delegator's manager).
- **Console:** Governance → Authority matrix (versioned table editor, diff
  between versions); My work → Delegate my approvals; Governance → SoD
  conflicts report.
- **Tests:** an approval above the limit is refused; a delegated approval
  within limits is accepted and audited with both identities; a delegation to
  self is refused; expiry mid-workflow.
- **Estimate:** 4 ew.
- **Delivered and verified (2026-11-07), as built:**
  - Migration `0040_authority_matrix.sql`; kernel `internal/kernel/authority.go`; API in
    `internal/httpapi/authority_handlers.go`; console page *Authority & duties*, forms and lists.
  - **Matrix** (`authority_matrices`, `authority_rules`): maker/checker, CFO approval with fresh MFA,
    one in force (a successor's attested approval retires the old one), overlapping bands refused.
    Limits are permission tiers: a band names the permission an approver must hold (and optionally fresh MFA).
  - **Enforcement:** budget documents by the database from the permissions in the decider's signed
    attestation (`budget_zz_authority`); requisitions, purchase orders, supplier invoices and payments
    by the kernel (`assertAuthority`) before the signature is requested.
  - **Delegation:** at most 90 days, approved by a third person with fresh MFA, revocable by the
    delegator or an approver; never lends `NonDelegable` permissions or ones the delegator does not hold
    by role; a delegate never decides what the delegator prepared (`CheckMakerChecker`).
  - **Duty conflicts:** `auth.DefaultSoDConflicts` (48 controls, also the role-catalogue test);
    a delegation that would give the delegate a conflicting set is dropped unless an approved,
    expiring `sod_exceptions` row covers each conflicting pair for that person.
  - Tests: `authority_test.go` (5), `TestAPI_AuthorityAndDelegationOverHTTP`, `pkg/auth` (3).
  - **Known limits (not hidden):** (1) delegated permissions travel in the attestation like role
    permissions, so the database trusts the signer for them (residual R-SIGNER); (2) the matrix check for
    requisitions, orders, invoices and payments is in the kernel, not yet in the database triggers of
    those documents; (3) journals, stock and contract approvals are not yet covered by the matrix;
    (4) role assignments live in the identity provider, so the conflict report covers the role
    catalogue, and per-person conflicts are stopped when delegations are applied.


#### WP-1.3 Budget revisions, transfers and contingency (BACKLOG #1)
- **SRS:** PCC-01, PCC-02, EST-07, invariant R8.
- **Migration:** `0039_budget_revisions.sql`.
- **Data model (as built):**
  - `budget_versions(id, project_id, kind ORIGINAL|REVISION, seq, source_boq_version_id, reason, status, created_by, approved_by)`, at most one APPROVED ORIGINAL per project;
  - `budget_lines(version_id, cost_code_id, amount ≠ 0, currency, contingency_flag)` (REVISION lines may be negative);
  - `budget_transfers(id, project_id, from_cost_code, to_cost_code, amount, reason, status)`;
  - `contingency_drawdowns(id, project_id, from_cost_code, to_cost_code, amount, reason, status)`: the source is a code that holds contingency;
  - view `project_budget_current(original, approved_revisions, transfers_in/out, drawdowns_in/out, current, is_contingency)`, `security_invoker` so RLS applies.
- **State machine:** DRAFT → SUBMITTED → APPROVED | REJECTED. An approved
  version is immutable. The current budget is the original plus approved
  movements and is never edited in place.
- **Guards:** a transfer cannot make a cost code's current budget lower than
  its committed amount (unless an override with MFA); the sum of transfers is
  zero per transfer; a drawdown cannot exceed the remaining contingency;
  commitment checks (`pr.approve`, `po.approve`) read `project_budget_current`.
- **Permissions:** `budget.revise` (project_manager), `budget.approve`
  (commercial_manager, and cfo above a threshold via WP-1.2).
- **Console:** Projects & planning → Budget, with original, revisions,
  transfers and current shown side by side, and a "Request transfer" action.
  The cost report gains Original and Current columns.
- **Tests:** a transfer race against a PO approval on the same cost code (only
  one wins when the budget is tight); a drawdown beyond contingency is
  refused; the BOQ re-approval path.
- **Estimate:** 5 ew.
- **Delivered and verified (2026-11-07):**
  - Kernel `internal/kernel/budget.go`; API `/v1/budgets/{revisions,transfers,drawdowns}`
    and `GET /v1/projects/{id}/budget`; console Budget overview, forms and lists.
  - Every transition is attested (`approval_use`) and re-checked by the database;
    illegal transitions, frozen content, project ownership of codes, headroom and
    the contingency rule are database guards.
  - Budget movements and requisition/PO approvals lock the project row
    (`budget_lock_project`), so headroom is never spent twice.
  - Tests: `TestBudget_ArithmeticAndControls`, `TestBudget_DatabaseRefusesBypass`
    (7 raw-SQL forgeries refused, tenant isolation of the view),
    `TestBudget_ConcurrentTransfersCannotOverspend`, `TestAPI_BudgetRevisionOverHTTP`.
  - Corrections made on review: the first draft of 0039 redefined
    `serk_subject_project` without six document types and `p2p_subject_text`
    with tables that do not exist, had no attestation checks and allowed
    DRAFT → APPROVED by raw SQL. The shared functions are now copied verbatim
    from their latest definitions (0030, 0037) with marked additions.
  - Open: the race between a transfer and a **PO approval** is serialised by the
    shared lock but has no dedicated test yet; the cross-project probe needs a
    fixture with a second project to be meaningful.

#### WP-1.4 Financial control dashboard with drill-down (BACKLOG #2)
- **SRS:** RPT-03, RPT-06, PCC-09, AUD-05.
- **Tasks:**
  1. A KPI catalogue `docs/reports/KPI_CATALOGUE.md`: formula, source query,
     owner and refresh for each KPI (cash, AP and AR due, budget consumption,
     CPI/SPI, overdue POs, open exceptions, unreconciled bank lines, SoD
     overrides).
  2. `GET /v1/dashboards/{role}`: each tile returns its value, formula id,
     freshness and a drill link to the report row, then to the documents.
  3. Console: an Overview → Dashboard page with role presets (executive,
     project, finance, procurement), a portfolio filter (entity, region,
     client) and click-through.
- **Acceptance:** every number on the dashboard traces to a report row and
  then to documents in at most two clicks; totals equal the trial balance.
- **Estimate:** 4 ew.
- **Delivered and verified (2026-11-07), as built:**
  - `internal/kernel/dashboard.go`, `GET /v1/dashboard?preset=&entity_id=`, `GET /v1/dashboard/kpis`,
    console *Overview → Dashboard*. No new migration: every tile is built from the reads the reports use.
  - 10 KPIs (K-BUD-01..03, K-RSK-01, K-CSH-01..04, K-LED-01, K-CTL-01) with documented formula, source and
    thresholds in `docs/reports/KPI_CATALOGUE.md`, **generated from the code** (a test fails if they differ).
  - Each tile: value, level, formula, source, computed-at, drill-down link, and its largest contributors
    linking to records. Roles see only what they may read; the rest is listed as hidden with what it needs.
  - Role presets: executive, project, finance, procurement. A preset never widens access.
  - Entity filter; budget tiles are per legal entity so currencies are never mixed.
  - **Known limits:** region / client / business-unit roll-ups wait for WP-1.7 (project master);
    HSE and procurement-cycle KPIs wait for their modules; tiles are computed on each open (no cache),
    which is right at this size but needs a materialised read model at the SRS scale (WP-1.1).


#### WP-1.5 Report catalogue to 25 and the report framework
- **SRS:** RPT-01, RPT-02, RPT-05.
- **Tasks:** a report registry (`internal/kernel/reports.go`) with parameter
  schemas and permission per report. Add the 14 missing reports from the
  directive list: GL detail; journal register; vendor statement; customer
  statement; PO register; open commitments; GRNI; three-way match exceptions;
  IPC register; retention ledger; stock valuation by store; stock movement;
  payment register; budget vs actual by period. Add scheduled delivery
  (outbox-driven email with the recipient's permissions re-checked at send
  time).
- **Estimate:** 5 ew.
- **Delivered and verified (2026-11-07), as built:**
  - One registry (`internal/kernel/reportcat.go`, `reportDefs`) and one runner; `GET /v1/reports/catalogue`,
    `GET /v1/reports/catalogue/{id}`; console report hub groups plus a generic report page; export to PDF, Word
    and Excel through the existing `/v1/export`.
  - The 14 reports: GL detail, journal register, supplier statement, customer statement, PO register, open
    commitments, GRNI, three-way match exceptions, certificate register, retention ledger, stock valuation by
    store, stock movement, payment register, budget against actual by month (25 with the 11 that existed).
  - Each runs as the caller (permission, project scope, RLS), shows data-as-of and filters, totals in exact
    decimals, 5,000-row limit. Catalogue document generated from the code (`docs/reports/REPORT_CATALOGUE.md`).
  - **Known limits:** scheduled email delivery is not built (needs the notification channels of WP-4.5);
    reports are queries over live tables (fine at demo size; large tenants need the read model of WP-1.1/4.4);
    the customer statement lists receipts allocated to invoices (unallocated money-in is on the Money received list).


#### WP-1.6 Units of measure, reservations and reorder points in stores
- **SRS:** INV-04, INV-05, INV-07.
- **Migration:** `0041_stock_uom_reservations.sql`.
- **Data model:**
  - `uom(code, dimension)` and `uom_conversion(item_id NULL, from_uom, to_uom, factor numeric)`;
  - `stock_reservation(id, item_id, warehouse_id, project_id, activity_id NULL, qty_base, status, expires_at)`;
  - `stock_reorder_rule(item_id, warehouse_id, min_qty, max_qty, reorder_qty)`.
- **Guards:** quantities are stored in the base unit; conversions are exact
  decimals; available = on hand - reserved >= 0 at issue (row lock); the
  automation rule `stock_below` reads reorder rules and drafts a requisition
  (draft only, no approval).
- **Estimate:** 4 ew.
- **Delivered and verified (2026-11-07), as built:** migration `0041_uom_reservations_reorder.sql`,
  `internal/kernel/stockplus.go`, `internal/httpapi/stockplus_handlers.go`, console *Reservations*,
  *Unit conversions*, *Low stock* and the extended stock-issue form.
  - **Units:** controlled conversions (prepared / approved by different people, attested, exact `numeric(24,9)`
    factor, one approved per pair); applied to stock issues and put-aways through an optional `unit`.
  - **Reservations:** database-enforced availability (on hand minus reserved), serialised by an item-row lock;
    an issue never eats into others' reservations; an issue against a reservation consumes it.
  - **Reorder rules:** minimum / maximum / reorder quantity per item and store; report `low-stock` suggests an order.
  - **Known limits:** conversions are applied on issue and put-away only (counts and transfers still take the base
    unit); reservations are not yet tied to schedule activities (needs WP-3.2); the `stock_below` automation rule
    still uses its old threshold rather than the reorder rules; batches/serials and returns wait for later packages.


#### WP-1.7 Multi-entity structure and project master
- **SRS:** ORG-01, ORG-02, ORG-09.
- **Migration:** `0042_org_structure.sql`.
- **Data model:**
  - `legal_entity` (extend the existing entity with TIN, VAT number, fiscal calendar);
  - `business_unit`, `region`;
  - `project(id, code, name, entity_id, bu_id, client_id, contract_type, lat, lng, start, finish, pm_user, phase, status)`;
  - `project_stage_gate(project_id, gate, decided_by, decided_at, decision, notes)`;
  - `calendar` and `calendar_exception` (Ethiopian holidays seeded per year).
- **Notes:** projects today are references only (BACKLOG 4.1). Migrate the
  existing project ids into the new table with a backfill migration that
  keeps the ids.
- **Estimate:** 4 ew.
- **Delivered and verified (2026-11-07), as built:** migration `0042_project_master.sql` (renumbered:
  the plan reserved 0042 for this), `internal/kernel/org.go`, `internal/httpapi/org_handlers.go`, console
  *Projects*, *Stage gates*, *Organisation & calendar*, region and business-unit filters on the dashboard.
  - Project master: client, contract type, location, dates, manager, region, business unit, phase.
  - Stage gates (BID, MOBILISATION, EXECUTION, PRACTICAL_COMPLETION, DEFECTS, CLOSED): prepared by one person,
    decided by another with fresh MFA, attested, one open gate, no skipping; approval moves the project atomically.
  - **Security fix:** tenant logins can no longer create ACTIVE projects directly.
  - **Known limits:** the new project is added to people's scope by the identity provider (WP-0.5), so only
    its creator and tenant-wide roles see it until then; the calendar is stored and shown but not yet used by the
    scheduler (WP-3.2); no Ethiopian holidays are seeded (movable dates differ yearly and need adviser sign-off);
    region and unit filters narrow budget and risk tiles only; the project-level legal-entity hierarchy
    (groups, joint ventures) waits for consolidation (WP-2.10).


#### WP-1.8 Guided tenant onboarding and imports
- **SRS:** ADM-01, ADM-03, INT-03, NFR-DAT-05.
- **Tasks:** a step wizard (company, entities, chart of accounts from the
  construction IFRS template, tax, banks, users and roles, policies);
  `cmd/serk-import` (it exists) generalised to staged CSV/Excel imports
  (suppliers, customers, items, opening balances, open AP/AR, opening stock)
  with a validation report and a reconciliation sign-off before commit.
- **Estimate:** 5 ew.
- **Delivered and verified (2026-11-07), as built:** migration `0043_imports.sql`, `internal/kernel/imports.go`,
  `internal/kernel/setup.go`, console *Company setup* and *Data imports*.
  - **Setup checklist:** 16 steps read from the real records (never ticked by hand), grouped, each with why, who and a link.
  - **Staged imports** (suppliers, customers, stock items from CSV): stage and validate every row (nothing is created), a
    different person commits and confirms the source's row count, records are created through the ordinary commands (still
    needing their normal approval), partial batches retry without duplicating, a discarded or committed batch is final.
  - **Known limits:** opening balances, open AP/AR documents and opening stock are not imported yet (they need the
    reconciliation-to-ledger design of WP-2.x and an accountant's sign-off); import kinds are the three master-data types;
    the guided setup is a checklist with links, not a step-by-step wizard that fills in policies.


#### WP-1.9 R1 hardening, documentation and GA
- Update the OpenAPI spec for all endpoints (it currently documents 28 paths);
  user guides; admin runbook; release notes; UAT with the first customer;
  the signed GO record.
- **Estimate:** 4 ew.

---
- **Delivered (2026-11-07), as built:** every served route is in `api/openapi.yaml` and a contract test keeps it so;
  the role matrix is generated from the code and tested; `docs/operations/ADMIN_RUNBOOK.md`,
  `docs/release/RELEASE_NOTES_R1.md`, `docs/release/UAT_PLAN_R1.md`; `GO_NO_GO.md` refreshed; `make gen-docs`.
  **Not done and cannot be done by engineering alone:** UAT by the area leads, the signed GO record, and the items of
  WP-0.5, 0.6, 0.7 and 1.1. Phase R1 is therefore **code-complete, not released**.


#### WP-1.10 People, roles and profiles (SRS IAM-05, IAM-06, IAM-09, IAM-11, ADM-01)
- Added after the R1 review: roles came only from the sign-in token, so a company could not see who works
  in it, give or take roles, compose its own roles, or disable a leaver in one place.
- **Delivered (2026-11-08), as built:** migrations `0048_people_access.sql` and `0049_people_access_fix.sql`;
  `internal/kernel/people.go`; routes `/v1/people*`, `/v1/role-grants/{id}/revoke`, `/v1/roles*`, `/v1/me/profile`;
  console pages **Governance → People & access** (cards, table, duty map, roles, access history, side sheets)
  and **My profile** (header avatar).
  - The first request of a signed-in person records them in the directory with the roles and projects their
    token states. Once an administrator takes the person "under management", the directory's roles and
    projects replace the token's on the next request (IAM-11), and a disabled person is refused at once
    and signed out everywhere.
  - Company roles are composed only from the fixed permission list; one that holds both halves of a control
    is refused, and so is a change that would give a present holder a conflict (IAM-05).
  - A role is refused if it gives the person both halves of a control, unless an approved duty-conflict
    exception covers that pair (IAM-09). The console previews the refusal before you click.
  - Nobody changes their own access (kernel and database); changes need fresh MFA and a reason; grants are
    never deleted, only ended, so the history is whole.
  - New permissions `user.manage` and `role.manage` (security administrator). Every permission now has a
    plain-words label, area and stage of work (`pkg/auth/labels.go`, tested complete).
  **Not done:** SCIM provisioning (WP-4.5) and project assignment by business unit.
- **Estimate:** 3 ew (actual).

### Phase R2: Project controls and commercial (weeks 21-40)

#### WP-2.1 Cost forecasting cycle
- **SRS:** PCC-04, PCC-05, PCC-10.
- **Migration:** `0044_forecast.sql` (built).
- **Data model:**
  - `forecast_cycle(id, project_id, period_id, status OPEN|SUBMITTED|APPROVED, prepared_by, approved_by)`;
  - `forecast_line(cycle_id, cost_code_id, method MANUAL|CPI|REMAINING_BUDGET, etc_amount, eac_amount, note)`;
  - snapshot `forecast_snapshot` (frozen figures at approval: budget, committed, actual, accrued, ETC, EAC).
- **Rules:** EAC = actual + accruals + ETC; the CPI method is ETC = (BAC - EV) / CPI
  using `pkg/evm`; variance against the last approved forecast; one approved
  cycle per project per period.
- **Console:** a forecast grid per cost code with method selection, comments
  and an "explain the change" field; S-curves (planned, actual, forecast).
- **Estimate:** 5 ew.
- **Delivered (2026-11-07), as built:** migration `0044_forecast.sql`, `internal/kernel/forecast.go`,
  routes `POST /v1/projects/{id}/forecasts`, `GET /v1/forecasts/{id}`, `PUT /v1/forecasts/{id}/lines/{code}`,
  `POST /v1/forecasts/{id}/submit|approve|reject`, console page **Projects & planning → Cost forecasts**.
  A forecast is a cycle (project and month end) with one line per cost code that has money on it. The
  database computes and freezes each line's budget, commitments, cost to date, ETC, EAC and VAC; the
  preparer chooses only the method (remaining budget, open commitments, manual) and a note. A line forecast
  above its budget must be explained (10+ characters) before submission. The decider is a different
  person with `forecast.approve` and a fresh MFA; the decision is attested and an approved forecast
  never changes. One open cycle per project; one approved cycle per project and month end.
  Test: `TestForecast_MethodsAndGuards`.
  Completed (2026-11-08, migration `0052_forecast_cpi.sql`): method CPI (ETC = budget not yet earned ÷ the
  project's CPI from `schedule_evm`, refused without a baseline with progress and posted cost); each line freezes the
  same code's EAC in the last approved forecast and a line that moved by more than 10% of its budget must be
  explained before submission (database guard); `GET /v1/projects/{id}/s-curve` and an S-curve chart (planned,
  earned, actual by month, scheduled budget and latest forecast) on every forecast. Test: `TestForecast_CPIChangeAndSCurve`.
  Follow-up from the browser check (migration `0047_forecast_cancel.sql`): the author can cancel a DRAFT
  forecast with a reason, so an abandoned draft no longer blocks the project; a project with no money on any
  cost code is refused in plain words; a second open forecast is refused in plain words naming the open one.
  Tests: `TestForecast_MethodsAndGuards` (cancel path), `TestForecast_EmptyProjectRefused`.
- **Still to do in this package:** the CPI method (needs earned value, WP-3.x), accruals in EAC (WP-2.13),
  S-curves, and variance against the previous approved forecast.

#### WP-2.2 WIP schedule (over and under billing)
- **SRS:** PCC-06.
- **Rules:** percent complete (cost to cost) = actual cost / EAC; earned revenue
  = contract value x percent complete; over/under billing = billed to date -
  earned revenue; month-end WIP adjustment journal (generated draft, approved
  and posted by finance).
- **Estimate:** 3 ew. Depends on WP-2.1.
- **Delivered (2026-11-07/08), as built:** two catalogue reports in `internal/kernel/reportcat.go` (no new
  migration): **Cost forecast against budget** (`forecast-summary`) and **Work in progress: over and under
  billing** (`wip-schedule`). Both read the latest APPROVED forecast of each project. Percent complete =
  cost to date / EAC (capped at 100%); earned revenue = active contract value x percent complete; billed =
  approved customer invoices up to the forecast date; over/(under) billed = billed - earned; a project whose EAC
  exceeds its contract value shows its total expected loss. Both export to PDF/Word/Excel like every report.
  Test: `TestForecast_WIPScheduleAndSummary` recomputes every figure from first principles.
- **Completed (2026-11-08):** the month-end WIP adjustment journal is the revenue run of WP-2.3 (migration
  `0053_revenue_recognition.sql`): a generated REV_RUN draft journal, approved by finance and posted by a different
  person. Percent complete is per contract for the output method; the input method uses the project's forecast.

#### WP-2.3 IFRS 15 revenue recognition over time
- **Delivered (2026-11-08):** `0053_revenue_recognition.sql`, `internal/kernel/revenue.go`, console **Accounting →
  Revenue & WIP**. Revenue policy per legal entity (INPUT_COST or OUTPUT_MEASURED; contract asset and liability
  accounts; controller proposes, CFO approves, attested). Revenue run per project and month end: the database computes
  per active contract earned (value × progress), billed (approved invoices less approved credit notes), cumulative
  adjustment and the movement since the last posted run; the run generates a REV_RUN subledger journal bound like
  P2P/AR/STK journals (lines only by the generating transaction, re-checked at approval and posting, never reversed;
  finance may cancel a draft run with its journal). Test: `TestRevenue_RunAndWIPJournal`. Pending: method chosen per
  contract, and decision D-05 (revenue policy sign-off by the finance adviser).
- **SRS:** FIN-15, CON-07 (contract assets and liabilities).
- **Migration:** `0044_revenue_recognition.sql`: `revenue_policy` (per
  contract: input or output method, measure), `revenue_run(period, status)`,
  `revenue_line(contract_id, measure, cumulative_revenue, period_revenue,
  contract_asset, contract_liability)`.
- **Guards:** a run is per period and posted once (unique source event);
  reversal only via the next run; requires adviser sign-off on the policy
  (decision D-05).
- **Estimate:** 4 ew.

#### WP-2.4 Change events
- **SRS:** PCC-07, CON-03, SUB-03.
- **Migration:** `0045_change_events.sql` (built): `change_event(id, project_id,
  source RFI|SITE_INSTRUCTION|CLIENT|DESIGN|INTERNAL, status POTENTIAL|PENDING|APPROVED|VOID,
  cost_impact, revenue_impact, schedule_impact_days)`, link tables to
  `contract_variation`, `subcontract_variation` and `budget_version`.
- **Flow:** potential, then estimate, then pending (submitted to client), then
  approved (variation approved), which raises a budget revision draft
  automatically (draft only).
- **Estimate:** 4 ew.
- **Delivered (2026-11-08), as built:** migration `0045_change_events.sql`, `internal/kernel/change.go`, routes
  `POST /v1/change-events`, `GET /v1/change-events/{id}`, `POST /v1/change-events/{id}/submit|approve|reject`,
  console **Projects & planning → Change events**. Statuses are DRAFT (potential), SUBMITTED (pending decision),
  APPROVED, REJECTED. Each event has a source (RFI, site instruction, client, design, internal), a signed
  revenue impact, a signed schedule impact in days and signed cost lines per cost code (header and lines
  immutable once submitted). Prepared with `change.manage` (project manager, quantity surveyor), decided by a
  different person with `change.approve` (commercial manager) and a fresh MFA, attested. Approval drafts a
  budget REVISION for the cost lines, authored by the change's author so that a different person approves it
  under budget control (draft only; the budget does not move). Test: `TestChange_EventToBudgetDraft`.
- **Completed (2026-11-08):** `0054_change_variation_links.sql`: an APPROVED change event is linked (under
  `change.manage`) to the contract variations that recover it from the client, on the same project, never a rejected
  or cancelled one; a variation carries one event; links are never removed. The change event page shows the revenue
  impact, what approved linked variations recovered and what is not yet recovered. Test: `TestChange_LinkedVariations`.
  Links to subcontract variations are part of WP-2.5.


#### WP-2.5 Subcontract management
- **SRS:** SUB-01..07, PCC-02.
- **Migration:** `0046_subcontracts.sql`.
- **Data model:**
  - `subcontract(id, project_id, supplier_id, number, scope, value, retention_pct, retention_cap, advance_amount, advance_recovery_pct, wht_rate, pay_when_certified, status DRAFT|SUBMITTED|APPROVED|CLOSED)`;
  - `subcontract_line(sov_item, description, unit, qty, rate, cost_code_id)`;
  - `subcontract_application(id, subcontract_id, period_to, status DRAFT|SUBMITTED|CERTIFIED|REJECTED)` with its lines (cumulative qty claimed) and certification lines (cumulative qty certified);
  - `subcontract_certificate(id, application_id, gross_cumulative, previous_certified, this_gross, retention, advance_recovery, backcharges, wht, net_payable)`;
  - `backcharge(id, subcontract_id, amount, evidence_doc, acknowledged_by_sub)`;
  - `subcontractor_compliance(supplier_id, doc_type, number, expires_at, status)`.
- **State and guards:**
  - an approved subcontract commits against the budget (R8);
  - the certifier must differ from the preparer (QS, then commercial manager);
  - the cumulative certified amount is at most the contract value plus approved variations;
  - retention is at most the cap;
  - the certificate generates an AP invoice document with a bound draft journal
    (ADR 0009 pattern);
  - if `pay_when_certified`, payment is refused until the linked IPC is certified;
  - expired compliance blocks payment when the tenant enables it.
- **Permissions:** `subcontract.manage`, `subcontract.approve`,
  `subcontract.apply` (portal or QS on behalf), `subcontract.certify`.
- **Console:** Procurement → Subcontracts; an application grid with cumulative
  columns (previous, this period, to date); a certificate PDF export.
- **Tests:** arithmetic fixtures signed by a QS; retention cap; advance fully
  recovered by the last certificate; a certificate race.
- **Part 1 delivered (2026-11-08):** `0055_subcontracts.sql`, `internal/kernel/subcontract.go`,
  `internal/httpapi/subcontract_handlers.go`, console menu **Subcontracts**. Subcontract policy (five accounts, maker/
  checker); subcontract with schedule of values on cost codes and expense accounts, approved by another person with
  budget control, committing its value in `p2p_cost_position`; applications (cumulative claim) certified by another
  person with fresh MFA; the database computes this period's gross, retention (capped), advance recovery (never above
  what is outstanding), approved back-charges (deducted once), withholding and net payable, refusing a negative period
  or net; a SUB_CERT subledger journal bound like the others. Test: `TestSubcontract_CertificatesAndJournal`.
- **Part 2 delivered (2026-11-08):** `0056_subcontract_payments.sql`, `internal/kernel/subpayment.go`: subcontractor
  documents (an expired required document blocks approval and release); subcontract payments prepared by treasury,
  approved by finance (fresh MFA) and released by a different treasury officer (fresh MFA), allocated to certified
  certificates never above what is open, paid to an approved bank account of the subcontractor; pay-when-certified
  refused until the client certified the period; a SUB_PAY journal (Dr subcontractor payable / Cr bank clearing).
  Test: `TestSubcontract_PaymentComplianceAndPayWhenCertified`.
- **Still pending:** matching subcontract payments to bank statements (settlement), subcontract variations and their
  link to change events, the subcontractor portal, and the certificate PDF.
- **Estimate:** 8 ew.

#### WP-2.6 Claims, EOT, notices and LDs
- **SRS:** CON-04, CON-06, CON-09, SCH-07.
- **Migration:** `0046_claims.sql` (built): `contract_notice(id, contract_id,
  clause_ref, event_date, notice_due_at, sent_at, status)`, `claim(id,
  contract_id, type TIME|COST|BOTH, amount, days, status
  DRAFT|SUBMITTED|UNDER_REVIEW|AGREED|REJECTED|DISPUTED)`, `obligation(id,
  contract_id, description, owner, due_at, status)`, and LD parameters on
  the contract (rate per day, cap).
- **Automation:** time-bar alerts (for example 28-day notices) at 14, 7 and 2
  days before expiry, through notification rules (notify only).
- **Estimate:** 4 ew.
- **Delivered (2026-11-08), as built:** migration `0046_claims.sql` (the numbering above shifted by one),
  `internal/kernel/claims.go`, routes `POST /v1/notices`, `GET /v1/notices`, `POST /v1/notices/{id}/send`,
  `POST /v1/claims`, `POST /v1/claims/{id}/submit|approve|reject`, `POST /v1/claims/{id}/outcome`, console
  **Commercial & revenue → Contract notices / Claims**, reports `notice-register` and `claims-register`.
  The database computes a notice's deadline (event date + days, default 28) and its standing (open, due soon,
  overdue, sent, sent late); a notice is sent once, never in the future or before its event. A claim has the
  money or days its kind (time, cost, both) says; if its notice is missing or late it cannot be submitted
  without a reason; it is approved for sending by a different person (`claim.approve`, fresh MFA, attested);
  the client's answer (agreed, rejected, disputed, with a reference) is recorded once and agreed money and
  days never exceed the claim. Test: `TestClaims_NoticesAndClaims`.
- **Completed (2026-11-08):** `0051_contract_time.sql`, `internal/kernel/contracttime.go`. Damages terms (time for
  completion, rate per day, cap) prepared under `claim.manage` and approved under `claim.approve` (fresh MFA, attested),
  once per contract; the revised completion is the approved date plus the days the client agreed on claims; damages to
  date and the forecast from the baseline schedule finish are computed and capped; an obligations register (raised open,
  closed once, a waiver needs a reason); alert rule kinds `notice_due`, `obligation_due` and `guarantee_expiring` that
  re-alert at 14, 7 and 2 days and when late. Console: *Time for completion and liquidated damages* on each active
  contract and **Commercial & revenue → Contract obligations**. Tests: `TestContractTime_DamagesExtensionsObligations`,
  `TestDeadlineBucket`.


#### WP-2.7 Guarantees and bonds; price adjustment
- **SRS:** CON-05, CON-07, TRS-04.
- **Status (2026-11-08):** done. `0050_bonds_escalation.sql`, `internal/kernel/bonds.go`,
  `internal/httpapi/bonds_handlers.go`, console page *Guarantees & price adjustment*. The
  adjustment is its own approved document per certified IPC (the IPC's amounts, invoice and
  journals are unchanged); billing an approved adjustment to the client is not yet built.
- **Migration:** `0050_bonds_escalation.sql`: `guarantee(id, kind
  BID|ADVANCE|PERFORMANCE|RETENTION, contract_id, issuer_bank, amount,
  currency, issued_at, expires_at, reduced_to, status)`;
  `price_index(code, period, value)` with maker/checker;
  `escalation_formula(contract_id, fixed_portion, weights jsonb, base_period)`;
  IPC computes the adjustment `Pn = a + Σ b·(Ln/Lo)` with exact decimals.
- **Estimate:** 4 ew.

#### WP-2.8 Workforce and payroll
- **SRS:** HCM-01, HCM-04, HCM-05, HCM-06.
- **Migrations:** `0061_payroll.sql` (built).
- **Data model:**
  - `employee(id, number, name, kind SALARIED|DAILY, grade, trade, base_rate, rate_unit, bank_or_wallet (encrypted, masked), tin, pension_no, start, end)`;
  - `payroll_rule_set(id, effective_from, status, approved_by)` and
    `payroll_rule(set_id, code, kind TAX_BAND|PENSION_EE|PENSION_ER|OVERTIME|ALLOWANCE, params jsonb)`;
  - `pay_run(id, entity_id, period, status DRAFT|CALCULATED|SUBMITTED|APPROVED|POSTED|PAID)`;
  - `pay_line(run_id, employee_id, project_id, cost_code_id, hours, gross, tax, pension_ee, pension_er, net)`.
- **Guards:** rule sets are effective-dated and maker/checker approved, with
  values from an adviser-signed fixture set (decision D-04); a pay run is
  calculated by the server only; preparer ≠ approver ≠ releaser; the posting
  generates a journal by project and cost code; a payment file is produced
  (bank CSV or ISO 20022) and paid through the existing payment instruction
  flow.
- **Tests:** golden fixtures per tax band and boundary; rounding per pay line;
  a retro-dated rule change is refused for a posted run.
- **Estimate:** 8 ew (plus adviser time).

#### WP-2.9 Fixed assets (IAS 16)
- **SRS:** FIN-06, EQP-01.
- **Migration:** `0057_fixed_assets.sql` (provisional): `asset(id, class, entity_id,
  acquired_at, cost, residual, life_months, method SL|DB|UNITS, component_of
  NULL, location, status)`, `asset_event(kind ACQUIRE|DEPRECIATE|REVALUE|IMPAIR|TRANSFER|DISPOSE)`,
  `depreciation_run(period, status)`.
- **Rules:** capitalisation from GRN or AP lines to an asset account; a
  monthly depreciation run (one per period, unique source event); disposal
  gain or loss journal.
- **Delivered (2026-11-08):** `0057_fixed_assets.sql`, `internal/kernel/fixedassets.go`, `internal/httpapi/asset_handlers.go`,
  console **Accounting → Fixed assets**. Asset classes with typed accounts; assets registered by an accountant
  (`asset.manage`) and activated by the CFO (`asset.approve`, fresh MFA), generating FA_CAP (Dr cost / Cr the
  capitalised-from account, e.g. assets under construction); a monthly depreciation run per entity (straight line or
  double declining, never below residual, months in order, one unposted run at a time) generating FA_DEP; disposals
  approved by the CFO with database-computed net book value and gain or loss, generating FA_DISP. FA journals are bound
  subledger journals. Test: `TestFixedAssets_CapitaliseDepreciateDispose`. Not built: revaluation, impairment,
  transfers between entities or projects, units-of-production method, capitalisation picked from a GRN line.
- **Estimate:** 5 ew.

#### WP-2.10 Multi-entity consolidation and intercompany
- **SRS:** FIN-08, FIN-09.
- **Migration:** `0052_consolidation.sql`: `consolidation_group`,
  `group_member(entity, ownership_pct, method FULL|PROPORTIONATE|EQUITY)`,
  `intercompany_link(journal_a, journal_b, status MATCHED|UNMATCHED)`,
  `consolidation_run(period, fx_rates_snapshot, status)` with elimination lines.
- **Rules:** intercompany journals carry a counterparty entity; matching by
  reference and amount; elimination generated per run; translation at
  closing and average rates (IAS 21) using the approved FX policy.
- **Estimate:** 6 ew.

#### WP-2.11 Tax: withholding, VAT returns, e-invoicing adapter
- **SRS:** FIN-07, FIN-14, RPT-08.
- **Tasks:** withholding on supplier payments and customer receipts (rate per
  policy, certificates numbered); VAT return schedule (output, input,
  adjustments) per period; an e-invoicing adapter interface with a fake until
  the format is confirmed (decision D-06).
- **Estimate:** 4 ew.

#### WP-2.12 Cash forecasting
- **SRS:** TRS-02.
- **Tasks:** extend `CashOutlookOf` with payroll runs, tax due dates, supplier
  payment schedules, expected IPC receipts (from certified but unpaid IPCs
  and forecast billing), loan service; separate CONFIRMED from FORECAST; a
  13-week view and a project-horizon view.
- **Estimate:** 3 ew.
- **Delivered (2026-11-08):** `0059_cash_plan.sql`, `internal/kernel/cashforecast.go`, console **Treasury & banking →
  Cash forecast**. Weekly from Monday, 13 weeks, from posted bank cash: confirmed (approved receivables and payables by
  due date; released payments not settled) and forecast (certified IPCs not invoiced at period end + 30 days; certified
  subcontract certificates not paid; open purchase-order balances by date needed) flows, plus planned items entered by
  treasury (never edited, cancelled with a reason). Overdue items in week 1; anything beyond 13 weeks summed. Test:
  `TestCashForecast_ThirteenWeeks`. Pending: the project-horizon (monthly) view.

#### WP-2.13 Fiscal calendar and accruals
- **SRS:** FIN-10, FIN-11, FIN-12, FIN-13.
- **Tasks:** the Ethiopian fiscal year (Hamle-Sene) as a calendar option;
  recurring journals; accruals with automatic reversal on the first day of the
  next period; overhead allocation rules (driver: direct cost or labour
  hours); corporate overhead budgets.
- **Estimate:** 4 ew.
- **Delivered (2026-11-08):** `0058_period_end.sql`, `internal/kernel/periodend.go`, console **Accounting → Period end**.
  Fiscal years generated as monthly OPEN periods on the Ethiopian (Hamle to Sene, Pagume within Nehase, leap rule
  tested) or Gregorian calendar, refused in plain words when they overlap; recurring journals (one draft journal per
  template and date, never twice, catch-up to 24 months, end date); accruals reversed once on their date after posting;
  overhead allocation by posted direct cost computed by the database (remainder to the largest share), one draft
  journal per project, one allocation per pool and range. Tests: `TestEthiopianFiscalYear`,
  `TestPeriodEnd_FiscalYearRecurringAccrualOverhead`. Pending: corporate overhead budgets and a labour-hours driver
  (needs timesheets, WP-3.3).

---

### Phase R3: Field, documents, QHSE (weeks 41-60)

#### WP-3.1 Mobile field application
- **SRS:** SIT-03, SIT-05, NFR-PERF-05, EI-UI-03.
- **Decision first:** D-01 (native or PWA). Recommendation: **React Native or
  Kotlin for Android first**, because of camera, background sync and the
  encrypted store. A PWA is acceptable only if the offline store and
  background sync pass the R6 tests on target devices.
- **Architecture:** the device registers through the existing
  `/v1/site/devices` flow; an encrypted SQLite store; a durable command queue
  that reuses the existing sync envelope (device id, op id, sequence, base
  version, payload digest); photo upload in chunks with resume.
- **Screens (in order):** sign-in with step-up; My tasks; site diary;
  timesheets (WP-3.3); goods receipt confirmation; inspection (WP-3.6);
  incident (WP-3.7); approvals inbox; photo capture with GPS and watermark.
- **Tests:** network cut mid-sync; revoked device; clock skew; 1,000 queued
  commands in under 5 minutes on 3G (throttled).
- **Estimate:** 14 ew.

#### WP-3.2 Planning: constraints, resources, P6 and MS Project
- **SRS:** SCH-02..06, SCH-09.
- **Migration:** `0053_schedule_ext.sql`: constraints, activity codes,
  resources, assignments, multiple baselines.
- **Tasks:** extend `pkg/schedule` for constraints and resource levelling
  (serial schedule generation); an XER/XML parser in `internal/integrations/p6`
  with an integrity report; MS Project XML; look-ahead and weekly work plans
  with PPC; a DCMA 14-point check.
- **Estimate:** 8 ew.

#### WP-3.3 Timesheets and attendance
- **SRS:** HCM-02, HCM-03, HCM-07, EQP-03.
- **Migration:** `0054_timesheets.sql`: `timesheet(id, project_id, date,
  crew_id, status, recorded_by, approved_by)` and `timesheet_line(employee_id,
  cost_code_id, hours_normal, hours_ot, equipment_id NULL)`.
- **Guards:** hours per person per day are bounded; one approved line per
  person, project and day; approval by a different supervisor; approved
  hours feed payroll (WP-2.8) and labour cost journals.
- **Estimate:** 4 ew.

#### WP-3.4 Equipment and fleet
- **SRS:** EQP-01..07.
- **Migration:** `0055_equipment.sql`: `equipment(id, code, class, ownership
  OWNED|LEASED|HIRED, asset_id NULL, hire_rate, rate_unit, status, location)`,
  `equipment_usage(date, equipment_id, project_id, cost_code_id, hours_work,
  hours_idle, hours_breakdown, source)`, `maintenance_plan`, `work_order`,
  `fuel_issue`, `equipment_transfer`.
- **Rules:** a monthly internal hire charge journal (Dr project cost, Cr
  equipment recovery) from approved usage; fuel issues come from stock
  issues; anomaly alert when litres per hour exceed the class norm by a
  configurable percentage.
- **Estimate:** 6 ew.

#### WP-3.5 Document control (CDE), RFIs, submittals, transmittals
- **SRS:** DOC-01..09.
- **Migration:** `0056_documents.sql`: `document(id, project_id, number,
  title, discipline, type, state WIP|SHARED|PUBLISHED|ARCHIVED)`,
  `document_revision(document_id, rev, file_key, sha256, status, superseded_by)`,
  `transmittal` and its recipients, `rfi(id, number, question,
  ball_in_court, due_at, status, response, cost_impact, schedule_impact)`,
  `submittal(id, spec_section, status, review_code A|B|C|D)`.
- **Storage:** S3-compatible with SSE; a ClamAV scan worker; signed URLs valid
  for 5 minutes, issued after an authorisation check; full-text index (`tsvector`
  plus OCR text from Tesseract in a worker).
- **Guards:** exactly one current revision per document (partial unique
  index); a published revision is immutable; numbering rules per tenant.
- **Viewer:** PDF.js for drawings; an IFC viewer (web-ifc / That Open Engine)
  with BCF import and export.
- **Estimate:** 10 ew.

#### WP-3.6 Quality management
- **SRS:** QA-01..05.
- **Migration:** `0057_quality.sql`: `itp`, `itp_item(hold|witness)`,
  `inspection_request`, `inspection_result`, `ncr(id, severity, root_cause,
  corrective_action, cost_impact, status)`, `material_test(batch_ref, type,
  result, pass)`.
- **Guard (configurable):** progress or measurement on an activity with an
  unreleased hold point is refused by the kernel and the DB.
- **Estimate:** 5 ew.

#### WP-3.7 Health, safety and environment
- **SRS:** HSE-01..06.
- **Migration:** `0058_hse.sql`: `incident(id, kind, severity, occurred_at,
  location, people, status)`, `investigation`, `capa`, `permit_to_work(kind,
  valid_from, valid_to, conditions, issued_by, accepted_by, closed_by)`,
  `induction(employee_id, project_id, valid_to)`, `toolbox_talk`,
  `safety_inspection`.
- **Rules:** LTIFR = lost-time injuries x 1,000,000 / hours worked (hours from
  timesheets); an induction gate on timesheets is configurable; incident
  notifications are immediate and cannot be disabled for severity ≥ major.
- **Estimate:** 5 ew.

#### WP-3.8 Punch lists, site instructions, meetings
- **SRS:** SIT-06, SIT-07, COL-04.
- **Estimate:** 3 ew.

#### WP-3.9 Progress rules of credit and installed quantities
- **SRS:** EVM-03, EVM-04, EVM-05.
- **Estimate:** 3 ew.

---

### Phase R4: Intelligence and ecosystem (weeks 61-76)

#### WP-4.1 External portals (client, consultant, supplier, subcontractor)
- **SRS:** COL-03, PRC-05, SUB-07, CON-10.
- **Design:** a separate principal type `external` bound to one partner record;
  RLS adds `partner_id` policies; a separate portal shell (same API); actions
  limited to their own documents (submit quotes sealed until due, submit
  applications, view IPCs, acknowledge transmittals).
- **Estimate:** 8 ew.

#### WP-4.2 Document AI (invoice, delivery note, certificate capture)
- **SRS:** AI-04, AI-06, AI-07.
- **Design:** OCR and extraction into **draft** documents only (an AI
  principal; a draft action per `IsDraftAction`); per-field confidence; a
  human confirms; injection red-team suite (`e2e/ai_redteam`); tenant opt-in
  and a model location setting.
- **Estimate:** 6 ew.

#### WP-4.3 Predictive risk
- **SRS:** AI-05, AI-08.
- **Design:** features from the recorded snapshots (CPI/SPI trend, PO delay
  history, cash outlook); start with gradient-boosted models per tenant once
  history exists; always shown beside the rule-based baseline with its
  method and confidence; outcome tracking for calibration.
- **Estimate:** 5 ew.

#### WP-4.4 Report builder and BI feed
- **SRS:** RPT-04, RPT-07.
- **Design:** a governed semantic layer (`report_model` definitions: entities,
  joins, measures, permissions); a builder UI producing report definitions,
  never raw SQL; a nightly Parquet export per tenant and a Power BI connector
  over a read-only API that enforces scope.
- **Estimate:** 7 ew.

#### WP-4.5 Notification channels and SCIM
- **SRS:** COL-02, IAM-03.
- **Tasks:** email (SMTP with DKIM), SMS and Telegram adapters with retry
  through the outbox; user preferences; a SCIM 2.0 endpoint.
- **Estimate:** 4 ew.

#### WP-4.6 Integrations
- **SRS:** INT-04, INT-05, TRS-03, INT-07.
- **Tasks:** bank file formats per bank (ISO 20022 where supported); a
  telebirr connector where available; Microsoft 365 / SharePoint document
  links; migration packs from Peachtree/Sage, QuickBooks and SAP Business One.
- **Estimate:** 6 ew.

#### WP-4.7 Scale and certification readiness
- **SRS:** NFR-PERF-04, NFR-MNT-07, NFR-SEC-10, NFR-AVL-01.
- **Tasks:** a dedicated-database option for large tenants (router support
  exists); read replicas for reports; ISO 27001 ISMS documents; SOC 2
  readiness assessment.
- **Estimate:** 5 ew.

---

## 7. Phase gates

| Gate | Checks | Who signs |
|---|---|---|
| End of R0 | CI blocks bad PRs; staging with TLS, IdP, vault; observability live | Tech lead, DevOps |
| End of R1 (GA) | Gate A PASS; gates R1-R5, R8, R9 evidence; pentest closed; load test; UAT; signed GO_NO_GO | Product owner, security, finance, ops |
| End of R2 | Payroll and IFRS 15 fixtures signed by advisers; consolidation reconciles; subcontract arithmetic signed by a QS | Finance lead, tax adviser, commercial lead |
| End of R3 | Gate R6 evidence; field pilot on 2 live projects for 4 weeks; HSE indicators reconciled to timesheets | Ops lead, HSE lead |
| End of R4 | Gate R7 red-team report; NFR-PERF at enterprise scale | Security, product owner |

---

## 8. Migration number reservations

| No. | WP | Name |
|---|---|---|
| 0039 | WP-1.3 | budget_revisions |
| 0040 | WP-1.2 | authority_matrix |
| 0041 | WP-1.6 | uom_reservations_reorder (built) |
| 0042 | WP-1.7 | org_structure |
| 0043 | WP-1.8 | imports (built) |
| 0044 | WP-2.1 | forecast (built) |
| 0045 | WP-2.4 | change_events (built) |
| 0046 | WP-2.6 | claims (built) |
| 0047 | WP-2.1 | forecast_cancel (built: a draft forecast can be cancelled by its author) |
| 0048 | WP-1.10 | people_access (built: directory, company roles, role grants) |
| 0049 | WP-1.10 | people_access_fix (built: pinned search_path, first seen) |
| 0050 | WP-2.7 | bonds_escalation (built: guarantees, price indices, formulas, price adjustments) |
| 0051 | WP-2.6 | contract_time (built: liquidated damages terms, extension of time, obligations) |
| 0052 | WP-2.1 | forecast_cpi (built: CPI method, last approved EAC, change rule) |
| 0053 | WP-2.2/2.3 | revenue_recognition (built: revenue policy, revenue runs, REV_RUN journals) |
| 0054 | WP-2.4 | change_variation_links (built) |
| 0055 | WP-2.5 | subcontracts (built: part 1) |
| 0056 | WP-2.5 | subcontract_payments (built: part 2) |
| 0057 | WP-2.9 | fixed_assets (built) |
| 0058 | WP-2.13 | period_end (built) |
| 0059 | WP-2.12 | cash_plan (built: planned cash items) |
| 0060 | WP-1.10 | role_adjustments (built: product roles narrowed per company) |
| 0061 | WP-2.8 | payroll (built: employees, rule sets, pay runs, PAY_RUN and PAY_PAY journals) |
| 0062 | WP-2.10 | consolidation (built: groups, intercompany accounts, runs) |
| 0063 | WP-2.11 | tax (built: tax accounts, VAT/WHT returns, e-invoice submissions) |
| 0064* | next | numbers from here on are provisional; each package takes the next free number when built |
| 0065 | WP-3.2 | schedule_ext |
| 0066 | WP-3.3 | timesheets |
| 0067 | WP-3.4 | equipment |
| 0068 | WP-3.5 | documents |
| 0069 | WP-3.6 | quality |
| 0070 | WP-3.7 | hse |
| 0071+ | R4 and corrections | assigned when started |

If WPs land in a different order, renumber **before** merge, never after.

---

## 9. Status board

Legend: ⬜ not started · 🟨 in progress · 🟦 in review · ✅ done · ⛔ blocked.

| WP | Title | Phase | Est. (ew) | Depends on | Status | PR / commit | Evidence |
|---|---|---|---|---|---|---|---|
| WP-0.1 | Scrollable, collapsible sidebar | R0 | 1 | - | ✅ | uncommitted | browser check 2026-11-06 |
| WP-0.2 | Continuous integration | R0 | 2 | - | ✅ | uncommitted | .github/workflows/ci.yml, CODEOWNERS |
| WP-0.3 | Commit containerised dev env | R0 | 0.5 | - | ✅ | uncommitted | Makefile targets up/seed/reset, runlocal.md |
| WP-0.8 | Self-explanatory pages (page guides) | R0 | 1 | 0.1 | ✅ | uncommitted | docs/release/evidence/wp-1.3-0.8-verification-2026-11-07.md |
| WP-0.4 | Design system + i18n | R0 | 5 | 0.1 | ⬜ | | |
| WP-0.5 | IdP: OIDC, step-up, revocation | R0 | 4 | 0.6 | ⬜ | | |
| WP-0.6 | Staging: TLS, vault, KMS signer | R0 | 3 | 0.2 | ⬜ | | |
| WP-0.7 | Observability | R0 | 2 | 0.6 | ⬜ | | |
| WP-1.1 | Gate A closure, pentest, load | R1 | 6 | 0.5, 0.6, 0.7 | ⬜ | | |
| WP-1.2 | Authority matrix, delegation, SoD | R1 | 4 | - | ✅ | uncommitted | docs/release/evidence/wp-1.2-verification-2026-11-07.md |
| WP-1.3 | Budget revisions and transfers | R1 | 5 | - | ✅ | uncommitted | docs/release/evidence/wp-1.3-0.8-verification-2026-11-07.md |
| WP-1.4 | Control dashboard + drill-down | R1 | 4 | 1.3 | ✅ | uncommitted | docs/release/evidence/wp-1.4-verification-2026-11-07.md |
| WP-1.5 | Report catalogue to 25 | R1 | 5 | - | ✅ | uncommitted | docs/release/evidence/wp-1.5-verification-2026-11-07.md |
| WP-1.6 | UoM, reservations, reorder | R1 | 4 | - | ✅ | uncommitted | docs/release/evidence/wp-1.6-verification-2026-11-07.md |
| WP-1.7 | Multi-entity, project master | R1 | 4 | - | ✅ | uncommitted | docs/release/evidence/wp-1.7-verification-2026-11-07.md |
| WP-1.8 | Onboarding wizard, imports | R1 | 5 | 1.7 | ✅ | uncommitted | docs/release/evidence/wp-1.8-verification-2026-11-07.md |
| WP-1.9 | R1 hardening and GA | R1 | 4 | all R1 | 🟨 | uncommitted | docs/release/evidence/wp-1.9-verification-2026-11-07.md (docs and contract done; UAT, pentest, GO sign-off pending) |
| WP-1.10 | People, roles and profiles | R1 | 3 | 1.2 | ✅ | uncommitted | docs/release/evidence/wp-1.10-verification-2026-11-08.md, docs/release/evidence/wp-1.10-addendum-account-menu-graph-roles-2026-11-08.md |
| WP-2.1 | Cost forecasting | R2 | 5 | 1.3 | ✅ | uncommitted | docs/release/evidence/wp-2.1-2.2-2.3-verification-2026-11-08.md |
| WP-2.2 | WIP schedule | R2 | 3 | 2.1 | ✅ | uncommitted | docs/release/evidence/wp-2.1-2.2-2.3-verification-2026-11-08.md |
| WP-2.3 | IFRS 15 revenue | R2 | 4 | 2.2, D-05 | ✅ | uncommitted | docs/release/evidence/wp-2.1-2.2-2.3-verification-2026-11-08.md (input and output methods per entity; per-contract method and D-05 sign-off pending) |
| WP-2.4 | Change events | R2 | 4 | 1.3 | ✅ | uncommitted | docs/release/evidence/wp-2.4-completion-verification-2026-11-08.md (subcontract variation links move to WP-2.5) |
| WP-2.5 | Subcontracts | R2 | 8 | 1.3, 2.4 | 🟨 | uncommitted | docs/release/evidence/wp-2.5-part1-verification-2026-11-08.md, docs/release/evidence/wp-2.5-part2-verification-2026-11-08.md (built: subcontracts, certificates, payments, documents, pay-when-certified; pending: bank-statement settlement of subcontract payments, subcontract variations and their change-event links, subcontractor portal, certificate PDF) |
| WP-2.6 | Claims, EOT, notices, LDs | R2 | 4 | - | ✅ | uncommitted | docs/release/evidence/wp-2.6-completion-verification-2026-11-08.md |
| WP-2.7 | Bonds and price adjustment | R2 | 4 | - | ✅ | uncommitted | docs/release/evidence/wp-2.7-verification-2026-11-08.md |
| WP-2.8 | Workforce and payroll | R2 | 8 | D-04 | ✅ | uncommitted | docs/release/evidence/wp-2.8-verification-2026-11-09.md (rule values are tenant data until the D-04 adviser-signed fixture set; ISO 20022 file and timesheet hours (WP-3.3) pending) |
| WP-2.9 | Fixed assets | R2 | 5 | - | ✅ | uncommitted | docs/release/evidence/wp-2.9-verification-2026-11-08.md (cost model; revaluation, impairment, transfers and units-of-production pending) |
| WP-2.10 | Consolidation, intercompany | R2 | 6 | 1.7 | ✅ | uncommitted | docs/release/evidence/wp-2.10-verification-2026-11-09.md (report-only consolidation with translation, NCI, equity method and eliminations; historical-rate equity, consolidation journals and automatic intercompany matching by reference pending) |
| WP-2.11 | Tax: WHT, VAT return, e-invoice | R2 | 4 | D-06 | ✅ | uncommitted | docs/release/evidence/wp-2.11-verification-2026-11-09.md (returns computed from posted journals, approval and filing; e-invoice through a fake adapter until D-06; automatic withholding on supplier payments and receipts, certificates and return forms pending) |
| WP-2.12 | Cash forecasting | R2 | 3 | 2.8 | ✅ | uncommitted | docs/release/evidence/wp-2.12-verification-2026-11-08.md (13-week view; the project-horizon view is pending) |
| WP-2.13 | Fiscal calendar, accruals | R2 | 4 | - | ✅ | uncommitted | docs/release/evidence/wp-2.13-verification-2026-11-08.md (corporate overhead budgets and a labour-hours driver pending) |
| WP-3.1 | Mobile field app | R3 | 14 | D-01, 0.5 | ⬜ | | |
| WP-3.2 | Planning ext., P6/MSP | R3 | 8 | - | ⬜ | | |
| WP-3.3 | Timesheets, attendance | R3 | 4 | 2.8 | ⬜ | | |
| WP-3.4 | Equipment and fleet | R3 | 6 | 2.9, 3.3 | ⬜ | | |
| WP-3.5 | Document control, RFIs, BIM | R3 | 10 | - | ⬜ | | |
| WP-3.6 | Quality | R3 | 5 | 3.5 | ⬜ | | |
| WP-3.7 | HSE | R3 | 5 | 3.3 | ⬜ | | |
| WP-3.8 | Punch lists, instructions, meetings | R3 | 3 | 3.1 | ⬜ | | |
| WP-3.9 | Rules of credit, installed qty | R3 | 3 | 3.1 | ⬜ | | |
| WP-4.1 | External portals | R4 | 8 | 0.5 | ⬜ | | |
| WP-4.2 | Document AI | R4 | 6 | 3.5 | ⬜ | | |
| WP-4.3 | Predictive risk | R4 | 5 | 2.1 | ⬜ | | |
| WP-4.4 | Report builder, BI feed | R4 | 7 | 1.5 | ⬜ | | |
| WP-4.5 | Notification channels, SCIM | R4 | 4 | 0.5 | ⬜ | | |
| WP-4.6 | Integrations | R4 | 6 | - | ⬜ | | |
| WP-4.7 | Scale, certification readiness | R4 | 5 | 1.1 | ⬜ | | |

Total: about 240 engineer-weeks across 76 calendar weeks with the team in
section 4.

---

## 10. Next three actions

1. **WP-0.3:** commit the dev environment and the sidebar (WP-0.1) on a
   branch, so the baseline is clean.
2. **WP-0.2:** stand up CI. Every later WP depends on it to prove "done".
3. **WP-1.3:** start budget revisions in parallel. It is the highest-value
   business gap and unblocks forecasting, change events and subcontracts.

Decisions needed from the product owner before R1 GA: D-02 hosting, D-03 IdP
and KMS, D-09 retention, D-11 licensing, D-12 languages (SRS Appendix E).
