# 23 — Final-completion programme, checkpoint 1

Session of 2026-11-15, directive *The Final Completion Directive (ENDGAME)*. This is a checkpoint, not a
completion report: most of the 80 domains are still open. Production remains **NO-GO**; no human acceptance
has been given.

## 1. Engineering conclusion

The candidate line now contains all the authored work that existed in the repository: an unmerged line on
`new-version` (about 21,000 lines by the owner, never merged) was found during discovery and integrated
(ADR 0025). An independent review of that work found three High-severity control defects; all three are fixed
with database-level regressions shown to fail without the fix. A completion validator in CI now keeps the
original 196-requirement scope from shrinking or changing meaning. Console forms keep typed lines across
dependent changes. Errors follow RFC 9457 and document lists page by cursor.

## 2. Starting condition and final revision

| | |
|---|---|
| Start | `dd0e9bc` (run 37 green; 471/0/0; 196 requirements: 101 verified) |
| Last pushed before this report | `9c74f52` (runs 38–40 green on GitHub; 41 running when written) |
| Branch | `claude/new-session-xkd5g3` only; `main` untouched (`1096bf2`, unprotected) |

## 3. Original requirement closure (the frozen 196)

Generated counts are in `docs/engineering/REQUIREMENTS.md`; the frozen baseline is
`docs/final-completion/01_BASELINE_REGISTER.json`. Of the 196 baseline IDs, all are present with unchanged
meaning (checked by `tools/final_completion.py --check` in CI). The register now also holds 14 appended
requirements (XCT-FORM, XCT-UI) and acceptance criteria under the large requirements.

| Register (all requirements) | At start | Now |
|---|---|---|
| VERIFIED | 101 | 105 |
| IMPLEMENTING | 9 | 17 |
| IMPLEMENTED_UNVERIFIED | 1 | 1 |
| BLOCKED | 9 | 9 |
| NOT_STARTED | 76 | 78 |
| Total | 196 | 210 |

IMPLEMENTING grew because carried modules (subcontracts, fixed assets, consolidation, quality, meetings)
moved from NOT_STARTED; none was marked verified: the independent review found that no carried test
evidences every clause of a register requirement. Acceptance criteria are counted separately
(`criteria_counts` in `requirements.json`).

## 4. Work packages closed or advanced

| Package | Result | Evidence |
|---|---|---|
| WP-000 control centre | Frozen baseline, master register, backlog with derived status, defect/security/blocker/integration registers, route coverage, validator in CI | `docs/final-completion/`, commit `12d4f30` |
| Integration of `new-version` | Migrations 0058–0071 carried byte-identical (sha256 checked), 0072 reconciles shared functions; colliding document/equipment parts deferred | ADR 0025, `abb80ec`, run 38 green; 489/0/0 |
| WP-001 form state | State-held lines, in-place choice refresh, unresolved choices, pending gating, stale-error suppression, leave prompt; barrier-driven journey fails on the old console | `5a8c052`; 492/0/0 |
| WP-002 API | RFC 9457 problem details everywhere (router 404/405 included); cursor pagination on `/v1/p2p/{type}` bound to tenant, type and filter | `5a8c052` |
| WP-024 part 1 | DEF-008 (attested inspection results, NCR closure), DEF-009 (DB maker-checker on tax returns, consolidation, ITPs), DEF-010 (employee records approved by a second person before pay) | `9bce5f0`, `dbe049a`; 492/0/0 each |
| WP-024 part 3 | DEF-012: overhead allocated once per day of the pool (race-free claim of days) | `dc0cd61`; 492/0/0 |
| WP-024 part 2 | DEF-016: no fake e-invoice adapter by default; 503 until an authorised adapter is configured | `47d5d5c`; 492/0/0 |

## 5. Defects

Canonical: `docs/final-completion/06_DEFECT_REGISTER.json`. Closed this session: DEF-003, DEF-006, DEF-008,
DEF-009, DEF-010, DEF-012, DEF-016. Open High: DEF-001 (colliding new-version parts not yet
rebuilt), DEF-002 (main unprotected; administrator). Open Medium: DEF-011, DEF-013, DEF-014, DEF-015, DEF-017; Low/Info:
DEF-004, DEF-005, DEF-007, DEF-018 to DEF-020.

## 6. Verification integrity

- Every full run here: fresh database, race detector, shuffled order, real MinIO and ClamAV, browser
  journeys; judged by the zero-skip gate; Gitleaks; supply-chain checks. govulncheck and cold-cache checks
  run only in GitHub (no network here).
- New controls were shown to fail without their fix (old console; fresh database without 0073 or 0074).
  One mutation attempt was invalid (it reused a database that already had the migration) and was redone on
  a fresh database; recorded here so the method is visible.
- The carried modules' own evidence files were not accepted as verification.

## 7. Remaining scope (not done in this session)

All items in `docs/final-completion/05_ENGINEERING_BACKLOG.json` other than the above, including: rebuilding
timesheets, HSE, internal hire, transmittals and submittals on this branch's tables; fleet costing and
fixed-asset linkage; the remaining Medium defects; AURA themes, sign-in, workspace, shell and screen review;
frontend modularisation ADR; offline field app; governed AI gateway; estate, estimating, design, portals;
load testing; complete-system recovery; the XCT-DR.2 drill at the current commit.

## 8. External dependencies and human decisions

Unchanged and listed in `docs/final-completion/08_EXTERNAL_BLOCKERS.json`. New owner questions (ADR 0025):
has any environment applied new-version's migrations under their original numbers; is this branch's document
and equipment design the one to keep. Branch protection and the integration pull request remain the
administrator's decision.

## 9. Release recommendation

**NO-GO.** `tools/final_completion.py --release` fails, as it must: requirements unverified, console routes
without journeys, open High defects, open security findings and external blockers.
