# ADR 0020: SERK FLEET part 1 — register, lifecycle, reservations, logs and meter control

Status: accepted by the delivering team (2026-11-15); awaiting human review. Migration 0054. Requirements FLT-01.1–13.

## Context

The repository had no equipment domain; site diaries carried free-text equipment hours. The Bible and the engineering directive ask for a register, an explicit lifecycle, scheduling without double-booking, daily operations with meter control, and later fuel, maintenance, costing, optimisation and telematics. This record covers the first part, on which the others depend.

## Decision

**One state machine, stated twice and proved equal.** `equipment_transition_perm(from, to)` in SQL and `fleet.TransitionPerm` in Go each return the permission a move needs, or "no such move". A test compares all 196 pairs. The database enforces it on every update; the kernel uses it for clear refusals and to tell the console which moves a person may make.

**Every move is a person's attested decision** with a reason (`equipment.transition` is not a draft action, so AI and service principals are refused in Go and in SQL). Lifecycle history is written by a definer trigger; the application role cannot insert, update or delete it.

**Out of service is one-way without a certifier.** From `SAFETY_HOLD`, `UNDER_MAINTENANCE` and `UNDER_INSPECTION` the only moves to a working state need `fleet.release` with fresh MFA. `BREAKDOWN` leads only to maintenance or hold. Anyone who operates can stop any machine in one move. `fleet.operate` and `fleet.release` are a separation-of-duties conflict, as are register/commission and register/certify.

**A machine changes project only by mobilisation**, and mobilisation requires an approved reservation of that project starting within seven days and not ended. This is the control on unauthorised cross-project transfer; there is no other statement that changes `current_project_id`.

**No double-booking is a constraint, not a check.** `EXCLUDE USING gist (tenant, equipment, daterange) WHERE status = 'APPROVED'` (the `btree_gist` extension was already installed). Sixteen concurrent approvals of overlapping requests yield exactly one.

**`RESERVED` follows facts.** A machine in the yard is `RESERVED` exactly while it has an approved reservation that has not ended; a trigger keeps it so and nobody decides it.

**Meter control.** A log's opening reading may not be below the machine's last approved reading of the same meter; a gap above it is recorded as unlogged meter movement, not refused. The machine's reading changes only in a transaction that holds the attested approval of a log (to its closing reading) or of a meter reset (to its new reading, on a new meter number). A reset is a two-person decision; logs written against the old meter are refused at approval.

**Operators.** An approved authorisation per person and equipment class with validity dates; nobody prepares or approves their own; a log must name an operator authorised on its date.

**Visibility.** The register is company-wide to anyone who reads documents, because a project must see the fleet to ask for it. Reservations, logs and usage are project-scoped. Moves of a machine in the yard need company-wide scope; moves at a project need that project.

**Pagination.** The register pages by keyset on the equipment number; there is no offset.

**Figures.** Utilization = productive / (productive + idle + down); availability = (productive + idle) / all; fuel per hour = fuel / engine hours; from approved logs only; absent when there is nothing to divide by.

## Alternatives considered

- **Derive status from reservations and logs** instead of explicit moves: fewer commands, but safety hold and certification need an explicit, attested decision, and derived states hide who decided.
- **Application-level overlap check with a row lock**: works, but an exclusion constraint cannot be bypassed by any code path.
- **Reject logs with a meter gap**: would block honest recording after a missed day; the gap is surfaced instead.

## Consequences and limits

- No cost is produced yet: approved hours do not post or allocate anything. Charge-out, depreciation and job-cost integration are the next part and must go through the existing journal interfaces.
- No fuel, maintenance work orders, telematics, optimisation, rental agreements, attachments or fixed-asset link.
- A reservation is for a whole machine and whole days.
- Offline capture relies on the idempotent client reference; logs are not yet a `/v1/sync` command.
- The operator in a log is an identifier checked against authorisations, not an authenticated confirmation by the operator.

## Reversal

The tables are additive. Removing the feature is a matter of withdrawing the permissions; history remains readable.
