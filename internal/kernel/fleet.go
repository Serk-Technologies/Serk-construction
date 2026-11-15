// SERK FLEET, part 1 (requirements FLT-01.x; ADR 0020; migration 0054):
// the equipment register, its lifecycle, reservations, operator
// authorisations, daily operation logs and meter control.
//
// The database is the authority on every rule here (state machine, no
// double-booking, meter continuity, who is at which project); the kernel
// checks permission and project scope first, attests the person's decision
// and gives clear messages.
package kernel

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/fleet"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

// ErrDoubleBooked: the equipment already has an approved reservation in that period.
var ErrDoubleBooked = errors.New("kernel: the equipment is already reserved in that period")

// EquipmentInput registers a machine, plant item or tool.
type EquipmentInput struct {
	LegalEntityID   string           `json:"legal_entity_id"`
	Name            string           `json:"name"`
	Class           string           `json:"class"`
	Ownership       string           `json:"ownership"`
	Manufacturer    string           `json:"manufacturer"`
	Model           string           `json:"model"`
	SerialNumber    string           `json:"serial_number"`
	EngineNumber    string           `json:"engine_number,omitempty"`
	Registration    string           `json:"registration,omitempty"`
	Capacity        string           `json:"capacity,omitempty"`
	FuelType        string           `json:"fuel_type,omitempty"`  // DIESEL (default) | PETROL | ELECTRIC | NONE
	MeterKind       string           `json:"meter_kind,omitempty"` // HOURS (default) | KM | NONE
	OpeningMeter    decimal.Decimal  `json:"opening_meter"`
	SupplierID      string           `json:"supplier_id,omitempty"`
	AcquiredOn      string           `json:"acquired_on,omitempty"`
	AcquisitionCost *decimal.Decimal `json:"acquisition_cost,omitempty"`
	Currency        string           `json:"currency,omitempty"`
	WarrantyUntil   string           `json:"warranty_until,omitempty"`
	InsuranceUntil  string           `json:"insurance_until,omitempty"`
	HomeLocation    string           `json:"home_location,omitempty"`
}

// Equipment is a register entry.
type Equipment struct {
	ID               string                 `json:"id"`
	Number           string                 `json:"number"`
	LegalEntityID    string                 `json:"legal_entity_id"`
	Name             string                 `json:"name"`
	Class            string                 `json:"class"`
	Ownership        string                 `json:"ownership"`
	Manufacturer     string                 `json:"manufacturer"`
	Model            string                 `json:"model"`
	SerialNumber     string                 `json:"serial_number"`
	EngineNumber     string                 `json:"engine_number,omitempty"`
	Registration     string                 `json:"registration,omitempty"`
	Capacity         string                 `json:"capacity,omitempty"`
	FuelType         string                 `json:"fuel_type"`
	MeterKind        string                 `json:"meter_kind"`
	LastMeter        decimal.Decimal        `json:"last_meter"`
	MeterSerial      int                    `json:"meter_serial"`
	Status           string                 `json:"status"`
	CurrentProjectID string                 `json:"current_project_id,omitempty"`
	StatusReason     string                 `json:"status_reason,omitempty"`
	StatusChangedBy  string                 `json:"status_changed_by"`
	StatusChangedAt  time.Time              `json:"status_changed_at"`
	WarrantyUntil    string                 `json:"warranty_until,omitempty"`
	InsuranceUntil   string                 `json:"insurance_until,omitempty"`
	HomeLocation     string                 `json:"home_location,omitempty"`
	CreatedBy        string                 `json:"created_by"`
	CreatedAt        time.Time              `json:"created_at"`
	Moves            []string               `json:"moves,omitempty"`   // states this caller may move it to now
	History          []EquipmentEvent       `json:"history,omitempty"` // newest first (detail read)
	Reservations     []EquipmentReservation `json:"reservations,omitempty"`
}

