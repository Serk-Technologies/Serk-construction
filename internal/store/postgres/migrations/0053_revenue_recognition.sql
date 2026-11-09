-- 0053 Revenue over time and the month-end WIP adjustment
-- (WP-2.2 completion and WP-2.3; SRS PCC-06, FIN-15, CON-07)
--
-- WHAT THIS MIGRATION DOES
--   Customer invoices already credit revenue with what was billed. Under IFRS
--   15 (over time) revenue is what was EARNED: contract value x progress. The
--   difference is a contract asset (earned, not yet billed: under-billing) or
--   a contract liability (billed ahead of the work: over-billing).
--
--   * REVENUE POLICY (per legal entity): the contract asset and contract
--     liability accounts and the progress measure: INPUT_COST (cost to date /
--     estimate at completion of the latest approved forecast) or
--     OUTPUT_MEASURED (verified measured value / contract value). Proposed by
--     one person, approved by another (attested); one approved per entity.
--   * REVENUE RUN (per project and month end): the database computes one line
--     per active contract: earned to date, billed to date (approved invoices
--     less approved credit notes), cumulative adjustment = earned - billed, the
--     adjustment already posted by earlier runs, and the movement. The run
--     generates a DRAFT journal (source REV_RUN) that finance approves and a
--     different person posts. The journal's lines are bound to the run like
--     every other subledger journal: written only by the generating
--     transaction, checked again at approval and posting, never reversed. A
--     run whose journal is still a draft may be cancelled (with its journal).
--
-- SHARED FUNCTIONS REDEFINED HERE:
--   subledger_expected_lines / subledger_expected_text (verbatim from 0021 plus REV_),
--   journals_subledger_guard / journal_lines_p2p_birth (verbatim from 0021, the
--   prefix pattern now includes REV, and a REV_ draft is cancelled with its run),
--   trigger journals_subledger_no_reversal and journals_reserved_idempotency (0024; pattern includes REV),
--   p2p_subject_text / serk_subject_project (verbatim from 0051 plus revenue_policy).

CREATE TABLE revenue_policies (
  id                           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id              uuid NOT NULL,
  version                      integer NOT NULL CHECK (version >= 1),
  method                       text NOT NULL CHECK (method IN ('INPUT_COST', 'OUTPUT_MEASURED')),
  contract_asset_account_id    uuid NOT NULL,
  contract_liability_account_id uuid NOT NULL,
  status                       text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'APPROVED', 'REJECTED', 'RETIRED')),
  created_by                   text NOT NULL,
  created_at                   timestamptz NOT NULL DEFAULT now(),
  approved_by                  text,
  approved_at                  timestamptz,
  decision_reason              text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, version),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id),
  CONSTRAINT revenue_policy_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by),
  CONSTRAINT revenue_policy_two_accounts CHECK (contract_asset_account_id <> contract_liability_account_id)
);
CREATE UNIQUE INDEX revenue_policies_one_approved ON revenue_policies (tenant_id, legal_entity_id) WHERE status = 'APPROVED';
CREATE UNIQUE INDEX revenue_policies_one_draft ON revenue_policies (tenant_id, legal_entity_id) WHERE status = 'DRAFT';

