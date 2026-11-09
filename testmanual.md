# SERK Construction: Step-by-Step Testing Manual

| | |
|---|---|
| Product | SERK Construction OS: construction ERP (BOQ, procure-to-pay, contract-to-cash, stock, planning, site) |
| Release under test | Local development build, `0.2.0-gate-a` line. **NOT production-approved** (`docs/release/GO_NO_GO.md`: NO-GO) |
| Stack | Go, PostgreSQL 16, Docker Compose. Web console served by the API |
| Prepared | November 2026 |
| Audience | Testers, accountants, project and site staff performing acceptance, release engineers |
| Basis | `README.md`, `runlocal.md`, `docs/testing/E2E_JOURNEY_A/B/C.md`, `pkg/auth/auth.go`, `internal/httpapi/staging.go` |

> **Important.** Screen labels are taken from the console's menu. If a label on your
> screen differs, record the actual label in the Notes box; that is a documentation
> finding, not necessarily a defect. Use **fictional data only**. Every credential
> in this manual is published in the repository and is refused by the API outside
> `SERK_ENV=development`.
>
> **This is not the "Serk Materials" manual.** `Serk_Materials_Testing_Manual.pdf` in
> this repo covers a different product (Django, cement and steel yard POS). Its users
> (`admin`, `cashier`, password `serk12345`) do **not** exist here.

## Contents

1. How to use this manual
2. Test environment and preparation
3. Login, roles and access control
4. BOQ and budget
5. Requisitions
6. Procure-to-pay (Journey A)
7. Contract to cash (Journey B)
8. Stock valuation (Journey C)
9. Reports, exports and audit
10. Platform: settings, notifications, automation, intelligence
11. Operations: start, stop, persistence
12. Automated test suites
- Appendix A Defect log
- Appendix B Test summary and sign-off
- Appendix C Test data sheet
- Appendix D Role and capability reference

---

## 1. How to use this manual

### 1.1 Purpose

This manual tells a tester exactly what to click, what to type and what must
happen. It is the **human acceptance layer** on top of the automated suites
(section 12).

### 1.2 Test case format

Every case is a box with an ID, a role, numbered steps, the expected result and a
result column. Tick exactly one of Pass / Fail / Blocked. A case passes only if
**every** bullet of the expected result is true. If it fails, write a defect number
(Appendix A) in the Notes line.

| Result | Meaning |
|---|---|
| Pass | All expected results observed exactly. |
| Fail | Any expected result not observed, an unexpected error, or a wrong figure. Raise a defect. |
| Blocked | Could not run (missing data, environment, access). State why; re-run later. |

**Reference run lines.** Where a case says *Reference run 2026-11-05*, the case was
executed by the author against the running stack through the **API** (the same
endpoints the console calls) and the stated result was observed. The console screens
for those cases were **not** clicked through by the author. Cases without a reference
run line are derived from the journey documents and automated tests and have **not**
been executed manually: treat their expected results as the specification to confirm.

### 1.3 Defect severity

| Severity | Definition | Examples |
|---|---|---|
| Critical | Money, ledger or security integrity wrong; data lost | A forged approval accepted; journal posted unbalanced; one company sees another's data |
| High | Core workflow blocked or wrong, no workaround | Cannot approve an invoice; three-way match wrong |
| Medium | Works with a workaround, or wrong label/format | Wrong column in an export; unclear error message |
| Low | Cosmetic | Alignment, spelling |

### 1.4 Ground rules for testers

- Record the **starting figures** (budget, stock, balances) before each money or stock test and the ending figures after. Compute expected results from your recorded values.
- Never test against real data. Use the seeded demo company only.
- Posted records cannot be edited or deleted by design. Mistakes are corrected by reversal, void or credit note, so plan the order of your tests and note every document number you create (Appendix C).
- **Approving a BOQ replaces that project's budget** with the approved BOQ. Approve a deliberately tiny BOQ and later requisitions will be refused (case PR-03). Start a full pass from a freshly seeded database (section 11.4).
- Do not skip the negative tests (things that must be **refused**). They are the controls the owners are accepting.
- Approve, post, pay and release actions are reserved to **human** sign-ins by the database itself.

---

## 2. Test environment and preparation

### 2.1 Start the stack (once per machine)

```bash
cd ~/serk/Serk-construction
export SERK_DB_PORT=55432            # only if port 5432 is taken on your machine
docker compose -p serk -f deploy/docker-compose.dev.yml up -d --build
scripts/dev-seed.sh                  # seeds the demo company, enables persona sign-in
```

Full instructions, stop/start and troubleshooting: [runlocal.md](runlocal.md).

### 2.2 Console and company

| Item | Value |
|---|---|
| Console URL | http://127.0.0.1:8080/ (redirects to `/console/`; use `SERK_API_PORT` if changed) |
| Company | `SERK Demo Construction (synthetic)` |
| Tenant id | `11111111-1111-4111-8111-111111111111` |
| Projects | `P-001 Mekelle Ring Road`, `P-002 Addis Mixed-Use Tower` (ids differ per database; see Appendix C) |
| Sign-in | Choose company and persona, enter the passphrase, press **Sign in as persona** |
| **Passphrase (all personas)** | `dev-only-staging-passphrase-0123456789` |

There are **no per-user passwords**. Each persona is a fixed test user and the
single shared passphrase is what authenticates the sign-in. Use a **separate private
browser window per persona** so sessions do not mix (the token lives in that tab's
memory only). Step-up (MFA) actions need a sign-in made within the last 10 minutes;
persona sign-in counts as a fresh multi-factor login.

### 2.3 Test users (personas)

Each person holds exactly one duty, so separation of duties is tested by switching
persona. "Scoped" personas see only the seeded projects; the others are company-wide.

| Persona id | Role | Scoped | Typical duty |
|---|---|---|---|
| `u-estimator` | estimator | yes | Creates and submits the BOQ |
| `u-commercial` | commercial_manager | yes | Approves BOQ, contracts; certifies payment certificates (IPC) |
| `u-pm` | project_manager | yes | Approves requisitions, schedules, site diaries |
| `u-planner` | planner | yes | Builds schedules and progress |
| `u-site-engineer` | site_engineer | yes | Raises requisitions, evaluates RFQs, confirms goods receipts, verifies measurements |
| `u-buyer` | procurement_officer | yes | Suppliers, RFQs, purchase orders |
| `u-buyer-2` | procurement_officer | yes | A second procurement officer, so a staged supplier or stock-item import can be committed by someone else |
| `u-proc-manager` | procurement_manager | yes | Approves suppliers, awards RFQs, approves POs |
| `u-storekeeper` | storekeeper | yes | Goods receipts, stock receive, issue, count, transfer |
| `u-inventory` | inventory_controller | yes | Approves stock adjustments and transfers |
| `u-qs` | quantity_surveyor | yes | Measurements, prepares payment certificates |
| `u-project-accountant` | project_accountant | yes | Creates and reads project journals |
| `u-ap-clerk` | ap_clerk | yes | Enters supplier invoices |
| `u-ap-supervisor` | ap_supervisor | yes | Approves supplier invoices |
| `u-ar-clerk` | ar_clerk | no | Customers, customer invoices, receipts, credit notes |
| `u-ar-clerk-2` | ar_clerk | no | A second AR clerk, so a staged customer import can be committed by someone else |
| `u-ar-supervisor` | ar_supervisor | no | Approves customer invoices, confirms receipts |
| `u-treasury` | treasury_officer | no | Prepares and releases payments |
| `u-treasury-2` | treasury_officer | no | Second treasury officer (release must differ from preparer) |
| `u-bank-officer` | bank_statement_officer | no | Imports bank statements |
| `u-accountant` | accountant | no | Drafts journals, runs reconciliation |
| `u-controller` | controller | no | Posts journals, closes periods |
| `u-finance-manager` | finance_manager | no | Approves journals and payments (first approver) |
| `u-cfo` | cfo | no | Second payment approver, FX approvals |
| `u-auditor` | auditor | no | Read-only everywhere |
| `u-process-owner` | process_owner | no | Authors automation rules and workflows |
| `u-workflow-admin` | workflow_admin | no | Activates automation rules |
| `u-security-admin` | security_admin | no | Company settings, session revocation |

