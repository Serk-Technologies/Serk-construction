package fleet

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestLifecycleRules(t *testing.T) {
	// Nothing leads from an out-of-service state to work except certification.
	for _, from := range []string{"SAFETY_HOLD", "UNDER_MAINTENANCE", "UNDER_INSPECTION"} {
		for _, to := range States {
			perm, ok := TransitionPerm(from, to)
			if ok && InService(to) && perm != Release {
				t.Errorf("%s -> %s needs %q, want certification", from, to, perm)
			}
			if ok && in(to, "MOBILIZING", "OPERATING", "IDLE", "DEMOBILIZING") {
				t.Errorf("%s -> %s exists: a held machine was put to work", from, to)
			}
		}
	}
	// A breakdown is never returned to work directly.
	for _, to := range States {
		if _, ok := TransitionPerm("BREAKDOWN", to); ok && InService(to) {
			t.Errorf("BREAKDOWN -> %s exists", to)
		}
	}
	// Disposal is the end; retirement leads only to disposal; nothing returns to REGISTERED.
	for _, to := range States {
		if _, ok := TransitionPerm("DISPOSED", to); ok {
			t.Errorf("DISPOSED -> %s exists", to)
		}
		if _, ok := TransitionPerm("RETIRED", to); ok && to != "DISPOSED" {
			t.Errorf("RETIRED -> %s exists", to)
		}
		if _, ok := TransitionPerm(to, "REGISTERED"); ok {
			t.Errorf("%s -> REGISTERED exists", to)
		}
		if _, ok := TransitionPerm(to, to); ok {
			t.Errorf("%s -> itself exists", to)
		}
	}
	// A machine reaches a project only through MOBILIZING, and only from the yard.
	for _, from := range States {
		if _, ok := TransitionPerm(from, "MOBILIZING"); ok && !in(from, "AVAILABLE", "RESERVED") {
			t.Errorf("%s -> MOBILIZING exists", from)
		}
		if perm, ok := TransitionPerm(from, "ASSIGNED"); ok && !in(from, "MOBILIZING", "OPERATING", "IDLE") && perm != Release {
			t.Errorf("%s -> ASSIGNED without mobilisation", from)
		}
	}
	// Anyone who operates can stop any machine that is in service, in one move.
	for _, from := range States {
		if !InService(from) {
			continue
		}
		for _, stop := range []string{"SAFETY_HOLD", "BREAKDOWN"} {
			if perm, ok := TransitionPerm(from, stop); !ok || perm != Operate {
				t.Errorf("%s -> %s: %q %v", from, stop, perm, ok)
			}
		}
	}
	// Every state except DISPOSED has a way out, and every state except REGISTERED a way in.
	for _, s := range States {
		out, into := 0, 0
		for _, o := range States {
			if _, ok := TransitionPerm(s, o); ok {
				out++
			}
			if _, ok := TransitionPerm(o, s); ok {
				into++
			}
		}
		if (out == 0) != (s == "DISPOSED") || (into == 0) != (s == "REGISTERED") {
			t.Errorf("%s: %d ways out, %d ways in", s, out, into)
		}
	}
	operator := func(p string) bool { return p == Operate }
	if m := Moves("SAFETY_HOLD", operator); len(m) != 2 { // inspection or maintenance only
		t.Errorf("an operator can move a held machine to %v", m)
	}
	if m := Moves("AVAILABLE", func(string) bool { return true }); in("RESERVED", m...) {
		t.Errorf("RESERVED offered as a decision: %v", m)
	}
}

func TestHoursRatios(t *testing.T) {
	d := decimal.RequireFromString
	if (Hours{}).Utilization() != nil || (Hours{}).Availability() != nil {
		t.Fatal("no logged hours must give no figure, not zero")
	}
	h := Hours{Productive: d("6"), Idle: d("2"), Down: d("4")}
	if u := h.Utilization(); u == nil || !u.Equal(d("0.5")) {
		t.Fatalf("utilization %v", u)
	}
	if a := h.Availability(); a == nil || !a.Equal(d("0.6667")) {
		t.Fatalf("availability %v", a)
	}
	if u := (Hours{Down: d("8")}).Utilization(); u == nil || !u.IsZero() {
		t.Fatalf("a machine that was down all shift has utilization %v, want 0", u)
	}
}
