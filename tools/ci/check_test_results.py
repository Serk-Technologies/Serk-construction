#!/usr/bin/env python3
"""Decide whether a `go test -json` run is acceptable, from the structured
events rather than from log text.

A run is rejected when:
  * go test itself exited non-zero (--go-exit);
  * any package or test failed, or a package produced no result (build error,
    panic, timeout);
  * any test was skipped (a skipped control is an unverified control), unless
    --allow-skips is given for local use;
  * a test named in the mandatory list is missing or did not pass;
  * fewer top-level tests passed than --min-pass.

It writes a machine-readable summary (--summary) bound to the commit and the
workflow run, which is the evidence a release record should cite.

usage: check_test_results.py RUN.json --mandatory FILE [--go-exit N]
           [--min-pass N] [--summary OUT.json] [--allow-skips]
"""
import argparse
import datetime
import json
import os
import sys


OUTPUT = {}  # (package, test) -> output lines, for printing what a failed test said


def load(path):
    tests, packages, bad_lines = {}, {}, 0
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                bad_lines += 1  # build output from a package that did not compile
                continue
            action, pkg, test = ev.get("Action"), ev.get("Package", ""), ev.get("Test")
            if action == "output" and test is not None:
                OUTPUT.setdefault((pkg, test), []).append(ev.get("Output", "").rstrip("\n"))
            if action not in ("pass", "fail", "skip"):
                if pkg and pkg not in packages:
                    packages[pkg] = None
                continue
            if test is None:
                packages[pkg] = action
            else:
                tests[(pkg, test)] = action
    return tests, packages, bad_lines


def mandatory(path):
    out = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.split("#", 1)[0].strip()
            if line:
                pkg, name = line.split()
                out.append((pkg, name))
    return out


def evaluate(tests, packages, bad_lines, required, go_exit=0, min_pass=0, allow_skips=False):
    problems = []
    top = {k: v for k, v in tests.items() if "/" not in k[1]}
    failed = sorted(k for k, v in tests.items() if v == "fail")
    skipped = sorted(k for k, v in tests.items() if v == "skip")
    passed = [k for k, v in top.items() if v == "pass"]
    if go_exit != 0:
        problems.append(f"go test exited with status {go_exit}")
    if not tests:
        problems.append("the run contains no test results")
    for pkg, res in sorted(packages.items()):
        if res == "fail":
            problems.append(f"package failed: {pkg}")
        elif res is None:
            problems.append(f"package produced no result (build error, panic or timeout): {pkg}")
    for pkg, name in failed:
        problems.append(f"FAILED: {pkg} {name}")
    if not allow_skips:
        for pkg, name in skipped:
            problems.append(f"SKIPPED: {pkg} {name}")
    for suffix, name in required:
        hits = [v for (pkg, t), v in tests.items() if t == name and pkg.endswith("/" + suffix)]
        if not hits:
            problems.append(f"mandatory test did not run: {suffix} {name}")
        elif hits != ["pass"]:
            problems.append(f"mandatory test did not pass ({hits[0]}): {suffix} {name}")
    if len(passed) < min_pass:
        problems.append(f"only {len(passed)} top-level tests passed; at least {min_pass} are expected")
    summary = {
        "verdict": "FAIL" if problems else "PASS",
        "top_level_tests": {"passed": len(passed), "failed": sum(1 for v in top.values() if v == "fail"),
                            "skipped": sum(1 for v in top.values() if v == "skip")},
        "packages": {"passed": sum(1 for v in packages.values() if v == "pass"),
                     "failed": sum(1 for v in packages.values() if v == "fail"),
                     "no_result": sum(1 for v in packages.values() if v is None),
                     "no_tests": sum(1 for v in packages.values() if v == "skip")},
        "mandatory_tests": len(required),
        "non_json_lines": bad_lines,
        "problems": problems,
    }
    return summary


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("run")
    ap.add_argument("--mandatory", required=True)
    ap.add_argument("--go-exit", type=int, default=0)
    ap.add_argument("--min-pass", type=int, default=0)
    ap.add_argument("--summary")
    ap.add_argument("--allow-skips", action="store_true")
    a = ap.parse_args()
    tests, packages, bad = load(a.run)
    s = evaluate(tests, packages, bad, mandatory(a.mandatory), a.go_exit, a.min_pass, a.allow_skips)
    s.update({
        "commit": os.environ.get("GITHUB_SHA", ""),
        "ref": os.environ.get("GITHUB_REF", ""),
        "workflow_run_id": os.environ.get("GITHUB_RUN_ID", ""),
        "workflow_run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT", ""),
        "generated_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    })
    if a.summary:
        with open(a.summary, "w", encoding="utf-8") as fh:
            json.dump(s, fh, indent=2)
            fh.write("\n")
    t = s["top_level_tests"]
    print(f"tests: {t['passed']} passed, {t['failed']} failed, {t['skipped']} skipped; "
          f"mandatory: {s['mandatory_tests']}; verdict: {s['verdict']}")
    for p in s["problems"]:
        print("  " + p)
    for key in sorted(k for k, v in tests.items() if v in ("fail", "skip")):
        print(f"--- {tests[key]}: {key[0]} {key[1]}")
        for line in OUTPUT.get(key, [])[-40:]:
            print("    " + line[:600])
    return 1 if s["problems"] else 0


if __name__ == "__main__":
    sys.exit(main())
