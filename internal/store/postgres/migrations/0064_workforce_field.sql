-- 0064_workforce_field.sql — WP-3.3 Timesheets and attendance, WP-3.4 Equipment and fleet, WP-3.7 HSE
-- (SRS HCM-02, HCM-03, HCM-07, EQP-01..07, HSE-01..06).
--
-- TIMESHEETS: a site engineer records a project's hours per person for one day (normal and overtime, per
-- cost code); a project manager (a different person) approves. Per person and day the approved hours across
-- projects never exceed 16; one approved line per person, project and day. Approved hours feed payroll.
-- A tenant may switch on an induction gate: nobody without a valid induction on the project is submitted.
--
-- EQUIPMENT: a register (owned, leased, hired) with hire rate and fuel norm; daily usage recorded and approved
-- the same way; the charge is computed when usage is approved; a monthly hire charge run turns approved usage
-- into one draft journal per project (Dr project cost / Cr equipment recovery, accounts chosen by the accountant,
-- approved and posted by finance). Work orders record services and repairs.
--
-- HSE: incidents (reported, investigated, closed by a different person), corrective actions (done, then
-- verified by a different person), permits to work (requested, issued, accepted by three different people),
-- inductions, toolbox talks, safety inspections.
--
-- p2p_subject_text / serk_subject_project: verbatim from 0063 plus timesheets and equipment usage.

