-- 0054 SERK FLEET, part 1: equipment register, lifecycle, reservations,
-- operator authorisations, daily operation logs and meter control
-- (requirements FLT-01.x; ADR 0020)
--
-- WHAT THIS MIGRATION DOES
--   * EQUIPMENT: one row per machine, plant item or tool, owned, rented,
--     leased or subcontracted. Its identity (class, ownership, maker, serial,
--     engine and registration numbers, legal entity) never changes; the same
--     maker and serial number, or the same registration, cannot be registered
--     twice (compared without case, spaces or punctuation).
--   * LIFECYCLE: fourteen states. equipment_transition_perm() is the whole
--     state machine: for a move it returns the permission it needs, or NULL
--     when the move does not exist. Every move is made by a person, attested,
--     with a reason, and written to equipment_events by the database itself;
--     history is append-only. Equipment on SAFETY_HOLD, UNDER_MAINTENANCE or
--     UNDER_INSPECTION returns to service only through fleet.release with a
--     fresh second factor; nothing else leads out of those states to work.
--   * RESERVATIONS: a project asks for a machine for a period; a second person
--     approves. Two approved reservations of one machine can never overlap
--     (an exclusion constraint, so no race can double-book). A machine is
--     mobilised to a project only against an approved reservation of that
--     project that covers the day: that is the only way it changes project.
--   * OPERATORS: who is authorised to operate which class of equipment, until
--     when, approved by a second person. A log names an authorised operator.
--   * DAILY LOGS: one per machine, day and shift. Meter readings never run
--     backwards and continue from the last approved reading; hours fit the
--     day and the meter; the machine must be at the project. Approved by a
--     second person; approval advances the machine's meter.
--   * METER RESETS: a replaced or rolled-over meter is recorded as a decision
--     of two people; the old and new readings are kept.
--
-- NOT DONE HERE: fuel, maintenance work orders, costing and charge-out,
--   telematics (later parts; see docs/engineering/REQUIREMENTS.md).
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0053, additions marked 0054):
--   p2p_subject_text, serk_subject_project

CREATE TABLE equipment (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id   uuid NOT NULL,
  number            text NOT NULL,
  name              text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 120),
  class             text NOT NULL CHECK (class IN ('EXCAVATOR', 'GRADER', 'LOADER', 'BULLDOZER', 'CRANE', 'GENERATOR', 'TRUCK', 'MIXER', 'BATCHING_PLANT', 'COMPACTOR', 'TOOL', 'OTHER')),
  ownership         text NOT NULL CHECK (ownership IN ('OWNED', 'RENTED', 'LEASED', 'SUBCONTRACTED')),
  manufacturer      text NOT NULL CHECK (length(btrim(manufacturer)) BETWEEN 1 AND 80),
  model             text NOT NULL CHECK (length(btrim(model)) BETWEEN 1 AND 80),
  serial_number     text NOT NULL CHECK (length(btrim(serial_number)) BETWEEN 1 AND 80),
  engine_number     text CHECK (engine_number IS NULL OR length(btrim(engine_number)) BETWEEN 1 AND 80),
  registration      text CHECK (registration IS NULL OR length(btrim(registration)) BETWEEN 1 AND 40),
  capacity          text CHECK (capacity IS NULL OR length(capacity) <= 120),
  fuel_type         text NOT NULL DEFAULT 'DIESEL' CHECK (fuel_type IN ('DIESEL', 'PETROL', 'ELECTRIC', 'NONE')),
  meter_kind        text NOT NULL DEFAULT 'HOURS' CHECK (meter_kind IN ('HOURS', 'KM', 'NONE')),
  supplier_id       uuid,
  acquired_on       date,
  acquisition_cost  numeric(24,6) CHECK (acquisition_cost IS NULL OR acquisition_cost >= 0),
  currency          char(3) REFERENCES currencies(code),
  warranty_until    date,
  insurance_until   date,
  home_location     text CHECK (home_location IS NULL OR length(home_location) <= 120),
  status            text NOT NULL DEFAULT 'REGISTERED' CHECK (status IN ('REGISTERED', 'AVAILABLE', 'RESERVED', 'MOBILIZING', 'ASSIGNED', 'OPERATING', 'IDLE',
                      'UNDER_INSPECTION', 'UNDER_MAINTENANCE', 'BREAKDOWN', 'SAFETY_HOLD', 'DEMOBILIZING', 'RETIRED', 'DISPOSED')),
  current_project_id uuid,
  status_reason     text,
  status_changed_by text NOT NULL,
  status_changed_at timestamptz NOT NULL DEFAULT now(),
  last_meter        numeric(14,2) NOT NULL DEFAULT 0 CHECK (last_meter >= 0),
  meter_serial      integer NOT NULL DEFAULT 1 CHECK (meter_serial >= 1),
  created_by        text NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES suppliers(tenant_id, id),
  FOREIGN KEY (tenant_id, current_project_id) REFERENCES projects(tenant_id, id),
  CONSTRAINT equipment_cost_has_currency CHECK ((acquisition_cost IS NULL) = (currency IS NULL)),
  -- At a project exactly while it is being moved to, used at, or moved from one.
  CONSTRAINT equipment_project_by_status CHECK ((status IN ('MOBILIZING', 'ASSIGNED', 'OPERATING', 'IDLE', 'DEMOBILIZING')) <= (current_project_id IS NOT NULL)
    AND (status IN ('REGISTERED', 'AVAILABLE', 'RESERVED', 'RETIRED', 'DISPOSED')) <= (current_project_id IS NULL))
);
-- The same machine is registered once: maker + serial, and registration, compared loosely.
CREATE UNIQUE INDEX equipment_serial_once ON equipment (tenant_id, lower(regexp_replace(manufacturer, '[^A-Za-z0-9]', '', 'g')), upper(regexp_replace(serial_number, '[^A-Za-z0-9]', '', 'g')));
CREATE UNIQUE INDEX equipment_registration_once ON equipment (tenant_id, upper(regexp_replace(registration, '[^A-Za-z0-9]', '', 'g'))) WHERE registration IS NOT NULL;
-- The register is read in number order, a page at a time (keyset): this index is that order.
CREATE INDEX equipment_by_number ON equipment (tenant_id, number COLLATE "C");
CREATE INDEX equipment_by_status ON equipment (tenant_id, status, number COLLATE "C");
CREATE INDEX equipment_by_project ON equipment (tenant_id, current_project_id) WHERE current_project_id IS NOT NULL;

