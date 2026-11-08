#!/usr/bin/env python3
"""Builds the SERK Construction OS User Acceptance Test (UAT) Manual.

    python3 docs/uat/build_uat.py            # writes HTML + PDF, prints coverage
    python3 docs/uat/build_uat.py --check    # coverage check only (CI friendly)

Maintainability model
---------------------
* The manual is derived from the SRS (docs/srs/build_srs.py), NOT from the
  current implementation. Every test case names the SRS requirement IDs it
  verifies; this script imports the SRS requirement list and reports any
  Must requirement that has no test. Edit tests in docs/uat/src/*.txt, never
  in the PDF.
* Source file mini-markup (see docs/uat/README.md):
    # Chapter title            new chapter (page break)
    ## Section / ### Sub       headings
    - bullet                   list item
    > text                     highlighted note box
    | a | b |                  table (first row is the header)
    @release R1                default release for cases in the chapter
    @term Word = definition    adds to the glossary
    @case ID | title | rel | SRS ids | roles     starts a test case, then
        pre: ...   1. step   => expected   why: ...   note: ...
    @scenario ID | title | roles | SRS ids | rel  starts an end-to-end scenario
        @before text / | # | who | action | expected | rows / @after text
"""
import html, os, re, subprocess, sys, tempfile
from collections import OrderedDict, defaultdict

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.path.join(HERE, "src")
sys.path.insert(0, os.path.join(HERE, "..", "srs"))
import build_srs as SRS  # noqa: E402

OUT_HTML = os.path.join(HERE, "SERK_Construction_UAT_Manual_v1.0.html")
OUT_PDF = os.path.join(HERE, "SERK_Construction_UAT_Manual_v1.0.pdf")
CHROME = os.environ.get("CHROME", "google-chrome")
VERSION = "v1.0 DRAFT | 2026-11-07"
DATE = "7 November 2026"
e = html.escape

RELEASES = OrderedDict([
    ("R1", "Financial core and procure-to-pay (GA)"),
    ("R2", "Project controls, commercial, payroll, tax and consolidation"),
    ("R3", "Field, documents and QHSE"),
    ("R4", "Intelligence, portals and enterprise scale"),
])

ROLES = OrderedDict([
    ("EXE", ("Executive / Owner", "Portfolio performance, cash, risk. Group-wide read; high-value approvals.")),
    ("EST", ("Business development / Estimator", "Win profitable work: opportunities, estimates, BOQ drafting.")),
    ("QS", ("Commercial manager / Quantity surveyor", "Protect contract value: BOQ approval, measurement, IPC, variations, claims, subcontract certification.")),
    ("PM", ("Project manager / Project controls / Planner", "Deliver on time and budget: budget revisions, requisition approval, schedules, progress, forecasts.")),
    ("SITE", ("Site engineer / Foreman", "Execute work safely: requisitions, receipt confirmation, diaries, timesheets, inspections, incidents (mobile, offline).")),
    ("PROC", ("Procurement officer / manager", "Buy right, on time: suppliers, RFQs, awards, purchase orders, subcontracts.")),
    ("STORE", ("Storekeeper / Inventory controller", "Account for every item: receipts, issues, transfers, counts and their approval.")),
    ("EQP", ("Equipment manager", "Keep plant productive: register, usage, maintenance, fuel, transfers.")),
    ("HR", ("HR / Payroll officer", "Pay people correctly: employees, timesheets, payroll, statutory returns.")),
    ("AP", ("AP / AR clerk and supervisor", "Supplier invoices, customer invoices, receipts, ageing.")),
    ("FIN", ("Accountant / Controller / Finance manager", "Correct, timely books: journals, subledgers, reconciliations, close, consolidation.")),
    ("CFO", ("CFO", "Financial authority: period reopen, high-value approval, policy approval.")),
    ("TRS", ("Treasury officer / Bank statement officer", "Liquidity and safe payments: payment preparation and release; independent statement import.")),
    ("QA", ("QA/QC officer", "Quality: inspection and test plans, hold points, NCRs, material tests.")),
    ("HSE", ("HSE officer", "Zero harm: permits, incidents, inspections, induction, indicators.")),
    ("AUD", ("Auditor (internal / external)", "Independent assurance: read-only data and audit log, time-boxed.")),
    ("ADM", ("Process owner / Workflow admin / Security admin", "Govern the system: workflows, automation, settings, sessions, devices. No financial authority.")),
    ("EXT", ("External: client, consultant, supplier, subcontractor", "Collaborate in their own scope through a portal.")),
    ("UAT", ("UAT lead / Release engineer", "Runs cross-cutting, environment and non-functional checks.")),
])


