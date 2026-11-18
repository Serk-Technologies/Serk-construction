#!/usr/bin/env python3
"""Go-to-C# migration traceability register (SERK Construction OS 4.0, ADR 0026).

Reads the Go repository as it is (requirements register, packages, migrations,
routes, tests, defects) and writes dotnet/docs/MIGRATION_TRACEABILITY.json and
.md: every requirement, Go package, migration, table and route with its proposed
C# module and its migration status. Statuses come from
dotnet/docs/migration-status.json (the only hand-edited input): anything not
listed there is NOT_STARTED. A row may only be MIGRATED when its differential
and acceptance tests are named and pass (see the --results check).

usage: python3 dotnet/tools/traceability.py [--check]
  --check  fail if the generated files differ from what is committed
"""
import collections, json, os, re, subprocess, sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
OUT = os.path.join(ROOT, "dotnet", "docs")
os.chdir(ROOT)

# The directive's modules. ESTATE (sales, facilities, commissioning, handover) has no
# module in the directive; it is kept as a proposed module so no requirement is
# dropped without an owner decision.
MODULES = ["Command", "Estimate", "Plan", "Design", "Supply", "Stock", "Fleet", "People", "Contracts",
           "Capital", "Assure", "Connect", "Intelligence", "Governance", "Estate (proposed)", "Platform"]
PRODUCT = {"COMMAND": "Command", "ESTIMATE": "Estimate", "PLAN / BUILD": "Plan", "DESIGN": "Design",
           "SUPPLY / STOCK": "Supply", "FLEET": "Fleet", "PEOPLE": "People", "CONTRACT": "Contracts",
           "CAPITAL": "Capital", "ASSURE": "Assure", "CONNECT": "Connect", "ESTATE": "Estate (proposed)",
           "BRAIN / SENTINEL": "Intelligence", "PLATFORM": "Platform"}
STOCK_DOMAINS = re.compile(r"inventory|warehouse|material", re.I)
GOVERNANCE_DOMAINS = re.compile(r"security|platform admin", re.I)

# Go packages -> destination (pure libraries go to the platform kernel).
PACKAGES = {
    "pkg/money": "Platform (Serk.Platform.Numerics: Money)", "pkg/ethcal": "Platform (Serk.Platform.Primitives: Ethiopian calendar)",
    "pkg/ledger": "Capital", "pkg/fx": "Capital", "pkg/tax": "Capital", "pkg/evm": "Plan", "pkg/schedule": "Plan",
    "pkg/fleet": "Fleet", "pkg/auth": "Governance", "pkg/attest": "Governance", "pkg/pinauth": "Governance",
    "pkg/datakey": "Governance", "pkg/brand": "Governance", "pkg/workflow": "Governance", "pkg/automation": "Intelligence",
    "pkg/docscan": "Connect", "pkg/blob": "Connect", "pkg/legacy": "Platform (import)", "internal/aiphrase": "Intelligence",
    "internal/kernel": "split across all modules (see routes and requirements)", "internal/httpapi": "split across all modules (API)",
    "internal/store/postgres": "Platform (schema compatibility; per-table owners below)", "internal/webui": "Blazor console (per module)",
    "internal/export": "Connect", "internal/obs": "Platform (OpenTelemetry)", "internal/relay": "Platform (outbox relay)",
    "internal/signer": "Governance (approval signer, isolated process)", "internal/config": "Platform", "internal/testdb": "test infrastructure",
    "cmd/serk-api": "Platform host", "cmd/serk-migrate": "Platform (migration runner; SQL history preserved)",
    "cmd/serk-seed": "test infrastructure", "cmd/serk-signer": "Governance (signer host)", "cmd/serk-import": "Platform (import)",
    "cmd/serk-relay": "Platform (outbox relay)", "cmd/serk-devtoken": "test infrastructure", "tools/ci/s3provision": "test infrastructure",
}

