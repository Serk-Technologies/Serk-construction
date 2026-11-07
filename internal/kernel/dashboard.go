// Financial control dashboard (WP-1.4; SRS RPT-03, RPT-06, PCC-09, AUD-05).
//
// A dashboard is a set of tiles. Every tile is one KPI with:
//
//	ID       the KPI's catalogue id (docs/reports/KPI_CATALOGUE.md)
//	Value    the number, computed by the server from posted and approved records
//	Formula  how it is calculated, in words, so nobody has to trust a number blindly
//	Level    ok | watch | alert | unknown, from documented thresholds
//	AsOf     when it was computed (nothing here is cached)
//	Drill    the console page holding the rows behind the number
//	Evidence the largest contributors, each linking to its record
//
// Nothing is calculated here that a report does not already show: tiles are
// built from the same kernel reads as the cost report, aging, trial balance,
// cash outlook, risk assessment and control review, each of which applies the
// caller's permissions and project scope. A tile the caller may not read is
// listed under Hidden (with the permission it needs) instead of being shown
// empty or silently omitted. Nothing here writes.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// DashboardPresets are the role views. A preset only chooses which tiles to
// show; it never widens what the caller may read.
var DashboardPresets = map[string][]string{
	"executive":   {"K-BUD-01", "K-BUD-02", "K-BUD-03", "K-RSK-01", "K-CSH-01", "K-CSH-02", "K-CSH-03", "K-CSH-04", "K-LED-01", "K-CTL-01"},
	"project":     {"K-BUD-01", "K-BUD-02", "K-BUD-03", "K-RSK-01"},
	"finance":     {"K-CSH-01", "K-CSH-02", "K-CSH-03", "K-CSH-04", "K-LED-01", "K-CTL-01", "K-BUD-01"},
	"procurement": {"K-BUD-01", "K-BUD-02", "K-CTL-01"},
}

// KPIDefinition documents a KPI for the catalogue and for "How is this calculated?".
type KPIDefinition struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Formula    string `json:"formula"`
	Source     string `json:"source"`
	Thresholds string `json:"thresholds"`
}

// KPICatalogue is the single list of KPIs. docs/reports/KPI_CATALOGUE.md is
// generated from the same text by TestKPICatalogueDocumentIsCurrent.
var KPICatalogue = []KPIDefinition{
	{"K-BUD-01", "Budget consumed", "(committed + reserved + other actual cost) / current budget, summed over the projects of the legal entity that you may see. Committed = approved purchase orders (or what was received, if more); reserved = approved requisitions not yet ordered; other actual cost = posted cost not already inside commitments.",
		"cost report (p2p_project_cost_report) per project", "ok below 85 %; watch from 85 % to 100 %; alert above 100 %"},
	{"K-BUD-02", "Over-budget cost codes", "Number of cost codes whose available budget is below zero. Available = current budget - committed - reserved - other actual cost.",
		"cost report per project", "ok at 0; alert above 0"},
	{"K-BUD-03", "Current budget", "Sum of the current budget over the same projects: original + approved revisions + transfers in - out + contingency drawdowns in - out. The tile also shows the original budget and the difference.",
		"project_budget_current via the cost report", "informational"},
	{"K-RSK-01", "Projects at high risk", "Number of projects in your scope whose overall assessment is high, out of all projects assessed. The assessment is rule-based (schedule, cost, procurement, cash, controls); see Risks for each rule.",
		"risk assessment (risk-rules/1)", "ok at 0; watch if any medium; alert if any high"},
	{"K-CSH-01", "Cash held", "Sum of the ledger balances of the company's active bank accounts that are linked to a ledger account.",
		"general ledger, company bank accounts", "informational; unknown without a linked bank account"},
	{"K-CSH-02", "Net 90-day position", "Cash held + receivables due within 90 days (and overdue) - payables due within 90 days (and overdue), from approved invoices only.",
		"cash outlook", "alert if negative; watch if below a quarter of payables; otherwise ok"},
	{"K-CSH-03", "Overdue payables", "Total of approved supplier invoices past their due date and not fully paid.",
		"payables aging", "ok at 0; watch above 0"},
	{"K-CSH-04", "Overdue receivables", "Total of approved customer invoices past their due date and not fully received.",
		"receivables aging", "ok at 0; watch above 0"},
	{"K-LED-01", "Ledger balance", "Total posted debits minus total posted credits at today's date for the legal entity. It is always zero if the books are sound.",
		"trial balance", "ok at 0.00; alert otherwise"},
	{"K-CTL-01", "Control findings to review", "Number of transactions flagged for a second look: duplicate supplier invoice numbers and payments just below the dual-approval threshold.",
		"control review", "ok at 0; watch above 0; alert if any is high"},
}

