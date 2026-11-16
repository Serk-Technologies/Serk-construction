# Governance and integration review package

For the repository owner. The engineering agent has no administrative access and no authorisation to open or merge a pull request; nothing below has been done on your behalf.

## State (read from the repository on 2026-11-15)

| | |
|---|---|
| `main` | `1096bf2`: the merge of PR #3. Its CI run (20) failed. Reported unprotected by the independent inspection; the agent cannot read protection settings |
| Development branch | `claude/new-session-xkd5g3`, 11 commits ahead of `main` at the time of writing, plus the commit containing this file |
| Migrations the branch adds over `main` | 0055, 0056 and 0057 (additive; none edits an earlier migration). 0052 to 0054 are already on `main` through PR #3, which was merged without review while its checks were failing |
| CI on the branch | Runs 26 to 32 passed all six jobs (lint, supply-chain, test, image, secrets, console). The run for the commit containing this file is recorded in `docs/engineering/execution-state.json` |

## What to do, in order

1. **Protect `main`** (Settings → Branches → Add rule, or a ruleset):
   - Require a pull request before merging; at least 1 approval; dismiss stale approvals when new commits are pushed; require approval of the most recent push by someone other than its author.
   - Require status checks to pass, and the branch to be up to date: `lint`, `supply-chain`, `test`, `image`, `secrets`, `console`.
   - Block force pushes and deletions. Apply to administrators, or restrict bypass to a named break-glass account, as you decide.
2. **Add `.github/CODEOWNERS`** naming real reviewers (the agent does not know who they are), at least for:
   ```
   /internal/store/postgres/migrations/   <database reviewer>
   /internal/kernel/                      <engineering lead>
   /pkg/ledger/ /pkg/money/ /pkg/tax/     <financial systems reviewer>
   /pkg/auth/ /pkg/attest/ /pkg/blob/     <security reviewer>
   /.github/ /tools/ci/ /.gitleaks*       <security reviewer>
   ```
3. **Open a pull request** from `claude/new-session-xkd5g3` to `main` (or tell the agent to open it). Review in this order: `docs/engineering/AUDIT_REMEDIATION_2026-11-15.md`; migrations 0055 to 0057 with ADRs 0022 to 0024, and, because they reached `main` unreviewed, 0052 to 0054 with ADRs 0018 to 0021; `.github/workflows/ci.yml` and `tools/ci/`; `.gitleaksignore` (one accepted fingerprint, explained in the audit report, section 9).
4. **Merge only when** the pull request's six checks are green on its head commit and a reviewer other than the author has approved. Do not merge on a green run of an earlier commit.
5. **Before any release tag**: three consecutive green runs of the exact candidate commit (the workflow has a manual trigger for re-running a revision), and the decisions in `19_HUMAN_AUTHORIZATION.md`.

## What the reviewer should know is NOT in the branch

No real identity provider, KMS/HSM, bank, messaging or AI integration; no load or soak evidence; no complete-system restore drill; 40 of 80 domains not started. See `21_AURA_CYCLE_REPORT.md`.