### 2.4 Sign-in from the command line (for API-level cases)

```bash
B=http://127.0.0.1:8080; PW=dev-only-staging-passphrase-0123456789
tok(){ curl -s -X POST $B/staging/token -H 'Content-Type: application/json' \
  -d "{\"passphrase\":\"$PW\",\"persona\":\"$1\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])'; }
curl -s -H "Authorization: Bearer $(tok u-accountant)" $B/v1/identity/me
```

For any role or subject outside the 28 personas (including AI and service
principals), mint a token with the development token tool:

```bash
docker compose -p serk -f deploy/docker-compose.dev.yml --profile tools run --rm devtoken \
  --tenant 11111111-1111-4111-8111-111111111111 --sub alice --roles accountant \
  [--projects <project-id>] [--kind human|service|ai] [--mfa] 2>/dev/null | tail -1
```

### 2.5 Baseline to record before testing

Open the console and record in Appendix C: the two project ids, the cost codes of
P-001 (for example `01-MAT`, `02-MAT`) and the current budget of each; existing
suppliers, customers, warehouses and stock items. If the demo data lacks something a
case needs, create it with the persona that owns that duty and write it down. Do not
invent figures in expected results; compute them from your recorded start values.

---

## 3. Login, roles and access control

**Before you start:** stack running and seeded. A private browser window per persona.

**AUTH-01 · Every persona can sign in** — Role: all

| Steps | Expected result | Result |
|---|---|---|
| 1. Open the console.<br>2. Choose the company, persona `u-estimator`, enter the passphrase, **Sign in as persona**.<br>3. Note the menu, then **Sign out**.<br>4. Repeat for every persona in 2.3. | - Each persona reaches **My work**.<br>- Menus differ by role: an item appears only when the persona's role holds its permission (see Appendix D). For example `u-estimator` has no Risks or Ask.<br>- No error page for any persona. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): all 26 personas obtained a token (28 since 2026-11-07).*

**AUTH-02 · Wrong passphrase refused** — Role: any

| Steps | Expected result | Result |
|---|---|---|
| 1. Choose persona `u-accountant`, enter a wrong passphrase, sign in.<br>2. (API) Request a token for a persona id that does not exist. | - Sign-in refused with an error; no session, no menu.<br>- API: wrong passphrase gives **401**; unknown persona gives **422**. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): 401 and 422.*

**AUTH-03 · No sign-in, no access** — Role: none

| Steps | Expected result | Result |
|---|---|---|
| 1. In a fresh private window open http://127.0.0.1:8080/console/.<br>2. (API) `curl -i http://127.0.0.1:8080/v1/identity/me` with no token. | - The console shows the sign-in page, no data.<br>- API returns **401** "bearer token required". | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): 401.*

**AUTH-04 · Auditor is read-only** — Role: u-auditor

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-auditor`.<br>2. Open **Project → BOQ**, **Requisitions**, **Finance → Journals**.<br>3. Look for create, submit and approve actions.<br>4. (API) POST a new BOQ with the auditor's token. | - Records are visible.<br>- No create or approve actions are offered.<br>- API create returns **403**. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): 403.*

**AUTH-05 · Maker cannot approve own record** — Role: u-estimator, u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-estimator` create and **Submit** a BOQ (see BOQ-01).<br>2. As the same persona try to **Approve** it (UI action, or API `POST /v1/boqs/<id>/approve`).<br>3. Sign out, sign in as `u-commercial` and approve. | - Step 2 is refused: **403** "insufficient permission" (the estimator does not hold the approve duty).<br>- Step 3 succeeds and the BOQ becomes APPROVED. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API, via `scripts/staging_smoke.py`): self-approval 403, approval by commercial manager 200.*

**AUTH-06 · AI and service sign-ins cannot approve** — Role: API only

| Steps | Expected result | Result |
|---|---|---|
| 1. Create and submit a BOQ as `u-estimator`.<br>2. Mint a token with `--roles commercial_manager --kind ai` and another with `--kind service` (section 2.4; add `--projects <P-001 id>`).<br>3. Try `POST /v1/boqs/<id>/approve` with each.<br>4. Approve as `u-commercial` (human). | - AI token: **403** "insufficient permission".<br>- Service token: **403**.<br>- Human commercial manager: **200**.<br>- Approve, post, pay and release are human-only even when the role would allow them. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): ai 403, service 403, human 200.*

**AUTH-07 · Project scope** — Role: u-pm, u-accountant

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-pm`; open **My work** and the project list.<br>2. Sign in as `u-accountant`.<br>3. (API) `GET /v1/identity/me` for each. | - `u-pm` is project-scoped (`tenant_wide` false, two projects).<br>- `u-accountant` is company-wide (`tenant_wide` true).<br>- Both show `mfa` true (persona sign-in is a fresh MFA login). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): as stated.*

**AUTH-08 · Sign out ends the session** — Role: any

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-accountant`, press **Sign out**.<br>2. Press the browser Back button.<br>3. (API) Call `/v1/identity/me` with the same token before and after `POST /v1/identity/logout`. | - The sign-in page is shown; Back shows no protected data without a new sign-in.<br>- API: **200** before, **204** on logout, **401** with the same token afterwards. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): 200, 204, 401.*

**AUTH-09 · Passphrase guessing is limited** — Role: none

| Steps | Expected result | Result |
|---|---|---|
| 1. Submit a wrong passphrase repeatedly in quick succession. | - After enough failures the sign-in answers **429** (too many attempts) until the lock-out window passes. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Not executed by the author (it would lock the sign-in out). The exact threshold and window are not stated here; they are defined in `internal/httpapi/abuse.go` and asserted by `TestStagingLoginIssuesPersonaTokens`. Run this case last.*

**AUTH-10 · Authority matrix: propose, approve, take effect** — Role: u-process-owner, u-cfo

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-process-owner`. **Governance → Authority matrices → New**: add one rule, Document `Budget transfer`, From `100000`, permission `approval.high_value`, Fresh MFA `Yes`. Create and **Submit**.<br>2. Still as `u-process-owner`, look for **Approve**.<br>3. Sign in as `u-cfo` (just now, so MFA is fresh) and **Approve**. | - Step 2: no Approve button; the API refuses (author cannot approve).<br>- Step 3: status APPROVED; **Governance → Authority & duties** shows the matrix in force.<br>- Approving a second version later retires the first (only one is in force). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**AUTH-11 · The matrix limits approvals** — Role: u-pm, u-commercial, u-cfo (after AUTH-10)

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-pm` request and submit a budget transfer of 150,000, and another of 50,000.<br>2. As `u-commercial` try to approve the 150,000 one, then the 50,000 one.<br>3. As `u-cfo` approve the 150,000 one. | - 150,000 by `u-commercial`: refused, "the authority matrix requires approval.high_value ...".<br>- 50,000: approved (below the band).<br>- 150,000 by `u-cfo`: approved. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**AUTH-12 · Delegation of authority** — Role: u-commercial, u-security-admin, u-planner, u-pm

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-commercial`: **Governance → Authority & duties → Delegate my authority**: colleague `u-planner`, permission `budget.approve`, 7 days, reason `Leave cover`. Create and **Submit**.<br>2. As `u-commercial` try to approve it; then as `u-security-admin` (fresh sign-in) **Approve**.<br>3. As `u-pm` submit a budget revision. As `u-planner` approve it.<br>4. As `u-commercial` open the delegation and **Revoke** (reason required). As `u-planner` look at the identity menu / try to approve another revision. | - Step 2: the delegator cannot approve; the security administrator can.<br>- Step 3: approved; the audit trail shows `u-planner` as approver.<br>- Step 4: the delegation is REVOKED and `u-planner` can no longer approve at once. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**AUTH-13 · What can never be delegated, and never by proxy** — Role: u-commercial, u-planner

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-commercial` try to delegate `budget.override`, or a permission you do not hold.<br>2. Try a delegation to yourself, or longer than 90 days (API).<br>3. With an approved delegation of `budget.approve` from `u-commercial` to `u-planner`, have `u-commercial` (given both roles) prepare a budget revision and `u-planner` try to approve it. | - 1 and 2: refused with a plain explanation.<br>- 3: refused: "you hold delegated authority from the creator of this ... who therefore cannot be approved through you".<br>- Delegating to someone whose own permissions would then conflict (for example `budget.approve` to `u-pm`) has no effect until an approved duty-conflict exception covers that person. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-07 (kernel and HTTP tests): TestAuthority_MatrixGovernanceAndLimits, TestAuthority_DatabaseRefusesBypass (13 raw-SQL forgeries refused), TestAuthority_DelegationLifecycle, TestAuthority_ConflictingDelegationIsDroppedUnlessExcepted, TestAuthority_MatrixLimitsPurchaseOrders, TestAPI_AuthorityAndDelegationOverHTTP, TestDelegatedAuthority, TestDelegationAllowed, TestSoDViolations: PASS.*

