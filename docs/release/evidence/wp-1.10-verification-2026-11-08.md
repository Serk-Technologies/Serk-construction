# WP-1.10 People, roles and profiles: verification, 2026-11-08

SRS: IAM-05 (company roles from the fixed permission list, no role holding both halves of a control),
IAM-06 (project assignment), IAM-09 (duty-conflict analysis before a role is given), IAM-11 (a role
removal takes effect on the next request), ADM-01 (users and roles in onboarding).

## Delivered

| Layer | What |
|---|---|
| Database | `0048_people_access.sql`: `directory_users`, `custom_roles`, `role_grants` with row-level security, column grants, guards. `0049_people_access_fix.sql`: pinned `search_path` on the guards; "first seen" may be filled once. |
| Authorisation | `user.manage`, `role.manage` (security administrator); `Principal.Custom` for company-role permissions; `pkg/auth/labels.go` gives every permission an area, a plain label and a stage of work (tested complete). |
| Kernel | `internal/kernel/people.go`: Touch, ApplyDirectory, People, GetPerson, AddPerson, UpdatePerson, ManagePerson, SetPersonStatus, GrantRole, RevokeRole, SaveRole, RetireRole, MyProfile, UpdateMyProfile. |
| API | `GET/POST /v1/people`, `GET/PUT /v1/people/{uid}`, `POST /v1/people/{uid}/manage|disable|enable|roles`, `POST /v1/role-grants/{id}/revoke`, `POST /v1/roles`, `PUT /v1/roles/{code}`, `POST /v1/roles/{code}/retire`, `GET/PUT /v1/me/profile`; the auth middleware records the person, refuses a disabled one (403 `account_disabled`) and applies the directory, failing closed. |
| Console | **Governance → People & access**: summary tiles; People (cards or table, search, filters); Duty map (prepare → approve → release → govern, highlight by role); Roles (product and company roles, composer with live conflict marking); Access history. Side sheets for a person and a role. **My profile** from the avatar in the top band. Page guides for both. |

## Controls proven

| Control | Where enforced | Test |
|---|---|---|
| Nobody changes their own access | kernel (`notSelf`) and database (CHECK on grants, guard on status, projects, management) | TestPeople_DirectoryRolesAndProfile §4, TestAPIPeopleDirectory |
| A role creating a duty conflict is refused unless an approved exception covers the pair | kernel (`auth.SoDViolations`) | §5; API 409 |
| A company role never holds a conflict, nor becomes one for a present holder | kernel | §5 |
| Changes need fresh MFA, a human principal and a reason | kernel (`peopleTx`) | §3 |
| The directory replaces the token's roles and projects on the next request | middleware + `ApplyDirectory` | §3, API (same token, next request) |
| Disabling refuses every request and revokes sessions | middleware + `session_revocations` | §7, API (401 old token, 403 new sign-in) |
| Grants are never deleted; removal is dated with a reason; a held role cannot be retired | database (no DELETE grant, guards) | §6 |
| First seen is set once and never rewritten | database guard (0049) | §8 |
| Companies are isolated | row-level security | §8 |

## Browser check (demo on :8080)

`u-security-admin`: 28 people as cards, the table view, the duty map with highlight, the role catalogue, a person
sheet; *Manage access here* on the quantity surveyor; *Commercial manager* previewed as **Will be refused** (Payment
certificates; Change events; Claims) and refused by the server; the role composer marks *Prepare* + *Approve purchase
orders* in red. `u-pm`: My profile (one role, Approve stage, projects), job title saved and visible to the
administrator. `u-auditor`: sees the page, no change buttons. `u-pm` cannot open People & access (no menu link, 403).
Night theme checked. Console errors: only the two intended refusals (409, 403).

Defects found and fixed during verification:

1. Header avatar unreadable in the night theme (contrast 1.10): caught by the accessibility checks of five browser
   journeys; the header rule now outranks the night-theme avatar colour.
2. Initials of names with brackets ("AR clerk (second)" showed "A(").
3. Guard functions without a pinned `search_path` (privilege catalogue invariant): fixed in 0049.
4. A person added before their first sign-in never received a first-seen date: fixed in 0049 and `Touch`.
5. Refusal messages began with the internal prefix `auth:`.

## Demo data changed

The 28 test personas were signed in once (directory rows, named after their duty). The quantity surveyor's access
is managed here (same role as before). The project manager's job title is "Project manager, Mekelle".

## Full suite

`go test ./... -count=1` with PostgreSQL 16 and the Chrome browser journeys, after the fixes above: every package
`ok`, 0 FAIL, exit 0 (18 packages with tests). The earlier run also had one failure in TestR12_TransientFailureIsRetried
("cannot open tenant database session" at fixture setup). It passed on rerun alone and in this run, so it was an
environmental flake, not caused by this package.
