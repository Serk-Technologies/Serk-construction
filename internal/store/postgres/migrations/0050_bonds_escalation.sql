-- 0050 Guarantees and bonds; price indices, escalation formulas and price
-- adjustment (WP-2.7; SRS CON-05, CON-07, TRS-04)
--
-- WHAT THIS MIGRATION DOES
--   * GUARANTEES: bid, advance, performance and retention guarantees (bank
--     bonds) held against a contract. The face value may only be reduced and
--     the expiry only extended, each with a stated reason. Releasing a
--     guarantee back to the bank, or recording that it was called, is a
--     decision: a different person from the one who registered it, with a
--     fresh multi-factor sign-in and an approval attestation.
--   * PRICE INDICES: published index series (for example a cement or fuel
--     index) and their monthly values. A value is proposed by one person and
--     approved by another; an approved value never changes and there is
--     never a second value for the same index and month.
--   * ESCALATION FORMULAS: one approved formula per contract,
--     Pn = a + sum(b x Ln / Lo), where a is the fixed (non-adjustable)
--     portion, b the weight of each index, Lo the index in the base month and
--     Ln the index in the current month. a + sum(b) must be exactly 1.
--   * PRICE ADJUSTMENTS: for a certified payment certificate the database
--     computes the factor from approved index values and the adjustment
--     = value of the work certified in the period x (Pn - 1), rounded to the
--     currency. The preparer only chooses the certificate and the month of
--     the indices; every figure is the database's, recomputed at approval.
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0046, additions marked 0050):
--   p2p_subject_text, serk_subject_project

-- ------------------------------------------------------------- guarantees
CREATE TABLE guarantees (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  contract_id     uuid NOT NULL,
  number          text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('BID', 'ADVANCE', 'PERFORMANCE', 'RETENTION')),
  issuer          text NOT NULL CHECK (length(btrim(issuer)) BETWEEN 2 AND 200),
  reference       text NOT NULL CHECK (length(btrim(reference)) BETWEEN 2 AND 100),
  currency        char(3) NOT NULL REFERENCES currencies(code),
  original_amount numeric(24,6) NOT NULL CHECK (original_amount > 0),
  amount          numeric(24,6) NOT NULL CHECK (amount > 0),
  issued_on       date NOT NULL,
  expires_on      date NOT NULL,
  change_reason   text,
  version         integer NOT NULL DEFAULT 1,
  status          text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'RELEASED', 'CALLED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  closed_by       text,
  closed_at       timestamptz,
  close_reference text,
  close_reason    text,
  called_amount   numeric(24,6),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, issuer, reference),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES contracts(tenant_id, id),
  CONSTRAINT guarantee_dates CHECK (expires_on > issued_on),
  CONSTRAINT guarantee_reduce_only CHECK (amount <= original_amount),
  CONSTRAINT guarantee_closer_differs CHECK (closed_by IS NULL OR closed_by <> created_by),
  CONSTRAINT guarantee_close_whole CHECK (
    (status = 'ACTIVE' AND closed_by IS NULL AND closed_at IS NULL AND close_reference IS NULL AND close_reason IS NULL AND called_amount IS NULL)
    OR (status = 'RELEASED' AND closed_by IS NOT NULL AND closed_at IS NOT NULL AND close_reference IS NOT NULL AND close_reason IS NOT NULL AND called_amount IS NULL)
    OR (status = 'CALLED' AND closed_by IS NOT NULL AND closed_at IS NOT NULL AND close_reference IS NOT NULL AND close_reason IS NOT NULL
        AND called_amount > 0 AND called_amount <= amount))
);
CREATE INDEX guarantees_contract ON guarantees (tenant_id, contract_id, status);