-- Append-only lifecycle history, written by the database.
CREATE TABLE equipment_events (
  id           bigserial PRIMARY KEY,
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id uuid NOT NULL,
  from_status  text,
  to_status    text NOT NULL,
  project_id   uuid,
  reason       text,
  actor        text NOT NULL,
  meter        numeric(14,2) NOT NULL,
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id)
);
CREATE INDEX equipment_events_by_equipment ON equipment_events (tenant_id, equipment_id, id);

CREATE TABLE equipment_reservations (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id    uuid NOT NULL,
  project_id      uuid NOT NULL,
  number          text NOT NULL,
  purpose         text NOT NULL CHECK (length(btrim(purpose)) BETWEEN 3 AND 300),
  starts_on       date NOT NULL,
  ends_on         date NOT NULL,
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELLED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  cancelled_by    text,
  cancelled_at    timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  CONSTRAINT reservation_period CHECK (ends_on >= starts_on AND ends_on - starts_on <= 3660),
  CONSTRAINT reservation_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by),
  -- One machine, one place at a time: approved periods never overlap.
  CONSTRAINT reservation_no_double_booking EXCLUDE USING gist (tenant_id WITH =, equipment_id WITH =, daterange(starts_on, ends_on, '[]') WITH &&) WHERE (status = 'APPROVED')
);
CREATE INDEX equipment_reservations_by_project ON equipment_reservations (tenant_id, project_id, status, starts_on);

CREATE TABLE equipment_operator_auths (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  number          text NOT NULL,
  operator        text NOT NULL CHECK (operator ~ '^[A-Za-z0-9._@:+|-]{1,200}$'),
  equipment_class text NOT NULL,
  certificate_ref text NOT NULL CHECK (length(btrim(certificate_ref)) BETWEEN 2 AND 120),
  valid_from      date NOT NULL,
  valid_until     date NOT NULL,
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  CONSTRAINT operator_auth_period CHECK (valid_until >= valid_from),
  CONSTRAINT operator_auth_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by),
  CONSTRAINT operator_auth_not_self CHECK (operator <> created_by AND (approved_by IS NULL OR operator <> approved_by))
);
CREATE INDEX equipment_operator_auths_lookup ON equipment_operator_auths (tenant_id, operator, equipment_class, status);

CREATE TABLE equipment_logs (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id     uuid NOT NULL,
  project_id       uuid NOT NULL,
  number           text NOT NULL,
  log_date         date NOT NULL,
  shift            smallint NOT NULL DEFAULT 1 CHECK (shift BETWEEN 1 AND 3),
  operator         text NOT NULL CHECK (operator ~ '^[A-Za-z0-9._@:+|-]{1,200}$'),
  meter_serial     integer NOT NULL,
  meter_open       numeric(14,2) NOT NULL CHECK (meter_open >= 0),
  meter_close      numeric(14,2) NOT NULL,
  productive_hours numeric(5,2) NOT NULL CHECK (productive_hours >= 0),
  idle_hours       numeric(5,2) NOT NULL DEFAULT 0 CHECK (idle_hours >= 0),
  down_hours       numeric(5,2) NOT NULL DEFAULT 0 CHECK (down_hours >= 0),
  cost_code_id     uuid,
  work_quantity    numeric(20,4) CHECK (work_quantity IS NULL OR work_quantity >= 0),
  work_uom         text CHECK (work_uom IS NULL OR length(btrim(work_uom)) BETWEEN 1 AND 20),
  fuel_litres      numeric(12,2) NOT NULL DEFAULT 0 CHECK (fuel_litres >= 0),
  location         text CHECK (location IS NULL OR length(location) <= 200),
  findings         text CHECK (findings IS NULL OR length(findings) <= 2000),
  client_ref       text CHECK (client_ref IS NULL OR client_ref ~ '^[A-Za-z0-9._:-]{8,80}$'),
  meter_gap        numeric(14,2) NOT NULL DEFAULT 0,
  status           text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by       text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  approved_by      text,
  approved_at      timestamptz,
  decision_reason  text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  FOREIGN KEY (tenant_id, cost_code_id) REFERENCES cost_codes(tenant_id, id),
  CONSTRAINT log_meter_forward CHECK (meter_close >= meter_open),
  CONSTRAINT log_hours_fit_the_day CHECK (productive_hours + idle_hours + down_hours <= 24),
  CONSTRAINT log_quantity_has_unit CHECK ((work_quantity IS NULL) = (work_uom IS NULL)),
  CONSTRAINT log_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
-- One log per machine, day and shift (a rejected one may be re-entered); one per offline reference.
CREATE UNIQUE INDEX equipment_logs_once ON equipment_logs (tenant_id, equipment_id, log_date, shift) WHERE status <> 'REJECTED';
CREATE UNIQUE INDEX equipment_logs_client_ref ON equipment_logs (tenant_id, created_by, client_ref) WHERE client_ref IS NOT NULL;
CREATE INDEX equipment_logs_by_project ON equipment_logs (tenant_id, project_id, log_date);
CREATE INDEX equipment_logs_by_equipment ON equipment_logs (tenant_id, equipment_id, log_date);
-- Usage summaries read approved logs of a period, across projects.
CREATE INDEX equipment_logs_approved_by_date ON equipment_logs (tenant_id, log_date) INCLUDE (equipment_id, project_id, productive_hours, idle_hours, down_hours, fuel_litres, meter_gap) WHERE status = 'APPROVED';

CREATE TABLE equipment_meter_resets (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id    uuid NOT NULL,
  number          text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('REPLACEMENT', 'ROLLOVER', 'CORRECTION')),
  old_reading     numeric(14,2) NOT NULL,
  new_reading     numeric(14,2) NOT NULL CHECK (new_reading >= 0),
  reason          text NOT NULL CHECK (length(btrim(reason)) BETWEEN 10 AND 500),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id),
  CONSTRAINT meter_reset_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE UNIQUE INDEX equipment_meter_resets_one_open ON equipment_meter_resets (tenant_id, equipment_id) WHERE status IN ('DRAFT', 'SUBMITTED');