**ORG-01 · Create a project and move it through stage gates** — Role: u-commercial, u-pm, u-cfo

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-controller` open **Governance → Organisation & calendar** and add a region (`EAST`) and a business unit (`ROADS`).<br>2. As `u-commercial`: **Projects & planning → Projects → New project**: code `P-003`, a name, region, unit, contract type, dates. Create.<br>3. Sign in as a project manager whose token includes the new project (or as `u-cfo`, who sees all) and open **Projects**: use **Request mobilisation** with evidence text.<br>4. Submit; as the same person look for Approve; as `u-cfo` (fresh sign-in) **Approve**. | - The project is PLANNED in phase BID and is not offered in cost forms.<br>- A gate cannot skip a phase and only one gate is open per project.<br>- The author cannot approve; `u-cfo` approves and the project becomes ACTIVE, phase Mobilisation.<br>- The phase column cannot be changed any other way (API or database). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**ORG-02 · Project master data and roll-ups** — Role: u-pm, u-cfo

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-pm` open **Projects → Edit** on a project in scope: set manager, dates, location, region and business unit.<br>2. Try a finish date before the start date.<br>3. As `u-cfo` open **Overview → Dashboard** and pick the region.<br>4. As `u-controller` add a holiday in **Organisation & calendar**; try the same day twice. | - Details save; a finish before the start is refused.<br>- The dashboard budget and risk tiles narrow to that region's projects (cash and ledger stay company-wide).<br>- A day can have only one calendar entry. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-07: TestOrg_ProjectLifecycleThroughGates, TestOrg_MasterDataRegionsAndRollups, TestAPI_ProjectMasterAndGates: PASS.*

**SET-01 · The setup checklist reads real state** — Role: u-cfo, u-pm

| Steps | Expected result | Result |
|---|---|---|
| 1. Open **Overview → Company setup**.<br>2. Note which steps are ticked; do one (for example create a region in **Organisation & calendar**, or approve a BOQ) and reload the page. | - Each step says why, who does it and links to the page; optional steps are marked.<br>- A step ticks itself only when the records exist (nothing can be ticked by hand); the progress bar counts required steps only. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**IMP-01 · Stage, review and commit an import with two people** — Role: u-buyer, u-proc-manager

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-buyer`: **Governance → Data imports**: choose Suppliers, click **Fill in the template**, add a few rows (include one with a code that already exists and one with a name of 2 letters), **Stage for review**.<br>2. Review the rows: errors are shown per row. Try to commit as `u-buyer`.<br>3. Sign in as `u-buyer-2` (the second procurement officer) and open the batch; enter a wrong row count, then the right one, and commit.<br>4. Open **Procurement → Suppliers**. | - Staging creates nothing; invalid rows show their reasons.<br>- The stager cannot commit; a wrong confirmed row count is refused with a clear message.<br>- The valid rows are created as suppliers waiting for approval (still PENDING); the batch is Partial if some rows were invalid, Committed if all were valid.<br>- Committing again never duplicates records. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**IMP-02 · Stock items and customers** — Role: u-buyer, u-ar-clerk

| Steps | Expected result | Result |
|---|---|---|
| 1. Stage stock items with an unknown inventory account code and a method other than FIFO or WAC.<br>2. As `u-ar-clerk` stage customers (commit as `u-ar-clerk-2`); try the same as `u-buyer`. | - The unknown account and method are reported per row.<br>- Customers can be staged only by people who may manage customers; each person sees only the kinds they manage. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-07: TestImports_StageReviewCommit (database forgeries refused), TestImports_StockItemsAndCustomers, TestSetup_ChecklistReadsRealState, TestAPI_ImportsAndSetup: PASS.*

---

## 4. BOQ and budget

**Before you start:** freshly seeded database (11.4). Personas `u-estimator`, `u-commercial`, `u-auditor`.

**BOQ-01 · Estimator creates and submits a BOQ** — Role: u-estimator

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-estimator`.<br>2. **Project → BOQ**: create a BOQ for P-001 with one line: `1.1 Reinforcement steel`, unit `t`, quantity 100, rate 90,000, on a material cost code (for example `02-MAT`).<br>3. **Submit**. | - The BOQ is created in draft (HTTP 201) and shows quantity × rate = 9,000,000.<br>- After Submit the status is SUBMITTED.<br>- A line description that is too short (for example `x`) is refused by the database with a clear "invariant violation" message. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): create 201, submit 200; description `x` refused 409 `invariant_violation`.*

**BOQ-02 · Estimator cannot approve** — see AUTH-05.

