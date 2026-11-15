-- 0079_equipment_charge_out.sql — equipment charge-out to projects (FLT-01.14; DEF-001 remainder).
--
-- A machine's internal charge-out rate (per productive hour and per idle hour) is an effective-dated document
-- approved by a person with fleet.approve who did not prepare it, with a fresh second factor (attested). A charge
-- run prices a project's APPROVED equipment logs of one month at the rate in force on each log's day, and
-- produces one EQP_CHG subledger journal: Dr the project's equipment cost account (per cost code) / Cr the
-- entity's equipment recovery account. The journal is bound to its run like a depreciation journal (it must
-- equal the run's accounting when approved or posted) and is approved and posted by finance as any journal.
-- A log is charged by at most one live run; a run whose journal is still a draft can be cancelled with a reason,
-- which releases its logs. Rates are policy inputs entered by the business; none is shipped.
--
-- Shared accounting entry points (subledger_expected_lines/_text, journals_subledger_guard,
-- journal_lines_p2p_birth, journals_reserved_idempotency, the no-reversal trigger) are verbatim from 0067 plus
-- EQP. p2p_subject_text / serk_subject_project: verbatim from 0078 plus the charge documents.

CREATE TABLE equipment_charge_rates (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  number          text NOT NULL,
  equipment_id    uuid NOT NULL,
  effective_from  date NOT NULL,
  productive_rate numeric(24,6) NOT NULL CHECK (productive_rate > 0),
  idle_rate       numeric(24,6) NOT NULL DEFAULT 0 CHECK (idle_rate >= 0),
  basis           text NOT NULL CHECK (length(btrim(basis)) BETWEEN 5 AND 500),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, equipment_id) REFERENCES equipment(tenant_id, id)
);
CREATE UNIQUE INDEX equipment_charge_rates_one ON equipment_charge_rates (tenant_id, equipment_id, effective_from) WHERE status = 'APPROVED';