-- ---------------------------------------------------------- state machine ---
-- The whole lifecycle: the permission a move needs, or NULL if there is no such move.
-- '' means the move follows from facts the database checks itself (reservations).
CREATE FUNCTION equipment_transition_perm(p_from text, p_to text) RETURNS text
LANGUAGE sql IMMUTABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE
    WHEN p_from = 'REGISTERED' AND p_to = 'AVAILABLE' THEN 'fleet.approve'
    WHEN p_from = 'AVAILABLE' AND p_to = 'RESERVED' OR p_from = 'RESERVED' AND p_to = 'AVAILABLE' THEN ''
    WHEN p_from IN ('AVAILABLE', 'RESERVED') AND p_to = 'MOBILIZING' THEN 'fleet.operate'
    WHEN p_from = 'MOBILIZING' AND p_to = 'ASSIGNED' THEN 'fleet.operate'
    WHEN p_from IN ('ASSIGNED', 'IDLE') AND p_to = 'OPERATING' THEN 'fleet.operate'
    WHEN p_from = 'OPERATING' AND p_to IN ('IDLE', 'ASSIGNED') THEN 'fleet.operate'
    WHEN p_from = 'IDLE' AND p_to = 'ASSIGNED' THEN 'fleet.operate'
    WHEN p_from IN ('MOBILIZING', 'ASSIGNED', 'OPERATING', 'IDLE') AND p_to = 'DEMOBILIZING' THEN 'fleet.operate'
    WHEN p_from = 'DEMOBILIZING' AND p_to IN ('AVAILABLE', 'RESERVED') THEN 'fleet.operate'
    -- Out of service: anyone who operates may stop a machine, at once.
    WHEN p_from IN ('AVAILABLE', 'RESERVED', 'MOBILIZING', 'ASSIGNED', 'OPERATING', 'IDLE', 'DEMOBILIZING')
         AND p_to IN ('BREAKDOWN', 'SAFETY_HOLD', 'UNDER_INSPECTION', 'UNDER_MAINTENANCE') THEN 'fleet.operate'
    WHEN p_from = 'BREAKDOWN' AND p_to IN ('UNDER_MAINTENANCE', 'SAFETY_HOLD') THEN 'fleet.operate'
    WHEN p_from = 'UNDER_INSPECTION' AND p_to IN ('UNDER_MAINTENANCE', 'SAFETY_HOLD') THEN 'fleet.operate'
    WHEN p_from = 'UNDER_MAINTENANCE' AND p_to IN ('UNDER_INSPECTION', 'SAFETY_HOLD') THEN 'fleet.operate'
    WHEN p_from = 'SAFETY_HOLD' AND p_to IN ('UNDER_INSPECTION', 'UNDER_MAINTENANCE') THEN 'fleet.operate'
    -- Back into service: only a certifier.
    WHEN p_from IN ('UNDER_INSPECTION', 'UNDER_MAINTENANCE', 'SAFETY_HOLD') AND p_to IN ('AVAILABLE', 'RESERVED', 'ASSIGNED') THEN 'fleet.release'
    WHEN p_from = 'AVAILABLE' AND p_to = 'RETIRED' THEN 'fleet.approve'
    WHEN p_from = 'RETIRED' AND p_to = 'DISPOSED' THEN 'fleet.approve'
  END
$$;