// EquipmentEvent is one line of a machine's lifecycle history.
type EquipmentEvent struct {
	From      string          `json:"from,omitempty"`
	To        string          `json:"to"`
	ProjectID string          `json:"project_id,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Actor     string          `json:"actor"`
	Meter     decimal.Decimal `json:"meter"`
	At        time.Time       `json:"at"`
}

// EquipmentReservation is a project's claim on a machine for a period.
type EquipmentReservation struct {
	ID          string `json:"id"`
	Number      string `json:"number"`
	EquipmentID string `json:"equipment_id"`
	ProjectID   string `json:"project_id"`
	Purpose     string `json:"purpose"`
	StartsOn    string `json:"starts_on"`
	EndsOn      string `json:"ends_on"`
	Status      string `json:"status"`
	CreatedBy   string `json:"created_by"`
}

func oneOf(v string, set ...string) bool {
	for _, x := range set {
		if v == x {
			return true
		}
	}
	return false
}

func optDate(name, v string) (*time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be a date (YYYY-MM-DD)", ErrInvalidInput, name)
	}
	return &t, nil
}

// fleetErr turns the database's refusals into the kernel's errors.
func fleetErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.ConstraintName {
		case "reservation_no_double_booking":
			return ErrDoubleBooked
		case "equipment_serial_once":
			return fmt.Errorf("%w: equipment with this manufacturer and serial number is already registered", ErrInvalidInput)
		case "equipment_registration_once":
			return fmt.Errorf("%w: equipment with this registration is already registered", ErrInvalidInput)
		case "equipment_logs_once":
			return fmt.Errorf("%w: this equipment already has a log for that day and shift", ErrInvalidInput)
		}
	}
	if err != nil && strings.Contains(err.Error(), "reservation_no_double_booking") {
		return ErrDoubleBooked
	}
	return err
}

// RegisterEquipment enters equipment in the register (REGISTERED). It is put
// into service by a different person (commissioning).
func (s *Service) RegisterEquipment(ctx context.Context, p auth.Principal, in EquipmentInput) (string, error) {
	in.Class, in.Ownership = strings.ToUpper(strings.TrimSpace(in.Class)), strings.ToUpper(strings.TrimSpace(in.Ownership))
	in.FuelType, in.MeterKind = strings.ToUpper(strings.TrimSpace(in.FuelType)), strings.ToUpper(strings.TrimSpace(in.MeterKind))
	if in.FuelType == "" {
		in.FuelType = "DIESEL"
	}
	if in.MeterKind == "" {
		in.MeterKind = "HOURS"
	}
	switch {
	case !oneOf(in.Class, fleet.Classes...):
		return "", fmt.Errorf("%w: class must be one of %s", ErrInvalidInput, strings.Join(fleet.Classes, ", "))
	case !oneOf(in.Ownership, "OWNED", "RENTED", "LEASED", "SUBCONTRACTED"):
		return "", fmt.Errorf("%w: ownership must be OWNED, RENTED, LEASED or SUBCONTRACTED", ErrInvalidInput)
	case !oneOf(in.FuelType, "DIESEL", "PETROL", "ELECTRIC", "NONE"), !oneOf(in.MeterKind, "HOURS", "KM", "NONE"):
		return "", fmt.Errorf("%w: unknown fuel type or meter kind", ErrInvalidInput)
	case len(strings.TrimSpace(in.Name)) < 2, strings.TrimSpace(in.Manufacturer) == "", strings.TrimSpace(in.Model) == "", strings.TrimSpace(in.SerialNumber) == "":
		return "", fmt.Errorf("%w: equipment needs a name, manufacturer, model and serial number", ErrInvalidInput)
	case in.OpeningMeter.IsNegative():
		return "", fmt.Errorf("%w: the opening meter reading cannot be negative", ErrInvalidInput)
	case (in.AcquisitionCost == nil) != (strings.TrimSpace(in.Currency) == ""):
		return "", fmt.Errorf("%w: an acquisition cost needs its currency, and the other way round", ErrInvalidInput)
	}
	acq, err := optDate("acquired_on", in.AcquiredOn)
	if err != nil {
		return "", err
	}
	war, err := optDate("warranty_until", in.WarrantyUntil)
	if err != nil {
		return "", err
	}
	ins, err := optDate("insurance_until", in.InsuranceUntil)
	if err != nil {
		return "", err
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermFleetManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "EQ")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment (legal_entity_id, number, name, class, ownership, manufacturer, model, serial_number, engine_number, registration,
			capacity, fuel_type, meter_kind, last_meter, supplier_id, acquired_on, acquisition_cost, currency, warranty_until, insurance_until, home_location, status_changed_by, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), $12, $13, $14, NULLIF($15, '')::uuid, $16, $17, NULLIF($18, ''), $19, $20, NULLIF($21, ''), $22, $22)
			RETURNING id::text`, in.LegalEntityID, num, strings.TrimSpace(in.Name), in.Class, in.Ownership, strings.TrimSpace(in.Manufacturer), strings.TrimSpace(in.Model),
			strings.TrimSpace(in.SerialNumber), strings.TrimSpace(in.EngineNumber), strings.TrimSpace(in.Registration), strings.TrimSpace(in.Capacity), in.FuelType, in.MeterKind,
			in.OpeningMeter, in.SupplierID, acq, in.AcquisitionCost, strings.ToUpper(strings.TrimSpace(in.Currency)), war, ins, strings.TrimSpace(in.HomeLocation), p.UserID)
		if err != nil {
			return fleetErr(err)
		}
		return s.record(ctx, tx, p, "equipment.create", "equipment", id, "equipment.registered", map[string]any{"equipment_id": id, "number": num, "class": in.Class, "serial_number": in.SerialNumber})
	})
	return id, err
}

