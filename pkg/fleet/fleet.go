// Package fleet holds the equipment lifecycle state machine and the pure
// arithmetic of fleet figures. The database enforces the same state machine
// (equipment_transition_perm, migration 0054); a test proves the two agree on
// every pair of states.
package fleet

import "github.com/shopspring/decimal"

// States, in the order of a machine's life.
var States = []string{"REGISTERED", "AVAILABLE", "RESERVED", "MOBILIZING", "ASSIGNED", "OPERATING", "IDLE",
	"UNDER_INSPECTION", "UNDER_MAINTENANCE", "BREAKDOWN", "SAFETY_HOLD", "DEMOBILIZING", "RETIRED", "DISPOSED"}

// Classes of equipment.
var Classes = []string{"EXCAVATOR", "GRADER", "LOADER", "BULLDOZER", "CRANE", "GENERATOR", "TRUCK", "MIXER", "BATCHING_PLANT", "COMPACTOR", "TOOL", "OTHER"}

// Permissions a move can need. Automatic means nobody decides the move: it
// follows from the approved reservations.
const (
	Automatic = ""
	Approve   = "fleet.approve"
	Operate   = "fleet.operate"
	Release   = "fleet.release"
)

func in(s string, set ...string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

// TransitionPerm returns the permission a move needs; ok is false when the
// move does not exist.
func TransitionPerm(from, to string) (perm string, ok bool) {
	active := []string{"AVAILABLE", "RESERVED", "MOBILIZING", "ASSIGNED", "OPERATING", "IDLE", "DEMOBILIZING"}
	switch {
	case from == "REGISTERED" && to == "AVAILABLE":
		return Approve, true
	case from == "AVAILABLE" && to == "RESERVED", from == "RESERVED" && to == "AVAILABLE":
		return Automatic, true
	case in(from, "AVAILABLE", "RESERVED") && to == "MOBILIZING",
		from == "MOBILIZING" && to == "ASSIGNED",
		in(from, "ASSIGNED", "IDLE") && to == "OPERATING",
		from == "OPERATING" && in(to, "IDLE", "ASSIGNED"),
		from == "IDLE" && to == "ASSIGNED",
		in(from, "MOBILIZING", "ASSIGNED", "OPERATING", "IDLE") && to == "DEMOBILIZING",
		from == "DEMOBILIZING" && in(to, "AVAILABLE", "RESERVED"),
		in(from, active...) && in(to, "BREAKDOWN", "SAFETY_HOLD", "UNDER_INSPECTION", "UNDER_MAINTENANCE"),
		from == "BREAKDOWN" && in(to, "UNDER_MAINTENANCE", "SAFETY_HOLD"),
		from == "UNDER_INSPECTION" && in(to, "UNDER_MAINTENANCE", "SAFETY_HOLD"),
		from == "UNDER_MAINTENANCE" && in(to, "UNDER_INSPECTION", "SAFETY_HOLD"),
		from == "SAFETY_HOLD" && in(to, "UNDER_INSPECTION", "UNDER_MAINTENANCE"):
		return Operate, true
	case in(from, "UNDER_INSPECTION", "UNDER_MAINTENANCE", "SAFETY_HOLD") && in(to, "AVAILABLE", "RESERVED", "ASSIGNED"):
		return Release, true
	case from == "AVAILABLE" && to == "RETIRED", from == "RETIRED" && to == "DISPOSED":
		return Approve, true
	}
	return "", false
}

// InService reports whether a machine in this state may be put to work.
func InService(state string) bool {
	return in(state, "AVAILABLE", "RESERVED", "MOBILIZING", "ASSIGNED", "OPERATING", "IDLE", "DEMOBILIZING")
}

// Moves lists the states reachable from a state by a person holding has().
func Moves(from string, has func(perm string) bool) []string {
	var out []string
	for _, to := range States {
		if perm, ok := TransitionPerm(from, to); ok && perm != Automatic && has(perm) {
			out = append(out, to)
		}
	}
	return out
}

// Hours is what approved logs say a machine did in a period.
type Hours struct{ Productive, Idle, Down decimal.Decimal }

// Utilization is productive hours over all logged hours, and availability is
// the share of logged hours the machine was not down. Both are nil (not zero)
// when nothing was logged: an absent figure is not a bad figure.
func (h Hours) Utilization() *decimal.Decimal {
	total := h.Productive.Add(h.Idle).Add(h.Down)
	if !total.IsPositive() {
		return nil
	}
	u := h.Productive.Div(total).Round(4)
	return &u
}

func (h Hours) Availability() *decimal.Decimal {
	total := h.Productive.Add(h.Idle).Add(h.Down)
	if !total.IsPositive() {
		return nil
	}
	a := h.Productive.Add(h.Idle).Div(total).Round(4)
	return &a
}
