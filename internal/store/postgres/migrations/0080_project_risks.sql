-- 0080_project_risks.sql — project risk register with quantified exposure (PLB-04.1).
--
-- A risk belongs to a project and has an owner, a cause and effect, a response and a history of assessments.
-- Each assessment (probability, cost impact, schedule impact) is a new row: an assessment never changes, so the
-- register shows how a risk was judged over time. The current exposure of an open risk is its latest probability
-- times its latest cost impact; the project's open exposure is compared with the contingency still in its budget
-- (project_budget_current, cost codes flagged as contingency), so a shortfall is visible before it is spent.
-- Closing a risk needs a note and is final.

CREATE TABLE project_risks (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id  uuid NOT NULL,
  number      text NOT NULL,
  title       text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 300),
  category    text NOT NULL CHECK (category IN ('COST', 'SCHEDULE', 'SAFETY', 'QUALITY', 'COMMERCIAL', 'DESIGN', 'OTHER')),
  cause       text NOT NULL CHECK (length(btrim(cause)) BETWEEN 3 AND 2000),
  effect      text NOT NULL CHECK (length(btrim(effect)) BETWEEN 3 AND 2000),
  owner       text NOT NULL CHECK (length(btrim(owner)) BETWEEN 2 AND 200),
  response    text NOT NULL CHECK (length(btrim(response)) BETWEEN 3 AND 2000),
  status      text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'CLOSED')),
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  closed_by   text,
  closed_at   timestamptz,
  close_note  text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);

CREATE TABLE risk_assessments (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  risk_id        uuid NOT NULL,
  probability    numeric(5,4) NOT NULL CHECK (probability > 0 AND probability <= 1),
  cost_impact    numeric(24,6) NOT NULL CHECK (cost_impact >= 0),
  schedule_days  integer NOT NULL CHECK (schedule_days BETWEEN 0 AND 3650),
  note           text,
  assessed_by    text NOT NULL,
  assessed_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, risk_id) REFERENCES project_risks(tenant_id, id)
);
CREATE INDEX risk_assessments_latest ON risk_assessments (tenant_id, risk_id, assessed_at DESC, id DESC);

CREATE FUNCTION project_risks_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' OR NEW.closed_by IS NOT NULL THEN RAISE EXCEPTION 'risks are raised OPEN' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'CLOSED' THEN RAISE EXCEPTION 'a closed risk never changes' USING ERRCODE = 'check_violation'; END IF;
  IF NEW.status = 'CLOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at', 'close_note'], 'risk');
    IF NEW.closed_by IS NULL OR NEW.close_note IS NULL OR length(btrim(NEW.close_note)) < 5 THEN
      RAISE EXCEPTION 'closing a risk records who and why' USING ERRCODE = 'check_violation';
    END IF;
    NEW.closed_at := now();
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['owner', 'response'], 'risk');
  RETURN NEW;
END$$;
CREATE TRIGGER project_risks_guard BEFORE INSERT OR UPDATE ON project_risks FOR EACH ROW EXECUTE FUNCTION project_risks_guard();

CREATE FUNCTION risk_assessments_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM project_risks WHERE id = NEW.risk_id AND tenant_id = current_tenant() AND status = 'OPEN') THEN
    RAISE EXCEPTION 'only an open risk is assessed' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER risk_assessments_guard BEFORE INSERT ON risk_assessments FOR EACH ROW EXECUTE FUNCTION risk_assessments_guard();

-- A project's open risks at their latest assessment, with exposure in the project's minor unit.
CREATE VIEW project_risk_exposure WITH (security_invoker = true) AS
SELECT r.tenant_id, r.project_id, r.id AS risk_id, r.number, r.title, r.category, r.owner, r.status,
       a.probability, a.cost_impact, a.schedule_days, a.assessed_at,
       round_half_even(a.probability * a.cost_impact, currency_minor_units(le.functional_currency)) AS exposure
  FROM project_risks r
  JOIN projects p ON p.id = r.project_id
  JOIN legal_entities le ON le.id = p.legal_entity_id
  LEFT JOIN LATERAL (SELECT x.probability, x.cost_impact, x.schedule_days, x.assessed_at FROM risk_assessments x
                      WHERE x.risk_id = r.id ORDER BY x.assessed_at DESC, x.id DESC LIMIT 1) a ON true;

DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['project_risks', 'risk_assessments'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (owner, response, status, closed_by, closed_at, close_note) ON project_risks TO serk_app;
REVOKE ALL ON project_risk_exposure FROM PUBLIC;
GRANT SELECT ON project_risk_exposure TO serk_app;