# ---------------------------------------------------------------------------
# Parsing
# ---------------------------------------------------------------------------
class Case:
    def __init__(self, cid, title, rel, reqs, roles, chapter):
        self.id, self.title, self.rel, self.chapter = cid, title, rel, chapter
        self.reqs = [r.strip() for r in reqs.split(",") if r.strip()]
        self.roles = [r.strip() for r in roles.split(",") if r.strip()]
        self.pre, self.steps, self.exp, self.why, self.note = [], [], [], [], []
        for r in self.roles:
            assert r in ROLES, f"{cid}: unknown role {r}"
        assert self.rel in RELEASES, f"{cid}: bad release {self.rel}"


def inline(t):
    t = e(t)
    t = re.sub(r"\*\*(.+?)\*\*", r"<b>\1</b>", t)
    t = re.sub(r"`(.+?)`", r"<code>\1</code>", t)
    return t


def parse_file(path, chapters, cases, scenarios, terms):
    cur = None      # current chapter
    case = None
    scen = None
    field = None
    rel = "R1"
    for raw in open(path, encoding="utf-8"):
        line = raw.rstrip("\n")
        s = line.strip()
        if s.startswith("#") and not s.startswith("##"):
            case = scen = None
            cur = {"title": s[1:].strip(), "blocks": [], "cases": [], "file": os.path.basename(path)}
            chapters.append(cur)
            continue
        if cur is None:
            continue
        if s.startswith("@release"):
            rel = s.split()[1]
            cur["rel"] = rel
            continue
        if s.startswith("@term "):
            k, v = s[6:].split("=", 1)
            terms[k.strip()] = v.strip()
            continue
        if s.startswith("@case "):
            p = [x.strip() for x in s[6:].split("|")]
            assert len(p) == 5, f"{path}: bad @case line: {s}"
            cid, title, crel, reqs, roles = p
            case = Case(cid, title, crel or rel, reqs, roles, cur)
            assert cid not in {c.id for c in cases}, f"duplicate case id {cid}"
            cases.append(case)
            cur["blocks"].append(("case", case))
            cur["cases"].append(case)
            scen = None
            field = None
            continue
        if s.startswith("@scenario "):
            p = [x.strip() for x in s[10:].split("|")]
            assert len(p) == 5, f"{path}: bad @scenario line: {s}"
            scen = {"id": p[0], "title": p[1], "roles": [r.strip() for r in p[2].split(",")],
                    "reqs": [r.strip() for r in p[3].split(",") if r.strip()], "rel": p[4] or rel,
                    "before": [], "rows": [], "after": [], "chapter": cur}
            scenarios.append(scen)
            cur["blocks"].append(("scenario", scen))
            case = None
            field = None
            continue
        if case is not None:
            if not s:
                continue
            if s.startswith("pre:"):
                case.pre.append(s[4:].strip()); field = case.pre
            elif s.startswith("why:"):
                case.why.append(s[4:].strip()); field = case.why
            elif s.startswith("note:"):
                case.note.append(s[5:].strip()); field = case.note
            elif re.match(r"\d+\.\s", s):
                case.steps.append(re.sub(r"^\d+\.\s*", "", s)); field = case.steps
            elif s.startswith("=>"):
                case.exp.append(s[2:].strip()); field = case.exp
            elif field is not None and field:
                field[-1] += " " + s            # continuation line
            else:
                raise SystemExit(f"{path}: unexpected line in case {case.id}: {s}")
            continue
        if scen is not None:
            if not s:
                continue
            if s.startswith("@before"):
                scen["before"].append(s[7:].strip()); field = scen["before"]
            elif s.startswith("@after"):
                scen["after"].append(s[6:].strip()); field = scen["after"]
            elif s.startswith("|"):
                cells = [c.strip() for c in s.strip("|").split("|")]
                scen["rows"].append(cells)
            elif field is not None and field:
                field[-1] += " " + s
            continue
        # plain chapter content
        cur["blocks"].append(("raw", line))


