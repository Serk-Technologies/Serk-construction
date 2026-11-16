# Human authorisation record

Technical verification and human authorisation are separate. Nothing in this file has been signed. The engineering agent cannot sign, and has not filled in, any name, date or decision below; an entry is valid only when the named person writes it and commits it (or signs a printed copy referenced here).

Current release decision: **NO-GO** for production and for any live financial operation.

## Decisions (each is separate; none implies the next)

| # | Decision | Evidence it is taken on | Decision | Decided by | Date |
|---|---|---|---|---|---|
| 1 | Ready for controlled technical testing with synthetic data | `20_FINAL_ENGINEERING_REPORT.md`, CI runs of the candidate revision | _not decided_ | | |
| 2 | Ready for professional user acceptance testing | Decision 1, `18_INTERNAL_TESTING_PLAN.md`, UAT environment provisioned | _not decided_ | | |
| 3 | Ready for a limited customer pilot | Decision 2 results, penetration test report, restore drill on the pilot infrastructure | _not decided_ | | |
| 4 | Ready for production operation | Decision 3 results, all sign-offs below | _not decided_ | | |

## Sign-offs

| Authority | Responsible for | Scope reviewed (commit) | Outcome | Name | Signature | Date |
|---|---|---|---|---|---|---|
| Engineering lead | Architecture, implementation, maintainability | | | | | |
| QA lead | Test completeness and reproducibility | | | | | |
| Security reviewer | Security controls; penetration-test findings | | | | | |
| Operations / SRE | Deployment, reliability, monitoring, recovery | | | | | |
| Financial controller | Accounting integrity and reconciliation | | | | | |
| CFO | Financial policies and control acceptance | | | | | |
| Construction professional | Project and site workflows | | | | | |
| Quantity surveying reviewer | Measurement, BOQ, certification, claims | | | | | |
| HSE / quality professional | Safety and quality workflows | | | | | |
| Legal / tax specialist | Statutory and regulatory rules loaded into the system | | | | | |
| Product owner | Scope, usability, business acceptance | | | | | |

## What the system will not do on anyone's behalf

- It never records an approval, signature or stamp that a person did not give. Approvals in the product are attested by the person's own authenticated session; AI components have no approving, posting, paying, releasing or certifying authority.
- No requirement is marked HUMAN_ACCEPTED by tooling. `docs/engineering/requirements.json` has no such entries today.

## Repository governance requested of the administrator (open)

1. Protect `main`: pull request required; at least one approving review by someone other than the author; dismiss stale approvals; required checks `lint`, `supply-chain`, `test`, `image`, `secrets`, `console`; branch up to date before merge; no force push; no deletion; administrators included or bypass restricted as the owner decides.
2. Add `.github/CODEOWNERS` naming real reviewers for `internal/store/postgres/migrations/`, `internal/kernel/`, `pkg/ledger/`, `pkg/auth/`, `pkg/attest/`, `pkg/blob/`, `.github/workflows/`, `tools/ci/`, `.gitleaks*`. The agent has not created it because it does not know who the reviewers are.
3. Bring the validated branch to `main` through a reviewed pull request. `main` is at a revision whose CI fails.
