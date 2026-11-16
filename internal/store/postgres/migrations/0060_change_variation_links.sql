-- 0054 Change events linked to contract variations (WP-2.4 completion; SRS PCC-07, CON-03)
--
-- An approved change event (the cost side: budget) is recovered from the
-- client through one or more contract variations (the revenue side). This
-- records which variations carry which event, so the change register shows
-- whether each change was recovered and for how much.
--   * only an APPROVED event is linked, to a variation of a contract on the
--     same project that is not rejected or cancelled;
--   * a variation carries at most one event; links are never removed.
-- Subcontract variations are linked when subcontracts exist (WP-2.5).

CREATE TABLE change_event_variations (
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  event_id     uuid NOT NULL,
  variation_id uuid NOT NULL,
  linked_by    text NOT NULL,
  linked_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, event_id, variation_id),
  UNIQUE (tenant_id, variation_id),
  FOREIGN KEY (tenant_id, event_id) REFERENCES change_events(tenant_id, id),
  FOREIGN KEY (tenant_id, variation_id) REFERENCES contract_variations(tenant_id, id)
);

CREATE FUNCTION change_event_variations_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE e change_events; v record;
BEGIN
  SELECT * INTO e FROM change_events WHERE id = NEW.event_id AND tenant_id = current_tenant();
  IF e.id IS NULL OR e.status <> 'APPROVED' THEN
    RAISE EXCEPTION 'only an approved change event is linked to a variation' USING ERRCODE = 'check_violation';
  END IF;
  SELECT x.status, c.project_id INTO v FROM contract_variations x JOIN contracts c ON c.id = x.contract_id WHERE x.id = NEW.variation_id AND x.tenant_id = current_tenant();
  IF v.project_id IS DISTINCT FROM e.project_id THEN
    RAISE EXCEPTION 'the variation belongs to a contract of another project' USING ERRCODE = 'check_violation';
  END IF;
  IF v.status IN ('REJECTED', 'CANCELLED') THEN
    RAISE EXCEPTION 'a % variation cannot carry a change event', lower(v.status) USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER change_event_variations_guard BEFORE INSERT ON change_event_variations FOR EACH ROW EXECUTE FUNCTION change_event_variations_guard();

ALTER TABLE change_event_variations ENABLE ROW LEVEL SECURITY;
ALTER TABLE change_event_variations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON change_event_variations USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
DO $$ BEGIN
  EXECUTE format('CREATE POLICY owner_maintenance ON change_event_variations TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', current_user);
END$$;
REVOKE ALL ON change_event_variations FROM PUBLIC;
GRANT SELECT, INSERT ON change_event_variations TO serk_app;