def render_raw(lines):
    out, i = [], 0
    L = [l.rstrip() for l in lines]
    while i < len(L):
        s = L[i].strip()
        if not s:
            i += 1; continue
        if s.startswith("### "):
            out.append(f"<h3>{inline(s[4:])}</h3>"); i += 1
        elif s.startswith("## "):
            out.append(f"<h2>{inline(s[3:])}</h2>"); i += 1
        elif s.startswith("- "):
            items = []
            while i < len(L) and L[i].strip().startswith("- "):
                items.append(L[i].strip()[2:]); i += 1
                while i < len(L) and L[i].strip() and not L[i].strip().startswith(("- ", "|", ">", "#", "@")):
                    items[-1] += " " + L[i].strip(); i += 1
            out.append("<ul>" + "".join(f"<li>{inline(x)}</li>" for x in items) + "</ul>")
        elif s.startswith(">"):
            txt = []
            while i < len(L) and L[i].strip().startswith(">"):
                txt.append(L[i].strip()[1:].strip()); i += 1
            out.append(f'<div class="note">{inline(" ".join(txt))}</div>')
        elif s.startswith("|"):
            rows = []
            while i < len(L) and L[i].strip().startswith("|"):
                rows.append([c.strip() for c in L[i].strip().strip("|").split("|")]); i += 1
            head, body = rows[0], rows[1:]
            out.append("<table><thead><tr>" + "".join(f"<th>{inline(c)}</th>" for c in head) + "</tr></thead><tbody>"
                       + "".join("<tr>" + "".join(f"<td>{inline(c)}</td>" for c in r) + "</tr>" for r in body) + "</tbody></table>")
        else:
            para = [s]; i += 1
            while i < len(L) and L[i].strip() and not L[i].strip().startswith(("- ", "|", ">", "#", "@")):
                para.append(L[i].strip()); i += 1
            out.append(f"<p>{inline(' '.join(para))}</p>")
    return "\n".join(out)


def role_chips(roles):
    return ", ".join(e(ROLES[r][0]) for r in roles)


def render_case(c):
    pre = f'<div class="pre"><b>Before you start:</b> {inline(" ".join(c.pre))}</div>' if c.pre else ""
    steps = "<ol>" + "".join(f"<li>{inline(s)}</li>" for s in c.steps) + "</ol>"
    exp = "<ul>" + "".join(f"<li>{inline(s)}</li>" for s in c.exp) + "</ul>"
    why = f'<div class="why"><b>Why it matters in practice:</b> {inline(" ".join(c.why))}</div>' if c.why else ""
    note = f'<div class="cnote"><b>Note:</b> {inline(" ".join(c.note))}</div>' if c.note else ""
    return f"""<div class="case" id="{c.id}">
<div class="ch"><span class="cid">{c.id}</span><span class="ct">{inline(c.title)}</span><span class="rb {c.rel}">{c.rel}</span></div>
<div class="cm"><b>Role(s):</b> {role_chips(c.roles)} &nbsp;|&nbsp; <b>SRS:</b> {e(", ".join(c.reqs))}</div>
{pre}
<table class="ctab"><thead><tr><th style="width:44%">Steps</th><th style="width:36%">Expected result</th><th>Result</th></tr></thead>
<tbody><tr><td>{steps}</td><td>{exp}</td>
<td class="res">&#9744; Pass<br>&#9744; Fail<br>&#9744; Blocked<br><span class="l">Tester / date:</span><br><br><span class="l">Defect no.:</span></td></tr></tbody></table>
{why}{note}</div>"""


