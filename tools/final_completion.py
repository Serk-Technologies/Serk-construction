#!/usr/bin/env python3
"""Final-completion control centre for SERK Construction OS 3.0 (directive "ENDGAME", sections 3, 5 and 23).

Generates docs/final-completion/ from the canonical sources and checks that the
completion record cannot drift from the code or quietly lose scope.

Canonical sources (one per kind of information; everything else is generated):
  requirements and acceptance criteria   tools/requirements_register.py -> docs/engineering/requirements.json
  frozen original scope                  docs/final-completion/01_BASELINE_REGISTER.json (written once, at dd0e9bc)
  work packages (curated fields)         WORK_PACKAGES below; status is derived from the register, never typed
  defects                                docs/final-completion/06_DEFECT_REGISTER.json (hand-maintained)
  security findings                      docs/release/REMEDIATION_REGISTER.yaml (generated from the audit register)
  external blockers                      docs/final-completion/08_EXTERNAL_BLOCKERS.json (hand-maintained)
  mandatory tests                        tools/ci/mandatory_tests.txt
  release decision                       docs/release/GATE_STATUS.json and docs/engineering/execution-state.json

usage:
  python3 tools/final_completion.py            regenerate the generated files
  python3 tools/final_completion.py --check    integrity check (CI): exits 1 on scope loss, re-meaning,
                                               duplicate IDs, evidence naming a test that does not exist,
                                               a missing mandatory test, or a release claim the register
                                               does not support
  python3 tools/final_completion.py --release  the release-qualification check: additionally fails on any
                                               unverified requirement, uncovered console route, open
                                               critical/high defect or security finding, or unresolved blocker
  python3 tools/final_completion.py --freeze   write the baseline (refuses if it exists)
"""
import argparse
import collections
import json
import pathlib
import re
import sys


ROOT = pathlib.Path(__file__).resolve().parent.parent
FC = ROOT / "docs" / "final-completion"
REG = ROOT / "docs" / "engineering" / "requirements.json"
BASELINE = FC / "01_BASELINE_REGISTER.json"
BASELINE_COMMIT = "dd0e9bc055176e84f6edc2fe741f6c40369f4464"

# A baseline requirement may be reworded only with a recorded reason. The
# check refuses any other change of meaning under an existing ID.
REWORDED = {}