-- ------------------------------------------------------------ settings ---
CREATE TABLE hse_settings (
  tenant_id      uuid PRIMARY KEY DEFAULT current_tenant() REFERENCES tenants(id),
  induction_gate boolean NOT NULL DEFAULT false,
  updated_by     text NOT NULL,
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- ----------------------------------------------------------- timesheets ---
CREATE TABLE timesheets (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id      uuid NOT NULL,
  work_date       date NOT NULL,
  number          text NOT NULL,
  notes           text,
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELLED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE INDEX timesheets_project_date ON timesheets (tenant_id, project_id, work_date);

CREATE TABLE timesheet_lines (
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  timesheet_id uuid NOT NULL,
  employee_id  uuid NOT NULL,
  cost_code_id uuid,
  hours_normal numeric(5,2) NOT NULL DEFAULT 0 CHECK (hours_normal >= 0 AND hours_normal <= 16),
  hours_ot     numeric(5,2) NOT NULL DEFAULT 0 CHECK (hours_ot >= 0 AND hours_ot <= 8),
  PRIMARY KEY (tenant_id, timesheet_id, employee_id),
  FOREIGN KEY (tenant_id, timesheet_id) REFERENCES timesheets(tenant_id, id),
  CONSTRAINT timesheet_line_hours CHECK (hours_normal + hours_ot > 0 AND hours_normal + hours_ot <= 16)
);

CREATE FUNCTION timesheet_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE t timesheets; e employees; ent uuid;
BEGIN
  SELECT * INTO t FROM timesheets WHERE id = NEW.timesheet_id AND tenant_id = current_tenant();
  IF t.status <> 'DRAFT' THEN
    RAISE EXCEPTION 'hours are entered while the timesheet is a draft' USING ERRCODE = 'check_violation';
  END IF;
  SELECT legal_entity_id INTO ent FROM projects WHERE id = t.project_id;
  SELECT * INTO e FROM employees WHERE id = NEW.employee_id AND tenant_id = current_tenant();
  IF NOT FOUND OR e.legal_entity_id <> ent OR e.starts_on > t.work_date OR (e.ends_on IS NOT NULL AND e.ends_on < t.work_date) THEN
    RAISE EXCEPTION 'the person is not employed by the project''s entity on that day' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.cost_code_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM cost_codes WHERE id = NEW.cost_code_id AND project_id = t.project_id) THEN
    RAISE EXCEPTION 'the cost code belongs to another project' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER timesheet_lines_guard BEFORE INSERT ON timesheet_lines FOR EACH ROW EXECUTE FUNCTION timesheet_lines_guard();

CREATE FUNCTION timesheets_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE l record; gate boolean;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'timesheets are recorded as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.work_date > current_date THEN
      RAISE EXCEPTION 'a timesheet is for a day that has happened' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED', 'CANCELLED') THEN
    RAISE EXCEPTION 'a % timesheet never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'CANCELLED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'timesheet');
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'timesheet');
    IF NOT EXISTS (SELECT 1 FROM timesheet_lines WHERE timesheet_id = NEW.id) THEN
      RAISE EXCEPTION 'a timesheet has at least one line' USING ERRCODE = 'check_violation';
    END IF;
    SELECT COALESCE(induction_gate, false) INTO gate FROM hse_settings WHERE tenant_id = current_tenant();
    IF COALESCE(gate, false) THEN
      FOR l IN SELECT e.number FROM timesheet_lines tl JOIN employees e ON e.id = tl.employee_id
                WHERE tl.timesheet_id = NEW.id
                  AND NOT EXISTS (SELECT 1 FROM inductions i WHERE i.employee_id = tl.employee_id AND i.project_id = NEW.project_id AND i.inducted_on <= NEW.work_date AND i.valid_to >= NEW.work_date) LOOP
        RAISE EXCEPTION 'employee % has no valid induction on this project (induction gate is on)', l.number USING ERRCODE = 'check_violation';
      END LOOP;
    END IF;
    PERFORM approval_use('timesheet.submit', 'timesheet', NEW.id::text, NEW.created_by, ARRAY['timesheet.record'], false, NULL, NULL);
    NEW.submitted_at := now();
    RETURN NEW;
  END IF;
  IF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'timesheet');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a timesheet requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'APPROVED' THEN
      FOR l IN SELECT e.number, tl.employee_id,
                      (SELECT COALESCE(sum(x.hours_normal + x.hours_ot), 0) FROM timesheet_lines x JOIN timesheets t2 ON t2.id = x.timesheet_id
                        WHERE x.employee_id = tl.employee_id AND t2.work_date = NEW.work_date AND t2.status = 'APPROVED') AS approved_hours,
                      EXISTS (SELECT 1 FROM timesheet_lines x JOIN timesheets t2 ON t2.id = x.timesheet_id
                               WHERE x.employee_id = tl.employee_id AND t2.work_date = NEW.work_date AND t2.project_id = NEW.project_id AND t2.status = 'APPROVED') AS dup,
                      tl.hours_normal + tl.hours_ot AS hours
                 FROM timesheet_lines tl JOIN employees e ON e.id = tl.employee_id WHERE tl.timesheet_id = NEW.id LOOP
        IF l.dup THEN
          RAISE EXCEPTION 'employee % already has approved hours on this project that day', l.number USING ERRCODE = 'check_violation';
        END IF;
        IF l.approved_hours + l.hours > 16 THEN
          RAISE EXCEPTION 'employee % would exceed 16 approved hours that day', l.number USING ERRCODE = 'check_violation';
        END IF;
      END LOOP;
    END IF;
    PERFORM approval_use('timesheet.decide', 'timesheet', NEW.id::text, NEW.approved_by, ARRAY['timesheet.approve'], false, NEW.status, NULL);
    NEW.approved_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal timesheet transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER timesheets_guard BEFORE INSERT OR UPDATE ON timesheets FOR EACH ROW EXECUTE FUNCTION timesheets_guard();