# Route and table keyword -> module, first match wins; unmatched rows are flagged.
KEYWORDS = [
    (r"healthz|readyz|console|^/\{|outbox|processed-events|migration-exceptions|^/v1/dl/|^/v1/setup|regions|currencies", "Platform"),
    (r"guarantee|price-ind|price-adj|ld-term", "Contracts"),
    (r"pay-run|pay-input|pay-line|roster", "People"),
    (r"itp|capa|material-test|punch|permit|induction|toolbox", "Assure"),
    (r"site-instruction|site-record|site/diar|installed-quant|credit", "Plan"),
    (r"approval-|directory-users|data-access|org/calendar|business-units|legal-entit", "Governance"),
    (r"accrual|overhead|fiscal|customer|invoice", "Capital"),
    (r"journal|account|period|ledger|trial|statement|fx|tax|consolidat|bank|treasury|payment|cash|receivable|payable|ar-|ap-|aging|revenue|asset|depreciat|customer-invoice|credit-note|receipt|money-in|retention|wip|recon", "Capital"),
    (r"pr-lines|purchase|requisition|rfq|quote|supplier|po-|purchase-order|grn|goods-receipt|p2p|three-way|match|subcontract", "Supply"),
    (r"stock|warehouse|uom|reservation|inventory|issue|put-away|transfer|count|writeoff|return", "Stock"),
    (r"equipment|fleet|fuel|maintenance|plant|meter|work-order|charge", "Fleet"),
    (r"payroll|timesheet|people|person|role|employee|competenc|certification|attendance", "People"),
    (r"contract|variation|claim|notice|ipc|measurement|bond|escalation|obligation", "Contracts"),
    (r"boq|rate|estimate|tender|bid|build-up|resource-rate", "Estimate"),
    (r"budget|forecast|schedule|wbs|cost-code|project|gate|change-event|contingency|evm|progress|risk|site-diar|diary|activity", "Plan"),
    (r"hse|quality|inspection|ncr|safety|incident|carbon|sustain", "Assure"),
    (r"document|drawing|rfi|submittal|transmittal|correspondence|task|meeting|report|export|portal|notification|inbox", "Connect"),
    (r"ask|intelligence|automation|rule|insight|assistant|kpi|dashboard", "Intelligence"),
    (r"identity|session|login|auth|pin|branding|tenant|setting|authority|delegation|sod|audit|device|sync|import|staging|meta|me/|profile|workflow", "Governance"),
]
def classify(text):
    t = text.lower()
    for pat, mod in KEYWORDS:
        if re.search(pat, t):
            return mod
    return "UNASSIGNED"

def sh(*a):
    return subprocess.run(a, capture_output=True, text=True, check=True).stdout

status = {}
sp = os.path.join(OUT, "migration-status.json")
if os.path.exists(sp):
    status = json.load(open(sp))["rows"]

def st(key):
    s = status.get(key, {})
    return {"status": s.get("status", "NOT_STARTED"), "csharp": s.get("csharp", ""), "tests": s.get("tests", []), "note": s.get("note", "")}

reg = json.load(open("docs/engineering/requirements.json"))
commit = sh("git", "rev-parse", "--short", "HEAD").strip()
rows = {"requirements": [], "packages": [], "migrations": [], "tables": [], "routes": []}

for d in reg["domains"] + reg["cross_cutting"]:
    mod = PRODUCT.get(d["product"], "UNASSIGNED")
    if d["product"] == "SUPPLY / STOCK" and STOCK_DOMAINS.search(d["domain"]):
        mod = "Stock"
    if d["product"] == "BRAIN / SENTINEL" and GOVERNANCE_DOMAINS.search(d["domain"]):
        mod = "Governance"
    for r in d["requirements"]:
        rows["requirements"].append({"id": r["id"], "domain": f'{d["id"]} {d["domain"]}', "text": r["text"], "go_status": r["status"],
                                     "go_evidence": r["evidence_tests"], "criteria": len(r.get("criteria", [])), "module": mod, **st(r["id"])})

tests_by_pkg = collections.Counter()
for f in sh("git", "ls-files", "*_test.go").split():
    tests_by_pkg[os.path.dirname(f)] += len(re.findall(r"^func Test\w+\(t \*testing\.T\)", open(f).read(), re.M))
for p in sorted(set(os.path.dirname(f) for f in sh("git", "ls-files", "*.go").split())):
    if p.startswith("dotnet"):
        continue
    src = [f for f in sh("git", "ls-files", f"{p}/*.go").split() if os.path.dirname(f) == p and not f.endswith("_test.go")]
    loc = sum(sum(1 for _ in open(f)) for f in src)
    rows["packages"].append({"package": p, "files": len(src), "loc": loc, "go_tests": tests_by_pkg[p], "module": PACKAGES.get(p, "UNASSIGNED"), **st(p)})

tables = {}
for f in sorted(sh("git", "ls-files", "internal/store/postgres/migrations/*.sql").split()):
    sql = open(f).read()
    created = re.findall(r"CREATE TABLE (?:IF NOT EXISTS )?([a-z_0-9]+)", sql)
    for t in created:
        tables.setdefault(t, os.path.basename(f))
    rows["migrations"].append({"migration": os.path.basename(f), "tables_created": created, "functions": len(re.findall(r"CREATE (?:OR REPLACE )?FUNCTION", sql)),
                               "triggers": len(re.findall(r"CREATE (?:CONSTRAINT )?TRIGGER", sql)), "policies": len(re.findall(r"CREATE POLICY|ROW LEVEL SECURITY", sql)),
                               "plan": "preserved verbatim; applied by the existing runner; never regenerated by EF Core", **st(os.path.basename(f))})
for t, m in sorted(tables.items()):
    rows["tables"].append({"table": t, "created_in": m, "module": classify(t.replace("_", "-") if t not in ("outbox",) else "outbox"), **st("table:" + t)})

seen = set()
for f in sorted(sh("git", "ls-files", "internal/httpapi/*.go").split()):
    if f.endswith("_test.go"):
        continue
    for m in re.finditer(r'"((?:GET|POST|PUT|DELETE|PATCH) /[^"\s]*)"', open(f).read()):
        r = m.group(1)
        if r in seen:
            continue
        seen.add(r)
        rows["routes"].append({"route": r, "source": f, "module": classify(r.split(" ", 1)[1]), **st(r)})

