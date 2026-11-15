#!/usr/bin/env python3
"""Turn `go test -json` output into the evidence format the requirement
register reads: one "PASS|FAIL|SKIP <Test> <package>" line per top-level test.

    python3 tools/test_results.py run.json docs/engineering/evidence/<date>-<name>.tests.txt
"""
import collections
import json
import sys

res = {}
for line in open(sys.argv[1]):
    try:
        e = json.loads(line)
    except ValueError:
        continue
    if e.get("Action") in ("pass", "fail", "skip") and "Test" in e and "/" not in e["Test"]:
        res[(e["Package"], e["Test"])] = e["Action"]
with open(sys.argv[2], "w") as f:
    for (pkg, test), a in sorted(res.items()):
        f.write(f"{a.upper()} {test} {pkg.split('serk-construction/')[-1]}\n")
print(dict(collections.Counter(res.values())))
