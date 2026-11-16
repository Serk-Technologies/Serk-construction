-- 0075_overhead_no_overlap.sql — overhead is never allocated twice for the same days (DEF-012).
--
-- The carried rule (0064, new-version 0058) refused only a second run over exactly the same range. The pool
-- calculation excludes OVERHEAD journals, so allocating January–February after January allocated January's
-- pool again. Each run now claims every day of its range for its entity and pool in overhead_run_days,
-- whose primary key makes a second claim of a day impossible — also for two runs committed concurrently,
-- without locks the application role is not allowed to take. A plain check first gives a readable message.

CREATE TABLE overhead_run_days (
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id uuid NOT NULL,
  pool_account_id uuid NOT NULL,
  day             date NOT NULL,
  run_id          uuid NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id, pool_account_id, day),
  FOREIGN KEY (tenant_id, run_id) REFERENCES overhead_runs(tenant_id, id)
);

CREATE FUNCTION overhead_runs_no_overlap() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE prior overhead_runs;
BEGIN
  SELECT * INTO prior FROM overhead_runs
   WHERE tenant_id = NEW.tenant_id AND legal_entity_id = NEW.legal_entity_id AND pool_account_id = NEW.pool_account_id AND id <> NEW.id
     AND daterange(period_from, period_to, '[]') && daterange(NEW.period_from, NEW.period_to, '[]')
   LIMIT 1;
  IF FOUND THEN
    RAISE EXCEPTION 'overhead for % to % overlaps run % (% to %): a day''s pool is allocated once', NEW.period_from, NEW.period_to, prior.number, prior.period_from, prior.period_to
      USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO overhead_run_days (tenant_id, legal_entity_id, pool_account_id, day, run_id)
    SELECT NEW.tenant_id, NEW.legal_entity_id, NEW.pool_account_id, d::date, NEW.id
      FROM generate_series(NEW.period_from, NEW.period_to, interval '1 day') d;
  RETURN NEW;
END$$;
CREATE TRIGGER overhead_runs_no_overlap AFTER INSERT ON overhead_runs FOR EACH ROW EXECUTE FUNCTION overhead_runs_no_overlap();

-- A claimed day never changes; it is written only by the trigger above, inside the run's own insert.
CREATE FUNCTION overhead_run_days_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP <> 'INSERT' OR pg_trigger_depth() < 2 THEN
    RAISE EXCEPTION 'overhead days are claimed only by an overhead run' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER overhead_run_days_guard BEFORE INSERT OR UPDATE OR DELETE ON overhead_run_days FOR EACH ROW EXECUTE FUNCTION overhead_run_days_guard();

ALTER TABLE overhead_run_days ENABLE ROW LEVEL SECURITY;
ALTER TABLE overhead_run_days FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON overhead_run_days USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
DO $$
BEGIN
  EXECUTE format('CREATE POLICY owner_maintenance ON overhead_run_days TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', current_user);
END$$;
REVOKE ALL ON overhead_run_days FROM PUBLIC;
GRANT SELECT, INSERT ON overhead_run_days TO serk_app;