CREATE FUNCTION revenue_policies_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'revenue policies are proposed as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.contract_asset_account_id AND tenant_id = current_tenant() AND legal_entity_id = NEW.legal_entity_id AND type = 'ASSET' AND active)
       OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.contract_liability_account_id AND tenant_id = current_tenant() AND legal_entity_id = NEW.legal_entity_id AND type = 'LIABILITY' AND active) THEN
      RAISE EXCEPTION 'the contract asset must be an active ASSET account and the contract liability an active LIABILITY account of the entity' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'RETIRED') OR (OLD.status = 'APPROVED' AND NEW.status <> 'RETIRED') THEN
    RAISE EXCEPTION 'a decided revenue policy never changes (an approved one is only retired by its successor)' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'APPROVED' AND NEW.status = 'RETIRED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'revenue policy');
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'revenue policy');
  IF OLD.status <> 'DRAFT' OR NEW.status NOT IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'illegal revenue policy transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
    RAISE EXCEPTION 'rejecting a revenue policy requires a reason' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('revenue.policy.decide', 'revenue_policy', NEW.id::text, NEW.approved_by, ARRAY['contract.policy.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
  NEW.approved_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER revenue_policies_guard BEFORE INSERT OR UPDATE ON revenue_policies FOR EACH ROW EXECUTE FUNCTION revenue_policies_guard();

CREATE TABLE revenue_runs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id uuid NOT NULL,
  project_id      uuid NOT NULL,
  number          text NOT NULL,
  period_end      date NOT NULL,
  policy_id       uuid NOT NULL,
  method          text NOT NULL,
  currency        char(3) NOT NULL REFERENCES currencies(code),
  status          text NOT NULL DEFAULT 'PREPARED' CHECK (status IN ('PREPARED', 'CANCELLED')),
  journal_id      uuid,
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  cancel_reason   text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, legal_entity_id, project_id) REFERENCES projects(tenant_id, legal_entity_id, id),
  FOREIGN KEY (tenant_id, policy_id) REFERENCES revenue_policies(tenant_id, id)
);