CREATE FUNCTION equipment_charge_rates_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN RAISE EXCEPTION 'charge rates are prepared as DRAFT' USING ERRCODE = 'check_violation'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN RAISE EXCEPTION 'a decided charge rate never changes; prepare a new one with a later effective date' USING ERRCODE = 'check_violation'; END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'charge rate');
    RETURN NEW;
  END IF;
  IF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'charge rate');
    IF NEW.approved_by IS NULL OR NEW.approved_by = OLD.created_by THEN
      RAISE EXCEPTION 'segregation of duties: the person who prepared a charge rate cannot decide it' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a charge rate requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('fleet.charge_rate.decide', 'equipment_charge_rate', NEW.id::text, NEW.approved_by, ARRAY['fleet.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal charge rate transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER equipment_charge_rates_guard BEFORE INSERT OR UPDATE ON equipment_charge_rates FOR EACH ROW EXECUTE FUNCTION equipment_charge_rates_guard();

-- The approved rate in force for a machine on a day (NULL when none).
CREATE FUNCTION equipment_charge_rate_at(p_equipment uuid, p_day date) RETURNS uuid
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT id FROM equipment_charge_rates
   WHERE tenant_id = current_tenant() AND equipment_id = p_equipment AND status = 'APPROVED' AND effective_from <= p_day
   ORDER BY effective_from DESC LIMIT 1
$$;

CREATE TABLE equipment_charge_runs (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  number              text NOT NULL,
  project_id          uuid NOT NULL,
  month               date NOT NULL CHECK (month = date_trunc('month', month)::date),
  cost_account_id     uuid NOT NULL,
  recovery_account_id uuid NOT NULL,
  status              text NOT NULL DEFAULT 'PREPARED' CHECK (status IN ('PREPARED', 'CANCELLED')),
  journal_id          uuid UNIQUE,
  created_by          text NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  cancel_reason       text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  FOREIGN KEY (tenant_id, cost_account_id) REFERENCES accounts(tenant_id, id),
  FOREIGN KEY (tenant_id, recovery_account_id) REFERENCES accounts(tenant_id, id),
  CONSTRAINT equipment_charge_accounts_differ CHECK (cost_account_id <> recovery_account_id)
);
CREATE UNIQUE INDEX equipment_charge_runs_one ON equipment_charge_runs (tenant_id, project_id, month) WHERE status = 'PREPARED';

CREATE TABLE equipment_charge_lines (
  tenant_id        uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id           uuid NOT NULL,
  log_id           uuid NOT NULL,
  equipment_id     uuid NOT NULL,
  rate_id          uuid NOT NULL,
  cost_code_id     uuid,
  productive_hours numeric(5,2) NOT NULL,
  idle_hours       numeric(5,2) NOT NULL,
  amount           numeric(24,6) NOT NULL CHECK (amount >= 0),
  PRIMARY KEY (tenant_id, run_id, log_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES equipment_charge_runs(tenant_id, id),
  FOREIGN KEY (tenant_id, log_id) REFERENCES equipment_logs(tenant_id, id),
  FOREIGN KEY (tenant_id, rate_id) REFERENCES equipment_charge_rates(tenant_id, id)
);

CREATE FUNCTION equipment_charge_runs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE ent uuid; ct text; rt text; js text;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'PREPARED' OR NEW.journal_id IS NOT NULL THEN
      RAISE EXCEPTION 'charge runs are created PREPARED, then bound to their journal' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.month > date_trunc('month', current_date)::date THEN
      RAISE EXCEPTION 'equipment is charged for the current month or an earlier one' USING ERRCODE = 'check_violation';
    END IF;
    SELECT legal_entity_id INTO ent FROM projects WHERE id = NEW.project_id;
    SELECT type INTO ct FROM accounts WHERE id = NEW.cost_account_id AND legal_entity_id = ent;
    SELECT type INTO rt FROM accounts WHERE id = NEW.recovery_account_id AND legal_entity_id = ent;
    IF ct IS DISTINCT FROM 'EXPENSE' OR rt IS NULL OR rt NOT IN ('EXPENSE', 'REVENUE') THEN
      RAISE EXCEPTION 'the cost account is an expense account and the recovery account an expense or revenue account of the project''s entity' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'CANCELLED' THEN RAISE EXCEPTION 'a cancelled run never changes' USING ERRCODE = 'check_violation'; END IF;
  IF OLD.journal_id IS NULL AND NEW.status = 'PREPARED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['journal_id'], 'charge run');
    IF NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.journal_id AND source_type = 'EQP_CHG' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'a charge run is bound only to its own DRAFT EQP_CHG journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'cancel_reason'], 'charge run');
  IF NEW.status <> 'CANCELLED' OR NEW.cancel_reason IS NULL OR length(btrim(NEW.cancel_reason)) < 5 THEN
    RAISE EXCEPTION 'a charge run changes only by being cancelled with a reason' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO js FROM journals WHERE id = OLD.journal_id;
  IF js IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'only a run whose journal is still a draft can be cancelled (this one is %)', js USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_charge_runs_guard BEFORE INSERT OR UPDATE ON equipment_charge_runs FOR EACH ROW EXECUTE FUNCTION equipment_charge_runs_guard();

-- A line is the run's own: written in the transaction that created the run, for an approved log of the run's
-- project and month, priced at the rate in force on the log's day (to the entity's minor unit), and not charged
-- by another live run. The log row is locked first, so two concurrent runs cannot both charge it.
CREATE FUNCTION equipment_charge_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r equipment_charge_runs; l equipment_logs; rate equipment_charge_rates; mu int;
BEGIN
  SELECT * INTO r FROM equipment_charge_runs WHERE id = NEW.run_id AND tenant_id = current_tenant();
  IF NOT FOUND OR r.status <> 'PREPARED' OR NOT EXISTS (SELECT 1 FROM equipment_charge_runs WHERE id = NEW.run_id AND xmin = pg_current_xact_id()::xid) THEN
    RAISE EXCEPTION 'charge lines are written only by the transaction that prepares the run' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO l FROM equipment_logs WHERE id = NEW.log_id AND tenant_id = current_tenant() FOR UPDATE;
  IF NOT FOUND OR l.status <> 'APPROVED' OR l.project_id <> r.project_id OR date_trunc('month', l.log_date)::date <> r.month OR l.equipment_id <> NEW.equipment_id THEN
    RAISE EXCEPTION 'a charge line is an approved log of the run''s project and month' USING ERRCODE = 'check_violation';
  END IF;
  IF EXISTS (SELECT 1 FROM equipment_charge_lines x JOIN equipment_charge_runs y ON y.id = x.run_id WHERE x.log_id = NEW.log_id AND y.status = 'PREPARED' AND y.id <> NEW.run_id) THEN
    RAISE EXCEPTION 'equipment log % is already charged by a live run', l.number USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.rate_id IS DISTINCT FROM equipment_charge_rate_at(l.equipment_id, l.log_date) THEN
    RAISE EXCEPTION 'a log is priced at the approved rate in force on its day' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO rate FROM equipment_charge_rates WHERE id = NEW.rate_id;
  SELECT currency_minor_units(e.functional_currency) INTO mu FROM projects p JOIN legal_entities e ON e.id = p.legal_entity_id WHERE p.id = r.project_id;
  IF NEW.productive_hours <> l.productive_hours OR NEW.idle_hours <> l.idle_hours OR NEW.cost_code_id IS DISTINCT FROM l.cost_code_id
     OR NEW.amount <> round_half_even(l.productive_hours * rate.productive_rate + l.idle_hours * rate.idle_rate, mu) THEN
    RAISE EXCEPTION 'a charge line restates its log and prices it at its rate' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER equipment_charge_lines_guard BEFORE INSERT ON equipment_charge_lines FOR EACH ROW EXECUTE FUNCTION equipment_charge_lines_guard();

-- The accounting of a charge run: Dr the project's cost account per cost code, Cr the recovery account.
CREATE FUNCTION eqp_expected_lines(p_doc text, p_id uuid)
RETURNS TABLE (account_id uuid, side text, amount numeric, project_id uuid, cost_code_id uuid)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  WITH e(account_id, side, amount, project_id, cost_code_id) AS (
    SELECT r.cost_account_id, 'DEBIT', sum(l.amount), r.project_id, l.cost_code_id
      FROM equipment_charge_runs r JOIN equipment_charge_lines l ON l.run_id = r.id
     WHERE p_doc = 'EQP_CHG' AND r.id = p_id AND r.tenant_id = current_tenant() AND r.status = 'PREPARED'
     GROUP BY r.cost_account_id, r.project_id, l.cost_code_id
    UNION ALL
    SELECT r.recovery_account_id, 'CREDIT', sum(l.amount), NULL::uuid, NULL::uuid
      FROM equipment_charge_runs r JOIN equipment_charge_lines l ON l.run_id = r.id
     WHERE p_doc = 'EQP_CHG' AND r.id = p_id AND r.tenant_id = current_tenant() AND r.status = 'PREPARED'
     GROUP BY r.recovery_account_id)
  SELECT account_id, side, amount, project_id, cost_code_id FROM e WHERE amount > 0
$$;
CREATE FUNCTION eqp_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(string_agg(concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')),
                             ';' ORDER BY concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')) COLLATE "C"), '')
    FROM eqp_expected_lines(p_doc, p_id)
$$;

CREATE FUNCTION journals_eqp_binding() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT (NEW.source_type = 'EQP_CHG' AND EXISTS (SELECT 1 FROM equipment_charge_runs WHERE id::text = NEW.source_ref AND journal_id = NEW.id)) THEN
    RAISE EXCEPTION 'journal % uses reserved source type % but no charge run references it', NEW.id, NEW.source_type USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER journals_eqp_binding AFTER INSERT ON journals DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.source_type LIKE 'EQP\_%') EXECUTE FUNCTION journals_eqp_binding();

CREATE FUNCTION journals_eqp_rebind() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE want text; got text;
BEGIN
  IF NEW.source_ref !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RAISE EXCEPTION 'subledger journal % has no document reference', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  IF EXISTS (SELECT 1 FROM equipment_charge_runs WHERE id = NEW.source_ref::uuid AND status = 'CANCELLED') THEN
    RAISE EXCEPTION 'the charge run of journal % was cancelled', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  want := eqp_expected_text(NEW.source_type, NEW.source_ref::uuid);
  got := p2p_lines_text(NEW.id);
  IF want IS NULL OR want = '' OR want IS DISTINCT FROM got THEN
    RAISE EXCEPTION 'subledger journal % no longer equals the accounting of % % (expected %, got %)', NEW.id, NEW.source_type, NEW.source_ref, want, got
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER journals_eqp_rebind BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.source_type LIKE 'EQP\_%' AND NEW.status IN ('APPROVED', 'POSTED') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION journals_eqp_rebind();

-- ------------------------------------------------- shared accounting entry points (verbatim from 0067 plus EQP)
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
  UNION ALL
  SELECT * FROM sub_expected_lines(p_doc, p_id) WHERE p_doc LIKE 'SUB\_%'
  UNION ALL
  SELECT * FROM fa_expected_lines(p_doc, p_id) WHERE p_doc LIKE 'FA\_%'
  UNION ALL
  SELECT * FROM pay_expected_lines(p_doc, p_id) WHERE p_doc LIKE 'PAY\_%'
  UNION ALL
  SELECT * FROM eqp_expected_lines(p_doc, p_id) WHERE p_doc LIKE 'EQP\_%'
$$;
CREATE OR REPLACE FUNCTION subledger_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE WHEN p_doc LIKE 'P2P\_%' THEN p2p_expected_text(p_doc, p_id) WHEN p_doc LIKE 'AR\_%' THEN ar_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'STK\_%' THEN stk_expected_text(p_doc, p_id) WHEN p_doc LIKE 'REV\_%' THEN rev_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'SUB\_%' THEN sub_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'FA\_%' THEN fa_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'PAY\_%' THEN pay_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'EQP\_%' THEN eqp_expected_text(p_doc, p_id) ELSE '' END
$$;
CREATE OR REPLACE FUNCTION journals_subledger_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.source_type ~ '^(P2P|AR|STK|REV|SUB|FA|PAY|EQP)_' AND TG_OP = 'UPDATE' AND NEW.status = 'CANCELLED' AND OLD.status <> 'CANCELLED' THEN
    IF NEW.source_type = 'FA_DEP' AND EXISTS (SELECT 1 FROM depreciation_runs r WHERE r.id::text = NEW.source_ref AND r.journal_id = NEW.id AND r.status = 'CANCELLED') THEN
      RETURN NEW;
    END IF;
    IF NEW.source_type = 'EQP_CHG' AND EXISTS (SELECT 1 FROM equipment_charge_runs r WHERE r.id::text = NEW.source_ref AND r.journal_id = NEW.id AND r.status = 'CANCELLED') THEN
      RETURN NEW;
    END IF;
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
  IF j.source_type ~ '^(P2P|AR|STK|REV|SUB|FA|PAY|EQP)_' AND j.created_txid IS DISTINCT FROM txid_current() THEN
    RAISE EXCEPTION 'lines of subledger journal % are written only by the transaction that generated it', jid USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END$$;
CREATE OR REPLACE FUNCTION journals_reserved_idempotency() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF lower(NEW.idempotency_key) LIKE 'p2p:%'
     AND (NEW.source_type !~ '^(P2P|AR|STK|REV|SUB|FA|PAY|EQP)_' OR NEW.idempotency_key IS DISTINCT FROM 'p2p:' || NEW.source_type || ':' || NEW.source_ref) THEN
    RAISE EXCEPTION 'idempotency keys beginning "p2p:" are reserved to the subledger journal of the named document' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

DROP TRIGGER journals_subledger_no_reversal ON journals;
CREATE TRIGGER journals_subledger_no_reversal BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.status = 'REVERSED' AND OLD.status <> 'REVERSED' AND NEW.source_type ~ '^(P2P|AR|STK|REV|SUB|FA|PAY|EQP)_')
  EXECUTE FUNCTION journals_subledger_no_reversal();

-- ------------------------------------------------- row security, grants
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['equipment_charge_rates', 'equipment_charge_runs', 'equipment_charge_lines'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON equipment_charge_rates TO serk_app;
GRANT UPDATE (status, journal_id, cancel_reason) ON equipment_charge_runs TO serk_app;
GRANT EXECUTE ON FUNCTION equipment_charge_rate_at(uuid, date), eqp_expected_lines(text, uuid), eqp_expected_text(text, uuid) TO serk_app;

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
  -- 0065: inspection and test plans with their items.
  WHEN 'itp' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'items', (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.seq) FROM itp_items i WHERE i.itp_id = x.id))
      INTO j FROM itps x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0072: this branch's document and fleet types (0052-0057), restored after the new-version migrations.
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
  -- 0056: maintenance work orders.
  WHEN 'maintenance_work_order' THEN SELECT to_jsonb(x) INTO j FROM maintenance_work_orders x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0057: fleet policies.
  WHEN 'fleet_policy' THEN SELECT to_jsonb(x) INTO j FROM fleet_policies x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0073: inspection results and NCR closures are attested decisions.
  WHEN 'inspection_request' THEN SELECT to_jsonb(x) INTO j FROM inspection_requests x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'ncr' THEN SELECT to_jsonb(x) INTO j FROM ncrs x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0074: an employee's approval attests the record with its bank account digest, never the ciphertext.
  WHEN 'employee' THEN SELECT to_jsonb(x) || jsonb_build_object('account_number', x.account_digest, 'account_number_enc', NULL) INTO j FROM employees x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0076: timesheets (with lines) and the HSE decisions that are attested.
  WHEN 'timesheet' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'lines', (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.employee_id) FROM timesheet_lines l WHERE l.timesheet_id = x.id))
      INTO j FROM timesheets x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'incident' THEN SELECT to_jsonb(x) INTO j FROM incidents x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'capa' THEN SELECT to_jsonb(x) INTO j FROM capas x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'permit_to_work' THEN SELECT to_jsonb(x) INTO j FROM permits_to_work x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0077: a transmittal with the revisions it carried; a submittal.
  WHEN 'transmittal' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'items', (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.revision_id) FROM transmittal_items i WHERE i.transmittal_id = x.id))
      INTO j FROM transmittals x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'submittal' THEN SELECT to_jsonb(x) INTO j FROM submittals x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0078: a rule of credit with its weighted steps (what its approver attests).
  WHEN 'credit_rule' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'steps', (SELECT jsonb_agg(to_jsonb(s) ORDER BY s.seq) FROM credit_steps s WHERE s.rule_id = x.id))
      INTO j FROM credit_rules x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0079: an equipment charge rate (what its approver attests).
  WHEN 'equipment_charge_rate' THEN SELECT to_jsonb(x) INTO j FROM equipment_charge_rates x WHERE tenant_id = t AND id = p_id::uuid;
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy', 'fixed_asset', 'asset_disposal', 'payroll_policy', 'payroll_rule_set', 'pay_run', 'consolidation_group', 'consolidation_run', 'tax_return', 'document_policy', 'equipment_operator_auth', 'fleet_policy', 'employee', 'equipment_charge_rate') THEN  -- 0079: charge rates are tenant-wide
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
  -- 0065: a plan belongs to its project.
  WHEN 'itp' THEN SELECT project_id::text INTO r FROM itps x WHERE x.id = v_id;
  -- 0072: this branch's document and fleet types (0052-0057), restored after the new-version migrations.
  -- 0052: a document revision belongs to its document's project; an RFI to its project.
  WHEN 'document_revision' THEN SELECT d.project_id::text INTO r FROM document_revisions x JOIN documents d ON d.id = x.document_id WHERE x.id = v_id;
  WHEN 'rfi' THEN SELECT project_id::text INTO r FROM rfis x WHERE x.id = v_id;
  -- 0054: equipment belongs to the project it is at (none in the yard: tenant-wide scope); reservations and logs to their project.
  WHEN 'equipment' THEN SELECT COALESCE(current_project_id::text, '') INTO r FROM equipment x WHERE x.id = v_id;
  WHEN 'equipment_meter_reset' THEN SELECT COALESCE(e.current_project_id::text, '') INTO r FROM equipment_meter_resets x JOIN equipment e ON e.id = x.equipment_id WHERE x.id = v_id;
  -- 0056: a work order belongs to the project its machine is at (none in the yard: tenant-wide scope).
  WHEN 'maintenance_work_order' THEN SELECT COALESCE(e.current_project_id::text, '') INTO r FROM maintenance_work_orders x JOIN equipment e ON e.id = x.equipment_id WHERE x.id = v_id;
  WHEN 'equipment_reservation' THEN SELECT project_id::text INTO r FROM equipment_reservations x WHERE x.id = v_id;
  WHEN 'equipment_log' THEN SELECT project_id::text INTO r FROM equipment_logs x WHERE x.id = v_id;
  -- 0073: inspection requests and NCRs belong to their project.
  WHEN 'inspection_request' THEN SELECT project_id::text INTO r FROM inspection_requests x WHERE x.id = v_id;
  WHEN 'ncr' THEN SELECT project_id::text INTO r FROM ncrs x WHERE x.id = v_id;
  -- 0076: timesheets, incidents and permits belong to their project; a corrective action to its incident's.
  WHEN 'timesheet' THEN SELECT project_id::text INTO r FROM timesheets x WHERE x.id = v_id;
  WHEN 'incident' THEN SELECT project_id::text INTO r FROM incidents x WHERE x.id = v_id;
  WHEN 'capa' THEN SELECT i.project_id::text INTO r FROM capas x JOIN incidents i ON i.id = x.incident_id WHERE x.id = v_id;
  WHEN 'permit_to_work' THEN SELECT project_id::text INTO r FROM permits_to_work x WHERE x.id = v_id;
  -- 0077: transmittals and submittals belong to their project.
  WHEN 'transmittal' THEN SELECT project_id::text INTO r FROM transmittals x WHERE x.id = v_id;
  WHEN 'submittal' THEN SELECT project_id::text INTO r FROM submittals x WHERE x.id = v_id;
  -- 0078: a rule of credit belongs to its project.
  WHEN 'credit_rule' THEN SELECT project_id::text INTO r FROM credit_rules x WHERE x.id = v_id;
  -- 0079: a charge run belongs to its project.
  WHEN 'equipment_charge_run' THEN SELECT project_id::text INTO r FROM equipment_charge_runs x WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;