# Curated work-package fields. Requirements are assigned to a package by
# domain prefix unless listed explicitly; every open requirement must land in
# exactly one package (checked). Status is derived from the register.
WORK_PACKAGES = [
    {"id": "WP-000", "title": "Scope, gap register and completion validator", "domains": [],
     "requirement_ids": [], "dependencies": [], "next_action": "Keep the validator in CI; regenerate after every slice."},
    {"id": "WP-001", "title": "Form request and state subsystem; deterministic race regressions for every dependent form",
     "domains": ["XCT-FORM"], "dependencies": [], "next_action": "Form-state module in the console; one barrier-driven regression per form."},
    {"id": "WP-002", "title": "RFC 9457 problem responses and cursor pagination", "domains": ["XCT-API"], "dependencies": [],
     "next_action": "problem+json on every error; cursor on the document lists."},
    {"id": "WP-003", "title": "Fleet: fuel variance investigation and tank dips", "domains": ["FLT-02"], "dependencies": [],
     "next_action": "Tank dips reconciled to the stock ledger; recorded, approved investigation closure."},
    {"id": "WP-004", "title": "Fleet: maintenance labour, workshop cost centres, calendar", "domains": ["FLT-03"], "dependencies": [],
     "next_action": "Labour lines on work orders priced by an approved rate policy; workshop cost centres."},
    {"id": "WP-005", "title": "Fleet: costing, charge-out and fixed assets", "domains": ["FLT-01", "FLT-04"], "dependencies": ["WP-003", "WP-004"],
     "next_action": "Approved charge-out policy (synthetic in test); fixed-asset register through the ledger."},
    {"id": "WP-006", "title": "AURA: themes, sign-in, workspace, shell, responsive and visual review", "domains": ["XCT-UI"], "dependencies": ["WP-001"],
     "next_action": "Warm and high-contrast token sets; sign-in page; personal workspace with saved preferences."},
    {"id": "WP-007", "title": "Command: hierarchy, portfolio, programs, strategy, cash and digests", "domains": ["CMD"], "dependencies": ["WP-012"],
     "next_action": "13-week cash projection with payroll, tax and supplier schedules; messaging adapter with a local test provider."},
    {"id": "WP-008", "title": "Estimate: CRM, feasibility, land, tender, take-off, rates and bids", "domains": ["EST"], "dependencies": ["WP-011"],
     "next_action": "Effective-dated resource and rate library (no invented rates); rate build-up."},
    {"id": "WP-009", "title": "Plan & build: risk, resources, look-ahead, work packages, measurement, engineering", "domains": ["PLB"], "dependencies": [],
     "next_action": "Work packages released against drawings, permits, ITP and predecessors."},
    {"id": "WP-010", "title": "Design: IFC, digital twin records, GIS", "domains": ["DSN"], "dependencies": ["WP-009"],
     "next_action": "Library and licence review for an isolated IFC parser."},
    {"id": "WP-011", "title": "Supply & stock remainder", "domains": ["SUP"], "dependencies": [],
     "next_action": "Returns, scrap and quarantine; bins, batches and serials on the stock ledger."},
    {"id": "WP-012", "title": "People: records, attendance, payroll engine, productivity, competency", "domains": ["PPL"], "dependencies": [],
     "next_action": "Employee records with protected personal data; attendance with overlap detection."},
    {"id": "WP-013", "title": "Contract: revenue-recognition states and remainder", "domains": ["CON"], "dependencies": [],
     "next_action": "Contract-specific revenue policy with independent review."},
    {"id": "WP-014", "title": "Capital: statements, bank adapter, tax engine, consolidation, forecasts, audit anchor", "domains": ["CAP"],
     "dependencies": ["WP-012"], "next_action": "Financial statements from the ledger with drill-down."},
    {"id": "WP-015", "title": "Assure: quality, laboratory, HSE, sustainability", "domains": ["ASR"], "dependencies": ["WP-009"],
     "next_action": "ITP with hold/witness points and NCR closed by an independent person."},
    {"id": "WP-016", "title": "Connect: CDE remainder, submittals, correspondence, tasks, meetings, portals", "domains": ["CNX"], "dependencies": [],
     "next_action": "Submittals with review cycles; correspondence register."},
    {"id": "WP-017", "title": "Estate: units, sales, facilities, commissioning, handover", "domains": ["RES"], "dependencies": [],
     "next_action": "Unit inventory with atomic reservation."},
    {"id": "WP-018", "title": "Brain & Sentinel: knowledge, model gateway, security assurance, administration", "domains": ["BRS"], "dependencies": [],
     "next_action": "Provider-neutral gateway with a local test provider and an evaluation set."},
    {"id": "WP-019", "title": "Offline field application", "domains": ["XCT-OFFLINE"], "dependencies": [],
     "next_action": "Architecture decision for the native client; record what can be verified without devices."},
    {"id": "WP-020", "title": "Scale, archive and partitioning", "domains": ["XCT-SCALE"], "dependencies": [],
     "next_action": "Reproducible load harness and a measured run on recorded hardware."},
    {"id": "WP-021", "title": "Recovery and operated environments", "domains": ["XCT-DR"], "dependencies": [],
     "next_action": "Re-run the encrypted, signed offsite PITR drill at the current commit."},
    {"id": "WP-022", "title": "Localisation: Ethiopian calendar, translations, statutory", "domains": ["XCT-LOC"], "dependencies": [],
     "next_action": "Ethiopian calendar conversion with round-trip tests."},
    {"id": "WP-023", "title": "Repository governance: protected main, reviewed integration", "domains": [], "dependencies": [],
     "external_dependencies": ["Repository administrator"],
     "next_action": "Owner decision: branch protection on main and authority to open the integration pull request."},
]


def load_security():
    """The remediation register is generated YAML with one 'key: "value"' per line; read it without a YAML library."""
    items, cur = [], None
    for line in (ROOT / "docs" / "release" / "REMEDIATION_REGISTER.yaml").read_text().splitlines():
        m = re.match(r'^\s*(- )?(\w+): "?(.*?)"?$', line)
        if not m:
            continue
        if m.group(1) and m.group(2) == "id":
            cur = {}
            items.append(cur)
        if cur is not None:
            cur[m.group(2)] = m.group(3)
    return items


def load_register():
    reg = json.loads(REG.read_text())
    groups = reg["domains"] + reg["cross_cutting"]
    return reg, groups


def flat(groups):
    for g in groups:
        for q in g["requirements"]:
            yield g, q


def test_functions():
    names = set()
    for p in ROOT.rglob("*_test.go"):
        if "node_modules" in p.parts:
            continue
        names.update(re.findall(r"^func (Test\w+)\(", p.read_text(errors="replace"), re.M))
    return names