CREATE FUNCTION guarantees_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'ACTIVE' OR NEW.closed_by IS NOT NULL OR NEW.version <> 1 OR NEW.change_reason IS NOT NULL THEN
      RAISE EXCEPTION 'guarantees are registered ACTIVE' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.amount <> NEW.original_amount THEN
      RAISE EXCEPTION 'a guarantee is registered at its face value' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.issued_on > current_date THEN
      RAISE EXCEPTION 'a guarantee cannot be issued in the future' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM contracts WHERE id = NEW.contract_id AND tenant_id = current_tenant() AND status IN ('DRAFT', 'SUBMITTED', 'ACTIVE')) THEN
      RAISE EXCEPTION 'guarantees are held against a draft, submitted or active contract' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'a released or called guarantee never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'ACTIVE' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['amount', 'expires_on', 'change_reason', 'version'], 'guarantee');
    IF NEW.amount > OLD.amount THEN
      RAISE EXCEPTION 'a guarantee''s value can only be reduced (register a new guarantee for more cover)' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.expires_on < OLD.expires_on THEN
      RAISE EXCEPTION 'a guarantee''s expiry can only be extended' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.amount = OLD.amount AND NEW.expires_on = OLD.expires_on THEN
      RAISE EXCEPTION 'nothing to change' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.change_reason IS NULL OR length(btrim(NEW.change_reason)) < 5 THEN
      RAISE EXCEPTION 'reducing or extending a guarantee requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    NEW.version := OLD.version + 1;
    RETURN NEW;
  END IF;
  -- Released to the bank or called: a decision with an attestation.
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at', 'close_reference', 'close_reason', 'called_amount', 'version'], 'guarantee');
  IF length(btrim(COALESCE(NEW.close_reason, ''))) < 5 OR length(btrim(COALESCE(NEW.close_reference, ''))) < 2 THEN
    RAISE EXCEPTION 'releasing or calling a guarantee needs the letter''s reference and a reason' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'CALLED' AND OLD.expires_on < current_date THEN
    RAISE EXCEPTION 'an expired guarantee cannot be called' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('bond.close', 'guarantee', NEW.id::text, NEW.closed_by, ARRAY['bond.approve'], true, NEW.status, NULL);
  NEW.closed_at := now();
  NEW.version := OLD.version + 1;
  RETURN NEW;
END$$;
CREATE TRIGGER guarantees_guard BEFORE INSERT OR UPDATE ON guarantees FOR EACH ROW EXECUTE FUNCTION guarantees_guard();

-- ---------------------------------------------------------- price indices
CREATE TABLE price_indices (
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  code       text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9_.-]{1,29}$'),
  name       text NOT NULL CHECK (length(btrim(name)) BETWEEN 3 AND 200),
  source     text NOT NULL CHECK (length(btrim(source)) BETWEEN 2 AND 200),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, code)
);

CREATE TABLE price_index_values (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  index_code      text NOT NULL,
  period          date NOT NULL CHECK (period = date_trunc('month', period)::date),
  value           numeric(18,6) NOT NULL CHECK (value > 0),
  source_ref      text NOT NULL CHECK (length(btrim(source_ref)) BETWEEN 2 AND 200),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, index_code) REFERENCES price_indices(tenant_id, code),
  CONSTRAINT index_value_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
-- One live value per index and month; an approved one is final.
CREATE UNIQUE INDEX price_index_values_one ON price_index_values (tenant_id, index_code, period) WHERE status IN ('DRAFT', 'SUBMITTED', 'APPROVED');