-- ------------------------------------------------------------ equipment ---
CREATE TABLE equipment (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id       uuid NOT NULL,
  code                  text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9-]{1,29}$'),
  name                  text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 200),
  class                 text NOT NULL CHECK (length(btrim(class)) BETWEEN 2 AND 60),
  ownership             text NOT NULL CHECK (ownership IN ('OWNED', 'LEASED', 'HIRED')),
  asset_id              uuid,
  hire_rate             numeric(24,6) NOT NULL CHECK (hire_rate >= 0),
  rate_unit             text NOT NULL CHECK (rate_unit IN ('HOUR', 'DAY')),
  fuel_norm_lph         numeric(8,3) CHECK (fuel_norm_lph IS NULL OR fuel_norm_lph > 0),
  service_interval_hours numeric(10,2) CHECK (service_interval_hours IS NULL OR service_interval_hours > 0),
  location              text,
  status                text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE')),
  created_by            text NOT NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, code),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id)
);

CREATE FUNCTION equipment_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'ACTIVE' THEN RAISE EXCEPTION 'equipment is registered ACTIVE' USING ERRCODE = 'check_violation'; END IF;
    IF NEW.asset_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM fixed_assets WHERE id = NEW.asset_id AND legal_entity_id = NEW.legal_entity_id) THEN
      RAISE EXCEPTION 'the fixed asset belongs to another entity' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'location', 'hire_rate', 'fuel_norm_lph', 'service_interval_hours'], 'equipment');
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_guard BEFORE INSERT OR UPDATE ON equipment FOR EACH ROW EXECUTE FUNCTION equipment_guard();

CREATE TABLE equipment_usage (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id    uuid NOT NULL,
  project_id      uuid NOT NULL,
  cost_code_id    uuid,
  work_date       date NOT NULL,
  number          text NOT NULL,
  hours_work      numeric(5,2) NOT NULL DEFAULT 0 CHECK (hours_work >= 0),
  hours_idle      numeric(5,2) NOT NULL DEFAULT 0 CHECK (hours_idle >= 0),
  hours_breakdown numeric(5,2) NOT NULL DEFAULT 0 CHECK (hours_breakdown >= 0),
  litres_fuel     numeric(10,2) NOT NULL DEFAULT 0 CHECK (litres_fuel >= 0),
  operator_name   text,
  charge          numeric(24,6) NOT NULL DEFAULT 0 CHECK (charge >= 0),
  hire_run_id     uuid,
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELLED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  CONSTRAINT equipment_usage_day CHECK (hours_work + hours_idle + hours_breakdown > 0 AND hours_work + hours_idle + hours_breakdown <= 24)
);
CREATE UNIQUE INDEX equipment_usage_one_per_day ON equipment_usage (tenant_id, equipment_id, project_id, work_date) WHERE status IN ('DRAFT', 'SUBMITTED', 'APPROVED');

CREATE TABLE equipment_hire_runs (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id uuid NOT NULL,
  number       text NOT NULL,
  period_start date NOT NULL,
  period_end   date NOT NULL CHECK (period_end >= period_start),
  cost_account_id     uuid NOT NULL,
  recovery_account_id uuid NOT NULL,
  created_by   text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id)
);
CREATE TABLE equipment_hire_lines (
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id       uuid NOT NULL,
  usage_id     uuid NOT NULL UNIQUE,
  project_id   uuid NOT NULL,
  cost_code_id uuid,
  amount       numeric(24,6) NOT NULL CHECK (amount > 0),
  FOREIGN KEY (tenant_id, run_id) REFERENCES equipment_hire_runs(tenant_id, id),
  FOREIGN KEY (tenant_id, usage_id) REFERENCES equipment_usage(tenant_id, id)
);
CREATE TABLE equipment_hire_journals (
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id     uuid NOT NULL,
  project_id uuid NOT NULL,
  journal_id uuid NOT NULL UNIQUE,
  FOREIGN KEY (tenant_id, run_id) REFERENCES equipment_hire_runs(tenant_id, id)
);