-- Where a machine rests when it is in service and not on the move.
CREATE FUNCTION equipment_rest_status(p_equipment uuid, p_project uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE WHEN p_project IS NOT NULL THEN 'ASSIGNED'
    WHEN EXISTS (SELECT 1 FROM equipment_reservations r WHERE r.equipment_id = p_equipment AND r.tenant_id = current_tenant() AND r.status = 'APPROVED' AND r.ends_on >= current_date) THEN 'RESERVED'
    ELSE 'AVAILABLE' END
$$;

CREATE FUNCTION equipment_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE perm text; res equipment_reservations;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'REGISTERED' OR NEW.current_project_id IS NOT NULL OR NEW.meter_serial <> 1 OR NEW.status_changed_by <> NEW.created_by THEN
      RAISE EXCEPTION 'equipment is entered as REGISTERED by the person who registers it' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.meter_kind = 'NONE' AND NEW.last_meter <> 0 THEN
      RAISE EXCEPTION 'equipment without a meter has no reading' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.status = OLD.status THEN
    IF NEW.last_meter IS DISTINCT FROM OLD.last_meter OR NEW.meter_serial IS DISTINCT FROM OLD.meter_serial THEN
      -- The meter moves only with the attested approval, in this transaction, of a log
      -- (to its closing reading) or of a meter reset (to its new reading, on a new meter).
      IF NOT (NEW.meter_serial = OLD.meter_serial AND EXISTS (SELECT 1 FROM approval_attestations a JOIN equipment_logs l ON l.id::text = a.subject_id
                 WHERE a.txid = txid_current() AND a.tenant_id = current_tenant() AND a.action = 'fleet.log.decide' AND a.ctx = 'APPROVED'
                   AND a.subject_type = 'equipment_log' AND l.equipment_id = NEW.id AND l.meter_close = NEW.last_meter))
         AND NOT (NEW.meter_serial = OLD.meter_serial + 1 AND EXISTS (SELECT 1 FROM approval_attestations a JOIN equipment_meter_resets m ON m.id::text = a.subject_id
                 WHERE a.txid = txid_current() AND a.tenant_id = current_tenant() AND a.action = 'fleet.meter.decide' AND a.ctx = 'APPROVED'
                   AND a.subject_type = 'equipment_meter_reset' AND m.equipment_id = NEW.id AND m.new_reading = NEW.last_meter)) THEN
        RAISE EXCEPTION 'a meter reading changes only through an approved log or an approved meter reset' USING ERRCODE = 'insufficient_privilege';
      END IF;
      PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['last_meter', 'meter_serial'], 'equipment');
      RETURN NEW;
    END IF;
    -- Descriptive details only; identity never changes.
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['name', 'capacity', 'warranty_until', 'insurance_until', 'home_location'], 'equipment');
    RETURN NEW;
  END IF;
  perm := equipment_transition_perm(OLD.status, NEW.status);
  IF perm IS NULL THEN
    RAISE EXCEPTION 'equipment cannot move from % to %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'current_project_id', 'status_reason', 'status_changed_by', 'status_changed_at'], 'equipment');
  IF perm = '' THEN
    -- AVAILABLE <-> RESERVED follows the approved reservations; nobody decides it.
    IF NEW.status <> equipment_rest_status(NEW.id, NULL) OR NEW.current_project_id IS NOT NULL THEN
      RAISE EXCEPTION 'equipment is RESERVED exactly while it has an approved reservation that has not ended' USING ERRCODE = 'check_violation';
    END IF;
    NEW.status_changed_at := now();
    RETURN NEW;
  END IF;
  IF NEW.status_reason IS NULL OR length(btrim(NEW.status_reason)) < 3 THEN
    RAISE EXCEPTION 'a change of equipment status states its reason' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'MOBILIZING' THEN
    -- The only way a machine goes to a project: an approved reservation of that project covering today.
    SELECT * INTO res FROM equipment_reservations r WHERE r.equipment_id = NEW.id AND r.tenant_id = current_tenant() AND r.status = 'APPROVED'
       AND r.project_id = NEW.current_project_id AND r.starts_on <= current_date + 7 AND r.ends_on >= current_date ORDER BY r.starts_on LIMIT 1;
    IF res.id IS NULL THEN
      RAISE EXCEPTION 'equipment is mobilised only to a project that holds an approved reservation for it starting within 7 days and not yet ended' USING ERRCODE = 'insufficient_privilege';
    END IF;
  ELSIF NEW.status IN ('AVAILABLE', 'RESERVED') AND OLD.status <> 'REGISTERED' THEN
    -- Leaving a project or returning to the yard.
    IF OLD.status = 'DEMOBILIZING' OR OLD.current_project_id IS NULL THEN
      NEW.current_project_id := NULL;
    END IF;
    IF NEW.current_project_id IS NOT NULL OR NEW.status <> equipment_rest_status(NEW.id, NULL) THEN
      RAISE EXCEPTION 'equipment that is not at a project rests as % ', equipment_rest_status(NEW.id, NULL) USING ERRCODE = 'check_violation';
    END IF;
  ELSIF NEW.status = 'ASSIGNED' AND OLD.status IN ('UNDER_INSPECTION', 'UNDER_MAINTENANCE', 'SAFETY_HOLD') THEN
    IF OLD.current_project_id IS NULL OR NEW.current_project_id IS DISTINCT FROM OLD.current_project_id THEN
      RAISE EXCEPTION 'equipment returns to service at the project it was at, or to the yard' USING ERRCODE = 'check_violation';
    END IF;
  ELSIF NEW.status = 'AVAILABLE' AND OLD.status = 'REGISTERED' THEN
    IF NEW.status_changed_by = OLD.created_by THEN
      RAISE EXCEPTION 'equipment is commissioned by a person other than the one who registered it' USING ERRCODE = 'insufficient_privilege';
    END IF;
  ELSIF NEW.status IN ('RETIRED', 'DISPOSED') THEN
    NULL;
  ELSIF NEW.current_project_id IS DISTINCT FROM OLD.current_project_id THEN
    RAISE EXCEPTION 'equipment changes project only by being mobilised or demobilised' USING ERRCODE = 'insufficient_privilege';
  END IF;
  -- Every move is a decision of a person: attested. Certifying and commissioning need a fresh second factor.
  PERFORM approval_use('equipment.transition', 'equipment', NEW.id::text, NEW.status_changed_by, ARRAY[perm], perm IN ('fleet.release', 'fleet.approve'), NEW.status, NULL);
  NEW.status_changed_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_guard BEFORE INSERT OR UPDATE ON equipment FOR EACH ROW EXECUTE FUNCTION equipment_guard();

-- History is written by the database for every registration and every move.
CREATE FUNCTION equipment_history() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    INSERT INTO equipment_events (tenant_id, equipment_id, from_status, to_status, project_id, reason, actor, meter)
    VALUES (NEW.tenant_id, NEW.id, NULL, NEW.status, NULL, 'Registered', NEW.created_by, NEW.last_meter);
  ELSIF NEW.status <> OLD.status THEN
    INSERT INTO equipment_events (tenant_id, equipment_id, from_status, to_status, project_id, reason, actor, meter)
    VALUES (NEW.tenant_id, NEW.id, OLD.status, NEW.status, COALESCE(NEW.current_project_id, OLD.current_project_id),
            CASE WHEN equipment_transition_perm(OLD.status, NEW.status) = '' THEN 'Reservations changed' ELSE NEW.status_reason END,
            CASE WHEN equipment_transition_perm(OLD.status, NEW.status) = '' THEN 'system' ELSE NEW.status_changed_by END, NEW.last_meter);
  END IF;
  RETURN NULL;
END$$;
CREATE TRIGGER equipment_history AFTER INSERT OR UPDATE ON equipment FOR EACH ROW EXECUTE FUNCTION equipment_history();

