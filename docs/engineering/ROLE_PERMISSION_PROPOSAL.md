# Role and permission proposal for professional review

The complete, generated matrix of every role and permission is `docs/security/ROLE_PERMISSION_MATRIX.md` (a test fails if it is stale). This page lists only what the 2026-11-15 work added and the decisions a reviewer is asked to make. Nothing here is approved; the grants are defaults in code and can be changed per company with custom roles.

## Documents (migrations 0052, 0053)

| Permission | What it allows | Proposed holders |
|---|---|---|
| `document.manage` | Register documents; upload, submit, rescan and discard revisions; raise, answer and close RFIs | site engineer, quantity surveyor, planner |
| `document.approve` | Publish or reject a revision (fresh MFA, not the uploader) | project manager |
| `tenant.settings.manage` (existing) | Set the document policy: whether scanning is required, storage quota; run the storage migration | security administrator |
| `p2p.read` (existing) | Read the register, download non-quarantined files of projects in scope, read the policy | every project role |

Conflict (no one may hold both): `document.manage` + `document.approve`.

Questions for the reviewer:
1. Should a dedicated **document controller** role exist, rather than site engineers and surveyors uploading?
2. Should publication of some document types (drawings, method statements) need a second approver or a specific discipline lead?
3. Should an RFI be answerable only by the named assignee? Today any `document.manage` holder on the project other than the author may answer.
4. Is the security administrator the right person to turn scanning off, or should that need two people?
5. Should confidential documents (contracts, tender documents) be restricted inside a project? Today every reader of the project sees every document of it.

## Fleet (migration 0054)

| Permission | What it allows | Proposed holders |
|---|---|---|
| `fleet.manage` | Register equipment; prepare operator authorisations and meter resets | plant officer |
| `fleet.operate` | Move equipment through working states, stop it (hold, breakdown, send to inspection or maintenance), write daily logs | plant officer (yard and any project), site engineer (own projects) |
| `fleet.reserve` | Request equipment for a project; cancel own approved request | site engineer, project manager |
| `fleet.approve` | Commission, retire and dispose of equipment; approve or cancel reservations (fresh MFA) | plant manager |
| `fleet.log.approve` | Approve or reject daily logs | project manager |
| `fleet.release` | Certify equipment fit for service after a hold, breakdown, maintenance or inspection; approve operator authorisations and meter resets (fresh MFA) | plant inspector |

New roles: `plant_officer`, `plant_manager`, `plant_inspector` (all company-wide in scope, none with any financial permission).

Conflicts: `fleet.manage` + `fleet.approve`; `fleet.manage` + `fleet.release`; `fleet.reserve` + `fleet.approve`; `fleet.operate` + `fleet.release`; `fleet.operate` + `fleet.log.approve`.

Questions for the reviewer:
1. Who may certify equipment fit for service in your organisation, and does it differ by class (cranes and lifting equipment usually need a licensed inspector)? Today one permission covers all classes.
2. Should placing a safety hold be open to more people than those who operate (any site worker, the HSE officer)?
3. Should a release from a safety hold require a different person from the one who carried out the repair? Today the roles are separated, but the repair itself is not yet recorded (maintenance work orders are not built).
4. Should rented and subcontracted equipment follow the same commissioning rule?
5. Is the project manager the right approver of equipment logs, or the plant manager?

## Unchanged controls relied on

Maker/checker on every decision; fresh MFA (10 minutes) for approvals marked above; human-only decisions (AI and service principals can prepare drafts only); project scope from the identity token; tenant isolation by database login and row-level security; daily export budget per person.