// MoveEquipment moves a machine to another lifecycle state. to may be
// "IN_SERVICE": the state the machine rests in when it is returned to service
// or to the yard (ASSIGNED at its project, else RESERVED or AVAILABLE).
// projectID is needed only to mobilise.
func (s *Service) MoveEquipment(ctx context.Context, p auth.Principal, id, to, reason, projectID string) (string, error) {
	to, reason = strings.ToUpper(strings.TrimSpace(to)), strings.TrimSpace(reason)
	if len(reason) < 3 {
		return "", fmt.Errorf("%w: state the reason for the move", ErrInvalidInput)
	}
	if p.UserID == "" || p.TenantID == "" {
		return "", auth.ErrUnauthenticated
	}
	var final string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var from, createdBy string
		var current *string
		if err := tx.QueryRow(ctx, `SELECT status, current_project_id::text, created_by FROM equipment WHERE id = $1::uuid FOR UPDATE`, id).Scan(&from, &current, &createdBy); err != nil {
			return mapNoRows(err)
		}
		target := to
		if to == "IN_SERVICE" {
			keep := current
			if from == "DEMOBILIZING" {
				keep = nil
			}
			if err := tx.QueryRow(ctx, `SELECT equipment_rest_status($1::uuid, $2::uuid)`, id, keep).Scan(&target); err != nil {
				return err
			}
		}
		perm, ok := fleet.TransitionPerm(from, target)
		if !ok || perm == fleet.Automatic {
			return fmt.Errorf("%w: equipment cannot be moved from %s to %s", ledger.ErrBadTransition, from, target)
		}
		if err := s.authz.Require(p, auth.Permission(perm), p.TenantID); err != nil {
			return err
		}
		// Scope: the project the machine is at, and the project it is going to.
		newProject := current
		switch {
		case target == "MOBILIZING":
			if projectID == "" {
				return fmt.Errorf("%w: name the project the equipment is mobilised to", ErrInvalidInput)
			}
			newProject = &projectID
		case oneOf(target, "AVAILABLE", "RESERVED") && from == "DEMOBILIZING":
			newProject = nil
		}
		for _, proj := range []*string{current, newProject} {
			if proj != nil {
				if err := s.authz.RequireProject(p, auth.Permission(perm), p.TenantID, *proj); err != nil {
					return ErrNotFound // outside the caller's projects looks like absence
				}
			}
		}
		if current == nil && !s.tenantWide(p) {
			return ErrNotFound // equipment in the yard is moved by people with company-wide scope
		}
		if perm != fleet.Operate {
			if err := s.authz.RequireStepUp(p); err != nil {
				return err
			}
		}
		if from == "REGISTERED" {
			if err := auth.CheckMakerChecker(p, createdBy, "equipment"); err != nil {
				return err
			}
		}
		if err := s.authorize(ctx, tx, p, "equipment.transition", "equipment", id, target); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE equipment SET status = $2, current_project_id = $3::uuid, status_reason = $4, status_changed_by = $5 WHERE id = $1::uuid`,
			id, target, newProject, reason, p.UserID); err != nil {
			return fleetErr(err)
		}
		final = target
		return s.record(ctx, tx, p, "equipment.transition", "equipment", id, "equipment.moved", map[string]any{"equipment_id": id, "from": from, "to": target, "reason": reason, "project_id": newProject})
	})
	return final, err
}

const equipmentCols = `e.id::text, e.number, e.legal_entity_id::text, e.name, e.class, e.ownership, e.manufacturer, e.model, e.serial_number, COALESCE(e.engine_number, ''), COALESCE(e.registration, ''),
	COALESCE(e.capacity, ''), e.fuel_type, e.meter_kind, e.last_meter, e.meter_serial, e.status, COALESCE(e.current_project_id::text, ''), COALESCE(e.status_reason, ''), e.status_changed_by, e.status_changed_at,
	COALESCE(e.warranty_until::text, ''), COALESCE(e.insurance_until::text, ''), COALESCE(e.home_location, ''), e.created_by, e.created_at`

func scanEquipment(row pgx.Row, e *Equipment) error {
	return row.Scan(&e.ID, &e.Number, &e.LegalEntityID, &e.Name, &e.Class, &e.Ownership, &e.Manufacturer, &e.Model, &e.SerialNumber, &e.EngineNumber, &e.Registration,
		&e.Capacity, &e.FuelType, &e.MeterKind, &e.LastMeter, &e.MeterSerial, &e.Status, &e.CurrentProjectID, &e.StatusReason, &e.StatusChangedBy, &e.StatusChangedAt,
		&e.WarrantyUntil, &e.InsuranceUntil, &e.HomeLocation, &e.CreatedBy, &e.CreatedAt)
}

func (s *Service) movesFor(p auth.Principal, e Equipment) []string {
	scoped := s.tenantWide(p) || (e.CurrentProjectID != "" && s.authz.ScopeOf(p).Allows(e.CurrentProjectID))
	if !scoped {
		return nil
	}
	return fleet.Moves(e.Status, func(perm string) bool { return s.authz.Has(p, auth.Permission(perm)) })
}

// EquipmentFilter narrows the register. Cursor is the value returned as Next
// by the previous page; Limit is capped at 200.
type EquipmentFilter struct {
	Status, Class, ProjectID, Search, Cursor string
	Limit                                    int
}

// EquipmentPage is one page of the register, in number order.
type EquipmentPage struct {
	Items []Equipment `json:"items"`
	Next  string      `json:"next,omitempty"` // pass as cursor for the next page; empty at the end
}

// ListEquipment returns the register a page at a time (keyset pagination: no
// offset, so a page costs the same however deep it is). The register is
// company-wide: anyone who reads documents sees the whole fleet.
func (s *Service) ListEquipment(ctx context.Context, p auth.Principal, f EquipmentFilter) (EquipmentPage, error) {
	out := EquipmentPage{Items: []Equipment{}}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	after := ""
	if f.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if err != nil || len(b) > 100 {
			return out, fmt.Errorf("%w: invalid cursor", ErrInvalidInput)
		}
		after = string(b)
	}
	f.Status, f.Class = strings.ToUpper(strings.TrimSpace(f.Status)), strings.ToUpper(strings.TrimSpace(f.Class))
	if f.Status != "" && !oneOf(f.Status, fleet.States...) || f.Class != "" && !oneOf(f.Class, fleet.Classes...) {
		return out, fmt.Errorf("%w: unknown status or class", ErrInvalidInput)
	}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+equipmentCols+` FROM equipment e
			WHERE ($1 = '' OR e.status = $1) AND ($2 = '' OR e.class = $2) AND ($3 = '' OR e.current_project_id = $3::uuid)
			  AND ($4 = '' OR e.name ILIKE '%' || $4 || '%' OR e.number ILIKE '%' || $4 || '%' OR e.serial_number ILIKE '%' || $4 || '%' OR e.registration ILIKE '%' || $4 || '%')
			  AND e.number COLLATE "C" > $5
			ORDER BY e.number COLLATE "C" LIMIT $6`, f.Status, f.Class, f.ProjectID, strings.TrimSpace(f.Search), after, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Equipment
			if err := scanEquipment(rows, &e); err != nil {
				return err
			}
			e.Moves = s.movesFor(p, e)
			out.Items = append(out.Items, e)
		}
		return rows.Err()
	})
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		out.Next = base64.RawURLEncoding.EncodeToString([]byte(out.Items[f.Limit-1].Number))
	}
	return out, err
}

// GetEquipment returns one machine with its latest 100 lifecycle events and
// its reservations that have not ended.
func (s *Service) GetEquipment(ctx context.Context, p auth.Principal, id string) (Equipment, error) {
	var e Equipment
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if err := scanEquipment(tx.QueryRow(ctx, `SELECT `+equipmentCols+` FROM equipment e WHERE e.id = $1::uuid`, id), &e); err != nil {
			return mapNoRows(err)
		}
		e.Moves = s.movesFor(p, e)
		rows, err := tx.Query(ctx, `SELECT COALESCE(from_status, ''), to_status, COALESCE(project_id::text, ''), COALESCE(reason, ''), actor, meter, occurred_at
			FROM equipment_events WHERE equipment_id = $1::uuid ORDER BY id DESC LIMIT 100`, id)
		if err != nil {
			return err
		}
		e.History = []EquipmentEvent{}
		for rows.Next() {
			var ev EquipmentEvent
			if err := rows.Scan(&ev.From, &ev.To, &ev.ProjectID, &ev.Reason, &ev.Actor, &ev.Meter, &ev.At); err != nil {
				rows.Close()
				return err
			}
			e.History = append(e.History, ev)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		e.Reservations, err = reservationsOf(ctx, tx, `r.equipment_id = $1::uuid AND r.ends_on >= current_date AND r.status IN ('SUBMITTED', 'APPROVED')`, id)
		return err
	})
	return e, err
}

func reservationsOf(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]EquipmentReservation, error) {
	rows, err := tx.Query(ctx, `SELECT r.id::text, r.number, r.equipment_id::text, r.project_id::text, r.purpose, r.starts_on::text, r.ends_on::text, r.status, r.created_by
		FROM equipment_reservations r WHERE `+where+` ORDER BY r.starts_on, r.number LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EquipmentReservation{}
	for rows.Next() {
		var r EquipmentReservation
		if err := rows.Scan(&r.ID, &r.Number, &r.EquipmentID, &r.ProjectID, &r.Purpose, &r.StartsOn, &r.EndsOn, &r.Status, &r.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReservationInput asks for a machine for a project and a period.
type EquipmentReservationInput struct {
	EquipmentID string `json:"equipment_id"`
	ProjectID   string `json:"project_id"`
	Purpose     string `json:"purpose"`
	StartsOn    string `json:"starts_on"`
	EndsOn      string `json:"ends_on"`
}

// EquipmentCalendar returns the approved and submitted reservations of a
// machine that overlap a period: what a requester checks before asking, and
// what an approver sees as the conflict.
func (s *Service) EquipmentCalendar(ctx context.Context, p auth.Principal, equipmentID, from, to string) ([]EquipmentReservation, error) {
	a, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, fmt.Errorf("%w: from must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	b, err := time.Parse("2006-01-02", to)
	if err != nil || b.Before(a) {
		return nil, fmt.Errorf("%w: to must be a date on or after from", ErrInvalidInput)
	}
	var out []EquipmentReservation
	err = s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = reservationsOf(ctx, tx, `r.equipment_id = $1::uuid AND r.status IN ('SUBMITTED', 'APPROVED') AND daterange(r.starts_on, r.ends_on, '[]') && daterange($2::date, $3::date, '[]')`, equipmentID, a, b)
		return err
	})
	return out, err
}

// CreateEquipmentReservation requests equipment for a project (DRAFT).
func (s *Service) CreateEquipmentReservation(ctx context.Context, p auth.Principal, in EquipmentReservationInput) (string, error) {
	a, err := time.Parse("2006-01-02", strings.TrimSpace(in.StartsOn))
	if err != nil {
		return "", fmt.Errorf("%w: starts_on must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	b, err := time.Parse("2006-01-02", strings.TrimSpace(in.EndsOn))
	if err != nil || b.Before(a) {
		return "", fmt.Errorf("%w: ends_on must be a date on or after starts_on", ErrInvalidInput)
	}
	if len(strings.TrimSpace(in.Purpose)) < 3 {
		return "", fmt.Errorf("%w: state what the equipment is needed for", ErrInvalidInput)
	}
	var id string
	err = s.documentTxPerm(ctx, p, auth.PermFleetReserve, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM equipment WHERE id = $1::uuid`, in.EquipmentID).Scan(&status); err != nil {
			return mapNoRows(err)
		}
		if oneOf(status, "REGISTERED", "SAFETY_HOLD", "RETIRED", "DISPOSED") {
			return fmt.Errorf("%w: equipment that is %s cannot be reserved", ErrInvalidInput, status)
		}
		num, err := postgres.NextNumber(ctx, tx, "EQR")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_reservations (equipment_id, project_id, number, purpose, starts_on, ends_on, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7) RETURNING id::text`, in.EquipmentID, in.ProjectID, num, strings.TrimSpace(in.Purpose), a, b, p.UserID)
		if err != nil {
			return fleetErr(err)
		}
		return s.record(ctx, tx, p, "fleet.reservation.create", "equipment_reservation", id, "equipment.reservation_requested",
			map[string]any{"reservation_id": id, "equipment_id": in.EquipmentID, "project_id": in.ProjectID, "starts_on": in.StartsOn, "ends_on": in.EndsOn})
	})
	return id, err
}

// documentTxPerm checks perm on the project, then runs fn in a tenant transaction.
func (s *Service) documentTxPerm(ctx context.Context, p auth.Principal, perm auth.Permission, projectID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return s.documentTx(ctx, p, perm, projectID, fn)
}

// SubmitEquipmentReservation submits a reservation for approval (its author).
func (s *Service) SubmitEquipmentReservation(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermFleetReserve, "equipment_reservation", "equipment_reservations", id, "fleet.reservation.submit", "equipment.reservation_submitted")
}

// DecideEquipmentReservation approves or rejects a reservation (fresh MFA to approve,
// not its author). The database refuses an approval that would overlap
// another approved reservation of the same machine, whatever the timing.
func (s *Service) DecideEquipmentReservation(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return fleetErr(s.decideGovernance(ctx, p, auth.PermFleetApprove, "equipment_reservation", "equipment_reservations", id, "fleet.reservation.decide", "reservation", approve, reason, nil))
}

// CancelEquipmentReservation cancels an approved reservation with a reason: its
// requester or an approver. Equipment already at the project is demobilised first.
func (s *Service) CancelEquipmentReservation(ctx context.Context, p auth.Principal, id, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	perm := auth.PermFleetApprove
	if !s.authz.Has(p, perm) {
		perm = auth.PermFleetReserve
	}
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, "equipment_reservation", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "APPROVED"); err != nil {
			return err
		}
		if perm == auth.PermFleetReserve && d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: a reservation is cancelled by its requester or by an approver", auth.ErrForbidden)
		}
		if err := s.authorize(ctx, tx, p, "fleet.reservation.cancel", "equipment_reservation", id, ""); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE equipment_reservations SET status = 'CANCELLED', cancelled_by = $2, decision_reason = $3 WHERE id = $1::uuid`, id, p.UserID, strings.TrimSpace(reason)); err != nil {
			return fleetErr(err)
		}
		return s.record(ctx, tx, p, "fleet.reservation.cancel", "equipment_reservation", id, "equipment.reservation_cancelled", map[string]any{"reservation_id": id, "reason": reason})
	})
}

// OperatorAuthInput authorises a person to operate a class of equipment.
type OperatorAuthInput struct {
	Operator       string `json:"operator"`
	EquipmentClass string `json:"equipment_class"`
	CertificateRef string `json:"certificate_ref"`
	ValidFrom      string `json:"valid_from"`
	ValidUntil     string `json:"valid_until"`
}

// CreateOperatorAuth prepares an operator authorisation (DRAFT).
func (s *Service) CreateOperatorAuth(ctx context.Context, p auth.Principal, in OperatorAuthInput) (string, error) {
	in.EquipmentClass = strings.ToUpper(strings.TrimSpace(in.EquipmentClass))
	if !oneOf(in.EquipmentClass, fleet.Classes...) {
		return "", fmt.Errorf("%w: unknown equipment class", ErrInvalidInput)
	}
	a, err := time.Parse("2006-01-02", strings.TrimSpace(in.ValidFrom))
	if err != nil {
		return "", fmt.Errorf("%w: valid_from must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	b, err := time.Parse("2006-01-02", strings.TrimSpace(in.ValidUntil))
	if err != nil || b.Before(a) {
		return "", fmt.Errorf("%w: valid_until must be a date on or after valid_from", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Operator) == "" || len(strings.TrimSpace(in.CertificateRef)) < 2 {
		return "", fmt.Errorf("%w: an authorisation names the operator and the certificate it rests on", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Operator) == p.UserID {
		return "", fmt.Errorf("%w: nobody prepares their own operator authorisation", auth.ErrForbidden)
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermFleetManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "EQO")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_operator_auths (number, operator, equipment_class, certificate_ref, valid_from, valid_until, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`, num, strings.TrimSpace(in.Operator), in.EquipmentClass, strings.TrimSpace(in.CertificateRef), a, b, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.operator.create", "equipment_operator_auth", id, "equipment.operator_auth_prepared", map[string]any{"id": id, "operator": in.Operator, "class": in.EquipmentClass})
	})
	return id, err
}