// Tile is one KPI on a dashboard.
type Tile struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Value    string     `json:"value"`
	Unit     string     `json:"unit,omitempty"` // currency code, "%" or "count"
	Level    string     `json:"level"`          // ok | watch | alert | unknown
	Note     string     `json:"note,omitempty"`
	Formula  string     `json:"formula"`
	Source   string     `json:"source"`
	AsOf     time.Time  `json:"as_of"`
	Drill    string     `json:"drill"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Detail   []Detail   `json:"detail,omitempty"`
}

// Detail is a labelled secondary figure of a tile.
type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// HiddenTile is a tile the caller may not read, and why.
type HiddenTile struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Needs  string `json:"needs"`
	Reason string `json:"reason"`
}

// Dashboard is the response.
type Dashboard struct {
	Preset   string       `json:"preset"`
	EntityID string       `json:"entity_id"`
	Entity   string       `json:"entity"`
	Currency string       `json:"currency"`
	Entities []EntityRef  `json:"entities"`
	Projects int          `json:"projects_in_scope"`
	AsOf     time.Time    `json:"as_of"`
	Tiles    []Tile       `json:"tiles"`
	Hidden   []HiddenTile `json:"hidden,omitempty"`
}

// EntityRef is a legal entity the caller may choose.
type EntityRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

// Dashboard builds the dashboard for a preset and legal entity. An empty
// entityID selects the first legal entity.
func (s *Service) Dashboard(ctx context.Context, p auth.Principal, preset, entityID string) (Dashboard, error) {
	ids, ok := DashboardPresets[preset]
	if !ok {
		return Dashboard{}, fmt.Errorf("%w: unknown dashboard %q", ErrInvalidInput, preset)
	}
	if p.UserID == "" || p.TenantID == "" {
		return Dashboard{}, auth.ErrUnauthenticated
	}
	ref, err := s.P2PReference(ctx, p)
	if err != nil {
		// Without p2p.read the caller has no entity list; finance-only roles still
		// get the entity-level tiles through the entity chosen below.
		if !errors.Is(err, auth.ErrForbidden) {
			return Dashboard{}, err
		}
		ref = Reference{}
	}
	out := Dashboard{Preset: preset, Tiles: []Tile{}, Entities: []EntityRef{}}
	for _, e := range ref.LegalEntities {
		out.Entities = append(out.Entities, EntityRef{ID: e.ID, Name: e.Name, Currency: e.Kind})
	}
	if entityID == "" && len(out.Entities) > 0 {
		entityID = out.Entities[0].ID
	}
	for _, e := range out.Entities {
		if e.ID == entityID {
			out.Entity, out.Currency = e.Name, e.Currency
		}
	}
	if entityID != "" && out.Entity == "" && len(out.Entities) > 0 {
		return Dashboard{}, ErrNotFound // an entity the caller cannot see is indistinguishable from absent
	}
	out.EntityID = entityID
	// Projects of the chosen entity in the caller's scope.
	var projects []projectRef
	for _, pr := range ref.Projects {
		if entityID == "" || pr.Parent == entityID {
			projects = append(projects, projectRef{ID: pr.ID, Code: pr.Code, Name: pr.Name})
		}
	}
	out.Projects = len(projects)
	out.AsOf = time.Now().UTC()

	b := &tileBuilder{s: s, ctx: ctx, p: p, entityID: entityID, currency: out.Currency, projects: projects, asOf: out.AsOf}
	for _, id := range ids {
		def := kpiDef(id)
		tile, needs, err := b.build(id)
		switch {
		case err == nil:
			out.Tiles = append(out.Tiles, tile)
		case errors.Is(err, auth.ErrForbidden):
			out.Hidden = append(out.Hidden, HiddenTile{ID: id, Title: def.Title, Needs: needs, Reason: "your role does not include this view"})
		default:
			return Dashboard{}, fmt.Errorf("dashboard tile %s: %w", id, err)
		}
	}
	return out, nil
}

func kpiDef(id string) KPIDefinition {
	for _, d := range KPICatalogue {
		if d.ID == id {
			return d
		}
	}
	return KPIDefinition{ID: id, Title: id}
}

type projectRef struct{ ID, Code, Name string }

type tileBuilder struct {
	s        *Service
	ctx      context.Context
	p        auth.Principal
	entityID string
	currency string
	projects []projectRef
	asOf     time.Time

	// Computed once, shared by the budget tiles.
	costDone bool
	cost     costTotals
	costErr  error
}

type costTotals struct {
	original, current, committed, reserved, other decimal.Decimal
	overCodes                                     int
	overEvidence, topEvidence                     []Evidence
}

func (b *tileBuilder) base(id, drill string) Tile {
	d := kpiDef(id)
	return Tile{ID: id, Title: d.Title, Formula: d.Formula, Source: d.Source, AsOf: b.asOf, Drill: drill, Level: "unknown"}
}

func (b *tileBuilder) costTotals() (costTotals, error) {
	if b.costDone {
		return b.cost, b.costErr
	}
	b.costDone = true
	var t costTotals
	type contrib struct {
		label, href string
		consumed    decimal.Decimal
		pct         decimal.Decimal
	}
	var all []contrib
	for _, pr := range b.projects {
		rows, err := b.s.ProjectCostReport(b.ctx, b.p, pr.ID)
		if err != nil {
			b.costErr = err
			return t, err
		}
		for _, r := range rows {
			t.original = t.original.Add(r.Original)
			t.current = t.current.Add(r.Current)
			t.committed = t.committed.Add(r.Committed)
			t.reserved = t.reserved.Add(r.Reserved)
			t.other = t.other.Add(r.ActualOther)
			consumed := r.Committed.Add(r.Reserved).Add(r.ActualOther)
			href := "#/cost/" + pr.ID
			label := pr.Code + " " + r.CostCode
			if r.Available.IsNegative() {
				t.overCodes++
				t.overEvidence = append(t.overEvidence, Evidence{fmt.Sprintf("%s: %s over budget", label, money2(r.Available.Neg())), href})
			}
			pct := decimal.Zero
			if r.Current.IsPositive() {
				pct = consumed.Div(r.Current).Mul(decimal.NewFromInt(100))
			}
			all = append(all, contrib{label: label, href: href, consumed: consumed, pct: pct})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].consumed.GreaterThan(all[j].consumed) })
	for i, c := range all {
		if i == 5 {
			break
		}
		t.topEvidence = append(t.topEvidence, Evidence{fmt.Sprintf("%s: %s consumed (%s %%)", c.label, money2(c.consumed), c.pct.StringFixed(1)), c.href})
	}
	b.cost = t
	return t, nil
}

// build returns the tile, or auth.ErrForbidden with the permission it needs.
func (b *tileBuilder) build(id string) (Tile, string, error) {
	hundred := decimal.NewFromInt(100)
	switch id {
	case "K-BUD-01", "K-BUD-02", "K-BUD-03":
		t, err := b.costTotals()
		if err != nil {
			return Tile{}, string(auth.PermP2PRead), err
		}
		tile := b.base(id, "#/cost")
		switch id {
		case "K-BUD-01":
			consumed := t.committed.Add(t.reserved).Add(t.other)
			tile.Unit = "%"
			if !t.current.IsPositive() {
				tile.Value, tile.Note = "-", "No approved budget yet for these projects."
				return tile, "", nil
			}
			pct := consumed.Div(t.current).Mul(hundred)
			tile.Value = pct.StringFixed(1)
			tile.Level = map[bool]string{true: "ok", false: "watch"}[pct.LessThan(decimal.NewFromInt(85))]
			if pct.GreaterThan(hundred) {
				tile.Level = "alert"
			}
			tile.Detail = []Detail{{"Committed", money2(t.committed)}, {"Reserved", money2(t.reserved)}, {"Other actual cost", money2(t.other)},
				{"Current budget", money2(t.current)}}
			tile.Evidence = t.topEvidence
		case "K-BUD-02":
			tile.Unit, tile.Value = "count", fmt.Sprint(t.overCodes)
			tile.Level = map[bool]string{true: "ok", false: "alert"}[t.overCodes == 0]
			tile.Evidence = t.overEvidence
		case "K-BUD-03":
			tile.Unit, tile.Value = b.currency, money2(t.current)
			tile.Level = "ok"
			tile.Detail = []Detail{{"Original budget", money2(t.original)}, {"Revisions, transfers and drawdowns", money2(t.current.Sub(t.original))}}
			tile.Drill = "#/budget"
		}
		return tile, "", nil

	case "K-RSK-01":
		as, err := b.s.PortfolioRisks(b.ctx, b.p)
		if err != nil {
			return Tile{}, string(auth.PermIntelligenceRead), err
		}
		tile := b.base(id, "#/risk")
		high, medium := 0, 0
		for _, a := range as {
			switch a.Overall {
			case "high":
				high++
				tile.Evidence = append(tile.Evidence, Evidence{a.Code + " " + a.Name + ": high", "#/risk"})
			case "medium":
				medium++
			}
		}
		tile.Unit, tile.Value = "count", fmt.Sprint(high)
		tile.Note = fmt.Sprintf("of %d projects assessed", len(as))
		tile.Level = "ok"
		if medium > 0 {
			tile.Level = "watch"
		}
		if high > 0 {
			tile.Level = "alert"
		}
		return tile, "", nil

	case "K-CSH-01", "K-CSH-02", "K-CSH-03", "K-CSH-04":
		c, err := b.s.CashOutlookOf(b.ctx, b.p, b.entityID)
		if err != nil {
			return Tile{}, string(auth.PermIntelligenceRead) + " with tenant-wide scope", err
		}
		tile := b.base(id, "#/aging/ap")
		tile.Unit = b.currency
		switch id {
		case "K-CSH-01":
			tile.Value, tile.Drill = c.CashHeld, "#/bank"
			if c.BankAccounts == 0 {
				tile.Level, tile.Note = "unknown", "Needs an active company bank account linked to a ledger account."
			} else {
				tile.Level, tile.Note = "ok", fmt.Sprintf("%d bank account(s)", c.BankAccounts)
			}
		case "K-CSH-02":
			tile.Value = c.Net90
			tile.Level = map[string]string{"high": "alert", "medium": "watch", "low": "ok"}[c.Level]
			tile.Detail = []Detail{{"Cash held", c.CashHeld}, {"Receivables due (90 days)", c.Receivables["total"]}, {"Payables due (90 days)", c.Payables["total"]}}
			tile.Drill = "#/risk"
		case "K-CSH-03":
			tile.Value, tile.Drill = c.Payables["overdue"], "#/aging/ap"
			tile.Level = map[bool]string{true: "ok", false: "watch"}[parseMoney(c.Payables["overdue"]).IsZero()]
		case "K-CSH-04":
			tile.Value, tile.Drill = c.Receivables["overdue"], "#/aging/ar"
			tile.Level = map[bool]string{true: "ok", false: "watch"}[parseMoney(c.Receivables["overdue"]).IsZero()]
		}
		return tile, "", nil

	case "K-LED-01":
		tb, err := b.s.TrialBalanceReport(b.ctx, b.p, b.entityID, b.asOf)
		if err != nil {
			return Tile{}, string(auth.PermJournalRead) + " with tenant-wide scope", err
		}
		tile := b.base(id, "#/tb")
		tile.Unit, tile.Value = b.currency, money2(tb.Difference)
		tile.Level = map[bool]string{true: "ok", false: "alert"}[tb.Balanced]
		tile.Detail = []Detail{{"Total debits", money2(tb.TotalDebit)}, {"Total credits", money2(tb.TotalCredit)}}
		return tile, "", nil

	case "K-CTL-01":
		fs, err := b.s.ControlAnomalies(b.ctx, b.p, b.entityID)
		if err != nil {
			return Tile{}, string(auth.PermIntelligenceRead) + " with tenant-wide scope", err
		}
		tile := b.base(id, "#/controls")
		tile.Unit, tile.Value = "count", fmt.Sprint(len(fs))
		tile.Level = "ok"
		for _, f := range fs {
			if tile.Level == "ok" {
				tile.Level = "watch"
			}
			if f.Level == "high" {
				tile.Level = "alert"
			}
			if len(tile.Evidence) < 5 {
				ev := Evidence{Label: f.Title, Href: "#/controls"}
				if len(f.Evidence) > 0 {
					ev.Href = f.Evidence[0].Href
				}
				tile.Evidence = append(tile.Evidence, ev)
			}
		}
		return tile, "", nil
	}
	return Tile{}, "", fmt.Errorf("%w: unknown KPI %s", ErrInvalidInput, id)
}

func parseMoney(s string) decimal.Decimal {
	d, err := decimal.NewFromString(removeCommas(s))
	if err != nil {
		return decimal.Zero
	}
	return d
}

func removeCommas(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != ',' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// KPICatalogueMarkdown renders docs/reports/KPI_CATALOGUE.md from KPICatalogue,
// so the document and the code cannot disagree (a test fails if they do).
func KPICatalogueMarkdown() string {
	out := "# KPI catalogue: financial control dashboard\n\n" +
		"Generated from `internal/kernel/dashboard.go` (`KPICatalogue`); do not edit by hand. Regenerate with\n" +
		"`UPDATE_KPI_DOC=1 go test ./internal/kernel -run TestKPICatalogueDocumentIsCurrent`.\n\n" +
		"Every number on the dashboard is computed by the server from posted and approved records at the moment\n" +
		"the page is opened (nothing is cached), within the caller's permissions and project scope. Each tile\n" +
		"links to the page holding the rows behind it (drill-down) and lists its largest contributors, which\n" +
		"link to their documents. A tile a role may not read is shown as hidden, with the permission it needs.\n\n" +
		"| ID | KPI | Formula | Source | Thresholds |\n|---|---|---|---|---|\n"
	for _, d := range KPICatalogue {
		out += "| " + d.ID + " | " + d.Title + " | " + d.Formula + " | " + d.Source + " | " + d.Thresholds + " |\n"
	}
	out += "\n## Role views (presets)\n\nA preset only chooses which tiles to show; it never widens what a person may read.\n\n| Preset | Tiles |\n|---|---|\n"
	for _, name := range []string{"executive", "project", "finance", "procurement"} {
		ids := ""
		for i, id := range DashboardPresets[name] {
			if i > 0 {
				ids += ", "
			}
			ids += id
		}
		out += "| " + name + " | " + ids + " |\n"
	}
	return out
}
