# Final completion programme — charter and control centre

Directive: *SERK Construction OS 3.0 — The Final Completion Directive (ENDGAME)*, received 2026-11-15.
Starting checkpoint: `dd0e9bc` (GitHub Actions run 37, all six jobs green). Release decision: **NO-GO**.

## Completion contract

The target is every in-scope requirement verified, with external dependencies and human decisions
reported separately and never counted as done. The contract is enforced by `tools/final_completion.py`:

| Check | Where | Fails when |
|---|---|---|
| `--check` (CI, lint job) | every push | a baseline requirement disappears or changes meaning, an ID is duplicated, evidence names a test that does not exist, a VERIFIED item has no evidence, a mandatory test is missing, an open requirement belongs to no work package, or a release document claims GO while work is open |
| `--release` | release qualification only | additionally: any unverified requirement or criterion, a console route with no browser journey, an open critical/high defect or security finding, an unresolved external blocker |

`--release` fails today, as it should.

## Canonical sources (one per kind of information)

| Information | Canonical source | Generated views |
|---|---|---|
| Requirements and acceptance criteria | `tools/requirements_register.py` (evidence rule: a test must PASS in an executed run) | `docs/engineering/requirements.json`, `REQUIREMENTS.md`, `02_MASTER_REQUIREMENTS.json`, `03_DOMAIN_COMPLETION_MATRIX.md` |
| Original scope (frozen) | `01_BASELINE_REGISTER.json` — 196 requirements, 80 domains, at `dd0e9bc`; never rewritten | — |
| Work packages | `WORK_PACKAGES` in `tools/final_completion.py` (curated fields only; status is derived) | `05_ENGINEERING_BACKLOG.json`, `04_DEPENDENCY_GRAPH.md` |
| Defects | `06_DEFECT_REGISTER.json` (hand-maintained) | — |
| Security findings | `docs/release/REMEDIATION_REGISTER.yaml` | `07_SECURITY_REGISTER.json` |
| External blockers | `08_EXTERNAL_BLOCKERS.json` (hand-maintained) | — |
| Test evidence | `docs/engineering/evidence/` | `09_TEST_EVIDENCE_INDEX.json` |
| Console route coverage | console navigation in `app.js` and `e2e/*.mjs` | `10_UI_COVERAGE_MATRIX.json` |
| Integrations | `11_INTEGRATION_MATRIX.json` (hand-maintained; test doubles never shown as live) | — |
| Release gates | `docs/release/GATE_STATUS.json` (`tools/gate_status.py`, run only when cutting a checkpoint) | — |
| Session state and resume point | `docs/engineering/execution-state.json` | `14_FINAL_HANDOVER.md` points to it |
| Human acceptance | `13_HUMAN_ACCEPTANCE_MATRIX.md` and `docs/completion/19_HUMAN_AUTHORIZATION.md` (unsigned) | — |

## Separate metrics (never combined)

1. Original requirement closure — the 196 baseline IDs.
2. Expanded verification — requirements plus acceptance criteria (`criteria_counts`).
3. Domain implementation coverage — `03_DOMAIN_COMPLETION_MATRIX.md`.
4. Browser journey coverage of console routes — `10_UI_COVERAGE_MATRIX.json`.
5. Security findings — `07_SECURITY_REGISTER.json`.
6. External integrations — `11_INTEGRATION_MATRIX.json`.
7. Human acceptance — pending for everything.
8. Production authorisation — not given.

## Rules carried from earlier directives

Additive migrations; tenant isolation and approvals enforced in PostgreSQL; no session setting used as
evidence of approval; no AI authority to approve, post, pay, release or certify; no invented statutory
rates; no skipped tests; no merge, deployment, production change or real data without separate
authorisation. Work is pushed only to `claude/new-session-xkd5g3`.