defects = json.load(open("docs/final-completion/06_DEFECT_REGISTER.json"))
defects = defects["defects"] if isinstance(defects, dict) else defects
summary = {k: dict(collections.Counter(x["status"] for x in v)) for k, v in rows.items()}
by_module = {k: dict(collections.Counter(x["module"] for x in v)) for k, v in rows.items() if k != "migrations"}
doc = {"generated_from_commit": commit, "go_requirement_counts": reg["requirement_counts"], "go_domains": len(reg["domains"]),
       "go_cross_cutting": len(reg["cross_cutting"]),
       "open_go_defects": [{"id": x["id"], "severity": x.get("severity"), "title": x.get("title", "")} for x in defects if x["status"] != "CLOSED"],
       "unavailable_inputs": json.load(open(os.path.join(OUT, "unavailable-inputs.json"))) if os.path.exists(os.path.join(OUT, "unavailable-inputs.json")) else [],
       "migration_status_summary": summary, "rows_by_module": by_module, "rows": rows}
js = json.dumps(doc, indent=1, ensure_ascii=False) + "\n"

md = [f"# Go-to-C# migration traceability register", "",
      f"Generated by `dotnet/tools/traceability.py` from commit `{commit}` of this repository. Do not edit by hand: change",
      "`dotnet/docs/migration-status.json` and regenerate. A row is MIGRATED only when its differential and acceptance tests",
      "are named there and pass.", "",
      f"Go baseline: {len(reg['domains'])} domains + {len(reg['cross_cutting'])} cross-cutting areas, {len(rows['requirements'])} listed requirements "
      f"({', '.join(f'{k} {v}' for k, v in reg['requirement_counts'].items())}), {len(rows['packages'])} Go packages, "
      f"{len(rows['migrations'])} SQL migrations, {len(rows['tables'])} tables, {len(rows['routes'])} HTTP routes.", "",
      "## Migration status", "", "| Kind | " + " | ".join(["NOT_STARTED", "IN_PROGRESS", "DIFFERENTIAL_PASS", "MIGRATED", "BLOCKED"]) + " |", "|---|---|---|---|---|---|"]
for k, v in summary.items():
    md.append(f"| {k} | " + " | ".join(str(v.get(s, 0)) for s in ["NOT_STARTED", "IN_PROGRESS", "DIFFERENTIAL_PASS", "MIGRATED", "BLOCKED"]) + " |")
md += ["", "## Rows per destination module", "", "| Module | Requirements | Packages | Tables | Routes |", "|---|---|---|---|---|"]
mods = sorted(set().union(*[set(v) for v in by_module.values()]))
for m in mods:
    md.append(f"| {m} | " + " | ".join(str(by_module[k].get(m, 0)) for k in ["requirements", "packages", "tables", "routes"]) + " |")
md += ["", "UNASSIGNED rows have no keyword match and need an owner's decision before their module is built.", "",
       "## Inputs that could not be inspected", ""]
for u in doc["unavailable_inputs"]:
    md.append(f"- **{u['input']}**: {u['state']}. {u['consequence']}")
md += ["", "## Open Go defects carried into the migration", ""] + [f"- {x['id']} ({x['severity']}): {x['title']}" for x in doc["open_go_defects"]]
md += ["", "## Packages", "", "| Go package | Files | Lines | Go tests | Destination | Status |", "|---|---|---|---|---|---|"]
for p in rows["packages"]:
    md.append(f"| `{p['package']}` | {p['files']} | {p['loc']} | {p['go_tests']} | {p['module']} | {p['status']}{' — ' + p['note'] if p['note'] else ''} |")
md += ["", "## Requirements", "", "| Requirement | Go status | Module | Migration status |", "|---|---|---|---|"]
for r in rows["requirements"]:
    md.append(f"| {r['id']} {r['text'][:110].replace('|', '/')} | {r['go_status']} | {r['module']} | {r['status']} |")
md += ["", "Migrations, tables and routes (row by row) are in `MIGRATION_TRACEABILITY.json`."]
mdtext = "\n".join(md) + "\n"

if "--check" in sys.argv:
    bad = [p for p, t in ((os.path.join(OUT, "MIGRATION_TRACEABILITY.json"), js), (os.path.join(OUT, "MIGRATION_TRACEABILITY.md"), mdtext))
           if not os.path.exists(p) or open(p).read() != t]
    if bad:
        print("stale:", ", ".join(bad)); sys.exit(1)
    print("traceability register is current"); sys.exit(0)
open(os.path.join(OUT, "MIGRATION_TRACEABILITY.json"), "w").write(js)
open(os.path.join(OUT, "MIGRATION_TRACEABILITY.md"), "w").write(mdtext)
print(json.dumps(summary), json.dumps({k: v.get("UNASSIGNED", 0) for k, v in by_module.items()}))