def mandatory_tests():
    out = []
    for line in (ROOT / "tools" / "ci" / "mandatory_tests.txt").read_text().splitlines():
        line = line.split("#", 1)[0].strip()
        if line:
            out.append(line.split()[-1])
    return out


def console_routes():
    js = (ROOT / "internal" / "webui" / "static" / "app.js").read_text()
    nav = js[js.index("const NAV = ["):js.index("const NAV_KEY")]
    return re.findall(r"\['(#/[^']+)', '([^']+)'", nav)


def route_coverage(routes):
    journeys = {p.name: p.read_text() for p in (ROOT / "e2e").glob("*.mjs")}
    out = []
    for href, label in routes:
        key = href[1:]  # "/list/rfi"
        hits = sorted(n for n, t in journeys.items() if key in t or href in t)
        out.append({"route": href, "label": label, "browser_journeys": hits,
                    "status": "COVERED_BY_BROWSER_JOURNEY" if hits else "NOT_COVERED"})
    return out


def wp_for(req_id, wps):
    dom = req_id.rsplit(".", 1)[0]          # FLT-02.6 -> FLT-02 ; XCT-API.2 -> XCT-API
    prefix = dom.split("-")[0]               # FLT
    for w in wps:
        if req_id in w.get("requirement_ids", []):
            return w["id"]
    for w in wps:
        if dom in w["domains"]:
            return w["id"]
    for w in wps:
        if prefix in w["domains"]:
            return w["id"]
    return None


def derive_status(statuses):
    s = set(statuses)
    if not s:
        return "NOT_APPLICABLE"
    if s == {"VERIFIED"}:
        return "VERIFIED"
    if s <= {"NOT_STARTED"}:
        return "NOT_STARTED"
    if s <= {"BLOCKED", "VERIFIED"}:
        return "BLOCKED"
    return "IN_PROGRESS"


def build(reg, groups):
    wps = json.loads(json.dumps(WORK_PACKAGES))
    for w in wps:
        w.setdefault("external_dependencies", [])
        w["requirement_ids"] = list(w.get("requirement_ids", []))
    assigned = collections.defaultdict(list)
    for g, q in flat(groups):
        wid = wp_for(q["id"], wps)
        assigned[wid].append(q)
    out_wps = []
    for w in wps:
        qs = assigned.get(w["id"], [])
        crit = [k for q in qs for k in q.get("criteria", [])]
        out_wps.append({
            "id": w["id"], "title": w["title"], "domains": w["domains"],
            "requirement_ids": [q["id"] for q in qs],
            "open_requirement_ids": [q["id"] for q in qs if q["status"] != "VERIFIED"],
            "dependencies": w["dependencies"],
            "acceptance_criteria": [{"id": q["id"], "text": q["text"], "status": q["status"]} for q in qs if q["status"] != "VERIFIED"]
            + [{"id": k["id"], "text": k["text"], "status": k["status"]} for k in crit if k["status"] != "VERIFIED"],
            "tests": sorted({t for q in qs for t in q["evidence_tests"]} | {t for k in crit for t in k["evidence_tests"]}),
            "external_dependencies": w["external_dependencies"],
            "technical_status": derive_status([q["status"] for q in qs]) if qs else "NOT_APPLICABLE",
            "human_acceptance": "PENDING",
            "next_action": w["next_action"],
        })
    return out_wps, assigned


