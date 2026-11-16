# Completion documents

The completion directive lists 21 numbered documents. This index says where each one is today. Where an existing document already covers the subject it is referenced rather than copied; where nothing adequate exists the entry says so. "Not written" means exactly that.

| # | Document | State |
|---|---|---|
| 00 | Engineering inventory | `00_ENGINEERING_INVENTORY.md` (generated) |
| 01 | Architecture | `../engineering/ARCHITECTURE.md`, `../architecture/MODULES.md`, `../adr/` (22 decisions) |
| 02 | Domain map and completion plan | `02_DOMAIN_MAP.md` (generated; 80 domains in 13 waves) |
| 03 | Requirements traceability | `03_REQUIREMENTS_TRACEABILITY.md` (generated) and `../engineering/requirements.json` |
| 04 | Database architecture | Partly: `../engineering/ARCHITECTURE.md`, migration headers. No consolidated schema document or relationship diagram |
| 05 | API architecture | `../../api/openapi.yaml` (generated from the routes; a test keeps it current). No narrative document |
| 06 | Financial invariants | Partly: `../engineering/TEST_MATRIX.md`, ADRs on posting. No single consolidated statement |
| 07 | Security architecture | `../engineering/THREAT_MODEL.md`, `../security/ROLE_PERMISSION_MATRIX.md` (generated), `../SECURITY.md`, ADR 0019 and 0021 |
| 08 | AI governance | Boundary enforced in code and SQL (AI and service principals can only prepare drafts). **No AI gateway exists**; not written |
| 09 | Offline architecture | Server-side sync API exists and is tested. **No field client exists**; not written |
| 10 | Deployment guide | `../../deploy/railway/README.md`, `../operations/`. Not updated for object storage, scanner and key versions |
| 11 | Operations runbook | `../engineering/RUNBOOK.md`, `../operations/INCIDENT_RESPONSE.md`, `../operations/KEY_CUSTODY_RUNBOOK.md`, `../operations/OBSERVABILITY_SLOS.md` |
| 12 | Test strategy | `../engineering/TEST_MATRIX.md`, `../../tools/ci/mandatory_tests.txt`, `../testing/` |
| 13 | Performance report | `../engineering/CAPACITY_PLAN.md`: one query measurement. **No load or soak test has been run** |
| 14 | Disaster recovery | `../engineering/evidence/2026-11-15-pitr-drill-0054.txt`, `../../deploy/drills/`. Database only; objects and keys are not yet part of a drill |
| 15 | UI design system | Not written; styles are in `internal/webui/static/app.css` |
| 16 | User manual | In-product guide (`internal/webui/static/guide.js`). No standalone manual |
| 17 | Administrator manual | `../operations/ADMIN_RUNBOOK.md` (predates documents, fleet and key versions; not updated) |
| 18 | Internal testing plan | `../release/UAT_PLAN_R1.md`, `../uat/` (written for release 1; not updated for documents, fleet and fuel) |
| 19 | Human authorisation | `19_HUMAN_AUTHORIZATION.md` (unsigned) |
| 20 | Final engineering report | `20_FINAL_ENGINEERING_REPORT.md` (a status report: the directive is not complete) |

Regenerate 00, 02 and 03 with `python3 tools/completion_map.py` after the register changes.
- `22_FINAL_SCOPE_AND_GAP_REGISTER.md` — discovery for the final-completion directive (with JSON)
- `23_FINAL_COMPLETION_CHECKPOINT_1.md` — checkpoint report of the first final-completion session
- Control centre: `../final-completion/00_PROGRAM_CHARTER.md`