**BOQ-03 · Commercial manager approves through the signer** — Role: u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-commercial`.<br>2. Open the submitted BOQ and **Approve**. | - Status becomes APPROVED.<br>- The approval is recorded with `u-commercial` as approver and the estimator as creator.<br>- The project's budget for that cost code now equals the approved BOQ amounts. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): approve 200.*

**BUD-01 · Project manager creates budget revision** — Role: u-pm

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-pm`.<br>2. **Projects & planning → Budget versions**: create a REVISION for P-001 with reason `Additional foundation works`, line on `01-MAT` with amount 500,000.<br>3. **Submit**. | - Version created in DRAFT and advances to SUBMITTED.<br>- Cannot be approved by `u-pm` (maker/checker). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**BUD-02 · Commercial manager approves budget revision** — Role: u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-commercial`.<br>2. Open the submitted budget revision and **Approve**. | - Status becomes APPROVED.<br>- The project budget overview and cost report reflect the revised budget for that cost code. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**BUD-03 · Budget transfer between cost codes** — Role: u-pm, u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-pm`.<br>2. **Projects & planning → Budget → Request transfer**: transfer 50,000 from `01-MAT` to `02-MAT`, reason `Material reallocation`.<br>3. **Submit** transfer.<br>4. Sign in as `u-commercial` and **Approve**. | - Status becomes APPROVED.<br>- `01-MAT` current budget decreases by 50,000; `02-MAT` current budget increases by 50,000.<br>- Net budget impact across the project is zero. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**BUD-04 · Transfer exceeding available budget is refused by database guard** — Role: u-pm, u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-pm`, request a transfer from `01-MAT` whose amount exceeds the available budget on `01-MAT`.<br>2. Submit and try to approve as `u-commercial`. | - Approval is refused by the database guard with an error message that transfer exceeds available budget.<br>- The transfer record is not approved. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**BUD-05 · Contingency drawdown** — Role: u-pm, u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-pm`, create a REVISION for P-001 with one line on an unused code, amount 100,000, **Contingency? = Yes**; submit. As `u-commercial`, approve.<br>2. As `u-pm`, **Projects & planning → Budget overview → Draw down contingency**: from the contingency code to `01-MAT`, 40,000, reason `Unforeseen ground conditions`; submit.<br>3. As `u-commercial`, approve. | - The overview marks the code "(contingency)".<br>- After approval: contingency code **Drawdowns out** 40,000 and current 60,000; `01-MAT` **Drawdowns in** 40,000.<br>- The project total is unchanged by the drawdown. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**BUD-06 · Drawdown limits** — Role: u-pm, u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. Request a drawdown larger than what remains in the contingency code; submit; approve as `u-commercial`.<br>2. Request a drawdown **from** a code that is not contingency (for example `02-MAT`); submit; approve. | - Both approvals are refused with a message naming the code and the available amount (1) or "a drawdown comes only from a cost code that holds contingency" (2).<br>- Both stay SUBMITTED and can be rejected with a reason. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**BUD-07 · Nobody approves their own budget change** — Role: u-pm

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-pm`, submit a revision, a transfer and a drawdown.<br>2. Still as `u-pm`, look for **Approve** on each. | - No Approve button is shown to the author; calling the API directly is refused (403/409).<br>- `u-commercial` sees all three under **My work → Budget ... to approve**. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-07 (kernel and HTTP tests): TestBudget_ArithmeticAndControls, TestBudget_DatabaseRefusesBypass, TestBudget_ConcurrentTransfersCannotOverspend, TestAPI_BudgetRevisionOverHTTP all PASS.*

**FC-01 · Monthly cost forecast: prepare, explain, approve** — Role: u-pm, u-cfo

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-pm` (with an approved budget on the project). **Projects & planning → Cost forecasts → New**: choose the project, keep today\'s date, **Create forecast**.<br>2. On the forecast page set a cost code to **Manual estimate** above its current budget and leave the note empty, then try **Submit**.<br>3. Enter a note of at least 10 characters for that code, then **Submit**.<br>4. Still as `u-pm`, look for **Approve**.<br>5. Sign in as `u-cfo` and **Approve**. | - Step 1: a table with one row per cost code that has money on it; Cost to date, Committed and Current budget are plain text, not editable.<br>- Step 2: refused with a message naming the cost code and asking for an explanation.<br>- Step 3: the forecast is SUBMITTED.<br>- Step 4: no Approve button; the API refuses the author.<br>- Step 5: status APPROVED; totals show Estimate at completion and Variance (budget - EAC, negative = overrun). The page is now read-only. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-07 (kernel test): TestForecast_MethodsAndGuards PASS.*

**CHG-01 · Change event: raise, decide, budget draft** — Role: u-pm, u-commercial, u-cfo

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-pm`, **Projects & planning → Change events → New**: source Client request, revenue impact `80000`, 5 days, two cost lines (`+50000`, `-10000`). Create and **Submit**.<br>2. Still as `u-pm`, look for **Approve**.<br>3. Sign in as `u-commercial` (fresh MFA) and **Approve**.<br>4. Open the page again. | - Step 2: no Approve button.<br>- Step 3: status APPROVED.<br>- Step 4: **Effect on the budget** shows cost impact 40,000, margin impact 40,000 and a link to a DRAFT budget revision; the budget overview has **not** changed until `u-pm` submits that revision and `u-cfo` approves it. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-08 (kernel test): TestChange_EventToBudgetDraft PASS.*

**CLM-01 · Notice, time bar and claim** — Role: u-qs, u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-qs`, **Commercial & revenue → Contract notices → New**: clause `20.1`, event date 40 days ago, 28 days to notify. Open the list.<br>2. Prepare a **Claim** (Time and cost, 450,000 and 9 days) linked to that notice, leaving the reason empty; **Submit**.<br>3. Record the notice as sent today. Prepare the claim again with the reason "Engineer waived the time bar in writing" and **Submit**.<br>4. As `u-commercial` (fresh MFA) **Approve**. As `u-qs` record the client\'s answer: Agreed, 300,000 and 6 days. Try 450,001. | - Step 1: the notice shows Overdue (12 days past).<br>- Step 2: refused: the notice was late, state why the claim stands.<br>- Step 3: after the notice is sent it shows Sent late; the claim with a reason submits.<br>- Step 4: approved; the answer is recorded once; 450,001 is refused (cannot exceed the claim); **Reports → Claims register** shows the agreed amount. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-08 (kernel test): TestClaims_NoticesAndClaims PASS.*

**ACC-01 · People, roles and duty conflicts** — Role: u-security-admin, u-planner

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-security-admin`. Open **Governance → People & access**. Try the filters, the **Table** view, then the **Duty map** tab and highlight *Procurement officer*.<br>2. **People** tab: open the **Planner** card. Type the reason "Directory rollout for planning" and click **Manage access here**.<br>3. In **Choose a role to give…** pick *Project manager*, read the preview, click **Give role**.<br>4. Give *Auditor* instead; then **Remove** it (same reason box).<br>5. Open your own card (**Security admin**).<br>6. **New company role**: tick *Prepare purchase orders* and *Approve purchase orders*; then untick *Approve*, code `site_buyer`, name "Site buyer", a reason; **Create role**.<br>7. Open the Planner again, reason "Left the company", **Disable and sign out**. In another browser sign in as `u-planner`. Then **Enable** the planner again. | - Step 1: 28 people; each card shows avatar, role, stage colour, where its roles come from ("From sign-in" or "Managed here") and when last seen; the map places each person at Prepare, Approve, Release or Govern and dims everyone but the two procurement officers.<br>- Step 2: the planner now shows "Managed here" with the same role as before (nothing changes at the switch).<br>- Step 3: the preview says *Will be refused* (Schedules); the server refuses with the control named.<br>- Step 4: *Auditor* appears with who gave it, when and why; after removal it stays in **Role history** as removed.<br>- Step 5: "This is you": no buttons to change your own access.<br>- Step 6: the two ticked labels turn red with *Not allowed*; creating with both is refused; with one it is created and listed under **Company roles**.<br>- Step 7: the planner\'s sign-in is refused (*account is disabled in this company*); after **Enable** they can sign in again. **Access history** lists every change with reason. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**ACC-02 · My profile** — Role: u-pm, u-security-admin

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-pm`. Click your name (with the avatar) in the top band.<br>2. Read **Your roles**, **What you may do**, **Where you work** and **This sign-in**.<br>3. In **Your details** set the job title "Project manager, Mekelle" and **Save**.<br>4. Open **Governance** in the menu.<br>5. As `u-security-admin` open **People & access** and find the project manager. | - Step 1: **My profile** opens: avatar, name, stage chip *Approve*, *Multi-factor sign-in*.<br>- Step 2: one role (Project manager); permissions grouped by area in plain words; the assigned project(s); sign-in time.<br>- Step 3: saved; the job title shows under the name.<br>- Step 4: no **People & access** link (only user/role administrators and auditors see it).<br>- Step 5: the card shows the new job title. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-08 (kernel and API tests): TestPeople_DirectoryRolesAndProfile, TestAPIPeopleDirectory PASS.*