def check(reg, groups, release=False):
    errors, warnings = [], []
    if not BASELINE.exists():
        errors.append(f"baseline {BASELINE.relative_to(ROOT)} is missing")
        base = []
    else:
        base = json.loads(BASELINE.read_text())["requirements"]
    current = {}
    dup = []
    for g, q in flat(groups):
        for x in [q] + q.get("criteria", []):
            if x["id"] in current:
                dup.append(x["id"])
            current[x["id"]] = x
    if dup:
        errors.append("duplicate requirement IDs: " + ", ".join(dup))
    for b in base:
        cur = current.get(b["id"])
        if cur is None:
            errors.append(f"baseline requirement {b['id']} has disappeared")
        elif cur["text"] != b["text"] and b["id"] not in REWORDED:
            errors.append(f"baseline requirement {b['id']} changed meaning without a recorded reason "
                          f"(was {b['text']!r}, now {cur['text']!r})")
    tests = test_functions()
    for rid, x in current.items():
        for t in x["evidence_tests"]:
            if t not in tests:
                errors.append(f"{rid} names evidence test {t}, which does not exist in the repository")
        if x["status"] == "VERIFIED" and not x["evidence_tests"] and not x.get("evidence_file"):
            errors.append(f"{rid} is VERIFIED without evidence")
    for t in mandatory_tests():
        if t not in tests:
            errors.append(f"mandatory test {t} does not exist")
    rf = reg.get("results_file", "")
    if not rf or not (ROOT / rf).is_file():
        errors.append(f"register results file {rf!r} does not exist")
    wps, assigned = build(reg, groups)
    if None in assigned:
        errors.append("requirements in no work package: " + ", ".join(q["id"] for q in assigned[None]))
    gate = json.loads((ROOT / "docs" / "release" / "GATE_STATUS.json").read_text())
    state = json.loads((ROOT / "docs" / "engineering" / "execution-state.json").read_text())
    unverified = [rid for rid, x in current.items() if x["status"] != "VERIFIED"]
    defects = json.loads((FC / "06_DEFECT_REGISTER.json").read_text())["defects"] if (FC / "06_DEFECT_REGISTER.json").exists() else []
    open_crit = [d["id"] for d in defects if d["status"] != "CLOSED" and d["severity"] in ("Critical", "High")]
    for name, decision in (("GATE_STATUS.json", gate.get("release_decision")), ("execution-state.json", state.get("release_decision"))):
        if decision != "NO-GO" and (unverified or open_crit):
            errors.append(f"{name} claims {decision!r} while {len(unverified)} requirements are unverified and "
                          f"{len(open_crit)} critical/high defects are open")
    routes = route_coverage(console_routes())
    uncovered = [r["route"] for r in routes if r["status"] != "COVERED_BY_BROWSER_JOURNEY"]
    blockers = json.loads((FC / "08_EXTERNAL_BLOCKERS.json").read_text())["blockers"] if (FC / "08_EXTERNAL_BLOCKERS.json").exists() else []
    sec = load_security()
    open_sec = [i["id"] for i in sec if not i.get("register_status", "").startswith(("CLOSED", "PASS", "ACCEPTED")) and str(i.get("severity", "")).split()[0] in ("Critical", "High")]
    msg = (f"{len(unverified)} unverified requirements/criteria, {len(uncovered)} console routes without a browser journey, "
           f"{len(open_crit)} open critical/high defects, {len(open_sec)} open critical/high security findings, "
           f"{len([b for b in blockers if b['status'] != 'RESOLVED'])} unresolved external blockers")
    (errors if release else warnings).append("release qualification: " + msg)
    return errors, warnings


