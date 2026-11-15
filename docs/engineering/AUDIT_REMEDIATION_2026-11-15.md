# Audit remediation report, 2026-11-15

Response to the independent inspection of GitHub and the remediation directive. Everything below was read from GitHub Actions or executed in the build environment; nothing is inferred. Release decision: **NO-GO** (unchanged).

## 1. What the audit found, and what is true now

| Audit finding | Verified | State now |
|---|---|---|
| Runs 17, 18, 19 failed | Yes. Run 20 (the merge commit on `main`) failed for the same reason | Fixed on the development branch from `be030f7`. **`main` still holds the failing revision** (see 4) |
| `TestDocSecurity_RealScannerAndObjectStore` failed at `docstore_test.go:545`, "blob: object not found" | Yes | Root cause fixed (see 2.3) |
| Commit `0636d1b` attempts to fix the missing bucket | Yes; run 21 passed on it, but it created the bucket as a directory, unauthenticated, and its green hid seven skipped browser tests | Superseded by authenticated provisioning with proof of privacy |
| PR #3 merged while CI was failing | Yes: merge commit `1096bf2` on `main`, parent `2294722` (run 18/19 failed), run 20 failed. The agent did not open or merge it | Needs a human decision (see 4) |
| Main branch protection disabled | Consistent with the merge; the agent has no administrative access to read or change it | **Blocked on a repository administrator** (see 4) |
| Seven Playwright journeys skipped | Yes, in every GitHub run before run 22: the runner had no Playwright, and the tests skip without it | Playwright 1.63.0 and Chromium are installed in CI; the seven journeys are mandatory and passed in run 22 |
| Release documents inconsistent with CI evidence | Yes: `GO_NO_GO.md` said the automated suite passed "on every work package"; that was true only of local runs | Reconciled (see 5) |

## 2. Root causes

### 2.1 A failing test run was reported green (all runs up to 16)

The test step was `go test ... | tee log`. GitHub runs steps with `bash -e` and no `pipefail`, so the step took `tee`'s exit status. Run 16 was green in that step while 8 tests had failed. The same line was in the workflow before this branch, so **no green `test` job before run 17 is evidence**, including run 14, which an earlier handover reported as "test passed".

Fix: no pipe. `go test -json` writes to a file; `tools/ci/check_test_results.py` receives go test's exit status and decides from the structured events. The gate has 11 tests of its own (run in the `lint` job).

### 2.2 Eight tests failed only on the runner (relay x4, database posture x4)

The tests created database logins without a password. The local cluster trusted local connections; the runner's PostgreSQL authenticates TCP logins with SCRAM. Not a race and not timing.

Fix: the health and relay test logins get a password generated for the run. Verified by running the whole suite locally against password-authenticated TCP, and on GitHub (run 17 onward: relay passes; the posture tests pass from run 21).

### 2.3 Missing bucket (runs 17 to 20)

`internal/kernel` used the bucket `serk-test-…`, which only a test in `pkg/blob` created. Go runs packages in path order, `internal/kernel` first. Locally the bucket survived from earlier runs; on a fresh runner it did not exist. This is an execution-order dependency between packages plus state leaking between runs. Two further defects hid it: the S3 client folded "bucket missing" into "object not found", and nothing checked the bucket before use.

Fixes:
- `pkg/blob`: `ErrBucketNotFound`, `ErrAccessDenied`, `ErrUnavailable` are distinct from `ErrNotFound`; transport failures are `ErrUnavailable`. `CheckBucket`, `CreateBucket` (provisioning only), `VerifyPrivate`.
- `tools/ci/s3provision`: authenticates, creates the bucket if absent, writes and reads back a probe, and proves anonymous list/read and a wrong secret are refused. CI runs it before the tests.
- Each test owns its bucket; the kernel test checks the provisioned bucket and asserts the missing-bucket class end to end.
- The API refuses to start without a usable bucket, and answers storage faults as HTTP 503 `storage_unavailable` without internal detail, never 404.

### 2.4 Skipped browser journeys

The seven journeys skip when Playwright is absent, and nothing rejected skips. Fix: CI installs Playwright 1.63.0 with its Chromium; the gate rejects **any** skipped test and requires the 23 tests in `tools/ci/mandatory_tests.txt` by name.

### 2.5 What was looked for in `internal/kernel` and not found

Race detector on every run; three consecutive full runs in random order (`-shuffle=on`, different seeds) on the CI Go version; password-authenticated TCP as on the runner. No data race, timing assumption or order dependency inside the package appeared. The one cross-package order dependency is 2.3. No artificial delay, retry, skip or weakened assertion was added anywhere.

## 3. Evidence