**DASH-01 · The dashboard agrees with the reports** — Role: u-cfo, u-pm

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-cfo`. Open **Overview → Dashboard** (Executive view).<br>2. Note **Budget consumed** and **Current budget**. Open **Projects & planning → Cost report** for the same project and add up Committed + Reserved + Actual and Current budget.<br>3. Open **Ledger balance** and compare with **Accounting → Trial balance**.<br>4. On any tile open **How is this calculated?** and click **See the rows behind this number**. | - The tile figures equal the cost report and trial balance figures (budget consumed % = consumed / current budget).<br>- Every tile shows a level (On track / Watch / Needs action / No data), its formula, source and the time it was computed.<br>- Contributors link to the cost report for that project; the drill link opens the page holding the rows. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**DASH-02 · The dashboard respects roles and scope** — Role: u-pm, u-estimator, u-auditor

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-pm` and open the Dashboard.<br>2. Switch views (Project, Finance, Procurement).<br>3. Repeat as `u-estimator` and `u-auditor`. | - `u-pm` sees the budget tiles of their projects; cash, ledger and control tiles are listed under **Not shown to your role** with the permission they need.<br>- A view never adds a tile the role cannot read.<br>- `u-estimator` sees budget tiles only; `u-auditor` sees finance and control tiles. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-07 (kernel and HTTP tests): TestDashboard_TilesEqualTheirReports, TestDashboard_ScopeAndPermissions, TestDashboard_RefusesBadInput, TestKPICatalogueDocumentIsCurrent, TestAPI_Dashboard: PASS. KPI formulas: docs/reports/KPI_CATALOGUE.md.*

---

## 5. Requisitions

**Before you start:** an approved BOQ for P-001 whose `02-MAT` budget is at least 880,000 (for example the 9,000,000 BOQ from BOQ-01).

**PR-01 · Raise, submit and approve a requisition** — Role: u-site-engineer, u-pm, u-auditor

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-site-engineer`: **Project → Requisitions**, new requisition for P-001, line: Steel, `t`, quantity 10, estimated price 88,000, cost code `02-MAT`. **Submit**.<br>2. As `u-pm`: open it and **Approve**.<br>3. As `u-auditor`: open it. | - A number such as `PR-2026-000001` is assigned.<br>- After approval the status is APPROVED and the budget check passed.<br>- The auditor sees it read-only, with `created_by` = `u-site-engineer`. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API, `scripts/staging_smoke.py`): create 201, submit 200, approve 200, auditor read 200, status APPROVED.*

**PR-02 · Project manager outside the project cannot see or approve** — Role: API only

| Steps | Expected result | Result |
|---|---|---|
| 1. Raise and submit a requisition for P-001 as `u-site-engineer`.<br>2. Mint a token with `--roles project_manager --projects <P-002 id>` (section 2.4).<br>3. Try to approve the P-001 requisition with it. | - Refused with **404** "not found": a manager outside the project is not even told the record exists. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): 404.*

**PR-03 · Budget is enforced at approval** — Role: u-estimator, u-commercial, u-site-engineer, u-pm

| Steps | Expected result | Result |
|---|---|---|
| 1. Make P-001 `02-MAT` budget tiny: approve a BOQ with one `02-MAT` line, quantity 1, rate 1.<br>2. As `u-site-engineer` raise and submit a requisition for 1 t at 88,000 on `02-MAT`.<br>3. As `u-pm` try to **Approve**. | - Refused with **409** `budget_exceeded`.<br>- The message states the cost code, the amount needed, the budget and the amount already reserved.<br>- The requisition stays SUBMITTED. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05 (API): 409 `budget_exceeded` ("needs 88000, available negative, budget 1, reserved 1,760,000"). **This case damages the project's budget; reset (11.4) before the next section.***

---

## 6. Procure-to-pay (Journey A)

**Basis:** `docs/testing/E2E_JOURNEY_A.md`. The figures below are asserted to the
cent by automated tests at kernel, API and browser level. **No case in this section
has been executed manually by the author**; the console click-paths are by menu
label. If a screen differs, the journey document and `e2e/console_journey_a.mjs` are
authoritative.

**Before you start:** freshly seeded database. Budget for cost code `01-MAT` is 12,100,000 (120 t steel at 85,000 plus 2,000 bags cement at 950): create and approve that BOQ first (BOQ-01 and BOQ-03 with these lines). Suppliers `ABC-STEEL`, `HABESHA-CEMENT`, `SAFARI-BUILD` and stock items need to exist (create as `u-buyer`; approvals by `u-proc-manager`); see the journey document.

**JA-01 · RFQ and award to the lowest compliant quote** — Role: u-buyer, u-site-engineer, u-proc-manager

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-site-engineer`: requisition 20 t steel (estimate 86,000) and 500 bags cement (estimate 960); `u-pm` approves.<br>2. `u-buyer`: **Procurement → RFQs**, RFQ to the three suppliers; record the quotes: ABC-STEEL steel 84,500 / cement 940; HABESHA-CEMENT 86,000 / 930; SAFARI-BUILD 90,000 / 980.<br>3. `u-site-engineer`: evaluate quotes; mark SAFARI-BUILD non-compliant (no mill certificates).<br>4. `u-proc-manager`: **Award** to ABC-STEEL.<br>5. Try to award SAFARI-BUILD. | - ABC-STEEL is the lowest compliant quote and the award succeeds.<br>- Awarding the non-compliant quote is refused. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JA-02 · Purchase order** — Role: u-buyer, u-proc-manager

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-buyer`: create the PO from the award.<br>2. `u-proc-manager`: **Approve**. | - PO total is **2,160,000** (20 t × 84,500 + 500 bags × 940 = 1,690,000 + 470,000).<br>- The creator cannot approve their own PO. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JA-03 · Goods receipts in two deliveries** — Role: u-storekeeper, u-site-engineer

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-storekeeper`: **Site → Goods receipts**, record 12 t steel and 500 bags cement; `u-site-engineer` confirms.<br>2. Record and confirm the second delivery: 8 t steel. | - Each confirmation generates a GRNI (goods received not invoiced) draft journal.<br>- Total received matches the PO: 20 t steel, 500 bags.<br>- The storekeeper cannot confirm their own receipt. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JA-04 · Invoice 1: three-way match within tolerance** — Role: u-ap-clerk, u-ap-supervisor

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-ap-clerk`: **Payables → Invoices**, enter an invoice for the cement billed at **945** against a PO price of 940. Submit.<br>2. `u-ap-supervisor`: **Approve**. | - Status MATCHED: variance 0.53% is inside the 2% tolerance.<br>- **2,500** goes to price variance.<br>- Invoice total **1,709,475** including VAT.<br>- Approval generates an AP journal. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JA-05 · Invoice 2 and over-billing control** — Role: u-ap-clerk, u-ap-supervisor

| Steps | Expected result | Result |
|---|---|---|
| 1. Enter an invoice for the 8 t of steel (total **777,400** including VAT) and approve it as `u-ap-supervisor`.<br>2. Try to submit an invoice that bills more than was received. | - Invoice 2 is approved with total 777,400.<br>- The over-billed invoice is refused by the database at submit (**409**).<br>- An unsubmitted invoice cannot be approved. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JA-06 · Journals: approve then post** — Role: u-finance-manager, u-controller

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-finance-manager`: **Finance → Journals**, approve the 4 subledger journals in one batch.<br>2. `u-controller`: post them in one batch.<br>3. Try posting as the finance manager. | - Journals move DRAFT → APPROVED → POSTED.<br>- The approver cannot post (and the maker cannot approve).<br>- Every journal balances (debits = credits). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JA-07 · Payments with dual approval** — Role: u-treasury, u-finance-manager, u-cfo, u-treasury-2

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-treasury`: **Payables → Payments**, prepare a payment for each invoice (both exceed the 500,000 dual-approval threshold).<br>2. `u-finance-manager`: approve (status PARTIALLY_APPROVED).<br>3. `u-cfo`: approve (status APPROVED).<br>4. `u-treasury-2`: **Release**.<br>5. Negative: release as `u-treasury` (the preparer); release before the second approval; approve twice as the same person. | - Steps 2 to 4 succeed in that order.<br>- All negatives are refused with a visible reason.<br>- Approvals require a sign-in within the last 10 minutes.<br>- 2 release journals are generated, approved and posted. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JA-08 · Bank statement and reconciliation** — Role: u-bank-officer, u-accountant

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-bank-officer`: **Treasury → Bank statements**, import a statement with 4 lines: both payments, a customer receipt, a bank charge.<br>2. `u-accountant`: **Finance → Reconciliation**, run auto-reconcile. | - The 2 payments are matched and SETTLED.<br>- The other 2 lines stay unmatched, each with a reason.<br>- 2 settlement journals are generated; approve and post them (JA-06 roles). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JA-09 · End state (as `u-auditor`)** — Role: u-auditor