CREATE FUNCTION price_index_values_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'index values are proposed as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.period > date_trunc('month', current_date)::date THEN
      RAISE EXCEPTION 'an index value cannot be for a future month' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided index value never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'index value');
    PERFORM approval_use('index.submit', 'price_index_value', NEW.id::text, NEW.created_by, ARRAY['escalation.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'index value');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting an index value requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('index.decide', 'price_index_value', NEW.id::text, NEW.approved_by, ARRAY['escalation.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal index value transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER price_index_values_guard BEFORE INSERT OR UPDATE ON price_index_values FOR EACH ROW EXECUTE FUNCTION price_index_values_guard();

-- ---------------------------------------------------- escalation formulas
CREATE TABLE escalation_formulas (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  contract_id     uuid NOT NULL,
  number          text NOT NULL,
  fixed_portion   numeric(9,6) NOT NULL CHECK (fixed_portion >= 0 AND fixed_portion < 1),
  base_period     date NOT NULL CHECK (base_period = date_trunc('month', base_period)::date),
  clause_ref      text NOT NULL CHECK (length(btrim(clause_ref)) BETWEEN 1 AND 100),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES contracts(tenant_id, id),
  CONSTRAINT formula_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE UNIQUE INDEX escalation_formulas_one ON escalation_formulas (tenant_id, contract_id) WHERE status IN ('DRAFT', 'SUBMITTED', 'APPROVED');

CREATE TABLE escalation_terms (
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  formula_id uuid NOT NULL,
  index_code text NOT NULL,
  weight     numeric(9,6) NOT NULL CHECK (weight > 0 AND weight <= 1),
  PRIMARY KEY (tenant_id, formula_id, index_code),
  FOREIGN KEY (tenant_id, formula_id) REFERENCES escalation_formulas(tenant_id, id),
  FOREIGN KEY (tenant_id, index_code) REFERENCES price_indices(tenant_id, code)
);

-- A formula is complete when its weights and fixed portion add up to
-- exactly 1 and every index has an approved value for the base month.
CREATE FUNCTION escalation_formula_check(p_formula uuid) RETURNS void
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE f escalation_formulas; total numeric; n integer; missing text;
BEGIN
  SELECT * INTO f FROM escalation_formulas WHERE id = p_formula AND tenant_id = current_tenant();
  SELECT count(*), COALESCE(sum(weight), 0) INTO n, total FROM escalation_terms WHERE formula_id = p_formula AND tenant_id = current_tenant();
  IF n = 0 THEN
    RAISE EXCEPTION 'a formula needs at least one index' USING ERRCODE = 'check_violation';
  END IF;
  IF f.fixed_portion + total <> 1 THEN
    RAISE EXCEPTION 'the fixed portion (%) and the index weights (%) must add up to exactly 1, not %', f.fixed_portion, total, f.fixed_portion + total
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT string_agg(t.index_code, ', ' ORDER BY t.index_code) INTO missing FROM escalation_terms t
   WHERE t.formula_id = p_formula AND t.tenant_id = current_tenant()
     AND NOT EXISTS (SELECT 1 FROM price_index_values v WHERE v.tenant_id = t.tenant_id AND v.index_code = t.index_code
                       AND v.period = f.base_period AND v.status = 'APPROVED');
  IF missing IS NOT NULL THEN
    RAISE EXCEPTION 'no approved base value (%) for: %', to_char(f.base_period, 'YYYY-MM'), missing USING ERRCODE = 'check_violation';
  END IF;
END$$;

CREATE FUNCTION escalation_formulas_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'formulas are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM contracts WHERE id = NEW.contract_id AND tenant_id = current_tenant() AND status = 'ACTIVE') THEN
      RAISE EXCEPTION 'an escalation formula is set for an ACTIVE contract' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided formula never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'formula');
    PERFORM escalation_formula_check(NEW.id);
    PERFORM approval_use('formula.submit', 'escalation_formula', NEW.id::text, NEW.created_by, ARRAY['escalation.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'formula');
    IF NEW.status = 'APPROVED' THEN
      PERFORM escalation_formula_check(NEW.id);
    ELSIF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a formula requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('formula.decide', 'escalation_formula', NEW.id::text, NEW.approved_by, ARRAY['escalation.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal formula transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER escalation_formulas_guard BEFORE INSERT OR UPDATE ON escalation_formulas FOR EACH ROW EXECUTE FUNCTION escalation_formulas_guard();

CREATE FUNCTION escalation_terms_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM escalation_formulas WHERE id = NEW.formula_id AND tenant_id = current_tenant() AND status = 'DRAFT') THEN
    RAISE EXCEPTION 'the indices of a formula are set while it is a draft' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER escalation_terms_guard BEFORE INSERT ON escalation_terms FOR EACH ROW EXECUTE FUNCTION escalation_terms_guard();

-- The factor Pn = a + sum(b x Ln / Lo), rounded half-even to 6 decimals,
-- from approved values only; and what went into it.
CREATE FUNCTION price_adjustment_factor(p_formula uuid, p_period date, OUT factor numeric, OUT detail jsonb)
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE f escalation_formulas; t record; lo numeric; ln numeric; s numeric := 0; missing text[] := '{}';
BEGIN
  SELECT * INTO f FROM escalation_formulas WHERE id = p_formula AND tenant_id = current_tenant();
  detail := '[]'::jsonb;
  FOR t IN SELECT index_code, weight FROM escalation_terms WHERE formula_id = p_formula AND tenant_id = current_tenant() ORDER BY index_code LOOP
    SELECT value INTO lo FROM price_index_values WHERE tenant_id = current_tenant() AND index_code = t.index_code AND period = f.base_period AND status = 'APPROVED';
    SELECT value INTO ln FROM price_index_values WHERE tenant_id = current_tenant() AND index_code = t.index_code AND period = p_period AND status = 'APPROVED';
    IF lo IS NULL OR ln IS NULL THEN
      missing := missing || (t.index_code || ' ' || to_char(CASE WHEN lo IS NULL THEN f.base_period ELSE p_period END, 'YYYY-MM'));
      CONTINUE;
    END IF;
    s := s + t.weight * ln / lo;
    detail := detail || jsonb_build_array(jsonb_build_object('index', t.index_code, 'weight', t.weight, 'base_value', lo, 'current_value', ln,
                                                              'ratio', round_half_even(ln / lo, 6)));
  END LOOP;
  IF cardinality(missing) > 0 THEN
    RAISE EXCEPTION 'approved index values are missing: %', array_to_string(missing, ', ') USING ERRCODE = 'check_violation';
  END IF;
  factor := round_half_even(f.fixed_portion + s, 6);
END$$;

-- ------------------------------------------------------ price adjustments
CREATE TABLE price_adjustments (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  ipc_id          uuid NOT NULL,
  contract_id     uuid NOT NULL,
  formula_id      uuid NOT NULL,
  number          text NOT NULL,
  current_period  date NOT NULL CHECK (current_period = date_trunc('month', current_period)::date),
  currency        char(3) NOT NULL REFERENCES currencies(code),
  base_value      numeric(24,6) NOT NULL DEFAULT 0,
  factor          numeric(12,6) NOT NULL DEFAULT 1,
  amount          numeric(24,6) NOT NULL DEFAULT 0,
  detail          jsonb NOT NULL DEFAULT '[]',
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, ipc_id) REFERENCES ipcs(tenant_id, id),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES contracts(tenant_id, id),
  FOREIGN KEY (tenant_id, formula_id) REFERENCES escalation_formulas(tenant_id, id),
  CONSTRAINT adjustment_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE UNIQUE INDEX price_adjustments_one ON price_adjustments (tenant_id, ipc_id) WHERE status <> 'REJECTED';

CREATE FUNCTION price_adjustments_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE i ipcs; f escalation_formulas; r record;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'price adjustments are prepared as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    SELECT * INTO i FROM ipcs WHERE id = NEW.ipc_id AND tenant_id = current_tenant();
    IF i.id IS NULL OR i.status NOT IN ('CERTIFIED', 'INVOICED') THEN
      RAISE EXCEPTION 'a price adjustment is computed for a certified payment certificate' USING ERRCODE = 'check_violation';
    END IF;
    SELECT * INTO f FROM escalation_formulas WHERE contract_id = i.contract_id AND tenant_id = current_tenant() AND status = 'APPROVED';
    IF f.id IS NULL THEN
      RAISE EXCEPTION 'the contract has no approved escalation formula' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.current_period > date_trunc('month', i.period_end)::date OR NEW.current_period < f.base_period THEN
      RAISE EXCEPTION 'the index month must be between the base month (%) and the certificate''s month (%)',
        to_char(f.base_period, 'YYYY-MM'), to_char(i.period_end, 'YYYY-MM') USING ERRCODE = 'check_violation';
    END IF;
    -- Every figure is the database's.
    NEW.contract_id := i.contract_id;
    NEW.formula_id := f.id;
    NEW.currency := i.currency;
    NEW.base_value := i.this_gross;
    r := price_adjustment_factor(f.id, NEW.current_period);
    NEW.factor := r.factor;
    NEW.detail := r.detail;
    NEW.amount := p2p_round(NEW.base_value * (r.factor - 1), NEW.currency);
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided price adjustment never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'price adjustment');
    PERFORM approval_use('adjustment.submit', 'price_adjustment', NEW.id::text, NEW.created_by, ARRAY['escalation.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'price adjustment');
    IF NEW.status = 'APPROVED' THEN
      r := price_adjustment_factor(NEW.formula_id, NEW.current_period);
      IF r.factor <> OLD.factor OR p2p_round(OLD.base_value * (r.factor - 1), OLD.currency) <> OLD.amount THEN
        RAISE EXCEPTION 'the price adjustment no longer matches the approved index values' USING ERRCODE = 'check_violation';
      END IF;
    ELSIF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a price adjustment requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('adjustment.decide', 'price_adjustment', NEW.id::text, NEW.approved_by, ARRAY['escalation.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal price adjustment transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER price_adjustments_guard BEFORE INSERT OR UPDATE ON price_adjustments FOR EACH ROW EXECUTE FUNCTION price_adjustments_guard();

-- ---------------------------------------- shared functions (verbatim + 0050)
-- p2p_subject_text: verbatim from 0046 plus guarantees, index values, formulas and adjustments (marked 0050).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0046 plus the 0050 documents (marked 0050).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value') THEN
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
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['guarantees', 'price_indices', 'price_index_values', 'escalation_formulas', 'escalation_terms', 'price_adjustments'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (amount, expires_on, change_reason, version, status, closed_by, closed_at, close_reference, close_reason, called_amount) ON guarantees TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON price_index_values TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON escalation_formulas TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON price_adjustments TO serk_app;
REVOKE ALL ON FUNCTION escalation_formula_check(uuid), price_adjustment_factor(uuid, date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION escalation_formula_check(uuid), price_adjustment_factor(uuid, date) TO serk_app;
