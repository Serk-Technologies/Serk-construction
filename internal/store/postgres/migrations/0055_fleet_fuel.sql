-- 0055 SERK FLEET, part 2: fuel (requirements FLT-02.x; ADR 0022)
--
-- WHAT THIS MIGRATION DOES
--   Fuel is ordinary stock. It is bought, received and put away through the
--   existing procure-to-pay chain; a tank or a bowser is a warehouse; filling
--   a machine is a STOCK ISSUE. Nothing here holds a quantity or a value of
--   fuel: the stock ledger does, and the STK_ISSUE journal carries the cost to
--   the project and cost code. This migration adds only what the stock ledger
--   does not know:
--   * FUEL ITEMS: which active (approved) stock items are fuel. Designated by a person
--     with fleet.approve, attested, never changed afterwards.
--   * FUEL DISPENSES: the fact that a given stock issue went into a given
--     machine, with the meter reading and the operator. One per issue, written
--     with the issue and never changed. The database checks that the issue is
--     the caller's own draft, of a fuel item, for the same project and litres,
--     and that the machine is at that project and able to take fuel. A dispense
--     counts only when its issue is APPROVED by a second person (the existing
--     stock-issue rule), which is also what values it and removes the stock.
--   * FUEL STANDARDS: the expected litres per metered unit (hour or km) of a
--     machine, effective from a date. Append-only: a new rate is a new row.
--     Set by a person with fleet.approve, attested.
--   * equipment_fuel_summary(): per machine and period, litres dispensed
--     (approved issues), litres written in approved daily logs, metered units
--     worked, the standard in force and the expected litres. Reading it is
--     how dispensed, logged and expected fuel are reconciled.
--
-- NOT DONE HERE: tank dip readings (a tank count is the existing stock count),
--   investigation records for a variance, telematics fuel sensors.
--
-- No shared function is redefined: no new document type is introduced. The
-- attested subjects are existing ones (stock_item, equipment, stock_issue).

CREATE TABLE fuel_items (
  tenant_id     uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  stock_item_id uuid NOT NULL,
  designated_by text NOT NULL,
  designated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, stock_item_id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_items(tenant_id, id)
);

CREATE FUNCTION fuel_items_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM stock_items WHERE id = NEW.stock_item_id AND tenant_id = current_tenant() AND status = 'ACTIVE') THEN
    RAISE EXCEPTION 'only an approved stock item can be designated as fuel' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('fleet.fuel.item.designate', 'stock_item', NEW.stock_item_id::text, NEW.designated_by, ARRAY['fleet.approve'], false, NULL, NULL);
  NEW.designated_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER fuel_items_guard BEFORE INSERT ON fuel_items FOR EACH ROW EXECUTE FUNCTION fuel_items_guard();

CREATE TABLE fuel_dispenses (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  stock_issue_id uuid NOT NULL,
  equipment_id   uuid NOT NULL,
  project_id     uuid NOT NULL,
  dispensed_on   date NOT NULL,
  litres         numeric(20,4) NOT NULL CHECK (litres > 0),
  meter_serial   integer NOT NULL,
  meter_reading  numeric(14,2) CHECK (meter_reading IS NULL OR meter_reading >= 0),
  operator       text NOT NULL CHECK (operator ~ '^[A-Za-z0-9._@:+|-]{1,200}$'),
  client_ref     text CHECK (client_ref IS NULL OR client_ref ~ '^[A-Za-z0-9._:-]{8,80}$'),
  created_by     text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, stock_issue_id),
  FOREIGN KEY (tenant_id, stock_issue_id) REFERENCES stock_issues(tenant_id, id),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);
CREATE UNIQUE INDEX fuel_dispenses_client_ref ON fuel_dispenses (tenant_id, created_by, client_ref) WHERE client_ref IS NOT NULL;
CREATE INDEX fuel_dispenses_by_equipment ON fuel_dispenses (tenant_id, equipment_id, dispensed_on);
CREATE INDEX fuel_dispenses_by_project ON fuel_dispenses (tenant_id, project_id, dispensed_on);

CREATE FUNCTION fuel_dispenses_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE i stock_issues; e equipment;
BEGIN
  SELECT * INTO i FROM stock_issues WHERE id = NEW.stock_issue_id AND tenant_id = current_tenant() FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'the stock issue of a fuel dispense does not exist' USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF i.status <> 'DRAFT' OR i.created_by <> NEW.created_by THEN
    RAISE EXCEPTION 'fuel is dispensed against the preparer''s own draft stock issue' USING ERRCODE = 'check_violation';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM fuel_items f WHERE f.tenant_id = current_tenant() AND f.stock_item_id = i.stock_item_id) THEN
    RAISE EXCEPTION 'the issued item is not designated as fuel' USING ERRCODE = 'check_violation';
  END IF;
  IF i.project_id <> NEW.project_id OR i.quantity <> NEW.litres OR i.issue_date <> NEW.dispensed_on THEN
    RAISE EXCEPTION 'a fuel dispense repeats its stock issue: same project, quantity and date' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant();
  IF NOT FOUND THEN
    RAISE EXCEPTION 'unknown equipment' USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF e.current_project_id IS DISTINCT FROM NEW.project_id THEN
    RAISE EXCEPTION 'the equipment is not at this project' USING ERRCODE = 'check_violation';
  END IF;
  IF e.status IN ('REGISTERED', 'RETIRED', 'DISPOSED') THEN
    RAISE EXCEPTION 'equipment in state % takes no fuel', e.status USING ERRCODE = 'check_violation';
  END IF;
  IF e.meter_kind = 'NONE' THEN
    IF NEW.meter_reading IS NOT NULL THEN
      RAISE EXCEPTION 'this equipment has no meter' USING ERRCODE = 'check_violation';
    END IF;
  ELSIF NEW.meter_reading IS NULL THEN
    RAISE EXCEPTION 'a metered machine is fuelled against its meter reading' USING ERRCODE = 'check_violation';
  ELSIF NEW.meter_reading < e.last_meter THEN
    RAISE EXCEPTION 'the meter reading (%) is behind the last approved reading (%)', NEW.meter_reading, e.last_meter USING ERRCODE = 'check_violation';
  END IF;
  NEW.meter_serial := e.meter_serial;
  PERFORM approval_use('fleet.fuel.dispense.create', 'stock_issue', NEW.stock_issue_id::text, NEW.created_by, ARRAY['stock.issue'], false, NEW.equipment_id::text, NULL);
  NEW.created_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER fuel_dispenses_guard BEFORE INSERT ON fuel_dispenses FOR EACH ROW EXECUTE FUNCTION fuel_dispenses_guard();