| Steps | Expected result | Result |
|---|---|---|
| 1. **Project → Cost report** for cost code `01-MAT`.<br>2. **Finance → Trial balance** and **Reconciliation**. | - Budget **12,100,000**; committed **2,160,000**; received **2,160,000**; invoiced **2,162,500**; actual posted **2,162,500**; available **9,937,500**.<br>- GRNI 0, accounts payable 0, payments in transit 0; subledger equals GL (difference 0).<br>- Reconciled bank movements **−2,486,875**, equal to the GL.<br>- No unposted P2P journal remains. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

---

## 7. Contract to cash (Journey B)

**Basis:** `docs/testing/E2E_JOURNEY_B.md` (`TestJourneyB_ContractToCashToGL`). Not
executed manually by the author. VAT 15% and the advance-recovery base are tenant
configuration used as test data; they are not a statement of any jurisdiction's tax law.

**Before you start:** freshly seeded database. Customer: Ethiopian Roads Administration (create as `u-ar-clerk`). Contract `CTR-2026-000001`: retention 5%, advance 1,000,000 recovered at 20% of each certificate's gross.

**JB-01 · Contract set-up** — Role: u-commercial, u-ar-clerk

| Steps | Expected result | Result |
|---|---|---|
| 1. **Revenue → Contracts**: create the contract with 3 lines: Bulk excavation 10,000 m³ × 350; Concrete C30 1,200 m³ × 6,500; Reinforcement 120 t × 98,000.<br>2. Approve as a different persona (`u-commercial`). | - Contract value **23,060,000** (3,500,000 + 7,800,000 + 11,760,000).<br>- The preparer cannot approve it. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JB-02 · Advance received** — Role: u-ar-clerk, u-ar-supervisor, u-bank-officer

| Steps | Expected result | Result |
|---|---|---|
| 1. Import a bank line of 1,000,000; allocate it as a receipt against the contract advance; confirm as `u-ar-supervisor`. | - Journal: Dr Bank 1,000,000 / Cr Advances received 1,000,000. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JB-03 · Measurement 1 and payment certificate 1** — Role: u-qs, u-site-engineer, u-commercial, u-ar-clerk

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-qs`: **Revenue → Measurements**, period ending 15 Oct: 4,000 m³ / 300 m³ / 20 t.<br>2. `u-site-engineer`: verify.<br>3. `u-qs` prepares and `u-commercial` certifies the **Payment certificate** (IPC 1).<br>4. `u-ar-clerk` raises the **Customer invoice**; `u-ar-supervisor` approves. | - Cumulative gross **5,310,000**; IPC 1 gross 5,310,000.<br>- Retention 5% = **265,500**.<br>- Advance recovery = **1,000,000** (20% = 1,062,000, capped at the advance received).<br>- VAT 15% × (5,310,000 − 1,000,000) = **646,500**.<br>- Amount due **4,691,000**.<br>- Journal: Dr AR 4,691,000, Dr Retention 265,500, Dr Advances 1,000,000 / Cr Revenue 5,310,000, Cr VAT output 646,500 (both sides 5,956,500). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JB-04 · Receipt, measurement 2, payment certificate 2** — Role: as JB-03

| Steps | Expected result | Result |
|---|---|---|
| 1. Receive 4,691,000 allocated to invoice 1.<br>2. Measurement 2 (period ending 20 Oct): cumulative 7,000 m³ / 500 m³ / 30 t. Certify and invoice as in JB-03.<br>3. Receive 2,000,000 allocated to invoice 2 (part payment). | - Invoice 1 settled.<br>- Cumulative gross **8,640,000**; IPC 2 gross **3,330,000**; retention **166,500**; advance recovery **0** (fully recovered); VAT **499,500**; amount due **3,663,000**.<br>- Invoice 2 part-paid, 1,663,000 outstanding. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JB-05 · End state** — Role: u-finance-manager, u-controller, u-auditor

| Steps | Expected result | Result |
|---|---|---|
| 1. Approve (`u-finance-manager`) and post (`u-controller`) all five journals.<br>2. As `u-auditor` check the contract and **Receivables aging**. | - Contract value 23,060,000; measured = certified **8,640,000**; retention held **432,000**; advance received and recovered **1,000,000 / 1,000,000**. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JB-06 · Guarantees and advance cover** — Role: u-treasury, u-commercial

**Before you start:** an active contract (the demo has `CTR-2026-000001`, Mekelle ring road lot 2). Page: **Commercial & revenue → Guarantees & price adjustment**.

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-treasury`: **Register a guarantee**: Advance payment, 1,500,000, issued 20 Aug 2026, expiring in 25 days.<br>2. Register a Performance guarantee of 1,675,000 expiring 31 Dec 2027.<br>3. Try to register one with amount −5. | - Two cards; the advance one shows **Expires soon** and the tile "Expire within 30 days" is 1.<br>- "Held (ETB)" shows 3,175,000.<br>- The negative amount is refused with a message. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |
| 4. Open the advance card; give a later expiry and a reason; **Record the change**.<br>5. Try a higher value. | - The card is back to **In force**; the change count goes up.<br>- A higher value is refused: a guarantee can only be reduced. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |
| 6. `u-treasury` opens a card. <br>7. `u-commercial` opens the Performance card: **Release or record a call** → Released, letter reference, reason → **Record**. | - Treasury sees no release form (it registered the guarantee).<br>- Release succeeds only with a sign-in that used a second factor in the last 10 minutes; the card then shows **Released** and cannot be changed. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JB-07 · Price indices, formula and price adjustment** — Role: u-qs, u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-qs`, **Price indices** tab: **Add an index** CEM and FUEL.<br>2. **Propose a monthly value**: CEM Jul 2026 = 2410.5, Oct 2026 = 2652; FUEL Jul 2026 = 88.4, Oct 2026 = 97.6.<br>3. Try a month in the future. | - Each value waits for approval; the QS has no Approve button.<br>- A future month is refused. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |
| 4. `u-commercial`: approve the four values. Propose a second CEM Oct 2026 value as `u-qs`. | - Cards show the latest value, the change from the previous month and a small trend line.<br>- The second value for an approved month is refused. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |
| 5. `u-qs`, **Price adjustment** tab: fixed portion 0.30, base month Jul 2026, CEM 0.45, FUEL 0.20.<br>6. Change FUEL to 0.25, **Save formula**, then **Submit** on the formula page.<br>7. `u-commercial` approves it. | - With 0.20 the box reads "a + Σb = 0.950000 — must add up to exactly 1" (red) and saving is refused.<br>- With 0.25 it reads 1.000000 (green).<br>- Submitting is refused if a base-month value is not approved. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |
| 8. `u-qs`: on the certified payment certificate choose Oct 2026 → **Compute** → **Prepare and submit for approval**.<br>9. `u-commercial` approves it from **Price adjustments to approve** on My work. | - Factor **1.071102** = 0.30 + 0.45 × 2652/2410.5 + 0.25 × 97.6/88.4.<br>- Adjustment = 1,830,000 × 0.071102 = **130,116.66 ETB**.<br>- A second adjustment for the same certificate is refused; the tile "Approved adjustment" shows 130,116.66. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JB-08 · Time for completion, damages and obligations** — Role: u-qs, u-commercial

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-qs`: open an active contract; in *Time for completion and liquidated damages* enter a completion date in the past, 15,000 a day, a cap; **Save and submit for approval**.<br>2. Try a cap above the contract value. | - The terms wait for approval; a cap above the contract value is refused. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |
| 3. `u-commercial` approves them from *Damages terms to approve*. | - Tiles: days late today, damages to date = days late × 15,000 (never above the cap). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |
| 4. Record a claim's client answer as AGREED with 8 days (see the claims cases). | - *Extensions agreed* shows +8 d; the revised completion moves 8 days and the damages fall accordingly. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |
| 5. **Contract obligations**: add one due in 5 days and one due 3 days ago; close the first as Done with a letter reference; try to waive the second without a reason. | - *Due within 7 days* and *Overdue* tiles count them; the waiver without a reason is refused; a closed obligation shows its note and cannot be reopened. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