def render_scenario(s):
    head = ["#", "Who", "Action", "Expected result / figure to check"]
    rows = "".join("<tr>" + "".join(f"<td>{inline(c)}</td>" for c in r) + "<td class='tick'>&#9744;</td></tr>" for r in s["rows"])
    before = f'<div class="pre"><b>Before you start:</b> {inline(" ".join(s["before"]))}</div>' if s["before"] else ""
    after = f'<div class="cnote">{inline(" ".join(s["after"]))}</div>' if s["after"] else ""
    return f"""<div class="case scen" id="{s['id']}">
<div class="ch"><span class="cid">{s['id']}</span><span class="ct">{inline(s['title'])}</span><span class="rb {s['rel']}">{s['rel']}</span></div>
<div class="cm"><b>Role(s):</b> {role_chips(s['roles'])} &nbsp;|&nbsp; <b>SRS:</b> {e(", ".join(s['reqs']))}</div>
{before}
<table class="stab"><thead><tr><th style="width:4%">#</th><th style="width:15%">Who</th><th style="width:36%">Action</th><th>Expected result / figure to check</th><th style="width:5%">OK</th></tr></thead><tbody>{rows}</tbody></table>
{after}
<div class="sres">Scenario result: &#9744; Pass &#9744; Fail &#9744; Blocked &nbsp; Date: ____________ Tester: ____________ Defect no.: ________</div></div>"""


# ---------------------------------------------------------------------------
# Load
# ---------------------------------------------------------------------------
def load():
    chapters, cases, scenarios, terms = [], [], [], {}
    for fn in sorted(os.listdir(SRC)):
        if fn.endswith(".txt"):
            parse_file(os.path.join(SRC, fn), chapters, cases, scenarios, terms)
    return chapters, cases, scenarios, terms


def coverage(cases, scenarios):
    cov = defaultdict(list)
    for c in cases:
        for r in c.reqs:
            cov[r].append(c.id)
    for s in scenarios:
        for r in s["reqs"]:
            cov[r].append(s["id"])
    allreqs = [(r[0], r[1], r[2], r[3], r[4]) for sec in SRS.ALL for r in sec["reqs"]]
    ids = {r[0] for r in allreqs}
    other = {t[0] for t in SRS.TESTS} | {u[0] for u in SRS.USE_CASES}   # SRS Appendix C test ids, section 5 use cases
    unknown = sorted(x for x in set(cov) - ids - other if not re.match(r"^(EI-(UI|HW|CM)-\d+|DR-\d+|COM-\d+)$", x))
    return cov, allreqs, unknown


def check_report(cases, scenarios):
    cov, allreqs, unknown = coverage(cases, scenarios)
    missing = [r for r in allreqs if r[0] not in cov]
    mm = [r for r in missing if r[1] == "M"]
    print(f"{len(cases)} test cases, {len(scenarios)} scenarios, {len(allreqs)} SRS requirements")
    print(f"covered: {len(allreqs) - len(missing)}   not covered: {len(missing)} (Must: {len(mm)})")
    for r in missing:
        print(f"  NOT COVERED [{SRS.PRI[r[1]]}] {r[0]}: {r[4][:80]}")
    if unknown:
        print("  UNKNOWN requirement IDs referenced by tests:", ", ".join(unknown))
    return mm, unknown


# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------
def build(pages):
    chapters, cases, scenarios, terms = load()
    cov, allreqs, unknown = coverage(cases, scenarios)
    parts, toc = [], []
    n = 0

    def add_toc(level, title, anchor):
        toc.append((level, title, anchor))

    cover = f"""<section class="cover"><div class="ctitle">SERK Construction OS</div><div class="bar"></div>
<div class="csub">User Acceptance Test Manual</div>
<div class="cline">All user roles | Industry primer | Requirement-traced test cases | End-to-end scenarios</div>
<div class="cline">Derived from the Software Requirements Specification v1.0 (what we promise to deliver),<br>not from what the current build happens to do.</div>
<div class="cfoot"><b>Document version 1.0 - DRAFT</b><br>Date: {DATE}<br>{len(cases)} test cases and {len(scenarios)} end-to-end scenarios, traced to {len([r for r in cov if r in {x[0] for x in allreqs}])} of {len(allreqs)} SRS requirements<br>
<i>Fictional data only. Not legal, tax, accounting or regulatory advice; statutory values come from adviser-signed fixtures.</i></div></section>"""
    parts.append(cover)

    for ch in chapters:
        n += 1
        anchor = f"ch{n}"
        add_toc(1, ch["title"], anchor)
        body = [f'<section class="page" id="{anchor}"><h1>{inline(ch["title"])}</h1>']
        raw = []
        for kind, val in ch["blocks"]:
            if kind == "raw":
                raw.append(val)
                continue
            if raw:
                body.append(render_raw(raw)); raw = []
            if kind == "case":
                body.append(render_case(val))
            else:
                body.append(render_scenario(val))
        if raw:
            body.append(render_raw(raw))
        body.append("</section>")
        parts.append("\n".join(body))
        # auto-generated chapters
        if ch["title"].startswith("Role guides"):
            pass

    # Role index (auto) -------------------------------------------------
    n += 1
    ri = [f'<section class="page" id="ch{n}"><h1>Role execution sheets</h1>',
          '<p class="intro">Generated from the test cases. Each tester runs the cases listed against their own role; cases marked <b>(checker)</b> in the steps need a second, different user. Tick the box when the case is finished.</p>']
    add_toc(1, "Role execution sheets", f"ch{n}")
    for rk, (rname, rdesc) in ROLES.items():
        rc = [c for c in cases if rk in c.roles]
        rs = [s for s in scenarios if rk in s["roles"]]
        if not rc and not rs:
            continue
        ri.append(f"<h2>{e(rname)}</h2><p class='intro'>{e(rdesc)} &mdash; <b>{len(rc)}</b> test cases, <b>{len(rs)}</b> scenarios.</p>")
        rows = "".join(f"<tr><td>{c.id}</td><td>{inline(c.title)}</td><td>{c.rel}</td><td>&#9744;</td></tr>" for c in rc)
        rows += "".join(f"<tr><td>{s['id']}</td><td>{inline(s['title'])} <i>(scenario)</i></td><td>{s['rel']}</td><td>&#9744;</td></tr>" for s in rs)
        ri.append(f"<table class='idx'><thead><tr><th style='width:14%'>ID</th><th>Title</th><th style='width:8%'>Rel.</th><th style='width:6%'>Done</th></tr></thead><tbody>{rows}</tbody></table>")
    ri.append("</section>")
    parts.append("\n".join(ri))

    # Appendix A: defect log -------------------------------------------
    n += 1
    add_toc(1, "Appendix A - Defect log", f"ch{n}")
    rows = "".join("<tr>" + "<td>D-%02d</td>" % i + "<td></td>" * 5 + "</tr>" for i in range(1, 19))
    parts.append(f"""<section class="page" id="ch{n}"><h1>Appendix A - Defect log</h1>
<p class="intro">Severity: <b>Critical</b> = money, stock, security or audit integrity wrong, data lost, or a segregation-of-duties / isolation control bypassed. <b>High</b> = a Must requirement blocked or wrong with no workaround. <b>Medium</b> = works with a workaround, or wrong label/format/message. <b>Low</b> = cosmetic. Per SRS section 9.1, no Critical or High defect may be open at release.</p>
<table class="log"><thead><tr><th style="width:7%">No.</th><th style="width:12%">Test ID</th><th style="width:9%">Severity</th><th>Description and steps to reproduce</th><th style="width:15%">Found by / date</th><th style="width:10%">Status</th></tr></thead><tbody>{rows}</tbody></table></section>""")

    # Appendix B: summary & sign-off -----------------------------------
    n += 1
    add_toc(1, "Appendix B - Test summary and sign-off", f"ch{n}")
    srows = ""
    for ch in chapters:
        k = len(ch["cases"]) + len([b for b in ch["blocks"] if b[0] == "scenario"])
        if k:
            srows += f"<tr><td>{inline(ch['title'])}</td><td>{k}</td><td></td><td></td><td></td></tr>"
    tot = len(cases) + len(scenarios)
    srows += f"<tr><td><b>Total</b></td><td><b>{tot}</b></td><td></td><td></td><td></td></tr>"
    sign = "".join(f"<tr><td>{r}</td><td></td><td></td><td></td></tr>" for r in [
        "Product Owner / Client", "Finance Lead", "Commercial / Project Controls Lead", "Operations (Site) Lead",
        "Security Lead", "Compliance / Legal reviewer", "QA Lead (UAT lead)", "Technical Lead / Vendor"])
    parts.append(f"""<section class="page" id="ch{n}"><h1>Appendix B - Test summary and sign-off</h1>
<table class="num"><thead><tr><th>Chapter</th><th>Cases</th><th>Pass</th><th>Fail</th><th>Blocked</th></tr></thead><tbody>{srows}</tbody></table>
<p><b>Release recommendation (tick one):</b> &#9744; GO for pilot &nbsp; &#9744; NO-GO &nbsp; &#9744; GO with listed conditions</p>
<p>Conditions / open defects accepted: ____________________________________________________________</p>
<table class="sign"><thead><tr><th>Role (SRS section 15)</th><th>Name</th><th>Signature</th><th>Date</th></tr></thead><tbody>{sign}</tbody></table></section>""")

    # Appendix C: requirement coverage ---------------------------------
    n += 1
    add_toc(1, "Appendix C - Requirement-to-test traceability", f"ch{n}")
    cr = ['<section class="page" id="ch%d"><h1>Appendix C - Requirement-to-test traceability</h1>' % n,
          '<p class="intro">Generated from the SRS requirement list and the <code>@case</code> headers. <b>Status</b> is the SRS baseline note (Exists / Partial / New): engineering evidence only, never acceptance. Every requirement is tested as written. Requirements verified by Inspection or Analysis list evidence checks rather than click tests.</p>']
    for sec in SRS.ALL:
        cr.append(f"<h2>{e(sec['num'])} {e(sec['title'])}</h2>")
        rows = ""
        for rid, p, v, b, text in sec["reqs"]:
            t = cov.get(rid, [])
            cell = ", ".join(f'<a href="#{x}">{x}</a>' for x in t) if t else '<b class="miss">NOT COVERED</b>'
            rows += f"<tr><td>{rid}</td><td>{SRS.PRI[p]}</td><td>{SRS.VER[v]}</td><td>{SRS.BASE[b]}</td><td>{cell}</td></tr>"
        cr.append(f"<table class='tr'><thead><tr><th style='width:15%'>Requirement</th><th style='width:9%'>Priority</th><th style='width:11%'>Verified by</th><th style='width:9%'>Baseline</th><th>UAT test cases</th></tr></thead><tbody>{rows}</tbody></table>")
    cr.append("</section>")
    parts.append("\n".join(cr))

    # Appendix D: role matrix -----------------------------------------
    n += 1
    add_toc(1, "Appendix D - Role-capability matrix (SRS Appendix D)", f"ch{n}")
    head = "".join(f"<th>{e(c)}</th>" for c in SRS.MATRIX_COLS)
    rows = "".join("<tr><td>" + e(r[0]) + "</td>" + "".join(f"<td>{e(x)}</td>" for x in r[1:]) + "</tr>" for r in SRS.MATRIX)
    parts.append(f"""<section class="page" id="ch{n}"><h1>Appendix D - Role-capability matrix</h1>
<p class="intro">C = create/prepare, A = approve/certify, P = post, X = execute, R = read, - = none; * = above threshold or exception, with MFA. This is the SRS initial proposal (to be confirmed in R1 design); the role tests in chapter 3 and the segregation tests check the system against it.</p>
<table class="matrix"><thead><tr><th style="width:21%">Capability</th>{head}</tr></thead><tbody>{rows}</tbody></table></section>""")

    # Glossary ---------------------------------------------------------
    n += 1
    add_toc(1, "Appendix E - Glossary", f"ch{n}")
    gl = "".join(f"<tr><td><b>{e(k)}</b></td><td>{inline(v)}</td></tr>" for k, v in sorted(terms.items(), key=lambda kv: kv[0].lower()))
    parts.append(f"""<section class="page" id="ch{n}"><h1>Appendix E - Glossary of construction and system terms</h1>
<table><thead><tr><th style="width:22%">Term</th><th>Meaning</th></tr></thead><tbody>{gl}</tbody></table></section>""")

    # TOC --------------------------------------------------------------
    t = ['<section class="page" id="toc"><h1>Contents</h1>']
    for level, title, anchor in toc:
        pg = pages.get(anchor, "")
        t.append(f'<div class="t1"><a href="#{anchor}">{inline(title)}</a><span class="dots"></span><span>{pg}</span></div>')
    t.append("</section>")
    parts.insert(1, "\n".join(t))
    return "\n".join(parts), toc, (cases, scenarios)