-- ---------------------------------------------------------- reservations ---
CREATE FUNCTION equipment_reservations_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE e equipment;
BEGIN
  SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.cancelled_by IS NOT NULL THEN
      RAISE EXCEPTION 'a reservation is requested as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.ends_on < current_date THEN
      RAISE EXCEPTION 'a reservation cannot lie wholly in the past' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'CANCELLED') THEN
    RAISE EXCEPTION 'a rejected or cancelled reservation never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'reservation');
    PERFORM approval_use('fleet.reservation.submit', 'equipment_reservation', NEW.id::text, NEW.created_by, ARRAY['fleet.reserve'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'reservation');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a reservation requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'APPROVED' AND e.status IN ('REGISTERED', 'SAFETY_HOLD', 'RETIRED', 'DISPOSED') THEN
      RAISE EXCEPTION 'equipment that is % cannot be reserved', e.status USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.reservation.decide', 'equipment_reservation', NEW.id::text, NEW.approved_by, ARRAY['fleet.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'APPROVED' AND NEW.status = 'CANCELLED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'cancelled_by', 'cancelled_at', 'decision_reason'], 'reservation');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 OR NEW.cancelled_by IS NULL THEN
      RAISE EXCEPTION 'cancelling a reservation requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    IF e.current_project_id = NEW.project_id AND e.status IN ('MOBILIZING', 'ASSIGNED', 'OPERATING', 'IDLE') AND NEW.starts_on <= current_date THEN
      RAISE EXCEPTION 'the equipment is at the project under this reservation; demobilise it first' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.reservation.cancel', 'equipment_reservation', NEW.id::text, NEW.cancelled_by, ARRAY['fleet.approve', 'fleet.reserve'], false, NULL, NULL);
    NEW.cancelled_at := now();
  ELSE
    RAISE EXCEPTION 'illegal reservation transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_reservations_guard BEFORE INSERT OR UPDATE ON equipment_reservations FOR EACH ROW EXECUTE FUNCTION equipment_reservations_guard();

-- A machine in the yard shows RESERVED exactly while an approved reservation is outstanding.
CREATE FUNCTION equipment_reservations_sync() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  UPDATE equipment e SET status = equipment_rest_status(e.id, NULL)
   WHERE e.id = NEW.equipment_id AND e.tenant_id = NEW.tenant_id AND e.status IN ('AVAILABLE', 'RESERVED') AND e.status <> equipment_rest_status(e.id, NULL);
  RETURN NULL;
END$$;
CREATE TRIGGER equipment_reservations_sync AFTER UPDATE ON equipment_reservations FOR EACH ROW
  WHEN (OLD.status IS DISTINCT FROM NEW.status AND (NEW.status IN ('APPROVED', 'CANCELLED'))) EXECUTE FUNCTION equipment_reservations_sync();

-- ---------------------------------------------------- operators, logs, meter ---
CREATE FUNCTION equipment_operator_auths_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'an operator authorisation is prepared as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.equipment_class NOT IN ('EXCAVATOR', 'GRADER', 'LOADER', 'BULLDOZER', 'CRANE', 'GENERATOR', 'TRUCK', 'MIXER', 'BATCHING_PLANT', 'COMPACTOR', 'TOOL', 'OTHER') THEN
      RAISE EXCEPTION 'unknown equipment class %', NEW.equipment_class USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided operator authorisation never changes; issue a new one' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'operator authorisation');
    PERFORM approval_use('fleet.operator.submit', 'equipment_operator_auth', NEW.id::text, NEW.created_by, ARRAY['fleet.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'operator authorisation');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting an operator authorisation requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.operator.decide', 'equipment_operator_auth', NEW.id::text, NEW.approved_by, ARRAY['fleet.release'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal operator authorisation transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_operator_auths_guard BEFORE INSERT OR UPDATE ON equipment_operator_auths FOR EACH ROW EXECUTE FUNCTION equipment_operator_auths_guard();

CREATE FUNCTION equipment_logs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE e equipment;
BEGIN
  SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'a log is entered as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.log_date > current_date THEN
      RAISE EXCEPTION 'a log cannot be dated in the future' USING ERRCODE = 'check_violation';
    END IF;
    IF e.current_project_id IS DISTINCT FROM NEW.project_id OR e.status NOT IN ('ASSIGNED', 'OPERATING', 'IDLE', 'BREAKDOWN', 'UNDER_MAINTENANCE', 'UNDER_INSPECTION', 'SAFETY_HOLD', 'DEMOBILIZING') THEN
      RAISE EXCEPTION 'this equipment is not at that project' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.cost_code_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM cost_codes c WHERE c.id = NEW.cost_code_id AND c.project_id = NEW.project_id AND c.status = 'ACTIVE') THEN
      RAISE EXCEPTION 'the cost code is not an active code of that project' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM equipment_operator_auths a WHERE a.tenant_id = current_tenant() AND a.operator = NEW.operator AND a.equipment_class = e.class
                    AND a.status = 'APPROVED' AND NEW.log_date BETWEEN a.valid_from AND a.valid_until) THEN
      RAISE EXCEPTION '% is not authorised to operate % equipment on %', NEW.operator, lower(e.class), NEW.log_date USING ERRCODE = 'check_violation';
    END IF;
    NEW.meter_serial := e.meter_serial;
    IF e.meter_kind = 'NONE' THEN
      IF NEW.meter_open <> 0 OR NEW.meter_close <> 0 THEN
        RAISE EXCEPTION 'this equipment has no meter' USING ERRCODE = 'check_violation';
      END IF;
    ELSIF e.meter_kind = 'HOURS' AND NEW.productive_hours + NEW.idle_hours > (NEW.meter_close - NEW.meter_open) + 0.5 THEN
      RAISE EXCEPTION 'productive and idle hours (%) exceed the engine hours on the meter (%)', NEW.productive_hours + NEW.idle_hours, NEW.meter_close - NEW.meter_open USING ERRCODE = 'check_violation';
    ELSIF e.meter_kind = 'HOURS' AND NEW.meter_close - NEW.meter_open > 24 THEN
      RAISE EXCEPTION 'an hour meter cannot advance more than 24 in one log' USING ERRCODE = 'check_violation';
    END IF;
    NEW.meter_gap := 0;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided log never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'log');
    PERFORM approval_use('fleet.log.submit', 'equipment_log', NEW.id::text, NEW.created_by, ARRAY['fleet.operate'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'log');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a log requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.log.decide', 'equipment_log', NEW.id::text, NEW.approved_by, ARRAY['fleet.log.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason', 'meter_gap'], 'log');
    PERFORM approval_use('fleet.log.decide', 'equipment_log', NEW.id::text, NEW.approved_by, ARRAY['fleet.log.approve'], false, 'APPROVED', NULL);
    -- The meter continues from the last approved reading of the same meter; it never runs backwards.
    SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant() FOR UPDATE;
    IF e.meter_kind <> 'NONE' THEN
      IF NEW.meter_serial <> e.meter_serial THEN
        RAISE EXCEPTION 'the meter was reset after this log was written; re-enter the log against the new meter' USING ERRCODE = 'check_violation';
      END IF;
      IF NEW.meter_open < e.last_meter THEN
        RAISE EXCEPTION 'the opening reading (%) is below the last approved reading (%); a meter never runs backwards', NEW.meter_open, e.last_meter USING ERRCODE = 'check_violation';
      END IF;
      NEW.meter_gap := NEW.meter_open - e.last_meter;
      UPDATE equipment SET last_meter = NEW.meter_close WHERE id = e.id;
    END IF;
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal log transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_logs_guard BEFORE INSERT OR UPDATE ON equipment_logs FOR EACH ROW EXECUTE FUNCTION equipment_logs_guard();

CREATE FUNCTION equipment_meter_resets_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE e equipment;
BEGIN
  SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'a meter reset is prepared as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF e.meter_kind = 'NONE' THEN
      RAISE EXCEPTION 'this equipment has no meter' USING ERRCODE = 'check_violation';
    END IF;
    NEW.old_reading := e.last_meter;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided meter reset never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'meter reset');
    PERFORM approval_use('fleet.meter.submit', 'equipment_meter_reset', NEW.id::text, NEW.created_by, ARRAY['fleet.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'meter reset');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a meter reset requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.meter.decide', 'equipment_meter_reset', NEW.id::text, NEW.approved_by, ARRAY['fleet.release'], NEW.status = 'APPROVED', NEW.status, NULL);
    IF NEW.status = 'APPROVED' THEN
      SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant() FOR UPDATE;
      IF e.last_meter <> OLD.old_reading THEN
        RAISE EXCEPTION 'the meter has moved since this reset was prepared (% -> %); prepare it again', OLD.old_reading, e.last_meter USING ERRCODE = 'check_violation';
      END IF;
      UPDATE equipment SET last_meter = NEW.new_reading, meter_serial = e.meter_serial + 1 WHERE id = e.id;
    END IF;
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal meter reset transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_meter_resets_guard BEFORE INSERT OR UPDATE ON equipment_meter_resets FOR EACH ROW EXECUTE FUNCTION equipment_meter_resets_guard();

-- ---------------------------------------- shared functions (verbatim + 0054)
-- p2p_subject_text: verbatim from 0053 plus fleet (marked 0054).
CREATE OR REPLACE FUNCTION public.p2p_subject_text(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE t uuid := current_tenant(); j jsonb;
BEGIN
  IF p_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RETURN NULL;
  END IF;
  CASE p_type
  WHEN 'p2p_policy' THEN SELECT to_jsonb(x) INTO j FROM p2p_policies x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'supplier' THEN SELECT to_jsonb(x) INTO j FROM suppliers x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'supplier_bank_account' THEN SELECT to_jsonb(x) || jsonb_build_object('account_number', x.account_digest, 'account_number_enc', NULL) INTO j FROM supplier_bank_accounts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'company_bank_account' THEN SELECT to_jsonb(x) || jsonb_build_object('account_number', x.account_digest, 'account_number_enc', NULL) INTO j FROM company_bank_accounts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'boq_version' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'items', (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.item_no COLLATE "C") FROM boq_items i WHERE i.version_id = x.id))
      INTO j FROM boq_versions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'purchase_requisition' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM pr_lines l WHERE l.pr_id = x.id))
      INTO j FROM purchase_requisitions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'rfq' THEN
    SELECT jsonb_build_object('h', to_jsonb(x),
             'suppliers', (SELECT jsonb_agg(s.supplier_id ORDER BY s.supplier_id) FROM rfq_suppliers s WHERE s.rfq_id = x.id),
             'quotes', (SELECT jsonb_agg(jsonb_build_object('q', to_jsonb(q), 'lines',
                          (SELECT jsonb_agg(to_jsonb(ql) ORDER BY ql.pr_line_no) FROM rfq_quote_lines ql WHERE ql.quote_id = q.id)) ORDER BY q.id)
                          FROM rfq_quotes q WHERE q.rfq_id = x.id))
      INTO j FROM rfqs x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'rfq_quote' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.pr_line_no) FROM rfq_quote_lines l WHERE l.quote_id = x.id))
      INTO j FROM rfq_quotes x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'purchase_order' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM po_lines l WHERE l.po_id = x.id))
      INTO j FROM purchase_orders x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'grn' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'journal_id', 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.po_line_no) FROM grn_lines l WHERE l.grn_id = x.id))
      INTO j FROM grns x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'supplier_invoice' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'journal_id', 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.po_line_no) FROM invoice_lines l WHERE l.invoice_id = x.id))
      INTO j FROM supplier_invoices x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'payment_instruction' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'release_journal_id',
             'alloc', (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.invoice_id) FROM payment_allocations a WHERE a.payment_id = x.id),
             'payee', (SELECT to_jsonb(b) || jsonb_build_object('account_number', b.account_digest, 'account_number_enc', NULL) FROM supplier_bank_accounts b WHERE b.id = x.supplier_bank_id))
      INTO j FROM payment_instructions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'bank_statement_line' THEN
    SELECT jsonb_build_object('l', to_jsonb(x) - 'journal_id', 's', (SELECT to_jsonb(s) FROM bank_statements s WHERE s.id = x.statement_id))
      INTO j FROM bank_statement_lines x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'customer' THEN SELECT to_jsonb(x) INTO j FROM customers x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contract_policy' THEN SELECT to_jsonb(x) INTO j FROM contract_policies x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contract' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM contract_lines l WHERE l.contract_id = x.id))
      INTO j FROM contracts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'measurement' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.contract_line_no) FROM measurement_lines l WHERE l.measurement_id = x.id))
      INTO j FROM measurements x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'ipc' THEN SELECT to_jsonb(x) INTO j FROM ipcs x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'customer_invoice' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM customer_invoices x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'customer_receipt' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'journal_id',
             'alloc', (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.line_no) FROM receipt_allocations a WHERE a.receipt_id = x.id),
             'line', (SELECT to_jsonb(l) - 'journal_id' FROM bank_statement_lines l WHERE l.id = x.statement_line_id))
      INTO j FROM customer_receipts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_item' THEN SELECT to_jsonb(x) INTO j FROM stock_items x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'warehouse' THEN SELECT to_jsonb(x) INTO j FROM warehouses x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_receipt' THEN SELECT to_jsonb(x) INTO j FROM stock_receipts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_issue' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM stock_issues x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_count' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM stock_counts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'customer_credit_note' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM customer_credit_notes x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'retention_release' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM retention_releases x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'stock_transfer' THEN SELECT to_jsonb(x) INTO j FROM stock_transfers x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'schedule_version' THEN
    SELECT jsonb_build_object('h', to_jsonb(x),
             'activities', (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.code COLLATE "C") FROM schedule_activities a WHERE a.version_id = x.id),
             'links', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.pred_code COLLATE "C", l.succ_code COLLATE "C") FROM schedule_links l WHERE l.version_id = x.id))
      INTO j FROM schedule_versions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'schedule_progress' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.activity_code COLLATE "C") FROM schedule_progress_lines l WHERE l.progress_id = x.id))
      INTO j FROM schedule_progress x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contract_variation' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM variation_lines l WHERE l.variation_id = x.id))
      INTO j FROM contract_variations x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'site_diary' THEN
    SELECT jsonb_build_object('h', to_jsonb(x),
             'labour', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM site_diary_labour l WHERE l.diary_id = x.id AND l.version = x.version),
             'equipment', (SELECT jsonb_agg(to_jsonb(e) ORDER BY e.line_no) FROM site_diary_equipment e WHERE e.diary_id = x.id AND e.version = x.version))
      INTO j FROM site_diaries x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'site_device' THEN SELECT to_jsonb(x) - 'last_seq' - 'last_sync_at' INTO j FROM site_devices x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0039: budget documents (header and lines are what the decider attests).
  WHEN 'budget_version' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.cost_code_id) FROM budget_lines l WHERE l.version_id = x.id))
      INTO j FROM budget_versions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'budget_transfer' THEN SELECT to_jsonb(x) INTO j FROM budget_transfers x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contingency_drawdown' THEN SELECT to_jsonb(x) INTO j FROM contingency_drawdowns x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0040: governance documents (the decider attests the header and, for a matrix, its rules).
  WHEN 'authority_matrix' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'rules', (SELECT jsonb_agg(to_jsonb(r) ORDER BY r.doc_type, r.currency, r.min_amount) FROM authority_rules r WHERE r.matrix_id = x.id))
      INTO j FROM authority_matrices x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'delegation' THEN SELECT to_jsonb(x) INTO j FROM delegations x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'sod_exception' THEN SELECT to_jsonb(x) INTO j FROM sod_exceptions x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0041: unit conversions (the decider attests the factor and the units).
  WHEN 'uom_conversion' THEN SELECT to_jsonb(x) INTO j FROM uom_conversions x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0042: projects and their stage gates (what the creator and the gate's decider attest).
  WHEN 'project' THEN SELECT to_jsonb(x) INTO j FROM projects x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'project_stage_gate' THEN SELECT to_jsonb(x) INTO j FROM project_stage_gates x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0044: forecast cycles (header and lines are what the decider attests).
  WHEN 'forecast_cycle' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.cost_code_id) FROM forecast_lines l WHERE l.cycle_id = x.id))
      INTO j FROM forecast_cycles x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0045: change events (header and lines are what the decider attests).
  WHEN 'change_event' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) || jsonb_build_object('cost_code', c.code) ORDER BY c.code)
        FROM change_event_lines l JOIN cost_codes c ON c.id = l.cost_code_id WHERE l.event_id = x.id))
      INTO j FROM change_events x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0046: notices and claims.
  WHEN 'contract_notice' THEN SELECT to_jsonb(x) INTO j FROM contract_notices x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'contract_claim' THEN SELECT to_jsonb(x) INTO j FROM contract_claims x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0050: guarantees, index values, escalation formulas (with their indices) and price adjustments.
  WHEN 'guarantee' THEN SELECT to_jsonb(x) INTO j FROM guarantees x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'price_index_value' THEN SELECT to_jsonb(x) INTO j FROM price_index_values x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'escalation_formula' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.index_code COLLATE "C") FROM escalation_terms l WHERE l.formula_id = x.id))
      INTO j FROM escalation_formulas x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'price_adjustment' THEN SELECT to_jsonb(x) INTO j FROM price_adjustments x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0051: liquidated damages terms.
  WHEN 'ld_terms' THEN SELECT to_jsonb(x) INTO j FROM contract_ld_terms x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0052: a document revision (with its register entry; the file is covered by its sha256) and an RFI.
  WHEN 'document_revision' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'doc', (SELECT to_jsonb(d) FROM documents d WHERE d.id = x.document_id))
      INTO j FROM document_revisions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'rfi' THEN SELECT to_jsonb(x) INTO j FROM rfis x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0053: the tenant's document policy. Its id is the tenant id; a tenant with no row has the default policy.
  WHEN 'document_policy' THEN
    IF p_id::uuid <> t THEN RETURN NULL; END IF;
    SELECT COALESCE((SELECT to_jsonb(x) FROM document_policies x WHERE x.tenant_id = t), jsonb_build_object('scan_required', true, 'quota_bytes', NULL, 'default', true)) INTO j;
  -- 0054: fleet. What the person who moves, reserves, logs or resets attests.
  WHEN 'equipment' THEN SELECT to_jsonb(x) INTO j FROM equipment x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_reservation' THEN SELECT to_jsonb(x) INTO j FROM equipment_reservations x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_operator_auth' THEN SELECT to_jsonb(x) INTO j FROM equipment_operator_auths x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_log' THEN SELECT to_jsonb(x) INTO j FROM equipment_logs x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_meter_reset' THEN SELECT to_jsonb(x) INTO j FROM equipment_meter_resets x WHERE tenant_id = t AND id = p_id::uuid;
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0053 plus fleet (marked 0054).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'document_policy', 'equipment_operator_auth') THEN
    RETURN NULL;
  END IF;
  IF p_id IS NULL OR p_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RETURN '';
  END IF;
  v_id := p_id::uuid;
  CASE p_type
  WHEN 'boq_version' THEN SELECT project_id::text INTO r FROM boq_versions WHERE boq_versions.id = v_id;
  WHEN 'purchase_requisition' THEN SELECT project_id::text INTO r FROM purchase_requisitions x WHERE x.id = v_id;
  WHEN 'purchase_order' THEN SELECT project_id::text INTO r FROM purchase_orders x WHERE x.id = v_id;
  WHEN 'rfq' THEN SELECT pr.project_id::text INTO r FROM rfqs q JOIN purchase_requisitions pr ON pr.id = q.pr_id WHERE q.id = v_id;
  WHEN 'rfq_quote' THEN SELECT pr.project_id::text INTO r FROM rfq_quotes x JOIN rfqs q ON q.id = x.rfq_id JOIN purchase_requisitions pr ON pr.id = q.pr_id WHERE x.id = v_id;
  WHEN 'grn' THEN SELECT o.project_id::text INTO r FROM grns g JOIN purchase_orders o ON o.id = g.po_id WHERE g.id = v_id;
  WHEN 'supplier_invoice' THEN SELECT o.project_id::text INTO r FROM supplier_invoices x JOIN purchase_orders o ON o.id = x.po_id WHERE x.id = v_id;
  WHEN 'contract' THEN SELECT project_id::text INTO r FROM contracts x WHERE x.id = v_id;
  WHEN 'measurement' THEN SELECT c.project_id::text INTO r FROM measurements m JOIN contracts c ON c.id = m.contract_id WHERE m.id = v_id;
  WHEN 'ipc' THEN SELECT c.project_id::text INTO r FROM ipcs x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'customer_invoice' THEN SELECT project_id::text INTO r FROM customer_invoices x WHERE x.id = v_id;
  WHEN 'customer_credit_note' THEN SELECT project_id::text INTO r FROM customer_credit_notes x WHERE x.id = v_id;
  WHEN 'retention_release' THEN SELECT project_id::text INTO r FROM retention_releases x WHERE x.id = v_id;
  WHEN 'contract_variation' THEN SELECT c.project_id::text INTO r FROM contract_variations v JOIN contracts c ON c.id = v.contract_id WHERE v.id = v_id;
  WHEN 'schedule_version' THEN SELECT project_id::text INTO r FROM schedule_versions x WHERE x.id = v_id;
  WHEN 'schedule_progress' THEN SELECT project_id::text INTO r FROM schedule_progress x WHERE x.id = v_id;
  WHEN 'warehouse' THEN SELECT COALESCE(project_id::text, '') INTO r FROM warehouses x WHERE x.id = v_id;
  WHEN 'stock_receipt' THEN SELECT COALESCE(w.project_id::text, '') INTO r FROM stock_receipts x JOIN warehouses w ON w.id = x.warehouse_id WHERE x.id = v_id;
  WHEN 'stock_issue' THEN SELECT project_id::text INTO r FROM stock_issues x WHERE x.id = v_id;
  WHEN 'stock_count' THEN SELECT COALESCE(project_id::text, '') INTO r FROM stock_counts x WHERE x.id = v_id;
  WHEN 'stock_transfer' THEN SELECT COALESCE(w.project_id::text, '') INTO r FROM stock_transfers x JOIN warehouses w ON w.id = x.from_warehouse_id WHERE x.id = v_id;
  WHEN 'journal' THEN SELECT project_id::text INTO r FROM journals x WHERE x.id = v_id;
  WHEN 'wbs_version' THEN SELECT project_id::text INTO r FROM wbs_versions x WHERE x.id = v_id;
  WHEN 'workflow_instance' THEN SELECT NULLIF(facts->>'project_id', '') INTO r FROM workflow_instances x WHERE x.id = v_id;
  WHEN 'site_diary' THEN SELECT project_id::text INTO r FROM site_diaries x WHERE x.id = v_id;
  -- 0039: budget documents belong to their project.
  WHEN 'budget_version' THEN SELECT project_id::text INTO r FROM budget_versions x WHERE x.id = v_id;
  WHEN 'budget_transfer' THEN SELECT project_id::text INTO r FROM budget_transfers x WHERE x.id = v_id;
  WHEN 'contingency_drawdown' THEN SELECT project_id::text INTO r FROM contingency_drawdowns x WHERE x.id = v_id;
  -- 0042: a stage gate belongs to its project.
  WHEN 'project_stage_gate' THEN SELECT project_id::text INTO r FROM project_stage_gates x WHERE x.id = v_id;
  -- 0044: a forecast cycle belongs to its project.
  WHEN 'forecast_cycle' THEN SELECT project_id::text INTO r FROM forecast_cycles x WHERE x.id = v_id;
  -- 0045: a change event belongs to its project.
  WHEN 'change_event' THEN SELECT project_id::text INTO r FROM change_events x WHERE x.id = v_id;
  -- 0046: a notice or claim belongs to its contract's project.
  WHEN 'contract_notice' THEN SELECT c.project_id::text INTO r FROM contract_notices x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'contract_claim' THEN SELECT c.project_id::text INTO r FROM contract_claims x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  -- 0050: guarantees, formulas and adjustments belong to their contract's project.
  WHEN 'guarantee' THEN SELECT c.project_id::text INTO r FROM guarantees x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'escalation_formula' THEN SELECT c.project_id::text INTO r FROM escalation_formulas x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'price_adjustment' THEN SELECT c.project_id::text INTO r FROM price_adjustments x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  -- 0051: damages terms belong to their contract's project.
  WHEN 'ld_terms' THEN SELECT c.project_id::text INTO r FROM contract_ld_terms x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  -- 0052: a document revision belongs to its document's project; an RFI to its project.
  WHEN 'document_revision' THEN SELECT d.project_id::text INTO r FROM document_revisions x JOIN documents d ON d.id = x.document_id WHERE x.id = v_id;
  WHEN 'rfi' THEN SELECT project_id::text INTO r FROM rfis x WHERE x.id = v_id;
  -- 0054: equipment belongs to the project it is at (none in the yard: tenant-wide scope); reservations and logs to their project.
  WHEN 'equipment' THEN SELECT COALESCE(current_project_id::text, '') INTO r FROM equipment x WHERE x.id = v_id;
  WHEN 'equipment_meter_reset' THEN SELECT COALESCE(e.current_project_id::text, '') INTO r FROM equipment_meter_resets x JOIN equipment e ON e.id = x.equipment_id WHERE x.id = v_id;
  WHEN 'equipment_reservation' THEN SELECT project_id::text INTO r FROM equipment_reservations x WHERE x.id = v_id;
  WHEN 'equipment_log' THEN SELECT project_id::text INTO r FROM equipment_logs x WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;


-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['equipment', 'equipment_events', 'equipment_reservations', 'equipment_operator_auths', 'equipment_logs', 'equipment_meter_resets'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
-- History is written only by the database (equipment_history is a definer function).
GRANT INSERT ON equipment, equipment_reservations, equipment_operator_auths, equipment_logs, equipment_meter_resets TO serk_app;
GRANT UPDATE (name, capacity, warranty_until, insurance_until, home_location, status, current_project_id, status_reason, status_changed_by, status_changed_at, last_meter, meter_serial) ON equipment TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason, cancelled_by, cancelled_at) ON equipment_reservations TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON equipment_operator_auths TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason, meter_gap) ON equipment_logs TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON equipment_meter_resets TO serk_app;
REVOKE ALL ON FUNCTION equipment_transition_perm(text, text), equipment_rest_status(uuid, uuid), equipment_history(), equipment_reservations_sync() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION equipment_transition_perm(text, text), equipment_rest_status(uuid, uuid) TO serk_app;