CREATE FUNCTION equipment_hire_runs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.cost_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'EXPENSE' AND active)
     OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.recovery_account_id AND legal_entity_id = NEW.legal_entity_id AND type IN ('EXPENSE', 'REVENUE') AND active)
     OR NEW.cost_account_id = NEW.recovery_account_id THEN
    RAISE EXCEPTION 'hire charge accounts: a cost (expense) account and a different recovery (expense or revenue) account of the entity' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_hire_runs_guard BEFORE INSERT ON equipment_hire_runs FOR EACH ROW EXECUTE FUNCTION equipment_hire_runs_guard();

CREATE FUNCTION equipment_usage_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE e equipment; ent uuid; ccy char(3); booked numeric;
BEGIN
  SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    SELECT legal_entity_id INTO ent FROM projects WHERE id = NEW.project_id;
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.charge <> 0 OR NEW.hire_run_id IS NOT NULL THEN
      RAISE EXCEPTION 'usage is recorded as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF e.status <> 'ACTIVE' OR e.legal_entity_id <> ent THEN
      RAISE EXCEPTION 'the equipment is inactive or belongs to another entity' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.work_date > current_date THEN
      RAISE EXCEPTION 'usage is for a day that has happened' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.cost_code_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM cost_codes WHERE id = NEW.cost_code_id AND project_id = NEW.project_id) THEN
      RAISE EXCEPTION 'the cost code belongs to another project' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'APPROVED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['hire_run_id'], 'equipment usage');
    IF OLD.hire_run_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM equipment_hire_lines WHERE usage_id = NEW.id AND run_id = NEW.hire_run_id AND amount = NEW.charge) THEN
      RAISE EXCEPTION 'usage is charged once, by a hire run line equal to its charge' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED', 'CANCELLED') THEN
    RAISE EXCEPTION 'a % usage record never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'CANCELLED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'equipment usage');
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'equipment usage');
    PERFORM approval_use('equipment.submit', 'equipment_usage', NEW.id::text, NEW.created_by, ARRAY['equipment.record'], false, NULL, NULL);
    RETURN NEW;
  END IF;
  IF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason', 'charge'], 'equipment usage');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a usage record requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'APPROVED' THEN
      SELECT COALESCE(sum(hours_work + hours_idle + hours_breakdown), 0) INTO booked FROM equipment_usage
       WHERE equipment_id = NEW.equipment_id AND work_date = NEW.work_date AND status = 'APPROVED';
      IF booked + NEW.hours_work + NEW.hours_idle + NEW.hours_breakdown > 24 THEN
        RAISE EXCEPTION 'the equipment would exceed 24 hours that day across projects' USING ERRCODE = 'check_violation';
      END IF;
      SELECT functional_currency INTO ccy FROM legal_entities WHERE id = e.legal_entity_id;
      NEW.charge := p2p_round(CASE WHEN e.rate_unit = 'HOUR' THEN NEW.hours_work * e.hire_rate WHEN NEW.hours_work > 0 THEN e.hire_rate ELSE 0 END, ccy);
    ELSIF NEW.charge <> 0 THEN
      RAISE EXCEPTION 'a rejected usage record carries no charge' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('equipment.decide', 'equipment_usage', NEW.id::text, NEW.approved_by, ARRAY['equipment.approve'], false, NEW.status, NULL);
    NEW.approved_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal usage transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER equipment_usage_guard BEFORE INSERT OR UPDATE ON equipment_usage FOR EACH ROW EXECUTE FUNCTION equipment_usage_guard();

