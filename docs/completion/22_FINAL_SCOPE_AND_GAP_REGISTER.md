# 22 — Final scope and gap register

Discovery for the final-completion directive, performed 2026-11-15 on `dd0e9bc` (run 37 green).
Machine-readable equivalent: `22_FINAL_SCOPE_AND_GAP_REGISTER.json`. Requirement-level gaps are generated in
`docs/final-completion/02_MASTER_REQUIREMENTS.json` and `03_DOMAIN_COMPLETION_MATRIX.md`; this page records what
the register alone does not show.

## Repository state

| Ref | Commit | Note |
|---|---|---|
| `claude/new-session-xkd5g3` | `dd0e9bc` | candidate line; 57 migrations |
| `main` | `1096bf2` | unprotected; merge of PR #3 |
| `new-version` | `7ac2a6c` | two commits after PR #2, never merged; migrations up to 0067 |

No pull request is open. PRs #1–#3 are merged.

## Findings

| ID | Class | Area | Finding | Decision |
|---|---|---|---|---|
| GAP-01 | acceptance-critical | Repository | Branch new-version holds two unmerged commits (6cbcdac, 7ac2a6c, 2026-11-09, by the repository owner): migrations 0052-0067, about 21,000 lines, covering cost forecasting with CPI, IFRS 15 revenue, change/variation links, subcontracts and their payments, fixed assets, period end, cash plan, role adjustments, payroll, consolidation, tax, workforce and HSE, quality and site, documents, credit rules. | Integrate on this branch (ADR 0025): carry the non-colliding work with renumbered migrations and both lines' tests; rebuild the colliding parts (transmittals, submittals, timesheets, HSE, internal hire) on this branch's document and equipment tables. |
| GAP-02 | acceptance-critical | Governance | main is unprotected, at 1096bf2, merged while CI was failing. | Owner/administrator decision (GOV-01). |
| GAP-03 | acceptance-critical | Console | Form state lives in the DOM; eight of eleven reloading forms have no own race regression. | WP-001. |
| GAP-04 | acceptance-critical | API | Errors are {error:{code,message}}, not RFC 9457; lists are bounded by LIMIT, cursors only on the equipment register. | WP-002. |
| GAP-05 | acceptance-critical | Requirements | 95 of 196 baseline requirements are not verified (76 not started, 9 implementing, 9 blocked, 1 implemented-unverified). | Work packages WP-003..WP-022; large requirements decomposed into acceptance criteria. |
| GAP-06 | acceptance-critical | Console coverage | Many navigation routes are visited by no browser journey (see 10_UI_COVERAGE_MATRIX.json). | Extend journeys per screen family under WP-006. |
| GAP-07 | informational | Code markers | No TODO, FIXME, HACK or not-implemented markers in production code; matches are test fixture strings only. | None. |
| GAP-08 | informational | Tests | Integration tests skip when SERK_TEST_ADMIN_DSN or browser variables are absent (internal/testdb, console_browser_*). | Keep the gate's zero-skip rule. |
| GAP-09 | corrected | Documentation | execution-state.json named fuel as the next milestone and said AURA workstream B had not started; run 37 was not recorded in its CI history. | Corrected; historical entries labelled. |
| GAP-10 | hygiene | Repository | new-version commits a 15 MB built binary (serk-seed) at the repository root. | Not carried into the integration. |

## Scope accounting

- Baseline: 196 requirements across 80 domains, frozen in `docs/final-completion/01_BASELINE_REGISTER.json`.
- Added (appended, no ID moved): `XCT-FORM` (form reliability, directive section 7) and `XCT-UI` (AURA, section 9).
- Large requirements named in sections 11–13 are decomposed into acceptance criteria (`<id>.a`, `.b`, …); a parent cannot be VERIFIED until every criterion is.
- Counts are generated; read them from `docs/engineering/REQUIREMENTS.md`, never from this page.