// SubmitOperatorAuth and DecideOperatorAuth: maker/checker; the certifier approves with fresh MFA.
func (s *Service) SubmitOperatorAuth(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermFleetManage, "equipment_operator_auth", "equipment_operator_auths", id, "fleet.operator.submit", "equipment.operator_auth_submitted")
}

func (s *Service) DecideOperatorAuth(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermFleetRelease, "equipment_operator_auth", "equipment_operator_auths", id, "fleet.operator.decide", "operator authorisation", approve, reason, nil)
}

// EquipmentLogInput is one machine's record for one day and shift.
type EquipmentLogInput struct {
	EquipmentID     string           `json:"equipment_id"`
	ProjectID       string           `json:"project_id"`
	LogDate         string           `json:"log_date"`
	Shift           int              `json:"shift"` // 1 (default), 2 or 3
	Operator        string           `json:"operator"`
	MeterOpen       decimal.Decimal  `json:"meter_open"`
	MeterClose      decimal.Decimal  `json:"meter_close"`
	ProductiveHours decimal.Decimal  `json:"productive_hours"`
	IdleHours       decimal.Decimal  `json:"idle_hours"`
	DownHours       decimal.Decimal  `json:"down_hours"`
	CostCodeID      string           `json:"cost_code_id,omitempty"`
	WorkQuantity    *decimal.Decimal `json:"work_quantity,omitempty"`
	WorkUOM         string           `json:"work_uom,omitempty"`
	FuelLitres      decimal.Decimal  `json:"fuel_litres"`
	Location        string           `json:"location,omitempty"`
	Findings        string           `json:"findings,omitempty"`
	ClientRef       string           `json:"client_ref,omitempty"` // set by an offline client: the same reference is recorded once
}