CSS = """<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>SERK Construction OS - UAT Manual v1.0</title><style>
@page { size: A4; margin: 22mm 15mm 18mm 15mm;
  @top-left { content: "SERK Construction OS - UAT Manual"; font: bold 8.5pt Helvetica, Arial, sans-serif; color: #fff; background: #13233f; padding-left: 15mm; margin-left: -15mm; }
  @top-right { content: "v1.0 DRAFT | 2026-11-07"; font: 8pt Helvetica, Arial, sans-serif; color: #fff; background: #13233f; padding-right: 15mm; margin-right: -15mm; }
  @bottom-left { content: "Derived from SRS v1.0 - fictional test data only"; font: 8pt Helvetica, Arial, sans-serif; color: #667; border-top: .5pt solid #ccd; }
  @bottom-right { content: "Page " counter(page); font: 8pt Helvetica, Arial, sans-serif; color: #667; border-top: .5pt solid #ccd; } }
@page cover { margin: 0; }
html { -webkit-print-color-adjust: exact; print-color-adjust: exact; background: #fff; }
body { font: 9.2pt/1.4 Helvetica, Arial, "Noto Sans Ethiopic", sans-serif; color: #1b2433; margin: 0; }
.cover { page: cover; background: #13233f; color: #fff; height: 100vh; overflow: hidden; box-sizing: border-box; padding: 70mm 22mm 20mm; position: relative; page-break-after: always; }
.ctitle { font-size: 36pt; font-weight: bold; } .bar { width: 26mm; height: 2.4mm; background: #0f7c73; margin: 12mm 0 10mm; }
.csub { font-size: 21pt; margin-bottom: 6mm; } .cline { color: #c9d3e3; font-size: 10.5pt; margin-bottom: 4mm; }
.cfoot { position: absolute; bottom: 22mm; left: 22mm; right: 22mm; font-size: 9.5pt; color: #c9d3e3; } .cfoot b { color: #fff; }
.page { page-break-before: always; }
h1 { font-size: 19pt; color: #13233f; margin: 3mm 0 4mm; page-break-after: avoid; }
h2 { font-size: 12.5pt; color: #0f7c73; margin: 6mm 0 2mm; page-break-after: avoid; }
h3 { font-size: 10.5pt; color: #13233f; margin: 4mm 0 1.5mm; page-break-after: avoid; }
p { margin: 0 0 2.2mm; } p.intro { font-size: 8.6pt; color: #3a4558; }
code { font: 8.4pt Consolas, "DejaVu Sans Mono", monospace; background: #eef1f6; padding: 0 1mm; border-radius: .8mm; }
table { width: 100%; border-collapse: collapse; table-layout: fixed; margin: 1.5mm 0 3mm; font-size: 8.4pt; }
thead { display: table-header-group; } tr { page-break-inside: avoid; }
th { background: #13233f; color: #fff; text-align: left; padding: 1.5mm 1.8mm; font-weight: bold; }
td { border: .5pt solid #d3dae6; padding: 1.4mm 1.8mm; vertical-align: top; overflow-wrap: anywhere; }
tbody tr:nth-child(even) td { background: #f2f5fa; }
table.num td:not(:first-child), table.num th:not(:first-child), table.matrix td:not(:first-child), table.matrix th:not(:first-child) { text-align: center; }
table.matrix { font-size: 7.6pt; } table.matrix th { padding: 1.3mm .5mm; font-size: 7pt; }
table.sign td { height: 11mm; } table.log td { height: 9mm; }
.t1 { display: flex; align-items: baseline; font-size: 10pt; margin: 1.8mm 0; } .t1 a { color: #1b2433; text-decoration: none; }
.dots { flex: 1; border-bottom: 1pt dotted #8892a6; margin: 0 1.5mm; transform: translateY(-1mm); }
ul { margin: 1mm 0 2.6mm; padding-left: 5mm; } li { margin-bottom: .8mm; }
.note { border: .7pt solid #0f7c73; background: #eaf8f4; padding: 2.4mm 3mm; margin: 2.6mm 0; font-size: 8.8pt; }
.case { border: .8pt solid #b9c3d6; border-radius: 1.2mm; margin: 0 0 4mm; page-break-inside: avoid; }
.case.scen { page-break-inside: avoid; }
.ch { background: #e8edf6; padding: 1.8mm 2.4mm; display: flex; align-items: baseline; gap: 2.5mm; border-bottom: .6pt solid #b9c3d6; }
.cid { font-weight: bold; color: #13233f; white-space: nowrap; } .ct { flex: 1; font-weight: bold; }
.rb { font-size: 7.6pt; font-weight: bold; padding: .2mm 1.6mm; border-radius: 1mm; color: #fff; }
.rb.R1 { background: #0f7c73; } .rb.R2 { background: #23408e; } .rb.R3 { background: #8a5a00; } .rb.R4 { background: #7a2a6b; }
.cm { font-size: 8pt; color: #3a4558; padding: 1.2mm 2.4mm; }
.pre, .why, .cnote { font-size: 8.2pt; padding: 1.2mm 2.4mm; } .why { background: #fbf6e6; border-top: .5pt solid #e5d9a8; } .cnote { background: #f2f5fa; }
.case table { margin: 0; } .case .ctab td, .case .ctab th { border-left: 0; border-right: 0; }
.case ol { margin: 0; padding-left: 4.4mm; } .case ul { margin: 0; padding-left: 4mm; }
td.res { font-size: 8pt; } .l { color: #667; font-size: 7.4pt; } td.tick { text-align: center; }
.sres { font-size: 8.2pt; padding: 1.8mm 2.4mm; background: #f2f5fa; }
table.idx td:last-child, table.tr td:first-child { white-space: nowrap; } .miss { color: #b00020; } table.tr a { color: #23408e; text-decoration: none; }
</style></head><body>@@BODY@@</body></html>"""