---

## 8. Stock valuation (Journey C)

**Basis:** `docs/testing/E2E_JOURNEY_C.md` (`TestJourneyC_StockValuationToGL`). Not
executed manually by the author. Items: steel `STEEL-G60` (t, **FIFO**) and cement
`CEM-OPC50` (bag, **WAC**), both on inventory account 1200, project store. Currency ETB.

**JC-01 · Receipts and put-away** — Role: u-storekeeper, u-site-engineer

| Steps | Expected result | Result |
|---|---|---|
| 1. PO 1: steel 20 t @ 84,500, cement 500 @ 940. PO 2: steel 20 t @ 86,000, cement 500 @ 950.<br>2. **Site → Goods receipts**: GRN 1 = steel 12 t, cement 300 bags; confirm.<br>3. **Stores → Put-aways**: steel 12 t; cement 200 bags, then 100 bags.<br>4. GRN 2 = steel 10 t, cement 200 bags; confirm.<br>5. Put away steel 10 t and cement 150 bags (leave 50 bags received but not put away). | - GRN 1 journal Dr inventory **1,296,000** (1,014,000 + 282,000); GRN 2 **1,050,000** (860,000 + 190,000).<br>- Cement put-aways: 188,000, then the exact remainder **94,000**; second GRN 142,500 with **47,500** left un-put-away.<br>- **On hand: steel 22 t = 1,874,000; cement 450 bags = 424,500** (average 943.333…). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JC-02 · Issues: FIFO and weighted average** — Role: u-storekeeper, u-site-engineer

| Steps | Expected result | Result |
|---|---|---|
| 1. **Stores → Stock issues**: issue 15 t steel and 120 bags cement to P-001, cost code `01-MAT`; approve as the second person. | - Steel issue cost **1,272,000** (layer 1 whole 12 t = 1,014,000 + 3 t × 86,000 = 258,000); steel left **7 t = 602,000**.<br>- Cement issue cost **113,200.00** (120 × 424,500 / 450); cement left **330 bags = 311,300**. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JC-03 · Counts and variance** — Role: u-storekeeper, u-inventory

| Steps | Expected result | Result |
|---|---|---|
| 1. **Stores → Stock counts**: steel counted 6.5 t (book 7); cement counted 335 (book 330).<br>2. `u-inventory` approves each. | - Steel shortage 0.5 t valued FIFO from layer 2 = **43,000** (Dr 5200 / Cr 1200).<br>- Cement surplus 5 at last receipt cost 950 = **4,750.00** (Dr 1200 / Cr 5200).<br>- The counter cannot approve their own count. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**JC-04 · End state** — Role: u-auditor

| Steps | Expected result | Result |
|---|---|---|
| 1. Post all journals, then open **Stores → Stock on hand**. | - Steel **6.5 t = 559,000**; cement **335 bags = 316,050**; total stock value **875,050**. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

Stock transfers between warehouses and planning (schedules, progress, earned value) are
specified in `docs/testing/E2E_PLANNING.md` and `internal/kernel/stock_transfer_test.go`;
add cases to this section when you test them.

**STK-10 · Unit conversion** — Role: u-buyer, u-finance-manager, u-storekeeper

| Steps | Expected result | Result |
|---|---|---|
| 1. As `u-buyer`: **Stores → Unit conversions → New**: from `bundle`, to `t` (the unit of a steel item), factor `2.5`. Create and **Submit**.<br>2. Try to approve as the same person; then as `u-finance-manager`.<br>3. As `u-storekeeper` request a **stock issue** of 1 in unit `bundle`. | - Approving your own conversion is refused; the finance manager can approve.<br>- Before approval the issue in bundles is refused ("no approved conversion").<br>- After approval the issue shows 2.5 (t); an unknown unit is refused. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**STK-11 · Reservations protect stock** — Role: u-site-engineer, u-storekeeper, u-project-manager

| Steps | Expected result | Result |
|---|---|---|
| 1. With 12 t of an item on hand, `u-site-engineer`: **Stores → Reservations → Reserve stock** 8 t for P-001 until next week.<br>2. Try to reserve 5 t more.<br>3. As `u-storekeeper` request an unreserved issue of 2 t and have `u-site-engineer` approve it, then request an issue of 8 t **against the reservation** and approve it.<br>4. Release a reservation (reason required) and try again. | - Step 2 is refused (only 4 t unreserved).<br>- Step 3: the 2 t issue is refused while it would use reserved stock; the issue against the reservation is approved and the reservation shows **Used by an issue**.<br>- After releasing, the unreserved issue goes through. A stranger cannot release your reservation. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**STK-12 · Reorder rule and low-stock report** — Role: u-buyer, u-cfo

| Steps | Expected result | Result |
|---|---|---|
| 1. **Reservations → Reorder rule**: minimum 10, maximum 20, reorder quantity 5 for an item in a store holding less than 10 available.<br>2. Open **Stores → Low stock**. | - The item appears with on hand, reserved, available, the rule, and a suggested order = the larger of the reorder quantity and (maximum - available).<br>- Nothing is ordered automatically. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-07: TestStockPlus_ConversionsAreControlled, TestStockPlus_ReservationsIssuesAndReorder (race for the last units, raw-SQL forgeries refused), TestAPI_StockPlus: PASS.*

---

## 9. Reports, exports and audit

**RPT-01 · Exports show no more than the screen** — Role: u-accountant, u-auditor

| Steps | Expected result | Result |
|---|---|---|
| 1. Open a list or report (for example **Finance → Trial balance** or **Reports**).<br>2. Use the **PDF**, **Word** and **Excel** export buttons.<br>3. Repeat as a project-scoped persona (`u-pm`) on a project list. | - Each file downloads and opens.<br>- The content equals what that persona sees on screen, and no more. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**AUD-01 · Posted records are immutable** — Role: u-controller

| Steps | Expected result | Result |
|---|---|---|
| 1. Post a journal (JA-06).<br>2. Look for an edit or delete action on the posted journal; use **Reverse** instead. | - The console offers reversal, not edit or delete.<br>- A reversal creates a new journal that cancels the original; the original is unchanged.<br>- Database-level immutability (triggers) is proven by the automated suites (`internal/kernel/gate_a_ledger_test.go`, `review*_regression_test.go`), not by this manual. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |


**REP-10 · Catalogue reports run, tie out and export** — Role: u-cfo, u-controller

