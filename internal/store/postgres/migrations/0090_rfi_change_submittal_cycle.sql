-- 0090_rfi_change_submittal_cycle.sql — an RFI's cost and time impact through its change events (CNX-03.2); a
-- submittal's review cycle (CNX-04.1.b).
--
-- RFI -> CHANGE EVENT: a change event raised from an RFI names it. The RFI must be on the same project and already
-- answered (or closed); the event's source is RFI; the link is fixed when the event is created. The RFI's cost and
-- time impact is then the sum of its change events' lines and schedule days (computed, never typed).
--
-- SUBMITTAL CYCLE: a submittal reviewed C (revise and resubmit) or D (rejected) is answered by a resubmission on the
-- same project and specification section that names it. Each submittal is resubmitted at most once (the cycle is a
-- chain), a submittal approved A or B is never resubmitted, and the cycle number is counted by the database.

ALTER TABLE change_events ADD COLUMN rfi_id uuid;
ALTER TABLE change_events ADD CONSTRAINT change_events_rfi_fk FOREIGN KEY (tenant_id, rfi_id) REFERENCES rfis(tenant_id, id);
CREATE INDEX change_events_rfi ON change_events (tenant_id, rfi_id) WHERE rfi_id IS NOT NULL;

CREATE FUNCTION change_events_rfi_link() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r rfis;
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF NEW.rfi_id IS DISTINCT FROM OLD.rfi_id THEN
      RAISE EXCEPTION 'the RFI a change event arises from is fixed when the event is created' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.rfi_id IS NULL THEN
    RETURN NEW;
  END IF;
  SELECT * INTO r FROM rfis WHERE id = NEW.rfi_id AND tenant_id = current_tenant();
  IF r.id IS NULL OR r.project_id <> NEW.project_id OR r.status NOT IN ('ANSWERED', 'CLOSED') THEN
    RAISE EXCEPTION 'a change event arises from an answered RFI of its own project' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.source <> 'RFI' THEN
    RAISE EXCEPTION 'a change event raised from an RFI has the source RFI' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER change_events_zrfi BEFORE INSERT OR UPDATE ON change_events FOR EACH ROW EXECUTE FUNCTION change_events_rfi_link();

-- An RFI's impact: its change events (any status) with their cost lines and schedule days.
CREATE VIEW rfi_impact WITH (security_barrier = true, security_invoker = true) AS
  SELECT r.tenant_id, r.id AS rfi_id, e.id AS change_event_id, e.number, e.status, e.schedule_impact_days, e.revenue_impact,
         COALESCE((SELECT sum(l.cost_impact) FROM change_event_lines l WHERE l.event_id = e.id), 0) AS cost_impact
    FROM rfis r JOIN change_events e ON e.rfi_id = r.id AND e.tenant_id = r.tenant_id;
REVOKE ALL ON rfi_impact FROM PUBLIC;
GRANT SELECT ON rfi_impact TO serk_app;

ALTER TABLE submittals ADD COLUMN resubmission_of uuid, ADD COLUMN cycle integer NOT NULL DEFAULT 1 CHECK (cycle >= 1);
ALTER TABLE submittals ADD CONSTRAINT submittals_resubmission_fk FOREIGN KEY (tenant_id, resubmission_of) REFERENCES submittals(tenant_id, id);
CREATE UNIQUE INDEX submittals_resubmitted_once ON submittals (tenant_id, resubmission_of) WHERE resubmission_of IS NOT NULL;

CREATE FUNCTION submittals_cycle() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE prev submittals;
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF (NEW.resubmission_of, NEW.cycle) IS DISTINCT FROM (OLD.resubmission_of, OLD.cycle) THEN
      RAISE EXCEPTION 'a submittal''s place in its review cycle never changes' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.resubmission_of IS NULL THEN
    NEW.cycle := 1;
    RETURN NEW;
  END IF;
  SELECT * INTO prev FROM submittals WHERE id = NEW.resubmission_of AND tenant_id = current_tenant();
  IF prev.id IS NULL OR prev.project_id <> NEW.project_id OR prev.spec_section <> NEW.spec_section THEN
    RAISE EXCEPTION 'a resubmission answers a submittal of the same project and specification section' USING ERRCODE = 'check_violation';
  END IF;
  IF prev.status <> 'REVIEWED' OR prev.review_code NOT IN ('C', 'D') THEN
    RAISE EXCEPTION 'only a submittal reviewed C (revise and resubmit) or D (rejected) is resubmitted' USING ERRCODE = 'check_violation';
  END IF;
  NEW.cycle := prev.cycle + 1;
  RETURN NEW;
END$$;
CREATE TRIGGER submittals_zcycle BEFORE INSERT OR UPDATE ON submittals FOR EACH ROW EXECUTE FUNCTION submittals_cycle();
