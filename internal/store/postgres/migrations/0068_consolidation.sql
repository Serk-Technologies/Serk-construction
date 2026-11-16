-- 0062_consolidation.sql — WP-2.10 Multi-entity consolidation and intercompany (SRS FIN-08, FIN-09).
--
-- A consolidation group (versioned, proposed by a controller with consolidation.manage, approved by the
-- CFO with consolidation.approve and fresh MFA) lists member entities with ownership and method
-- (FULL, PROPORTIONATE, EQUITY). Intercompany accounts are designated per entity with their counterparty
-- and role. A consolidation run, prepared for a period with the closing and average rates it uses,
-- holds the consolidated lines that the database computes (consolidation_compute):
--   * each member's posted balances by account code: balance-sheet accounts to the period end at the
--     closing rate, income and expense for the period at the average rate, earlier income and expense
--     as retained earnings brought forward at the closing rate (IAS 21, simplified: equity is not kept
--     at historical rates); the difference is the translation reserve (CTA);
--   * PROPORTIONATE members at their share; EQUITY members as one investment line at their share of
--     net assets; FULL members below 100% with a non-controlling-interest reclassification;
--   * intercompany balances and income/expense between members eliminated; whatever does not match
--     between a pair of entities stays visible as an intercompany difference line.
-- A run is a report: nothing is posted to any entity's ledger. Approved runs never change.
--
-- Also: p2p_subject_text / serk_subject_project verbatim from 0061 plus the two new documents.

-- ------------------------------------------------------------- groups ---
CREATE TABLE consolidation_groups (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  code                  text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9-]{1,19}$'),
  version               integer NOT NULL CHECK (version >= 1),
  name                  text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 200),
  presentation_currency char(3) NOT NULL REFERENCES currencies(code),
  parent_entity_id      uuid NOT NULL,
  status                text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'APPROVED', 'REJECTED', 'RETIRED')),
  created_by            text NOT NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  approved_by           text,
  approved_at           timestamptz,
  decision_reason       text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, code, version),
  FOREIGN KEY (tenant_id, parent_entity_id) REFERENCES legal_entities(tenant_id, id)
);
CREATE UNIQUE INDEX consolidation_groups_one_approved ON consolidation_groups (tenant_id, code) WHERE status = 'APPROVED';

CREATE TABLE consolidation_members (
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  group_id        uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ownership_pct   numeric(7,6) NOT NULL CHECK (ownership_pct > 0 AND ownership_pct <= 1),
  method          text NOT NULL CHECK (method IN ('FULL', 'PROPORTIONATE', 'EQUITY')),
  PRIMARY KEY (tenant_id, group_id, legal_entity_id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES consolidation_groups(tenant_id, id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id)
);

CREATE FUNCTION consolidation_members_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM consolidation_groups WHERE id = NEW.group_id AND status = 'DRAFT') THEN
    RAISE EXCEPTION 'members are added while the group is a draft' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER consolidation_members_guard BEFORE INSERT ON consolidation_members FOR EACH ROW EXECUTE FUNCTION consolidation_members_guard();