| Check | Result | Where |
|---|---|---|
| Local, 3 runs, `-race -shuffle=on`, Go 1.26.11, SCRAM TCP, real ClamAV daemon and MinIO, Playwright | 457 passed, 0 failed, 0 skipped, each run | `evidence/2026-11-15-stability-3x-shuffle.json`, `evidence/2026-11-15-stabilization.tests.txt` |
| GitHub run 22 (`be030f7`, id 43712828637) | All six jobs passed. Gate: 457 passed, 0 failed, 0 skipped, 23 mandatory | Actions; artifact `test-logs` holds `test-run.json` and `test-summary.json` (commit and run id inside) |
| Bucket provisioning on the runner | created; authenticated write and read ok; anonymous refused; wrong secret refused | run 22 log |
| ClamAV on the runner | engine 1.4.6; daemon loaded daily database 28177 (8 Nov 2026); database on disk 28184, built 15 Nov 2026, 355,871 daily signatures | run 22 log, artifact `clamav-version.txt` |
| Vulnerability scan (reachable), go.sum against the public checksum database, SBOM, licence review, repository secret scan, gitleaks over history, image build and image vulnerability scan | passed in run 22; none was relaxed | Actions |

GitHub runs on this branch and `main`:

| Run | Commit | Result | Note |
|---|---|---|---|
| 14 | `12c3cec` | failed | image, supply-chain; `test` green is not evidence (2.1) |
| 15 | `1ae8f5b` | failed | MinIO image not pullable; secret-scan findings |
| 16 | `55bc7fd` | failed | 8 tests failed behind a green step |
| 17 | `81a8ec8` | failed | first honest test job: missing bucket |
| 18, 19 | `2294722` | failed | missing bucket (push and pull-request events) |
| 20 | `1096bf2` (`main`, merge of PR #3) | failed | missing bucket |
| 21 | `0636d1b` | passed | **not acceptable evidence**: 7 browser journeys skipped, log-grep check |
| 22 | `be030f7` | **passed** | new gate, no skips, journeys ran |

The three consecutive runs of the release-candidate revision are recorded in `execution-state.json` (`ci.release_candidate`) once they have run; this file is part of that revision and cannot contain its own run numbers.

## 4. Blocked on people (the agent cannot do these and has not tried to go around them)

1. **Branch protection on `main`.** Needs a repository administrator. Recommended settings: require a pull request with at least one approving review from someone other than the author; dismiss stale approvals on new commits; require status checks `lint`, `supply-chain`, `test`, `image`, `secrets`, `console` and require the branch to be up to date; include administrators; block force pushes and deletions; require linear history or signed commits by policy. Until this is on, a failing revision can be merged again.
2. **`main` is at a failing revision** (`1096bf2`). The fix exists only on `claude/new-session-xkd5g3`. The agent is not authorised to merge to a protected branch or to open a pull request without being asked. A human should open a pull request from the branch after the release-candidate runs, review it, and merge it only when its checks are green.
3. Independent review of this report and of the role matrix, accounting, tax and payroll rules; penetration test; release sign-off.

## 5. Documents reconciled

`HANDOVER.md`, `RELEASE_GATES.md`, `docs/release/GO_NO_GO.md` and `execution-state.json` now state: which GitHub runs passed and failed, that green test jobs before run 17 are not evidence, that local evidence is local, the merge of a failing revision, and the unprotected `main`. Statements of the form "passes on every work package" now say where the run happened.

## 6. P1 and P2 status (directive items 9 to 17)

| Item | State |
|---|---|
| 9 Versioned document keys, rotation, recovery, custody | **Not built.** One master key, no rotation. Next engineering item; needs an ADR (per-object data keys wrapped by a versioned master, so rotation re-wraps keys and never rewrites objects) |
| 10 ClamAV with a current production signature database | **Partly verified**: the CI daemon runs the public database (above) and the integration tests pass against it. Detection coverage beyond the standard test file is not measured; update cadence and alerting on stale signatures are not built |
| 11 Real OIDC identity, MFA step-up, session revocation | Token verification, step-up enforcement and server-side session revocation exist and are tested with locally signed tokens. **No real identity provider has been connected or tested**; blocked on an authorised tenant |
| 12 KMS/HSM custody for approval-signing keys | **Not built**; the key is an environment secret. Needs a provider decision and credentials |
| 13 Concurrency, load, soak, fault injection, disaster recovery | Concurrency tests and one recovery drill (PASS, 54 migrations) exist; one query measurement at 100,000 machines. **No load, soak or fault-injection run** |
| 14 Readiness for a human penetration test | Threat model and role matrix exist. No test environment has been provisioned for testers; no scope document |
| 15 Commit-bound machine-generated evidence | Started: every CI run uploads `test-summary.json` with commit, run id, counts and problems. Release documents are still partly hand-written |
| 16 Consolidated audit report | This file |
| 17 Reconcile release documents | Done (5) |

## 7. Remaining risks

- `main` is unprotected and at a failing revision.
- Document encryption has a single key with no rotation or documented custody; losing it loses every stored file.
- Malware detection coverage is unmeasured beyond the standard test file.
- No real identity provider, no KMS/HSM, no load or soak evidence, no penetration test.
- CI uses the runner's preinstalled Node.js (22.x, printed in the log) rather than a pinned installer action; Playwright itself is pinned. MinIO is built from a pinned commit; the ClamAV image is pinned by tag, not digest.
- Earlier handovers reported local results as if they were CI results; those statements are corrected, but any decision made on them should be revisited.

## 8. Release readiness

**NO-GO.** P0 is met on the development branch except for the items only an administrator can do (4.1, 4.2). Feature work (fleet, payroll, AI, BIM) stays suspended until those are done. Production deployment and live financial operation remain prohibited pending independent human authorisation.