CREATE TABLE revenue_lines (
  tenant_id        uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id           uuid NOT NULL,
  contract_id      uuid NOT NULL,
  contract_value   numeric(24,6) NOT NULL,
  measure          numeric(24,6) NOT NULL,  -- cost to date (input) or verified measured value (output)
  measure_total    numeric(24,6) NOT NULL,  -- EAC (input) or contract value (output)
  progress         numeric(12,8) NOT NULL CHECK (progress >= 0 AND progress <= 1),
  earned           numeric(24,6) NOT NULL,
  billed           numeric(24,6) NOT NULL,
  cumulative       numeric(24,6) NOT NULL,  -- earned - billed (+ asset, - liability)
  previous         numeric(24,6) NOT NULL,  -- cumulative of the last run with a posted journal
  movement         numeric(24,6) NOT NULL,
  PRIMARY KEY (tenant_id, run_id, contract_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES revenue_runs(tenant_id, id),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES contracts(tenant_id, id),
  CONSTRAINT revenue_line_arithmetic CHECK (cumulative = earned - billed AND movement = cumulative - previous)
);

-- The one computation of a project's revenue lines at a month end.
CREATE FUNCTION revenue_compute(p_project uuid, p_period date, p_method text)
RETURNS TABLE (contract_id uuid, contract_value numeric, measure numeric, measure_total numeric, progress numeric,
               earned numeric, billed numeric, cumulative numeric, previous numeric, movement numeric)
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE c contracts; cost numeric; eac numeric; m numeric; t numeric; pr numeric; e numeric; b numeric; prev numeric;
BEGIN
  IF p_method = 'INPUT_COST' THEN
    SELECT COALESCE(sum(l.cost_to_date), 0), COALESCE(sum(l.eac), 0) INTO cost, eac FROM forecast_lines l
     WHERE l.cycle_id = (SELECT f.id FROM forecast_cycles f WHERE f.project_id = p_project AND f.tenant_id = current_tenant() AND f.status = 'APPROVED'
                           AND f.period_end <= p_period ORDER BY f.period_end DESC, f.approved_at DESC LIMIT 1);
  END IF;
  FOR c IN SELECT * FROM contracts x WHERE x.project_id = p_project AND x.tenant_id = current_tenant() AND x.status = 'ACTIVE' ORDER BY x.number LOOP
    IF p_method = 'INPUT_COST' THEN
      m := COALESCE(cost, 0); t := COALESCE(eac, 0);
    ELSE
      SELECT COALESCE(sum(p2p_round(contract_verified_qty(c.id, l.line_no, NULL) * l.rate, c.currency)), 0) INTO m FROM contract_lines l WHERE l.contract_id = c.id;
      t := c.contract_value;
    END IF;
    pr := CASE WHEN t > 0 THEN least(round(m / t, 8), 1) ELSE 0 END;
    e := p2p_round(c.contract_value * pr, c.currency);
    SELECT COALESCE((SELECT sum(i.gross) FROM customer_invoices i WHERE i.contract_id = c.id AND i.status = 'APPROVED' AND i.invoice_date <= p_period), 0)
         - COALESCE((SELECT sum(n.net_amount) FROM customer_credit_notes n WHERE n.contract_id = c.id AND n.status = 'APPROVED' AND n.credit_date <= p_period), 0)
      INTO b;
    SELECT l.cumulative INTO prev FROM revenue_lines l JOIN revenue_runs r ON r.id = l.run_id JOIN journals j ON j.id = r.journal_id
     WHERE l.contract_id = c.id AND r.status = 'PREPARED' AND j.status = 'POSTED' ORDER BY r.period_end DESC, r.created_at DESC LIMIT 1;
    contract_id := c.id; contract_value := c.contract_value; measure := m; measure_total := t; progress := pr;
    earned := e; billed := b; cumulative := e - b; previous := COALESCE(prev, 0); movement := (e - b) - COALESCE(prev, 0);
    RETURN NEXT;
  END LOOP;
END$$;

CREATE FUNCTION revenue_runs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE pol revenue_policies; js text;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'PREPARED' OR NEW.journal_id IS NOT NULL THEN
      RAISE EXCEPTION 'revenue runs are created PREPARED, then bound to their journal' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.period_end > current_date THEN
      RAISE EXCEPTION 'a revenue run is for a month end that has passed or today' USING ERRCODE = 'check_violation';
    END IF;
    SELECT * INTO pol FROM revenue_policies WHERE legal_entity_id = NEW.legal_entity_id AND tenant_id = current_tenant() AND status = 'APPROVED';
    IF pol.id IS NULL THEN
      RAISE EXCEPTION 'the entity has no approved revenue policy' USING ERRCODE = 'check_violation';
    END IF;
    NEW.policy_id := pol.id;
    NEW.method := pol.method;
    IF EXISTS (SELECT 1 FROM revenue_runs r LEFT JOIN journals j ON j.id = r.journal_id
                WHERE r.project_id = NEW.project_id AND r.tenant_id = current_tenant() AND r.status = 'PREPARED' AND (j.id IS NULL OR j.status IN ('DRAFT', 'APPROVED'))) THEN
      RAISE EXCEPTION 'the project has a revenue run whose journal is not posted yet: post or cancel it first' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM revenue_runs r WHERE r.project_id = NEW.project_id AND r.tenant_id = current_tenant() AND r.status = 'PREPARED' AND r.period_end > NEW.period_end) THEN
      RAISE EXCEPTION 'a later month end has already been run for this project' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'CANCELLED' THEN
    RAISE EXCEPTION 'a cancelled revenue run never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.journal_id IS NULL AND NEW.status = 'PREPARED' THEN
    -- Binding the generated journal (once, same transaction as the lines).
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['journal_id'], 'revenue run');
    IF NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.journal_id AND tenant_id = current_tenant() AND source_type = 'REV_RUN' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'a revenue run is bound only to its own DRAFT REV_RUN journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'cancel_reason'], 'revenue run');
  IF NEW.status <> 'CANCELLED' OR NEW.cancel_reason IS NULL OR length(btrim(NEW.cancel_reason)) < 5 THEN
    RAISE EXCEPTION 'a revenue run changes only by being cancelled with a reason' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO js FROM journals WHERE id = OLD.journal_id;
  IF js IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'only a run whose journal is still a draft can be cancelled (this one is %)', js USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER revenue_runs_guard BEFORE INSERT OR UPDATE ON revenue_runs FOR EACH ROW EXECUTE FUNCTION revenue_runs_guard();

CREATE FUNCTION revenue_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r revenue_runs; x record;
BEGIN
  SELECT * INTO r FROM revenue_runs WHERE id = NEW.run_id AND tenant_id = current_tenant();
  IF r.journal_id IS NOT NULL OR r.status <> 'PREPARED' THEN
    RAISE EXCEPTION 'revenue lines are written when the run is prepared' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO x FROM revenue_compute(r.project_id, r.period_end, r.method) c WHERE c.contract_id = NEW.contract_id;
  IF NOT FOUND OR (x.contract_value, x.measure, x.measure_total, x.progress, x.earned, x.billed, x.previous)
                  IS DISTINCT FROM (NEW.contract_value, NEW.measure, NEW.measure_total, NEW.progress, NEW.earned, NEW.billed, NEW.previous) THEN
    RAISE EXCEPTION 'revenue line for contract % is not the database''s computation', NEW.contract_id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER revenue_lines_guard BEFORE INSERT ON revenue_lines FOR EACH ROW EXECUTE FUNCTION revenue_lines_guard();