def render(html_text, pdf):
    with tempfile.NamedTemporaryFile("w", suffix=".html", delete=False, encoding="utf-8") as f:
        f.write(html_text)
        path = f.name
    subprocess.run([CHROME, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-pdf-header-footer",
                    f"--print-to-pdf={pdf}", "file://" + path], check=True, capture_output=True)
    os.unlink(path)


def page_map(pdf, toc):
    n = int(re.search(r"Pages:\s+(\d+)", subprocess.run(["pdfinfo", pdf], capture_output=True, text=True).stdout).group(1))
    texts = [subprocess.run(["pdftotext", "-f", str(i), "-l", str(i), pdf, "-"], capture_output=True, text=True).stdout for i in range(1, n + 1)]
    pages, start = {}, 3
    for level, title, anchor in toc:
        for i in range(start - 1, n):
            if title.split(" - ")[0][:30] in texts[i] and title[:25] in texts[i]:
                pages[anchor] = i + 1
                start = i + 1
                break
    return pages, n


def main():
    chapters, cases, scenarios, terms = load()
    mm, unknown = check_report(cases, scenarios)
    if "--check" in sys.argv:
        sys.exit(1 if (mm or unknown) else 0)
    body, toc, _ = build({})
    doc = CSS.replace("@@BODY@@", body)
    open(OUT_HTML, "w", encoding="utf-8").write(doc)
    render(doc, OUT_PDF)
    pages, n = page_map(OUT_PDF, toc)
    body, toc, _ = build(pages)
    doc = CSS.replace("@@BODY@@", body)
    open(OUT_HTML, "w", encoding="utf-8").write(doc)
    render(doc, OUT_PDF)
    pages2, n = page_map(OUT_PDF, toc)
    if pages2 != pages:
        body, toc, _ = build(pages2)
        open(OUT_HTML, "w", encoding="utf-8").write(CSS.replace("@@BODY@@", body))
        render(CSS.replace("@@BODY@@", body), OUT_PDF)
    print(f"{OUT_PDF}: {n} pages")


if __name__ == "__main__":
    main()
