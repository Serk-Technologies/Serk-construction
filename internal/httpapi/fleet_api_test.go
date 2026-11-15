package httpapi

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// SERK FLEET through the API (0054): roles reach only their routes; the
// register pages by cursor; commissioning and certification need fresh MFA; a
// second overlapping reservation answers 409 double_booked; a machine goes to
// a project only on that project's reservation; logs are approved by a second
// person and feed the summary; other projects get "not found".
func TestAPIFleet(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	officer := a.tok("u-fl-officer", nil, false, "plant_officer")
	manager, managerMFA := a.tok("u-fl-manager", nil, false, "plant_manager"), a.tok("u-fl-manager", nil, true, "plant_manager")
	inspectorMFA := a.tok("u-fl-inspector", nil, true, "plant_inspector")
	eng := a.tok("u-fl-eng", P, false, "site_engineer")
	pm := a.tok("u-fl-pm", P, false, "project_manager")
	elsewhere := a.tok("u-fl-else", []string{uuid.NewString()}, true, "site_engineer", "project_manager")
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }
	ok := func(what string, want, code int, out map[string]any) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: %d %v", what, code, out)
		}
	}

	eq := map[string]any{"legal_entity_id": a.entity, "name": "Grader 01", "class": "GRADER", "ownership": "OWNED", "manufacturer": "CAT", "model": "140K", "serial_number": "CAT140K-0091", "opening_meter": "500"}
	code, out := a.do("POST", "/v1/equipment", eng, eq)
	ok("a site engineer registered equipment", 403, code, out)
	code, out = a.do("POST", "/v1/equipment", officer, eq)
	ok("register", 201, code, out)
	id := out["id"].(string)
	code, out = a.do("POST", "/v1/equipment", officer, eq)
	ok("duplicate serial", 422, code, out)
	for i := 0; i < 4; i++ {
		e2 := map[string]any{}
		for k, v := range eq {
			e2[k] = v
		}
		e2["serial_number"], e2["name"] = fmt.Sprintf("CAT140K-01%02d", i), fmt.Sprintf("Grader 1%d", i)
		code, out = a.do("POST", "/v1/equipment", officer, e2)
		ok("register more", 201, code, out)
	}
	// Cursor paging: 5 machines, 2 a page, each once.
	seen, cursor := map[string]bool{}, ""
	for pages := 0; pages < 5; pages++ {
		code, out = a.do("GET", "/v1/equipment?limit=2&cursor="+cursor, eng, nil)
		ok("page", 200, code, out)
		for _, it := range out["items"].([]any) {
			seen[it.(map[string]any)["id"].(string)] = true
		}
		next, _ := out["next"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("paged register returned %d machines", len(seen))
	}
	code, out = a.do("GET", "/v1/equipment?cursor=!!", eng, nil)
	ok("bad cursor", 422, code, out)

	move := func(tok, to, reason, project string) (int, map[string]any) {
		return a.do("POST", "/v1/equipment/"+id+"/move", tok, map[string]any{"to": to, "reason": reason, "project_id": project})
	}
	code, out = move(manager, "AVAILABLE", "Commissioned", "")
	ok("commissioning without fresh MFA", 401, code, out)
	code, out = move(officer, "AVAILABLE", "Commissioned", "")
	ok("commissioning by the registrar's role", 403, code, out)
	code, out = move(managerMFA, "AVAILABLE", "Commissioned after inspection", "")
	ok("commission", 200, code, out)
	code, out = move(officer, "MOBILIZING", "Dispatched", a.project)
	if code == 200 {
		t.Fatalf("mobilised without a reservation: %v", out)
	}

	res := map[string]any{"equipment_id": id, "project_id": a.project, "purpose": "Subgrade trimming", "starts_on": day(0), "ends_on": day(14)}
	code, out = a.do("POST", "/v1/equipment-reservations", elsewhere, res)
	ok("reservation for another project", 403, code, out)
	code, out = a.do("POST", "/v1/equipment-reservations", eng, res)
	ok("reservation", 201, code, out)
	r1 := out["id"].(string)
	code, out = a.do("POST", "/v1/equipment-reservations/"+r1+"/submit", eng, nil)
	ok("submit", 200, code, out)
	code, out = a.do("POST", "/v1/equipment-reservations/"+r1+"/approve", manager, nil)
	ok("approve without MFA", 401, code, out)
	code, out = a.do("POST", "/v1/equipment-reservations/"+r1+"/approve", managerMFA, nil)
	ok("approve", 200, code, out)
	res["starts_on"], res["ends_on"] = day(14), day(20)
	code, out = a.do("POST", "/v1/equipment-reservations", eng, res)
	ok("second reservation", 201, code, out)
	r2 := out["id"].(string)
	a.do("POST", "/v1/equipment-reservations/"+r2+"/submit", eng, nil)
	code, out = a.do("POST", "/v1/equipment-reservations/"+r2+"/approve", managerMFA, nil)
	if code != 409 || out["error"].(map[string]any)["code"] != "double_booked" {
		t.Fatalf("overlapping reservation: %d %v", code, out)
	}
	code, out = a.do("GET", "/v1/equipment/"+id+"/calendar?from="+day(0)+"&to="+day(30), pm, nil)
	if code != 200 || len(out["reservations"].([]any)) != 2 {
		t.Fatalf("calendar: %d %v", code, out)
	}

	code, out = move(officer, "MOBILIZING", "Low-bed dispatched", a.project)
	ok("mobilise", 200, code, out)
	code, out = move(elsewhere, "ASSIGNED", "Arrived", "")
	ok("received by another project", 404, code, out)
	code, out = move(eng, "ASSIGNED", "Arrived on site", "")
	ok("arrive", 200, code, out)
	code, out = move(eng, "SAFETY_HOLD", "Brake warning light", "")
	ok("hold", 200, code, out)
	code, out = move(eng, "IN_SERVICE", "Looks fine", "")
	ok("site staff released a hold", 403, code, out)
	code, out = move(inspectorMFA, "IN_SERVICE", "Brake circuit bled and tested", "")
	if code != 200 || out["status"] != "ASSIGNED" {
		t.Fatalf("certify: %d %v", code, out)
	}
	code, out = a.do("GET", "/v1/equipment/"+id, pm, nil)
	if code != 200 || len(out["history"].([]any)) != 7 || out["status"] != "ASSIGNED" {
		t.Fatalf("detail: %d %v", code, out)
	}

	// Operator authorisation, a log, its approval, the summary.
	code, out = a.do("POST", "/v1/equipment-operators", officer, map[string]any{"operator": "op-hagos", "equipment_class": "GRADER", "certificate_ref": "TVET/GRD/118", "valid_from": day(-10), "valid_until": day(200)})
	ok("operator", 201, code, out)
	op := out["id"].(string)
	a.do("POST", "/v1/equipment-operators/"+op+"/submit", officer, nil)
	code, out = a.do("POST", "/v1/equipment-operators/"+op+"/approve", inspectorMFA, nil)
	ok("approve operator", 200, code, out)
	log := map[string]any{"equipment_id": id, "project_id": a.project, "log_date": day(0), "operator": "op-hagos", "meter_open": "500", "meter_close": "507.5",
		"productive_hours": "6", "idle_hours": "1.5", "down_hours": "0.5", "fuel_litres": "75", "client_ref": "tablet7-000000001"}
	code, out = a.do("POST", "/v1/equipment-logs", pm, log)
	ok("log without the permission", 403, code, out)
	code, out = a.do("POST", "/v1/equipment-logs", eng, log)
	ok("log", 201, code, out)
	l1 := out["id"].(string)
	code, out = a.do("POST", "/v1/equipment-logs", eng, log)
	if code != 201 || out["id"] != l1 {
		t.Fatalf("offline repeat: %d %v (want the same log %s)", code, out, l1)
	}
	log["productive_hours"] = "7"
	code, out = a.do("POST", "/v1/equipment-logs", eng, log)
	ok("offline repeat with other figures", 409, code, out)
	a.do("POST", "/v1/equipment-logs/"+l1+"/submit", eng, nil)
	code, out = a.do("POST", "/v1/equipment-logs/"+l1+"/approve", elsewhere, nil)
	ok("approved from another project", 404, code, out)
	code, out = a.do("POST", "/v1/equipment-logs/"+l1+"/approve", pm, nil)
	ok("approve log", 200, code, out)
	code, out = a.do("GET", "/v1/fleet/summary?from="+day(-7)+"&to="+day(0), pm, nil)
	usage := out["usage"].([]any)
	if code != 200 || len(usage) != 1 || usage[0].(map[string]any)["utilization"] != "0.75" || usage[0].(map[string]any)["fuel_litres_per_hour"] != "10" || out["total"] != float64(5) {
		t.Fatalf("summary: %d %v", code, out)
	}
	code, out = a.do("GET", "/v1/fleet/summary?from="+day(-7)+"&to="+day(0), elsewhere, nil)
	if code != 200 || len(out["usage"].([]any)) != 0 {
		t.Fatalf("another project's summary shows usage: %d %v", code, out)
	}
	code, out = a.do("GET", "/v1/fleet/summary?from="+day(-900)+"&to="+day(0), pm, nil)
	ok("unbounded period", 422, code, out)
	code, out = a.do("GET", "/v1/p2p/equipment_log/"+l1, pm, nil)
	if code != 200 || out["status"] != "APPROVED" {
		t.Fatalf("log as a document: %d %v", code, out)
	}
}