CREATE TABLE equipment_work_orders (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id   uuid NOT NULL,
  number         text NOT NULL,
  kind           text NOT NULL CHECK (kind IN ('SERVICE', 'REPAIR')),
  description    text NOT NULL CHECK (length(btrim(description)) BETWEEN 3 AND 1000),
  status         text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'DONE')),
  opened_on      date NOT NULL DEFAULT current_date,
  done_on        date,
  hours_at_done  numeric(12,2),
  cost           numeric(24,6) CHECK (cost IS NULL OR cost >= 0),
  created_by     text NOT NULL,
  closed_by      text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id)
);
CREATE FUNCTION equipment_work_orders_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' THEN RAISE EXCEPTION 'work orders are opened OPEN' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status <> 'OPEN' OR NEW.status <> 'DONE' THEN
    RAISE EXCEPTION 'a work order is completed once' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'done_on', 'hours_at_done', 'cost', 'closed_by'], 'work order');
  IF NEW.done_on IS NULL OR NEW.hours_at_done IS NULL OR NEW.cost IS NULL OR NEW.closed_by IS NULL THEN
    RAISE EXCEPTION 'completion records the date, the equipment hours and the cost' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_work_orders_guard BEFORE INSERT OR UPDATE ON equipment_work_orders FOR EACH ROW EXECUTE FUNCTION equipment_work_orders_guard();

-- ------------------------------------------------------------------ HSE ---
CREATE TABLE incidents (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id    uuid NOT NULL,
  number        text NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('NEAR_MISS', 'FIRST_AID', 'MEDICAL', 'LOST_TIME', 'FATALITY', 'ENVIRONMENTAL', 'PROPERTY')),
  severity      text NOT NULL CHECK (severity IN ('MINOR', 'MAJOR', 'CRITICAL')),
  occurred_at   timestamptz NOT NULL,
  location      text,
  description   text NOT NULL CHECK (length(btrim(description)) BETWEEN 5 AND 4000),
  people_involved integer NOT NULL DEFAULT 0 CHECK (people_involved >= 0),
  lost_days     integer NOT NULL DEFAULT 0 CHECK (lost_days >= 0),
  status        text NOT NULL DEFAULT 'REPORTED' CHECK (status IN ('REPORTED', 'INVESTIGATING', 'CLOSED')),
  reported_by   text NOT NULL,
  reported_at   timestamptz NOT NULL DEFAULT now(),
  closed_by     text,
  closed_at     timestamptz,
  close_note    text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  CONSTRAINT incident_lost_time CHECK ((kind IN ('LOST_TIME', 'FATALITY')) = (kind IN ('LOST_TIME', 'FATALITY')) AND (kind <> 'LOST_TIME' OR lost_days >= 1)),
  CONSTRAINT incident_fatality_critical CHECK (kind <> 'FATALITY' OR severity = 'CRITICAL')
);
CREATE TABLE incident_investigations (
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  incident_id uuid NOT NULL,
  root_cause  text NOT NULL CHECK (length(btrim(root_cause)) BETWEEN 5 AND 4000),
  findings    text NOT NULL CHECK (length(btrim(findings)) BETWEEN 5 AND 4000),
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, incident_id),
  FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents(tenant_id, id)
);
CREATE TABLE capas (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  incident_id uuid NOT NULL,
  description text NOT NULL CHECK (length(btrim(description)) BETWEEN 5 AND 2000),
  owner_name  text NOT NULL CHECK (length(btrim(owner_name)) BETWEEN 2 AND 120),
  due_on      date NOT NULL,
  status      text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'DONE', 'VERIFIED')),
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  done_by     text,
  done_on     date,
  verified_by text,
  verified_at timestamptz,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents(tenant_id, id)
);