| Steps | Expected result | Result |
|---|---|---|
| 1. **Overview → Reports**. Under the group headings (Ledger, Payables, Receivables, Procurement, Revenue, Stores, Project controls) open **General ledger detail**, **Journal register**, **Purchase order register**, **Open commitments**, **Goods received not invoiced**, **Payment register** and **Budget against actual by month** (choose a project).<br>2. For **Supplier statement** choose a supplier; for **Customer statement** choose a customer.<br>3. Check: GL detail total debits = total credits and equals the trial balance debits; Open commitments = order value - invoiced; the payment register lists each payment once with who prepared, approved and released it and **no bank details**.<br>4. Export any report as PDF, Word and Excel. | - Each report shows its rows, a **Total** line for money columns, the text "Data as of ..." and the filters used.<br>- Statements show a running balance that ends at invoiced - paid (or received).<br>- Exports open, carry the company letterhead and match the screen. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**REP-11 · Reports respect scope** — Role: u-pm, u-estimator

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-pm` and open **Reports**.<br>2. Try the API `GET /v1/reports/catalogue/gl-detail?entity=...` directly. | - Only project reports are listed (PO register, open commitments, GRNI, match exceptions, certificates, retention, stock, budget against actual); company-wide ones (GL detail, journal register, supplier and customer statements, payment register) are not offered.<br>- The direct call is refused (403). Project reports show only the projects assigned to the person. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-07: TestReportCatalogue_EveryReportRunsAndTiesOut, TestReportCatalogue_ScopeAndInput, TestAPI_ReportCatalogue, plus customer-statement / certificate / retention checks in Journey B and stock valuation / movement checks in Journey C (all tie to the books): PASS. Catalogue: docs/reports/REPORT_CATALOGUE.md.*

---

## 10. Platform: settings, notifications, automation, intelligence

**PLT-01 · Automation rule: author and activator must differ** — Role: u-process-owner, u-workflow-admin

| Steps | Expected result | Result |
|---|---|---|
| 1. `u-process-owner`: **Company → Automation rules**, create a rule.<br>2. Try to activate it as the same persona.<br>3. `u-workflow-admin`: activate it. | - Step 2 is refused (author cannot activate).<br>- Step 3 succeeds.<br>- Automation rules can only notify; they never approve, post or pay. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**PLT-02 · Notifications, risks and Ask** — Role: u-accountant

| Steps | Expected result | Result |
|---|---|---|
| 1. Open the bell icon, **Overview → Risks**, and **Ask**; ask a question about project cost.<br>2. Repeat as `u-estimator`. | - Notifications and risks list findings.<br>- Ask answers deterministically (no AI endpoint is configured locally) and only reads data.<br>- `u-estimator` has no Risks or Ask (no `intelligence.read`). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**PLT-03 · Company settings** — Role: u-security-admin

| Steps | Expected result | Result |
|---|---|---|
| 1. Sign in as `u-security-admin` and open **Company** settings.<br>2. Sign in as `u-accountant` and look for the same screen. | - The security admin can view and change settings.<br>- The accountant cannot. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

**GUIDE-01 · Every page explains itself** — Role: any persona (try u-pm and u-auditor)

| Steps | Expected result | Result |
|---|---|---|
| 1. Open **My work**, **Budget overview**, **Purchase orders** (a list), one purchase order (a document) and **New requisition** (a form).<br>2. Read the **How this page works** card under each title.<br>3. Close the card on one page, reload, revisit. | - Each card says what the page is for, how to use it, and what the system enforces.<br>- Lists, documents and forms also show the **Life cycle** and **Who does it** (role names, for example "Prepares: Project manager", "Decides: CFO, Commercial manager").<br>- A closed card stays closed on that page only; it never appears in printouts. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

---

## 11. Operations: start, stop, persistence

**OPS-01 · Health endpoints** — Role: operator

| Steps | Expected result | Result |
|---|---|---|
| 1. `curl http://127.0.0.1:8080/healthz` and `/readyz`.<br>2. `docker compose -p serk -f deploy/docker-compose.dev.yml ps -a`. | - `{"status":"ok"}` and `{"status":"ready"}`, both HTTP 200.<br>- `db` healthy, `signer` and `api` up, `migrate` **Exited (0)** (it applies migrations 0001 to 0038 and stops). | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05: as stated.*

**OPS-02 · Data survives a restart** — Role: operator

| Steps | Expected result | Result |
|---|---|---|
| 1. Note a document number.<br>2. `docker compose -p serk -f deploy/docker-compose.dev.yml down` then `up -d`.<br>3. Sign in and find the document. | - The API is ready again; the document is still there.<br>- `migrate` reports "schema up to date". | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

*Reference run 2026-11-05: tenant and projects persisted; "schema up to date".*

**OPS-03 · Persona sign-in works after a restart** — Role: operator

| Steps | Expected result | Result |
|---|---|---|
| 1. After OPS-02, open the console and sign in as `u-pm`.<br>2. Open **My work**. | - The persona list and the company are offered (project ids come from `deploy/.env`).<br>- `u-pm` sees both projects. | [ ] Pass<br>[ ] Fail<br>[ ] Blocked<br>Tester / date:<br>Notes / defect no.: |

### 11.4 Reset to a clean demo (do this before a full pass)

```bash
docker compose -p serk -f deploy/docker-compose.dev.yml --profile tools down -v
docker compose -p serk -f deploy/docker-compose.dev.yml up -d --build
scripts/dev-seed.sh
```

This deletes the demo database (synthetic data only) and recreates it. Document
numbers restart; re-record project ids in Appendix C.

### 11.5 Quick automated smoke (about 5 seconds)

```bash
python3 scripts/staging_smoke.py http://127.0.0.1:8080 dev-only-staging-passphrase-0123456789
```

Expected: ten `PASS` lines and exit code 0 (BOQ create and submit, **self-approval
refused 403**, commercial-manager approval through the signer, requisition create,
submit, approve, auditor read). It creates data, so it affects budgets: run it
**before** your cases or reset afterwards. *Reference run 2026-11-05: 10/10 PASS, run
twice.*

---

## 12. Automated test suites

These need a disposable database and the host Go toolchain; they never use the dev
stack. Commands: [runlocal.md](runlocal.md) section 4. The recorded full run is
**378 PASS, 0 FAIL, 0 SKIP** (`docs/testing/evidence/`); it was **not** re-run for this
manual. Browser journeys (`e2e/console_journey_*.mjs`) use Chromium via Playwright.

---

## Appendix A: Defect log

| No. | Case ID | Severity | Description | Found by / date | Status |
|---|---|---|---|---|---|
| D-001 | | | | | |
| D-002 | | | | | |
| D-003 | | | | | |
| D-004 | | | | | |
| D-005 | | | | | |

## Appendix B: Test summary and sign-off

| Section | Cases | Pass | Fail | Blocked | Not run |
|---|---|---|---|---|---|
| 3 Login, roles, access (AUTH-01 to 09) | 9 | | | | |
| 4 BOQ and budget | 3 | | | | |
| 5 Requisitions | 3 | | | | |
| 6 Procure-to-pay (JA-01 to 09) | 9 | | | | |
| 7 Contract to cash (JB-01 to 05) | 5 | | | | |
| 8 Stock valuation (JC-01 to 04) | 4 | | | | |
| 9 Reports and audit | 2 | | | | |
| 10 Platform | 3 | | | | |
| 11 Operations | 3 | | | | |
| **Total** | **41** | | | | |

| Role | Name | Signature | Date |
|---|---|---|---|
| Lead tester | | | |
| Finance owner | | | |
| Project / operations owner | | | |
| Release engineer | | | |

Remember: a sign-off here is **not** a production release decision. Production remains
NO-GO until the gates in `docs/release/READINESS.md` pass with independent human parties.

## Appendix C: Test data sheet

| Item | Value |
|---|---|
| Date / tester / browser | |
| Tenant id | `11111111-1111-4111-8111-111111111111` |
| P-001 project id | |
| P-002 project id | |
| P-001 `01-MAT` budget (start / end) | |
| P-001 `02-MAT` budget (start / end) | |
| BOQ numbers | |
| Requisition numbers (PR-) | |
| RFQ / PO numbers | |
| GRN numbers | |
| Supplier invoice numbers | |
| Payment instruction numbers | |
| Contract / IPC / customer invoice numbers | |
| Stock issue / count numbers | |
| Journal numbers | |

Project ids: `GET http://127.0.0.1:8080/staging/personas` (companies list) or `deploy/.env`.

## Appendix D: Role and capability reference

The authoritative definition of every role and permission is `pkg/auth/auth.go`
(the `roles` map). The 28 personas map one-to-one to those roles (section 2.3).
Approve, post, pay and release actions are reserved to human principals by the
database (`approval_is_draft_action`), so AI and service sign-ins can only draft
(AUTH-06).
