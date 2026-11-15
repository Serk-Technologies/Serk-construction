package httpapi

import "testing"

// TestAPIRecurringJournalApproval (DEF-013, migration 0083): a recurring
// journal template runs only after someone other than its maker approved it
// over the API with a fresh second factor; before that, running creates nothing.
func TestAPIRecurringJournalApproval(t *testing.T) {
	e := newEnv(t)
	maker := e.tok("u-rec-maker", nil, true, "accountant")
	stale := e.tok("u-rec-approver", nil, false, "finance_manager")
	approver := e.tok("u-rec-approver", nil, true, "finance_manager")

	st, out := e.do("POST", "/v1/recurring-journals", maker, map[string]any{"legal_entity_id": e.entity, "name": "Site office rent", "first_run_on": "2026-10-05",
		"ends_on": "2026-10-31", "lines": []map[string]any{{"account_id": e.accounts["5000"], "side": "DEBIT", "amount": "1500.00"}, {"account_id": e.accounts["1000"], "side": "CREDIT", "amount": "1500.00"}}})
	if st != 201 {
		t.Fatalf("template: %d %v", st, out)
	}
	id := out["id"].(string)
	run := func(want int) {
		t.Helper()
		st, out := e.do("POST", "/v1/recurring-journals/run", maker, map[string]any{"as_of": "2026-10-10"})
		if st != 200 || len(out["journals"].([]any)) != want {
			t.Fatalf("run: want %d journal(s), got %d %v", want, st, out)
		}
	}
	run(0) // not yet approved
	for _, c := range []struct {
		who, tok string
	}{{"its maker", maker}, {"an approver without a fresh second factor", stale}} {
		if st, out := e.do("POST", "/v1/recurring-journals/"+id+"/approve", c.tok, nil); st == 200 {
			t.Fatalf("%s approved the template: %v", c.who, out)
		}
	}
	if st, out := e.do("POST", "/v1/recurring-journals/"+id+"/approve", approver, nil); st != 200 || out["status"] != "APPROVED" {
		t.Fatalf("approve: %d %v", st, out)
	}
	if st, _ := e.do("POST", "/v1/recurring-journals/"+id+"/approve", approver, nil); st != 409 {
		t.Fatalf("a second approval: %d", st)
	}
	run(1)
	st, v := e.do("GET", "/v1/period-end", approver, nil)
	if st != 200 {
		t.Fatalf("period end: %d %v", st, v)
	}
	rec := v["recurring"].([]any)[0].(map[string]any)
	if rec["approved_by"] != "u-rec-approver" || rec["created_by"] != "u-rec-maker" {
		t.Fatalf("listed template: %v", rec)
	}
}