CREATE FUNCTION incidents_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'REPORTED' OR NEW.closed_by IS NOT NULL THEN
      RAISE EXCEPTION 'incidents are reported as REPORTED' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.occurred_at > now() + interval '5 minutes' THEN
      RAISE EXCEPTION 'an incident has already happened' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'CLOSED' THEN
    RAISE EXCEPTION 'a closed incident never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'REPORTED' AND NEW.status = 'INVESTIGATING' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'incident');
    IF NOT EXISTS (SELECT 1 FROM incident_investigations WHERE incident_id = NEW.id) THEN
      RAISE EXCEPTION 'an incident moves to investigation when the investigation is recorded' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'INVESTIGATING' AND NEW.status = 'CLOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at', 'close_note'], 'incident');
    IF NEW.closed_by = OLD.reported_by THEN
      RAISE EXCEPTION 'the person who reported an incident cannot close it' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM capas WHERE incident_id = NEW.id AND status <> 'VERIFIED') THEN
      RAISE EXCEPTION 'every corrective action must be done and verified before the incident is closed' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.severity IN ('MAJOR', 'CRITICAL') AND NOT EXISTS (SELECT 1 FROM capas WHERE incident_id = NEW.id) THEN
      RAISE EXCEPTION 'a major or critical incident needs at least one corrective action' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.close_note IS NULL OR length(btrim(NEW.close_note)) < 5 THEN
      RAISE EXCEPTION 'closing an incident needs a note' USING ERRCODE = 'check_violation';
    END IF;
    NEW.closed_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal incident transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER incidents_guard BEFORE INSERT OR UPDATE ON incidents FOR EACH ROW EXECUTE FUNCTION incidents_guard();

CREATE FUNCTION incident_investigations_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM incidents WHERE id = NEW.incident_id AND tenant_id = current_tenant() AND status = 'REPORTED') THEN
    RAISE EXCEPTION 'an investigation is recorded once, on a reported incident' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER incident_investigations_guard BEFORE INSERT ON incident_investigations FOR EACH ROW EXECUTE FUNCTION incident_investigations_guard();

CREATE FUNCTION capas_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' THEN RAISE EXCEPTION 'corrective actions are raised OPEN' USING ERRCODE = 'check_violation'; END IF;
    IF NOT EXISTS (SELECT 1 FROM incidents WHERE id = NEW.incident_id AND tenant_id = current_tenant() AND status <> 'CLOSED') THEN
      RAISE EXCEPTION 'raise actions on an open incident' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'OPEN' AND NEW.status = 'DONE' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'done_by', 'done_on'], 'corrective action');
    IF NEW.done_by IS NULL OR NEW.done_on IS NULL THEN RAISE EXCEPTION 'completion records who and when' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DONE' AND NEW.status = 'VERIFIED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'verified_by', 'verified_at'], 'corrective action');
    IF NEW.verified_by IS NULL OR NEW.verified_by = OLD.done_by THEN
      RAISE EXCEPTION 'the person who completed an action cannot verify it' USING ERRCODE = 'check_violation';
    END IF;
    NEW.verified_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal corrective action transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER capas_guard BEFORE INSERT OR UPDATE ON capas FOR EACH ROW EXECUTE FUNCTION capas_guard();