def write(reg, groups):
    FC.mkdir(parents=True, exist_ok=True)
    wps, assigned = build(reg, groups)
    (FC / "05_ENGINEERING_BACKLOG.json").write_text(json.dumps(
        {"generated_by": "tools/final_completion.py", "register_commit": reg.get("commit", ""),
         "rule": "technical_status is derived from the requirement register; human_acceptance is PENDING until a named person accepts",
         "work_packages": wps}, indent=1, ensure_ascii=False) + "\n")
    reqs = []
    for g, q in flat(groups):
        reqs.append({"id": q["id"], "domain": g["id"], "product": g["product"], "text": q["text"], "status": q["status"],
                     "evidence_tests": q["evidence_tests"], "criteria": q.get("criteria", []),
                     "work_package": wp_for(q["id"], WORK_PACKAGES), "human_acceptance": "PENDING"})
    (FC / "02_MASTER_REQUIREMENTS.json").write_text(json.dumps(
        {"generated_by": "tools/final_completion.py from docs/engineering/requirements.json",
         "results_file": reg.get("results_file", ""), "register_commit": reg.get("commit", ""),
         "requirement_counts": reg["requirement_counts"], "criteria_counts": reg.get("criteria_counts", {}),
         "requirements": reqs}, indent=1, ensure_ascii=False) + "\n")
    routes = route_coverage(console_routes())
    (FC / "10_UI_COVERAGE_MATRIX.json").write_text(json.dumps(
        {"generated_by": "tools/final_completion.py from the console navigation (app.js NAV) and e2e/*.mjs",
         "rule": "A route counts as covered only when a browser journey visits it; responsive and visual evidence are listed separately",
         "routes": routes}, indent=1) + "\n")
    ev = sorted(p.name for p in (ROOT / "docs" / "engineering" / "evidence").iterdir())
    (FC / "09_TEST_EVIDENCE_INDEX.json").write_text(json.dumps(
        {"generated_by": "tools/final_completion.py", "directory": "docs/engineering/evidence",
         "register_results_file": reg.get("results_file", ""), "files": ev}, indent=1) + "\n")
    sec = load_security()
    (FC / "07_SECURITY_REGISTER.json").write_text(json.dumps(
        {"generated_by": "tools/final_completion.py from docs/release/REMEDIATION_REGISTER.yaml (canonical)",
         "open": [{k: i.get(k) for k in ("id", "severity", "register_status", "finding", "residual_risk")}
                  for i in sec if not i.get("register_status", "").startswith(("CLOSED", "PASS", "ACCEPTED"))]}, indent=1, ensure_ascii=False) + "\n")
    # Domain completion matrix
    L = ["# Domain completion matrix", "",
         "Generated by `tools/final_completion.py` from `docs/engineering/requirements.json`; do not edit by hand.", "",
         f"Register evidence: `{reg.get('results_file', '')}` at `{reg.get('commit', '')}`.", "",
         "Requirement counts: " + ", ".join(f"{k} {v}" for k, v in sorted(reg["requirement_counts"].items())) + ".", "",
         "Acceptance criteria (children of large requirements): "
         + (", ".join(f"{k} {v}" for k, v in sorted(reg.get("criteria_counts", {}).items())) or "none") + ".", "",
         "A domain is never shown as accepted: human acceptance is pending for every domain.", "",
         "| Domain | Name | Status | Verified / listed | Open | Work package |", "|---|---|---|---|---|---|"]
    for g in groups:
        qs = g["requirements"]
        v = sum(1 for q in qs if q["status"] == "VERIFIED")
        open_ = ", ".join(q["id"] for q in qs if q["status"] != "VERIFIED") or "—"
        L.append(f"| {g['id']} | {g['domain']} | {g['status']} | {v}/{len(qs)} | {open_} | {wp_for(qs[0]['id'], WORK_PACKAGES) if qs else ''} |")
    (FC / "03_DOMAIN_COMPLETION_MATRIX.md").write_text("\n".join(L) + "\n")
    # Dependency graph
    G = ["# Work-package dependency graph", "", "Generated by `tools/final_completion.py`.", "", "```mermaid", "graph LR"]
    for w in wps:
        G.append(f'  {w["id"].replace("-", "")}["{w["id"]} {w["title"][:48]}<br/>{w["technical_status"]}"]')
        for d in w["dependencies"]:
            G.append(f'  {d.replace("-", "")} --> {w["id"].replace("-", "")}')
    G += ["```", "", "Critical path (longest dependency chain): " + " → ".join(critical_path(wps)), ""]
    (FC / "04_DEPENDENCY_GRAPH.md").write_text("\n".join(G))


def critical_path(wps):
    by = {w["id"]: w for w in wps}
    memo = {}

    def depth(i):
        if i not in memo:
            ds = by[i]["dependencies"]
            memo[i] = (1 + max(depth(d)[0] for d in ds), [*max((depth(d) for d in ds), key=lambda x: x[0])[1], i]) if ds else (1, [i])
        return memo[i]
    return max((depth(w["id"]) for w in wps), key=lambda x: x[0])[1]


def freeze(reg, groups):
    if BASELINE.exists():
        sys.exit(f"{BASELINE.relative_to(ROOT)} exists and is never rewritten")
    FC.mkdir(parents=True, exist_ok=True)
    reqs = [{"id": q["id"], "domain": g["id"], "text": q["text"], "status_at_baseline": q["status"],
             "evidence_tests": q["evidence_tests"]} for g, q in flat(groups)]
    BASELINE.write_text(json.dumps(
        {"frozen_at_commit": BASELINE_COMMIT, "frozen_on": "2026-11-15",
         "rule": "Never edited. tools/final_completion.py --check fails if any of these IDs disappears or changes meaning.",
         "domains": 80, "requirement_count": len(reqs),
         "status_counts": dict(collections.Counter(q["status_at_baseline"] for q in reqs)),
         "requirements": reqs}, indent=1, ensure_ascii=False) + "\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--release", action="store_true")
    ap.add_argument("--freeze", action="store_true")
    a = ap.parse_args()
    reg, groups = load_register()
    if a.freeze:
        freeze(reg, groups)
        return
    if a.check or a.release:
        errors, warnings = check(reg, groups, release=a.release)
        for w in warnings:
            print("note:", w)
        for e in errors:
            print("FAIL:", e)
        sys.exit(1 if errors else 0)
    write(reg, groups)


if __name__ == "__main__":
    main()