CREATE FUNCTION consolidation_groups_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'consolidation groups are proposed as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'RETIRED') THEN
    RAISE EXCEPTION 'a % consolidation group never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'APPROVED' THEN
    IF NEW.status <> 'RETIRED' THEN
      RAISE EXCEPTION 'an approved group is only retired by its successor' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'consolidation group');
    RETURN NEW;
  END IF;
  IF NEW.status NOT IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'illegal consolidation group transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'consolidation group');
  IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
    RAISE EXCEPTION 'rejecting a consolidation group requires a reason' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'APPROVED' THEN
    IF (SELECT count(*) FROM consolidation_members WHERE group_id = NEW.id) < 2 THEN
      RAISE EXCEPTION 'a consolidation group has at least two members' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM consolidation_members WHERE group_id = NEW.id AND legal_entity_id = NEW.parent_entity_id AND method = 'FULL' AND ownership_pct = 1) THEN
      RAISE EXCEPTION 'the parent is a member, fully consolidated at 100%%' USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  PERFORM approval_use('congroup.decide', 'consolidation_group', NEW.id::text, NEW.approved_by, ARRAY['consolidation.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
  NEW.approved_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER consolidation_groups_guard BEFORE INSERT OR UPDATE ON consolidation_groups FOR EACH ROW EXECUTE FUNCTION consolidation_groups_guard();

-- ------------------------------------------------ intercompany accounts ---
CREATE TABLE intercompany_accounts (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id              uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  account_id             uuid NOT NULL,
  counterparty_entity_id uuid NOT NULL,
  role                   text NOT NULL CHECK (role IN ('RECEIVABLE', 'PAYABLE', 'REVENUE', 'EXPENSE')),
  created_by             text NOT NULL,
  created_at             timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, account_id),
  FOREIGN KEY (tenant_id, counterparty_entity_id) REFERENCES legal_entities(tenant_id, id)
);

CREATE FUNCTION intercompany_accounts_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE a accounts; want text;
BEGIN
  SELECT * INTO a FROM accounts WHERE id = NEW.account_id AND tenant_id = current_tenant();
  IF NOT FOUND OR NOT a.active THEN
    RAISE EXCEPTION 'designate an active account' USING ERRCODE = 'check_violation';
  END IF;
  IF a.legal_entity_id = NEW.counterparty_entity_id THEN
    RAISE EXCEPTION 'the counterparty is another entity' USING ERRCODE = 'check_violation';
  END IF;
  want := NEW.role;
  IF want = 'RECEIVABLE' THEN want := 'ASSET'; END IF;
  IF want = 'PAYABLE' THEN want := 'LIABILITY'; END IF;
  IF a.type <> want THEN
    RAISE EXCEPTION 'an intercompany % account is of type %', lower(NEW.role), want USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER intercompany_accounts_guard BEFORE INSERT ON intercompany_accounts FOR EACH ROW EXECUTE FUNCTION intercompany_accounts_guard();

-- ---------------------------------------------------------------- runs ---
CREATE FUNCTION consolidation_rates_valid(p jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE k text; v jsonb;
BEGIN
  IF jsonb_typeof(p) <> 'object' THEN RETURN false; END IF;
  FOR k, v IN SELECT * FROM jsonb_each(p) LOOP
    IF k !~ '^[A-Z]{3}$' OR jsonb_typeof(v) <> 'object' OR jsonb_typeof(v->'closing') <> 'number' OR jsonb_typeof(v->'average') <> 'number'
       OR (v->>'closing')::numeric <= 0 OR (v->>'average')::numeric <= 0 THEN
      RETURN false;
    END IF;
  END LOOP;
  RETURN true;
END$$;

CREATE TABLE consolidation_runs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  group_id        uuid NOT NULL,
  number          text NOT NULL,
  period_start    date NOT NULL,
  period_end      date NOT NULL CHECK (period_end >= period_start),
  rates           jsonb NOT NULL DEFAULT '{}' CHECK (consolidation_rates_valid(rates)),  -- {"USD": {"closing": 120.5, "average": 118.2}}: presentation per 1 functional unit
  rate_source     text NOT NULL CHECK (length(btrim(rate_source)) BETWEEN 3 AND 300),
  sealed          boolean NOT NULL DEFAULT false,
  status          text NOT NULL DEFAULT 'PREPARED' CHECK (status IN ('PREPARED', 'APPROVED', 'REJECTED', 'CANCELLED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES consolidation_groups(tenant_id, id)
);
CREATE UNIQUE INDEX consolidation_runs_one_live ON consolidation_runs (tenant_id, group_id, period_end) WHERE status IN ('PREPARED', 'APPROVED');

CREATE TABLE consolidation_lines (
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id          uuid NOT NULL,
  legal_entity_id uuid,            -- NULL for group-level lines (intercompany differences)
  account_code    text NOT NULL,
  account_name    text NOT NULL,
  account_type    text NOT NULL CHECK (account_type IN ('ASSET', 'LIABILITY', 'EQUITY', 'REVENUE', 'EXPENSE', 'SUSPENSE')),
  kind            text NOT NULL CHECK (kind IN ('TB', 'TRANSLATION', 'NCI', 'EQUITY_METHOD', 'ELIMINATION', 'IC_DIFF')),
  local_amount    numeric(24,6) NOT NULL,
  rate            numeric(24,12),
  amount          numeric(24,6) NOT NULL,
  FOREIGN KEY (tenant_id, run_id) REFERENCES consolidation_runs(tenant_id, id)
);
CREATE INDEX consolidation_lines_run ON consolidation_lines (tenant_id, run_id);

-- The one computation of a run's lines (debit positive, presentation currency).
CREATE FUNCTION consolidation_compute(p_run uuid)
RETURNS TABLE (legal_entity_id uuid, account_code text, account_name text, account_type text, kind text, local_amount numeric, rate numeric, amount numeric)
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r consolidation_runs; g consolidation_groups; m record; x record; cl numeric; av numeric; f numeric; rt numeric; amt numeric;
        tot numeric; na numeric; nci numeric; diffs jsonb := '{}'; k text; v jsonb;
BEGIN
  SELECT * INTO r FROM consolidation_runs c WHERE c.id = p_run AND c.tenant_id = current_tenant();
  SELECT * INTO g FROM consolidation_groups c WHERE c.id = r.group_id;
  FOR m IN SELECT cm.legal_entity_id AS eid, cm.ownership_pct, cm.method, e.functional_currency AS ccy, e.name
             FROM consolidation_members cm JOIN legal_entities e ON e.id = cm.legal_entity_id WHERE cm.group_id = g.id ORDER BY e.name, cm.legal_entity_id LOOP
    IF m.ccy = g.presentation_currency THEN
      cl := 1; av := 1;
    ELSE
      cl := (r.rates->m.ccy->>'closing')::numeric; av := (r.rates->m.ccy->>'average')::numeric;
      IF cl IS NULL OR av IS NULL THEN
        RAISE EXCEPTION 'the run needs a closing and an average rate for % (entity %)', m.ccy, m.name USING ERRCODE = 'check_violation';
      END IF;
    END IF;
    f := CASE m.method WHEN 'PROPORTIONATE' THEN m.ownership_pct ELSE 1 END;
    tot := 0; na := 0;
    FOR x IN
      WITH b AS (
        SELECT a.code, a.name, a.type, (j.business_event_at::date < r.period_start AND a.type IN ('REVENUE', 'EXPENSE')) AS prior,
               CASE l.side WHEN 'DEBIT' THEN l.amount ELSE -l.amount END AS v
          FROM journal_lines l JOIN journals j ON j.id = l.journal_id JOIN accounts a ON a.id = l.account_id
         WHERE j.legal_entity_id = m.eid AND j.status IN ('POSTED', 'REVERSED') AND j.business_event_at::date <= r.period_end)
      SELECT * FROM (
        SELECT b.code, b.name, b.type, sum(b.v) AS bal FROM b WHERE NOT b.prior GROUP BY b.code, b.name, b.type
        UNION ALL
        SELECT 'RE-BF', 'Retained earnings brought forward', 'EQUITY', sum(b.v) FROM b WHERE b.prior) s
       WHERE s.bal IS NOT NULL AND s.bal <> 0 ORDER BY s.code COLLATE "C"
    LOOP
      rt := CASE WHEN x.type IN ('REVENUE', 'EXPENSE') THEN av ELSE cl END;
      amt := p2p_round(x.bal * f * rt, g.presentation_currency);
      tot := tot + amt;
      IF x.type IN ('ASSET', 'LIABILITY') THEN na := na + amt; END IF;
      IF m.method <> 'EQUITY' AND amt <> 0 THEN
        legal_entity_id := m.eid; account_code := x.code; account_name := x.name; account_type := x.type; kind := 'TB';
        local_amount := x.bal; rate := rt; amount := amt;
        RETURN NEXT;
      END IF;
    END LOOP;
    IF m.method = 'EQUITY' THEN
      amt := p2p_round(na * m.ownership_pct, g.presentation_currency);
      IF amt <> 0 THEN
        legal_entity_id := m.eid; kind := 'EQUITY_METHOD'; local_amount := 0; rate := NULL;
        account_code := 'INV-ASSOC'; account_name := 'Investment in associates (equity method)'; account_type := 'ASSET'; amount := amt;
        RETURN NEXT;
        account_code := 'SHARE-ASSOC'; account_name := 'Share of net assets of associates'; account_type := 'EQUITY'; amount := -amt;
        RETURN NEXT;
      END IF;
      CONTINUE;
    END IF;
    IF tot <> 0 THEN
      legal_entity_id := m.eid; account_code := 'CTA'; account_name := 'Foreign currency translation reserve'; account_type := 'EQUITY';
      kind := 'TRANSLATION'; local_amount := 0; rate := NULL; amount := -tot;
      RETURN NEXT;
    END IF;
    IF m.method = 'FULL' AND m.ownership_pct < 1 THEN
      nci := p2p_round(na * (1 - m.ownership_pct), g.presentation_currency);
      IF nci <> 0 THEN
        legal_entity_id := m.eid; kind := 'NCI'; local_amount := 0; rate := NULL; account_type := 'EQUITY';
        account_code := 'NCI'; account_name := 'Non-controlling interests'; amount := -nci;
        RETURN NEXT;
        account_code := 'NCI-EQ'; account_name := 'Equity attributable to non-controlling interests'; amount := nci;
        RETURN NEXT;
      END IF;
    END IF;
  END LOOP;

  -- Eliminations between members consolidated line by line.
  FOR x IN
    SELECT ic.role, a.legal_entity_id AS eid, ic.counterparty_entity_id AS cp, a.code, a.name, a.type, cm.method, cm.ownership_pct, e.functional_currency AS ccy,
           COALESCE(sum(CASE l.side WHEN 'DEBIT' THEN l.amount ELSE -l.amount END) FILTER (WHERE j.id IS NOT NULL), 0) AS bal
      FROM intercompany_accounts ic
      JOIN accounts a ON a.id = ic.account_id
      JOIN consolidation_members cm ON cm.group_id = g.id AND cm.legal_entity_id = a.legal_entity_id AND cm.method <> 'EQUITY'
      JOIN consolidation_members cp ON cp.group_id = g.id AND cp.legal_entity_id = ic.counterparty_entity_id AND cp.method <> 'EQUITY'
      JOIN legal_entities e ON e.id = a.legal_entity_id
      LEFT JOIN journal_lines l ON l.account_id = a.id
      LEFT JOIN journals j ON j.id = l.journal_id AND j.status IN ('POSTED', 'REVERSED') AND j.business_event_at::date <= r.period_end
             AND (a.type NOT IN ('REVENUE', 'EXPENSE') OR j.business_event_at::date >= r.period_start)
     WHERE ic.tenant_id = current_tenant()
     GROUP BY ic.role, a.legal_entity_id, ic.counterparty_entity_id, a.code, a.name, a.type, cm.method, cm.ownership_pct, e.functional_currency
     ORDER BY a.legal_entity_id, a.code COLLATE "C"
  LOOP
    IF x.ccy = g.presentation_currency THEN rt := 1;
    ELSE rt := (r.rates->x.ccy->>CASE WHEN x.type IN ('REVENUE', 'EXPENSE') THEN 'average' ELSE 'closing' END)::numeric;
    END IF;
    f := CASE x.method WHEN 'PROPORTIONATE' THEN x.ownership_pct ELSE 1 END;
    amt := p2p_round(x.bal * f * rt, g.presentation_currency);
    IF amt <> 0 THEN
      legal_entity_id := x.eid; account_code := x.code; account_name := x.name; account_type := x.type; kind := 'ELIMINATION';
      local_amount := -x.bal; rate := rt; amount := -amt;
      RETURN NEXT;
      k := least(x.eid::text, x.cp::text) || '/' || greatest(x.eid::text, x.cp::text) || '/' || CASE WHEN x.role IN ('RECEIVABLE', 'PAYABLE') THEN 'BALANCE' ELSE 'RESULT' END;
      diffs := jsonb_set(diffs, ARRAY[k], to_jsonb(COALESCE((diffs->>k)::numeric, 0) + amt));
    END IF;
  END LOOP;
  FOR k, v IN SELECT * FROM jsonb_each(diffs) ORDER BY 1 LOOP
    IF (v #>> '{}')::numeric <> 0 THEN
      legal_entity_id := NULL; account_code := 'IC-DIFF';
      account_name := 'Intercompany difference (' || lower(split_part(k, '/', 3)) || '): '
        || (SELECT name FROM legal_entities WHERE id = split_part(k, '/', 1)::uuid) || ' / ' || (SELECT name FROM legal_entities WHERE id = split_part(k, '/', 2)::uuid);
      account_type := 'SUSPENSE'; kind := 'IC_DIFF'; local_amount := 0; rate := NULL; amount := (v #>> '{}')::numeric;
      RETURN NEXT;
    END IF;
  END LOOP;
END$$;

CREATE FUNCTION consolidation_line_key(p_entity uuid, p_code text, p_name text, p_type text, p_kind text, p_local numeric, p_rate numeric, p_amount numeric) RETURNS text
LANGUAGE sql IMMUTABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT concat_ws('|', COALESCE(p_entity::text, '-'), p_code, p_name, p_type, p_kind, p_local::numeric(24,6), COALESCE(p_rate::numeric(24,12)::text, '-'), p_amount::numeric(24,6))
$$;

CREATE FUNCTION consolidation_expected_text(p_run uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(string_agg(k, ';' ORDER BY k COLLATE "C"), '') FROM (
    SELECT consolidation_line_key(c.legal_entity_id, c.account_code, c.account_name, c.account_type, c.kind, c.local_amount, c.rate, c.amount) AS k FROM consolidation_compute(p_run) c) s
$$;
CREATE FUNCTION consolidation_lines_text(p_run uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(string_agg(k, ';' ORDER BY k COLLATE "C"), '') FROM (
    SELECT consolidation_line_key(l.legal_entity_id, l.account_code, l.account_name, l.account_type, l.kind, l.local_amount, l.rate, l.amount) AS k
      FROM consolidation_lines l WHERE l.run_id = p_run AND l.tenant_id = current_tenant()) s
$$;

CREATE FUNCTION consolidation_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM consolidation_runs WHERE id = NEW.run_id AND tenant_id = current_tenant() AND status = 'PREPARED' AND NOT sealed) THEN
    RAISE EXCEPTION 'consolidation lines are written once, when the run is prepared' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER consolidation_lines_guard BEFORE INSERT ON consolidation_lines FOR EACH ROW EXECUTE FUNCTION consolidation_lines_guard();

CREATE FUNCTION consolidation_runs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'PREPARED' OR NEW.sealed OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'consolidation runs are prepared unsealed' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM consolidation_groups WHERE id = NEW.group_id AND status = 'APPROVED') THEN
      RAISE EXCEPTION 'consolidate an approved group' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status <> 'PREPARED' THEN
    RAISE EXCEPTION 'a % consolidation run never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'PREPARED' THEN
    IF OLD.sealed OR NOT NEW.sealed THEN
      RAISE EXCEPTION 'a run is sealed once' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['sealed'], 'consolidation run');
    RETURN NEW;
  END IF;
  IF NOT OLD.sealed THEN
    RAISE EXCEPTION 'the run is not sealed' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'CANCELLED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'decision_reason'], 'consolidation run');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'cancelling a consolidation run requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.status NOT IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'illegal consolidation run transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'consolidation run');
  IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
    RAISE EXCEPTION 'rejecting a consolidation run requires a reason' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('conrun.decide', 'consolidation_run', NEW.id::text, NEW.approved_by, ARRAY['consolidation.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
  NEW.approved_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER consolidation_runs_guard BEFORE INSERT OR UPDATE ON consolidation_runs FOR EACH ROW EXECUTE FUNCTION consolidation_runs_guard();

-- At commit, a prepared run is sealed and its lines are exactly the computation (and balance).
CREATE FUNCTION consolidation_runs_check() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r consolidation_runs;
BEGIN
  SELECT * INTO r FROM consolidation_runs WHERE id = NEW.id;
  IF NOT r.sealed THEN
    RAISE EXCEPTION 'consolidation run % was not sealed', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  IF consolidation_lines_text(NEW.id) IS DISTINCT FROM consolidation_expected_text(NEW.id) THEN
    RAISE EXCEPTION 'the lines of consolidation run % are not the database''s computation', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  IF COALESCE((SELECT sum(amount) FROM consolidation_lines WHERE run_id = NEW.id), 0) <> 0 THEN
    RAISE EXCEPTION 'consolidation run % does not balance', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER consolidation_runs_seal AFTER INSERT ON consolidation_runs DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION consolidation_runs_check();

-- ---------------------------------------- shared functions (verbatim + 0062)
-- p2p_subject_text: verbatim from 0061 plus consolidation documents (marked 0062).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0061; consolidation documents are tenant-level (marked 0062).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy', 'fixed_asset', 'asset_disposal', 'payroll_policy', 'payroll_rule_set', 'pay_run', 'consolidation_group', 'consolidation_run') THEN
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
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['consolidation_groups', 'consolidation_members', 'intercompany_accounts', 'consolidation_runs', 'consolidation_lines'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON consolidation_groups TO serk_app;
GRANT UPDATE (status, sealed, approved_by, approved_at, decision_reason) ON consolidation_runs TO serk_app;
REVOKE ALL ON FUNCTION consolidation_rates_valid(jsonb), consolidation_compute(uuid), consolidation_line_key(uuid, text, text, text, text, numeric, numeric, numeric),
  consolidation_expected_text(uuid), consolidation_lines_text(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION consolidation_rates_valid(jsonb), consolidation_compute(uuid), consolidation_line_key(uuid, text, text, text, text, numeric, numeric, numeric),
  consolidation_expected_text(uuid), consolidation_lines_text(uuid) TO serk_app;