CREATE TABLE permits_to_work (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id   uuid NOT NULL,
  number       text NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('HOT_WORK', 'CONFINED_SPACE', 'WORK_AT_HEIGHT', 'EXCAVATION', 'ELECTRICAL', 'LIFTING')),
  location     text NOT NULL CHECK (length(btrim(location)) BETWEEN 2 AND 200),
  description  text NOT NULL CHECK (length(btrim(description)) BETWEEN 5 AND 2000),
  conditions   text,
  valid_from   timestamptz NOT NULL,
  valid_to     timestamptz NOT NULL,
  status       text NOT NULL DEFAULT 'REQUESTED' CHECK (status IN ('REQUESTED', 'ISSUED', 'ACCEPTED', 'CLOSED', 'CANCELLED')),
  requested_by text NOT NULL,
  issued_by    text,
  accepted_by  text,
  closed_by    text,
  closed_at    timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  CONSTRAINT permit_window CHECK (valid_to > valid_from AND valid_to <= valid_from + interval '31 days')
);
CREATE FUNCTION permits_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'REQUESTED' OR NEW.issued_by IS NOT NULL THEN RAISE EXCEPTION 'permits are requested as REQUESTED' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('CLOSED', 'CANCELLED') THEN RAISE EXCEPTION 'a % permit never changes', lower(OLD.status) USING ERRCODE = 'check_violation'; END IF;
  IF OLD.status = 'REQUESTED' AND NEW.status = 'ISSUED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'issued_by', 'conditions'], 'permit');
    IF NEW.issued_by IS NULL OR NEW.issued_by = OLD.requested_by THEN RAISE EXCEPTION 'the person who requests a permit cannot issue it' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'ISSUED' AND NEW.status = 'ACCEPTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'accepted_by'], 'permit');
    IF NEW.accepted_by IS NULL OR NEW.accepted_by = OLD.issued_by THEN RAISE EXCEPTION 'the person who issues a permit cannot accept it' USING ERRCODE = 'check_violation'; END IF;
    IF now() > OLD.valid_to THEN RAISE EXCEPTION 'the permit has expired' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'ACCEPTED' AND NEW.status = 'CLOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at'], 'permit');
    IF NEW.closed_by IS NULL THEN RAISE EXCEPTION 'closing records who' USING ERRCODE = 'check_violation'; END IF;
    NEW.closed_at := now();
    RETURN NEW;
  END IF;
  IF NEW.status = 'CANCELLED' AND OLD.status IN ('REQUESTED', 'ISSUED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'permit');
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal permit transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER permits_guard BEFORE INSERT OR UPDATE ON permits_to_work FOR EACH ROW EXECUTE FUNCTION permits_guard();

CREATE TABLE inductions (
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  employee_id uuid NOT NULL,
  project_id  uuid NOT NULL,
  inducted_on date NOT NULL,
  valid_to    date NOT NULL,
  topics      text,
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT induction_validity CHECK (valid_to > inducted_on AND valid_to <= inducted_on + 400),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE INDEX inductions_lookup ON inductions (tenant_id, employee_id, project_id);
CREATE TABLE toolbox_talks (
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL,
  held_on    date NOT NULL,
  topic      text NOT NULL CHECK (length(btrim(topic)) BETWEEN 3 AND 300),
  attendees  integer NOT NULL CHECK (attendees >= 1),
  notes      text,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE TABLE safety_inspections (
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid NOT NULL,
  inspected_on date NOT NULL,
  area         text NOT NULL CHECK (length(btrim(area)) BETWEEN 2 AND 200),
  score        integer NOT NULL CHECK (score BETWEEN 0 AND 100),
  findings     text,
  created_by   text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);


-- ---------------------------------------- shared functions (verbatim + 0064)
-- p2p_subject_text: verbatim from the previous migration plus 0064.
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
  -- 0053: revenue policies.
  WHEN 'revenue_policy' THEN SELECT to_jsonb(x) INTO j FROM revenue_policies x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0055: subcontract documents.
  WHEN 'subcontract_policy' THEN SELECT to_jsonb(x) INTO j FROM subcontract_policies x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'subcontract' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM subcontract_lines l WHERE l.subcontract_id = x.id))
      INTO j FROM subcontracts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'subcontract_application' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'journal_id', 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM subcontract_application_lines l WHERE l.application_id = x.id))
      INTO j FROM subcontract_applications x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'subcontract_backcharge' THEN SELECT to_jsonb(x) INTO j FROM subcontract_backcharges x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0056: subcontract payments with their allocations.
  WHEN 'subcontract_payment' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'release_journal_id', 'alloc', (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.application_id) FROM subcontract_payment_allocations a WHERE a.payment_id = x.id),
             'payee', (SELECT to_jsonb(b) || jsonb_build_object('account_number', b.account_digest, 'account_number_enc', NULL) FROM supplier_bank_accounts b WHERE b.id = x.supplier_bank_id))
      INTO j FROM subcontract_payments x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0057: fixed assets and disposals.
  WHEN 'fixed_asset' THEN SELECT to_jsonb(x) - 'cap_journal_id' INTO j FROM fixed_assets x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'asset_disposal' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM asset_disposals x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0061: payroll documents (a pay run with its computed lines).
  WHEN 'payroll_policy' THEN SELECT to_jsonb(x) INTO j FROM payroll_policies x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'payroll_rule_set' THEN SELECT to_jsonb(x) INTO j FROM payroll_rule_sets x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'pay_run' THEN
    SELECT jsonb_build_object('h', to_jsonb(x) - 'pay_journal_id', 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.employee_id) FROM pay_lines l WHERE l.run_id = x.id))
      INTO j FROM pay_runs x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0062: consolidation groups (with members) and runs (with their computed lines).
  WHEN 'consolidation_group' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'members', (SELECT jsonb_agg(to_jsonb(m) ORDER BY m.legal_entity_id) FROM consolidation_members m WHERE m.group_id = x.id))
      INTO j FROM consolidation_groups x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'consolidation_run' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', consolidation_lines_text(x.id)) INTO j FROM consolidation_runs x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0063: tax returns with their computed lines.
  WHEN 'tax_return' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', tax_return_lines_text(x.id, true)) INTO j FROM tax_returns x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0064: timesheets (with lines) and equipment usage.
  WHEN 'timesheet' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.employee_id) FROM timesheet_lines l WHERE l.timesheet_id = x.id))
      INTO j FROM timesheets x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'equipment_usage' THEN SELECT to_jsonb(x) - 'hire_run_id' INTO j FROM equipment_usage x WHERE tenant_id = t AND id = p_id::uuid;
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from the previous migration plus 0064.
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy', 'fixed_asset', 'asset_disposal', 'payroll_policy', 'payroll_rule_set', 'pay_run', 'consolidation_group', 'consolidation_run', 'tax_return') THEN
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
  -- 0055: subcontract documents belong to the subcontract's project.
  WHEN 'subcontract' THEN SELECT project_id::text INTO r FROM subcontracts x WHERE x.id = v_id;
  WHEN 'subcontract_application' THEN SELECT s.project_id::text INTO r FROM subcontract_applications x JOIN subcontracts s ON s.id = x.subcontract_id WHERE x.id = v_id;
  WHEN 'subcontract_backcharge' THEN SELECT s.project_id::text INTO r FROM subcontract_backcharges x JOIN subcontracts s ON s.id = x.subcontract_id WHERE x.id = v_id;
  WHEN 'subcontract_payment' THEN SELECT s.project_id::text INTO r FROM subcontract_payments x JOIN subcontracts s ON s.id = x.subcontract_id WHERE x.id = v_id;
  -- 0064: timesheets and equipment usage belong to their project.
  WHEN 'timesheet' THEN SELECT project_id::text INTO r FROM timesheets x WHERE x.id = v_id;
  WHEN 'equipment_usage' THEN SELECT project_id::text INTO r FROM equipment_usage x WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['hse_settings', 'timesheets', 'timesheet_lines', 'equipment', 'equipment_usage', 'equipment_hire_runs', 'equipment_hire_lines', 'equipment_hire_journals', 'equipment_work_orders', 'incidents', 'incident_investigations', 'capas', 'permits_to_work', 'inductions', 'toolbox_talks', 'safety_inspections'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, submitted_at, approved_by, approved_at, decision_reason) ON timesheets TO serk_app;
GRANT UPDATE (status, location, hire_rate, fuel_norm_lph, service_interval_hours) ON equipment TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason, charge, hire_run_id) ON equipment_usage TO serk_app;
GRANT UPDATE (status, done_on, hours_at_done, cost, closed_by) ON equipment_work_orders TO serk_app;
GRANT UPDATE (status, closed_by, closed_at, close_note) ON incidents TO serk_app;
GRANT UPDATE (status, done_by, done_on, verified_by, verified_at) ON capas TO serk_app;
GRANT UPDATE (status, issued_by, conditions, accepted_by, closed_by, closed_at) ON permits_to_work TO serk_app;
GRANT UPDATE (induction_gate, updated_by, updated_at) ON hse_settings TO serk_app;