CREATE TABLE equipment_fuel_standards (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  equipment_id   uuid NOT NULL,
  litres_per_unit numeric(10,4) NOT NULL CHECK (litres_per_unit > 0 AND litres_per_unit <= 10000),
  effective_from date NOT NULL,
  basis          text NOT NULL CHECK (length(btrim(basis)) BETWEEN 5 AND 500),
  set_by         text NOT NULL,
  set_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, equipment_id, effective_from),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id)
);

CREATE FUNCTION equipment_fuel_standards_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE e equipment;
BEGIN
  SELECT * INTO e FROM equipment WHERE id = NEW.equipment_id AND tenant_id = current_tenant();
  IF NOT FOUND THEN
    RAISE EXCEPTION 'unknown equipment' USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF e.meter_kind = 'NONE' THEN
    RAISE EXCEPTION 'a fuel standard is litres per metered unit; this equipment has no meter' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('fleet.fuel.standard.set', 'equipment', NEW.equipment_id::text, NEW.set_by, ARRAY['fleet.approve'], false,
                       NEW.litres_per_unit::text || '|' || NEW.effective_from::text, NULL);
  NEW.set_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_fuel_standards_guard BEFORE INSERT ON equipment_fuel_standards FOR EACH ROW EXECUTE FUNCTION equipment_fuel_standards_guard();

-- Reconciliation figures per machine for a period. Invoker rights: row security
-- limits it to the caller's tenant; the kernel applies project scope.
-- The standard used is the one in force at the end of the period.
CREATE FUNCTION equipment_fuel_summary(p_from date, p_to date, p_projects uuid[])
RETURNS TABLE (equipment_id uuid, dispensed_litres numeric, dispenses bigint, pending_litres numeric, fuel_cost numeric,
               logged_litres numeric, metered_units numeric, litres_per_unit numeric, standard_from date)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  WITH d AS (
    SELECT f.equipment_id,
           COALESCE(sum(f.litres) FILTER (WHERE i.status = 'APPROVED'), 0) AS dispensed,
           count(*) FILTER (WHERE i.status = 'APPROVED') AS n,
           COALESCE(sum(f.litres) FILTER (WHERE i.status = 'DRAFT'), 0) AS pending,
           COALESCE(sum(i.amount) FILTER (WHERE i.status = 'APPROVED'), 0) AS cost
      FROM fuel_dispenses f JOIN stock_issues i ON i.id = f.stock_issue_id AND i.tenant_id = f.tenant_id
     WHERE f.tenant_id = current_tenant() AND f.dispensed_on BETWEEN p_from AND p_to AND (p_projects IS NULL OR f.project_id = ANY(p_projects))
     GROUP BY f.equipment_id),
  l AS (
    SELECT g.equipment_id, COALESCE(sum(g.fuel_litres), 0) AS logged, COALESCE(sum(g.meter_close - g.meter_open), 0) AS units
      FROM equipment_logs g
     WHERE g.tenant_id = current_tenant() AND g.status = 'APPROVED' AND g.log_date BETWEEN p_from AND p_to AND (p_projects IS NULL OR g.project_id = ANY(p_projects))
     GROUP BY g.equipment_id)
  SELECT COALESCE(d.equipment_id, l.equipment_id), COALESCE(d.dispensed, 0), COALESCE(d.n, 0), COALESCE(d.pending, 0), COALESCE(d.cost, 0),
         COALESCE(l.logged, 0), COALESCE(l.units, 0), s.litres_per_unit, s.effective_from
    FROM d FULL JOIN l ON l.equipment_id = d.equipment_id
    LEFT JOIN LATERAL (SELECT x.litres_per_unit, x.effective_from FROM equipment_fuel_standards x
                        WHERE x.tenant_id = current_tenant() AND x.equipment_id = COALESCE(d.equipment_id, l.equipment_id) AND x.effective_from <= p_to
                        ORDER BY x.effective_from DESC LIMIT 1) s ON true
$$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner_role text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['fuel_items', 'fuel_dispenses', 'equipment_fuel_standards'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner_role);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
  END LOOP;
END$$;
-- Insert only: a designation, a dispense and a standard are facts that are never edited or removed.
GRANT SELECT, INSERT ON fuel_items, fuel_dispenses, equipment_fuel_standards TO serk_app;
GRANT EXECUTE ON FUNCTION equipment_fuel_summary(date, date, uuid[]) TO serk_app;