-- The accounting of a run: move the contract asset and liability from the
-- balances of the previous posted run to the new ones; the difference goes
-- to revenue on the project.
CREATE FUNCTION rev_expected_lines(p_doc text, p_id uuid)
RETURNS TABLE (account_id uuid, side text, amount numeric, project_id uuid, cost_code_id uuid)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  WITH r AS (SELECT * FROM revenue_runs WHERE p_doc = 'REV_RUN' AND id = p_id AND tenant_id = current_tenant()),
  t AS (SELECT sum(greatest(l.cumulative, 0) - greatest(l.previous, 0)) AS d_asset,
               sum(greatest(-l.cumulative, 0) - greatest(-l.previous, 0)) AS d_liab
          FROM revenue_lines l JOIN r ON l.run_id = r.id),
  e(account_id, side, amount, project_id, cost_code_id) AS (
    SELECT pol.contract_asset_account_id, CASE WHEN t.d_asset > 0 THEN 'DEBIT' ELSE 'CREDIT' END, abs(t.d_asset), NULL::uuid, NULL::uuid
      FROM r JOIN revenue_policies pol ON pol.id = r.policy_id, t
    UNION ALL
    SELECT pol.contract_liability_account_id, CASE WHEN t.d_liab > 0 THEN 'CREDIT' ELSE 'DEBIT' END, abs(t.d_liab), NULL::uuid, NULL::uuid
      FROM r JOIN revenue_policies pol ON pol.id = r.policy_id, t
    UNION ALL
    SELECT (contract_policy_for(r.legal_entity_id)).revenue_account_id, CASE WHEN t.d_asset - t.d_liab > 0 THEN 'CREDIT' ELSE 'DEBIT' END,
           abs(t.d_asset - t.d_liab), r.project_id, NULL::uuid
      FROM r, t)
  SELECT account_id, side, amount, project_id, cost_code_id FROM e WHERE amount > 0