// CreateEquipmentLog enters a daily log (DRAFT). With a client reference the
// call is idempotent: a repeat returns the log already recorded, and a repeat
// with different figures is refused.
func (s *Service) CreateEquipmentLog(ctx context.Context, p auth.Principal, in EquipmentLogInput) (string, error) {
	day, err := time.Parse("2006-01-02", strings.TrimSpace(in.LogDate))
	if err != nil {
		return "", fmt.Errorf("%w: log_date must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	if in.Shift == 0 {
		in.Shift = 1
	}
	switch {
	case in.Shift < 1 || in.Shift > 3:
		return "", fmt.Errorf("%w: shift is 1, 2 or 3", ErrInvalidInput)
	case in.ProductiveHours.IsNegative() || in.IdleHours.IsNegative() || in.DownHours.IsNegative() || in.FuelLitres.IsNegative() || in.MeterOpen.IsNegative():
		return "", fmt.Errorf("%w: hours, fuel and meter readings cannot be negative", ErrInvalidInput)
	case in.MeterClose.LessThan(in.MeterOpen):
		return "", fmt.Errorf("%w: the closing meter reading is below the opening reading", ErrInvalidInput)
	case in.ProductiveHours.Add(in.IdleHours).Add(in.DownHours).GreaterThan(decimal.NewFromInt(24)):
		return "", fmt.Errorf("%w: productive, idle and down hours exceed 24", ErrInvalidInput)
	case (in.WorkQuantity == nil) != (strings.TrimSpace(in.WorkUOM) == ""):
		return "", fmt.Errorf("%w: a quantity of work needs its unit, and the other way round", ErrInvalidInput)
	case strings.TrimSpace(in.Operator) == "":
		return "", fmt.Errorf("%w: a log names the operator", ErrInvalidInput)
	}
	var id string
	err = s.documentTx(ctx, p, auth.PermFleetOperate, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		if in.ClientRef != "" {
			var same bool
			err := tx.QueryRow(ctx, `SELECT id::text, equipment_id = $3::uuid AND log_date = $4 AND shift = $5 AND meter_open = $6 AND meter_close = $7 AND productive_hours = $8 AND idle_hours = $9 AND down_hours = $10 AND fuel_litres = $11
				FROM equipment_logs WHERE created_by = $1 AND client_ref = $2`, p.UserID, in.ClientRef, in.EquipmentID, day, in.Shift, in.MeterOpen, in.MeterClose, in.ProductiveHours, in.IdleHours, in.DownHours, in.FuelLitres).Scan(&id, &same)
			if err == nil {
				if !same {
					return postgres.ErrIdempotencyConflict
				}
				return nil // recorded before: the same answer, no second log
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		num, err := postgres.NextNumber(ctx, tx, "EQL")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_logs (equipment_id, project_id, number, log_date, shift, operator, meter_serial, meter_open, meter_close, productive_hours, idle_hours, down_hours,
			cost_code_id, work_quantity, work_uom, fuel_litres, location, findings, client_ref, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, 1, $7, $8, $9, $10, $11, NULLIF($12, '')::uuid, $13, NULLIF($14, ''), $15, NULLIF($16, ''), NULLIF($17, ''), NULLIF($18, ''), $19) RETURNING id::text`,
			in.EquipmentID, in.ProjectID, num, day, in.Shift, strings.TrimSpace(in.Operator), in.MeterOpen, in.MeterClose, in.ProductiveHours, in.IdleHours, in.DownHours,
			in.CostCodeID, in.WorkQuantity, strings.TrimSpace(in.WorkUOM), in.FuelLitres, strings.TrimSpace(in.Location), strings.TrimSpace(in.Findings), in.ClientRef, p.UserID)
		if err != nil {
			return fleetErr(err)
		}
		return s.record(ctx, tx, p, "fleet.log.create", "equipment_log", id, "equipment.log_entered", map[string]any{"log_id": id, "equipment_id": in.EquipmentID, "log_date": in.LogDate, "shift": in.Shift})
	})
	return id, err
}

// SubmitEquipmentLog and DecideEquipmentLog: the person who wrote the log
// submits it; a different person approves. Approval advances the meter.
func (s *Service) SubmitEquipmentLog(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermFleetOperate, "equipment_log", "equipment_logs", id, "fleet.log.submit", "equipment.log_submitted")
}

func (s *Service) DecideEquipmentLog(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	if !approve {
		if err := requireReason(reason); err != nil {
			return err
		}
	}
	return s.p2pTx(ctx, p, auth.PermFleetLogApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermFleetLogApprove, "equipment_log", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "SUBMITTED"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "equipment log"); err != nil {
			return err
		}
		outcome := map[bool]string{true: "APPROVED", false: "REJECTED"}[approve]
		if err := s.authorize(ctx, tx, p, "fleet.log.decide", "equipment_log", id, outcome); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE equipment_logs SET status = $4, approved_by = $2, decision_reason = NULLIF($3, '') WHERE id = $1::uuid`, id, p.UserID, reason, outcome); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.log.decide", "equipment_log", id, "equipment.log_"+strings.ToLower(outcome), map[string]any{"log_id": id, "reason": reason})
	})
}

// MeterResetInput records a replaced or rolled-over meter.
type MeterResetInput struct {
	EquipmentID string          `json:"equipment_id"`
	Kind        string          `json:"kind"` // REPLACEMENT | ROLLOVER | CORRECTION
	NewReading  decimal.Decimal `json:"new_reading"`
	Reason      string          `json:"reason"`
}

// CreateMeterReset prepares a meter reset (DRAFT); the database records the
// reading it replaces.
func (s *Service) CreateMeterReset(ctx context.Context, p auth.Principal, in MeterResetInput) (string, error) {
	in.Kind = strings.ToUpper(strings.TrimSpace(in.Kind))
	if !oneOf(in.Kind, "REPLACEMENT", "ROLLOVER", "CORRECTION") || in.NewReading.IsNegative() || len(strings.TrimSpace(in.Reason)) < 10 {
		return "", fmt.Errorf("%w: a meter reset states its kind (REPLACEMENT, ROLLOVER or CORRECTION), the new reading and the reason (10+ characters)", ErrInvalidInput)
	}
	var id string
	err := s.p2pTx(ctx, p, auth.PermFleetManage, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "EQM")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO equipment_meter_resets (equipment_id, number, kind, old_reading, new_reading, reason, created_by)
			VALUES ($1::uuid, $2, $3, 0, $4, $5, $6) RETURNING id::text`, in.EquipmentID, num, in.Kind, in.NewReading, strings.TrimSpace(in.Reason), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "fleet.meter.create", "equipment_meter_reset", id, "equipment.meter_reset_prepared", map[string]any{"id": id, "equipment_id": in.EquipmentID, "new_reading": in.NewReading.String()})
	})
	return id, err
}

func (s *Service) SubmitMeterReset(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermFleetManage, "equipment_meter_reset", "equipment_meter_resets", id, "fleet.meter.submit", "equipment.meter_reset_submitted")
}

func (s *Service) DecideMeterReset(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermFleetRelease, "equipment_meter_reset", "equipment_meter_resets", id, "fleet.meter.decide", "meter reset", approve, reason, nil)
}

// FleetUsage is what the approved logs of a period say about one machine.
type FleetUsage struct {
	EquipmentID     string           `json:"equipment_id"`
	Number          string           `json:"number"`
	Name            string           `json:"name"`
	Class           string           `json:"class"`
	Status          string           `json:"status"`
	Logs            int              `json:"logs"`
	ProductiveHours decimal.Decimal  `json:"productive_hours"`
	IdleHours       decimal.Decimal  `json:"idle_hours"`
	DownHours       decimal.Decimal  `json:"down_hours"`
	FuelLitres      decimal.Decimal  `json:"fuel_litres"`
	Utilization     *decimal.Decimal `json:"utilization"`          // productive / logged hours; null when nothing was logged
	Availability    *decimal.Decimal `json:"availability"`         // (productive + idle) / logged hours; null when nothing was logged
	FuelPerHour     *decimal.Decimal `json:"fuel_litres_per_hour"` // fuel / engine hours; null when no engine hours
	UnloggedMeter   decimal.Decimal  `json:"unlogged_meter_units"` // meter movement between logs that no log explains
}

// FleetSummary is the Fleet Command Centre's data: counts by state and usage
// from approved logs, computed from the database for the caller's scope.
type FleetSummary struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	ByStatus map[string]int `json:"by_status"`
	Total    int            `json:"total"`
	Usage    []FleetUsage   `json:"usage"`
	Basis    string         `json:"basis"`
}

// FleetSummaryOf computes the summary for a period (at most 366 days) and
// optionally one project. Usage counts APPROVED logs only, of the projects the
// caller may see; machines are listed up to 500, most productive first.
func (s *Service) FleetSummaryOf(ctx context.Context, p auth.Principal, from, to, projectID string) (FleetSummary, error) {
	out := FleetSummary{From: from, To: to, ByStatus: map[string]int{}, Usage: []FleetUsage{},
		Basis: "Counts by state: the whole register now. Hours and fuel: approved daily logs dated in the period, of projects in your scope. Utilization = productive / (productive + idle + down). Availability = (productive + idle) / (productive + idle + down)."}
	a, err := time.Parse("2006-01-02", from)
	if err != nil {
		return out, fmt.Errorf("%w: from must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	b, err := time.Parse("2006-01-02", to)
	if err != nil || b.Before(a) || b.Sub(a) > 366*24*time.Hour {
		return out, fmt.Errorf("%w: to must be a date on or after from, at most 366 days later", ErrInvalidInput)
	}
	err = s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT status, count(*) FROM equipment GROUP BY status`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st string
			var n int
			if err := rows.Scan(&st, &n); err != nil {
				rows.Close()
				return err
			}
			out.ByStatus[st] = n
			out.Total += n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		sc := s.authz.ScopeOf(p)
		if sc.Projects == nil {
			sc.Projects = []string{}
		}
		rows, err = tx.Query(ctx, `SELECT e.id::text, e.number, e.name, e.class, e.status, count(l.id), COALESCE(sum(l.productive_hours), 0), COALESCE(sum(l.idle_hours), 0),
			COALESCE(sum(l.down_hours), 0), COALESCE(sum(l.fuel_litres), 0), COALESCE(sum(l.meter_gap), 0)
			FROM equipment e JOIN equipment_logs l ON l.equipment_id = e.id AND l.status = 'APPROVED' AND l.log_date BETWEEN $1 AND $2
			  AND ($3 = '' OR l.project_id = $3::uuid) AND ($4 OR l.project_id = ANY($5::uuid[]))
			GROUP BY e.id ORDER BY sum(l.productive_hours) DESC, e.number LIMIT 500`, a, b, projectID, sc.TenantWide, sc.Projects)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u FleetUsage
			if err := rows.Scan(&u.EquipmentID, &u.Number, &u.Name, &u.Class, &u.Status, &u.Logs, &u.ProductiveHours, &u.IdleHours, &u.DownHours, &u.FuelLitres, &u.UnloggedMeter); err != nil {
				return err
			}
			h := fleet.Hours{Productive: u.ProductiveHours, Idle: u.IdleHours, Down: u.DownHours}
			u.Utilization, u.Availability = h.Utilization(), h.Availability()
			if engine := u.ProductiveHours.Add(u.IdleHours); engine.IsPositive() {
				f := u.FuelLitres.Div(engine).Round(2)
				u.FuelPerHour = &f
			}
			out.Usage = append(out.Usage, u)
		}
		return rows.Err()
	})
	return out, err
}