$$;
CREATE FUNCTION rev_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(string_agg(concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')),
                             ';' ORDER BY concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')) COLLATE "C"), '')
    FROM rev_expected_lines(p_doc, p_id)
$$;

-- Subledger entry points (verbatim from 0021 plus REV_).
CREATE OR REPLACE FUNCTION subledger_expected_lines(p_doc text, p_id uuid)
RETURNS TABLE (account_id uuid, side text, amount numeric, project_id uuid, cost_code_id uuid)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT * FROM p2p_expected_lines(p_doc, p_id) WHERE p_doc LIKE 'P2P\_%'
  UNION ALL
  SELECT * FROM ar_expected_lines(p_doc, p_id) WHERE p_doc LIKE 'AR\_%'
  UNION ALL
  SELECT * FROM stk_expected_lines(p_doc, p_id) WHERE p_doc LIKE 'STK\_%'
  UNION ALL
  SELECT * FROM rev_expected_lines(p_doc, p_id) WHERE p_doc LIKE 'REV\_%'
$$;
CREATE OR REPLACE FUNCTION subledger_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE WHEN p_doc LIKE 'P2P\_%' THEN p2p_expected_text(p_doc, p_id) WHEN p_doc LIKE 'AR\_%' THEN ar_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'STK\_%' THEN stk_expected_text(p_doc, p_id) WHEN p_doc LIKE 'REV\_%' THEN rev_expected_text(p_doc, p_id) ELSE '' END
$$;

-- Subledger journal protections now cover REV_*; a REV_ draft is cancelled with its run.
CREATE OR REPLACE FUNCTION journals_subledger_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.source_type ~ '^(P2P|AR|STK|REV)_' AND TG_OP = 'UPDATE' AND NEW.status = 'CANCELLED' AND OLD.status <> 'CANCELLED' THEN
    IF NEW.source_type = 'REV_RUN' AND EXISTS (SELECT 1 FROM revenue_runs r WHERE r.id::text = NEW.source_ref AND r.journal_id = NEW.id AND r.status = 'CANCELLED') THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'subledger journals are not cancelled; correct the source document' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

CREATE OR REPLACE FUNCTION journal_lines_p2p_birth() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE j record; jid uuid := CASE WHEN TG_OP = 'DELETE' THEN OLD.journal_id ELSE NEW.journal_id END;
BEGIN
  SELECT source_type, created_txid INTO j FROM journals WHERE id = jid AND tenant_id = current_tenant();
  IF j.source_type ~ '^(P2P|AR|STK|REV)_' AND j.created_txid IS DISTINCT FROM txid_current() THEN
    RAISE EXCEPTION 'lines of subledger journal % are written only by the transaction that generated it', jid USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END$$;

-- The "p2p:" idempotency namespace (verbatim from 0024, REV added).
CREATE OR REPLACE FUNCTION journals_reserved_idempotency() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF lower(NEW.idempotency_key) LIKE 'p2p:%'
     AND (NEW.source_type !~ '^(P2P|AR|STK|REV)_' OR NEW.idempotency_key IS DISTINCT FROM 'p2p:' || NEW.source_type || ':' || NEW.source_ref) THEN
    RAISE EXCEPTION 'idempotency keys beginning "p2p:" are reserved to the subledger journal of the named document' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

DROP TRIGGER journals_subledger_no_reversal ON journals;
CREATE TRIGGER journals_subledger_no_reversal BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.status = 'REVERSED' AND OLD.status <> 'REVERSED' AND NEW.source_type ~ '^(P2P|AR|STK|REV)_')
  EXECUTE FUNCTION journals_subledger_no_reversal();

-- A REV_ journal must belong to a run that references it, and still equal
-- the run's accounting when it is approved or posted.
CREATE FUNCTION journals_rev_binding() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM revenue_runs WHERE id::text = NEW.source_ref AND journal_id = NEW.id) THEN
    RAISE EXCEPTION 'journal % uses reserved source type % but no revenue run references it', NEW.id, NEW.source_type USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER journals_rev_binding AFTER INSERT ON journals DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.source_type LIKE 'REV\_%') EXECUTE FUNCTION journals_rev_binding();

CREATE FUNCTION journals_rev_rebind() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE want text; got text;
BEGIN
  IF NEW.source_ref !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RAISE EXCEPTION 'subledger journal % has no document reference', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  IF EXISTS (SELECT 1 FROM revenue_runs WHERE id = NEW.source_ref::uuid AND status = 'CANCELLED') THEN
    RAISE EXCEPTION 'the revenue run of journal % was cancelled', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  want := rev_expected_text(NEW.source_type, NEW.source_ref::uuid);
  got := p2p_lines_text(NEW.id);
  IF want IS NULL OR want = '' OR want IS DISTINCT FROM got THEN
    RAISE EXCEPTION 'subledger journal % no longer equals the accounting of % % (expected %, got %)', NEW.id, NEW.source_type, NEW.source_ref, want, got
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER journals_rev_rebind BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.source_type LIKE 'REV\_%' AND NEW.status IN ('APPROVED', 'POSTED') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION journals_rev_rebind();

-- ---------------------------------------- shared functions (verbatim + 0053)
-- p2p_subject_text: verbatim from 0051 plus revenue policies (marked 0053).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0051; a revenue policy is entity-level (marked 0053).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy') THEN
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
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['revenue_policies', 'revenue_runs', 'revenue_lines'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON revenue_policies TO serk_app;
GRANT UPDATE (journal_id, status, cancel_reason) ON revenue_runs TO serk_app;
REVOKE ALL ON FUNCTION revenue_compute(uuid, date, text), rev_expected_lines(text, uuid), rev_expected_text(text, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION revenue_compute(uuid, date, text), rev_expected_lines(text, uuid), rev_expected_text(text, uuid) TO serk_app;
