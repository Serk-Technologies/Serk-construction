-- 0057 Fixed assets (WP-2.9; SRS FIN-06, EQP-01; IAS 16 cost model)
--
-- WHAT THIS MIGRATION DOES
--   * ASSET CLASS: the accounts of a kind of asset (cost, accumulated
--     depreciation, depreciation expense, gain or loss on disposal) and its
--     default method and useful life. Kept by the controller (account.manage).
--   * FIXED ASSET: registered as a DRAFT (cost, residual value, useful life in
--     months, straight-line SL or double-declining DB, in-service date, the
--     account the cost is capitalised FROM: assets under construction, an
--     expense or a clearing account) and ACTIVATED by another person (fresh
--     MFA, attested); activation generates the FA_CAP journal (Dr asset cost /
--     Cr the capitalised-from account).
--   * DEPRECIATION RUN: one per legal entity and month end. The database
--     computes each active asset's charge for the month: SL (cost - residual) /
--     life, DB net book value x 2 / life; never taking the net book value below
--     the residual value; assets in service by the month end, not disposed. The
--     run generates the FA_DEP journal (Dr depreciation expense / Cr
--     accumulated depreciation, per class) for finance to approve and post.
--     Months are run in order; a draft run may be cancelled with its journal.
--   * DISPOSAL: proceeds and date; the database computes net book value and
--     gain or loss; approved by another person (fresh MFA), generating FA_DISP
--     (Dr accumulated depreciation, Dr proceeds account, Cr cost, gain or loss).
--   * FA_* journals are subledger journals like P2P/AR/STK/REV/SUB ones.
--
-- SHARED FUNCTIONS REDEFINED HERE: subledger_expected_lines / _text,
-- journals_subledger_guard, journal_lines_p2p_birth, journals_reserved_idempotency,
-- trigger journals_subledger_no_reversal (verbatim from 0055, FA added);
-- p2p_subject_text / serk_subject_project (verbatim from 0056 plus the 0057 documents).

CREATE TABLE asset_classes (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id      uuid NOT NULL,
  code                 text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9-]{1,19}$'),
  name                 text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 120),
  cost_account_id      uuid NOT NULL,
  accum_account_id     uuid NOT NULL,
  expense_account_id   uuid NOT NULL,
  gain_loss_account_id uuid NOT NULL,
  default_method       text NOT NULL DEFAULT 'SL' CHECK (default_method IN ('SL', 'DB')),
  default_life_months  integer NOT NULL CHECK (default_life_months BETWEEN 1 AND 1200),
  created_by           text NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id)
);

CREATE FUNCTION asset_classes_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.cost_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'ASSET' AND active)
     OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.accum_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'ASSET' AND active)
     OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.expense_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'EXPENSE' AND active)
     OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.gain_loss_account_id AND legal_entity_id = NEW.legal_entity_id AND type IN ('REVENUE', 'EXPENSE') AND active) THEN
    RAISE EXCEPTION 'asset class accounts: cost and accumulated depreciation are ASSET accounts, depreciation is an EXPENSE account, gain or loss is REVENUE or EXPENSE, all active accounts of the entity'
      USING ERRCODE = 'check_violation';
  END IF;
  IF cardinality(ARRAY(SELECT DISTINCT x FROM unnest(ARRAY[NEW.cost_account_id, NEW.accum_account_id, NEW.expense_account_id, NEW.gain_loss_account_id]) x)) <> 4 THEN
    RAISE EXCEPTION 'the four accounts of an asset class must differ' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER asset_classes_guard BEFORE INSERT ON asset_classes FOR EACH ROW EXECUTE FUNCTION asset_classes_guard();

CREATE TABLE fixed_assets (
  id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id            uuid NOT NULL,
  class_id                   uuid NOT NULL,
  number                     text NOT NULL,
  description                text NOT NULL CHECK (length(btrim(description)) BETWEEN 3 AND 300),
  project_id                 uuid,
  location                   text,
  in_service_on              date NOT NULL,
  cost                       numeric(24,6) NOT NULL CHECK (cost > 0),
  residual                   numeric(24,6) NOT NULL DEFAULT 0 CHECK (residual >= 0),
  life_months                integer NOT NULL CHECK (life_months BETWEEN 1 AND 1200),
  method                     text NOT NULL CHECK (method IN ('SL', 'DB')),
  capitalised_from_account_id uuid NOT NULL,
  status                     text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'ACTIVE', 'REJECTED', 'DISPOSED')),
  created_by                 text NOT NULL,
  created_at                 timestamptz NOT NULL DEFAULT now(),
  approved_by                text,
  approved_at                timestamptz,
  decision_reason            text,
  cap_journal_id             uuid UNIQUE,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, class_id) REFERENCES asset_classes(tenant_id, id),
  CONSTRAINT asset_residual_below_cost CHECK (residual < cost),
  CONSTRAINT asset_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);

CREATE TABLE depreciation_runs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id uuid NOT NULL,
  number          text NOT NULL,
  period_end      date NOT NULL CHECK (period_end = (date_trunc('month', period_end) + interval '1 month - 1 day')::date),
  status          text NOT NULL DEFAULT 'PREPARED' CHECK (status IN ('PREPARED', 'CANCELLED')),
  journal_id      uuid UNIQUE,
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  cancel_reason   text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id)
);
CREATE UNIQUE INDEX depreciation_runs_one ON depreciation_runs (tenant_id, legal_entity_id, period_end) WHERE status = 'PREPARED';

CREATE TABLE depreciation_lines (
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id     uuid NOT NULL,
  asset_id   uuid NOT NULL,
  nbv_before numeric(24,6) NOT NULL,
  amount     numeric(24,6) NOT NULL CHECK (amount > 0),
  PRIMARY KEY (tenant_id, run_id, asset_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES depreciation_runs(tenant_id, id),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES fixed_assets(tenant_id, id)
);

-- Depreciation charged so far on an asset (runs not cancelled).
CREATE FUNCTION fa_accumulated(p_asset uuid) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(sum(l.amount), 0) FROM depreciation_lines l JOIN depreciation_runs r ON r.id = l.run_id
   WHERE l.asset_id = p_asset AND r.status = 'PREPARED' AND l.tenant_id = current_tenant()
$$;

-- The month's charge per asset of an entity.
CREATE FUNCTION fa_depreciation_compute(p_entity uuid, p_period date)
RETURNS TABLE (asset_id uuid, nbv_before numeric, amount numeric)
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE a fixed_assets; ccy char(3); nbv numeric; charge numeric;
BEGIN
  SELECT functional_currency INTO ccy FROM legal_entities WHERE id = p_entity;
  FOR a IN SELECT * FROM fixed_assets x WHERE x.legal_entity_id = p_entity AND x.tenant_id = current_tenant() AND x.status = 'ACTIVE' AND x.in_service_on <= p_period
             AND NOT EXISTS (SELECT 1 FROM depreciation_lines l JOIN depreciation_runs r ON r.id = l.run_id WHERE l.asset_id = x.id AND r.status = 'PREPARED' AND r.period_end = p_period)
           ORDER BY x.number LOOP
    nbv := a.cost - fa_accumulated(a.id);
    IF nbv <= a.residual THEN
      CONTINUE;
    END IF;
    charge := CASE a.method WHEN 'SL' THEN p2p_round((a.cost - a.residual) / a.life_months, ccy) ELSE p2p_round(nbv * 2 / a.life_months, ccy) END;
    charge := least(charge, nbv - a.residual);
    IF charge > 0 THEN
      asset_id := a.id; nbv_before := nbv; amount := charge;
      RETURN NEXT;
    END IF;
  END LOOP;
END$$;

CREATE TABLE asset_disposals (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  asset_id            uuid NOT NULL,
  number              text NOT NULL,
  disposed_on         date NOT NULL,
  proceeds            numeric(24,6) NOT NULL DEFAULT 0 CHECK (proceeds >= 0),
  proceeds_account_id uuid NOT NULL,
  reason              text NOT NULL CHECK (length(btrim(reason)) BETWEEN 5 AND 500),
  status              text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  accumulated         numeric(24,6),
  nbv                 numeric(24,6),
  gain                numeric(24,6),
  created_by          text NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  approved_by         text,
  approved_at         timestamptz,
  decision_reason     text,
  journal_id          uuid UNIQUE,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES fixed_assets(tenant_id, id),
  CONSTRAINT disposal_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE UNIQUE INDEX asset_disposals_one ON asset_disposals (tenant_id, asset_id) WHERE status IN ('DRAFT', 'SUBMITTED', 'APPROVED');

CREATE FUNCTION fixed_assets_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE c asset_classes;
BEGIN
  IF TG_OP = 'INSERT' THEN
    SELECT * INTO c FROM asset_classes WHERE id = NEW.class_id AND tenant_id = current_tenant();
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.cap_journal_id IS NOT NULL THEN
      RAISE EXCEPTION 'assets are registered as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    NEW.legal_entity_id := c.legal_entity_id;
    IF NEW.project_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id AND legal_entity_id = c.legal_entity_id) THEN
      RAISE EXCEPTION 'the asset''s project belongs to another entity' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.capitalised_from_account_id AND legal_entity_id = c.legal_entity_id AND active AND type IN ('ASSET', 'EXPENSE', 'LIABILITY'))
       OR NEW.capitalised_from_account_id IN (c.cost_account_id, c.accum_account_id) THEN
      RAISE EXCEPTION 'capitalise from an active asset-under-construction, expense or clearing account of the entity (not the class''s own accounts)' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'DISPOSED') THEN
    RAISE EXCEPTION 'a % asset never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'ACTIVE' AND NEW.status = 'ACTIVE' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['cap_journal_id'], 'asset');
    IF OLD.cap_journal_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.cap_journal_id AND source_type = 'FA_CAP' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'an asset is bound once to its own DRAFT FA_CAP journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'ACTIVE' AND NEW.status = 'DISPOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'asset');
    IF pg_trigger_depth() < 2 THEN
      RAISE EXCEPTION 'an asset is disposed only by approving its disposal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'asset');
    PERFORM approval_use('asset.submit', 'fixed_asset', NEW.id::text, NEW.created_by, ARRAY['asset.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('ACTIVE', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'asset');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting an asset requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('asset.decide', 'fixed_asset', NEW.id::text, NEW.approved_by, ARRAY['asset.approve'], NEW.status = 'ACTIVE', CASE WHEN NEW.status = 'ACTIVE' THEN 'APPROVED' ELSE 'REJECTED' END, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal asset transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER fixed_assets_guard BEFORE INSERT OR UPDATE ON fixed_assets FOR EACH ROW EXECUTE FUNCTION fixed_assets_guard();

CREATE FUNCTION depreciation_runs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE js text;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'PREPARED' OR NEW.journal_id IS NOT NULL THEN
      RAISE EXCEPTION 'depreciation runs are created PREPARED, then bound to their journal' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.period_end > (date_trunc('month', current_date) + interval '1 month - 1 day')::date THEN
      RAISE EXCEPTION 'depreciation is run for the current month end or an earlier one' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM depreciation_runs r WHERE r.legal_entity_id = NEW.legal_entity_id AND r.status = 'PREPARED' AND r.period_end > NEW.period_end) THEN
      RAISE EXCEPTION 'a later month has already been depreciated' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM depreciation_runs r JOIN journals j ON j.id = r.journal_id WHERE r.legal_entity_id = NEW.legal_entity_id AND r.status = 'PREPARED' AND j.status IN ('DRAFT', 'APPROVED')) THEN
      RAISE EXCEPTION 'the entity has a depreciation run whose journal is not posted yet: post or cancel it first' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'CANCELLED' THEN
    RAISE EXCEPTION 'a cancelled run never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.journal_id IS NULL AND NEW.status = 'PREPARED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['journal_id'], 'depreciation run');
    IF NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.journal_id AND source_type = 'FA_DEP' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'a depreciation run is bound only to its own DRAFT FA_DEP journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'cancel_reason'], 'depreciation run');
  IF NEW.status <> 'CANCELLED' OR NEW.cancel_reason IS NULL OR length(btrim(NEW.cancel_reason)) < 5 THEN
    RAISE EXCEPTION 'a depreciation run changes only by being cancelled with a reason' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO js FROM journals WHERE id = OLD.journal_id;
  IF js IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'only a run whose journal is still a draft can be cancelled (this one is %)', js USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER depreciation_runs_guard BEFORE INSERT OR UPDATE ON depreciation_runs FOR EACH ROW EXECUTE FUNCTION depreciation_runs_guard();

CREATE FUNCTION depreciation_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r depreciation_runs; x record;
BEGIN
  SELECT * INTO r FROM depreciation_runs WHERE id = NEW.run_id AND tenant_id = current_tenant();
  IF r.journal_id IS NOT NULL OR r.status <> 'PREPARED' THEN
    RAISE EXCEPTION 'depreciation lines are written when the run is prepared' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO x FROM fa_depreciation_compute(r.legal_entity_id, r.period_end) c WHERE c.asset_id = NEW.asset_id;
  IF NOT FOUND OR (x.nbv_before, x.amount) IS DISTINCT FROM (NEW.nbv_before, NEW.amount) THEN
    RAISE EXCEPTION 'the depreciation of asset % is not the database''s computation', NEW.asset_id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER depreciation_lines_guard BEFORE INSERT ON depreciation_lines FOR EACH ROW EXECUTE FUNCTION depreciation_lines_guard();

CREATE FUNCTION asset_disposals_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE a fixed_assets; acc numeric;
BEGIN
  SELECT * INTO a FROM fixed_assets WHERE id = NEW.asset_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.journal_id IS NOT NULL OR NEW.nbv IS NOT NULL THEN
      RAISE EXCEPTION 'disposals are proposed as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF a.status <> 'ACTIVE' THEN
      RAISE EXCEPTION 'only an ACTIVE asset is disposed of' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.disposed_on < a.in_service_on OR NEW.disposed_on > current_date THEN
      RAISE EXCEPTION 'the disposal date is between the in-service date and today' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.proceeds_account_id AND legal_entity_id = a.legal_entity_id AND type = 'ASSET' AND active) THEN
      RAISE EXCEPTION 'proceeds go to an active ASSET account of the entity (bank clearing or a receivable)' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') AND NOT (OLD.status = 'APPROVED' AND OLD.journal_id IS NULL) THEN
    RAISE EXCEPTION 'a decided disposal never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['journal_id'], 'disposal');
    IF NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.journal_id AND source_type = 'FA_DISP' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'a disposal is bound once to its own DRAFT FA_DISP journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'disposal');
    PERFORM approval_use('disposal.submit', 'asset_disposal', NEW.id::text, NEW.created_by, ARRAY['asset.manage'], false, NULL, NULL);
    RETURN NEW;
  END IF;
  IF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason', 'accumulated', 'nbv', 'gain'], 'disposal');
    IF NEW.status = 'REJECTED' THEN
      IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
        RAISE EXCEPTION 'rejecting a disposal requires a reason' USING ERRCODE = 'check_violation';
      END IF;
      PERFORM approval_use('disposal.decide', 'asset_disposal', NEW.id::text, NEW.approved_by, ARRAY['asset.approve'], false, 'REJECTED', NULL);
      NEW.approved_at := now();
      RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM depreciation_lines l JOIN depreciation_runs r ON r.id = l.run_id JOIN journals j ON j.id = r.journal_id
                WHERE l.asset_id = a.id AND r.status = 'PREPARED' AND j.status IN ('DRAFT', 'APPROVED')) THEN
      RAISE EXCEPTION 'the asset is in a depreciation run whose journal is not posted: post or cancel it first' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('disposal.decide', 'asset_disposal', NEW.id::text, NEW.approved_by, ARRAY['asset.approve'], true, 'APPROVED', NULL);
    acc := fa_accumulated(a.id);
    NEW.accumulated := acc;
    NEW.nbv := a.cost - acc;
    NEW.gain := NEW.proceeds - NEW.nbv;
    NEW.approved_at := now();
    UPDATE fixed_assets SET status = 'DISPOSED' WHERE id = a.id;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal disposal transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER asset_disposals_guard BEFORE INSERT OR UPDATE ON asset_disposals FOR EACH ROW EXECUTE FUNCTION asset_disposals_guard();

-- The accounting of FA_* documents.
CREATE FUNCTION fa_expected_lines(p_doc text, p_id uuid)
RETURNS TABLE (account_id uuid, side text, amount numeric, project_id uuid, cost_code_id uuid)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  WITH e(account_id, side, amount, project_id, cost_code_id) AS (
    -- Capitalisation: Dr cost / Cr the account the cost is capitalised from.
    SELECT c.cost_account_id, 'DEBIT', a.cost, NULL::uuid, NULL::uuid
      FROM fixed_assets a JOIN asset_classes c ON c.id = a.class_id WHERE p_doc = 'FA_CAP' AND a.id = p_id AND a.tenant_id = current_tenant() AND a.status IN ('ACTIVE', 'DISPOSED')
    UNION ALL
    SELECT a.capitalised_from_account_id, 'CREDIT', a.cost, NULL::uuid, NULL::uuid
      FROM fixed_assets a WHERE p_doc = 'FA_CAP' AND a.id = p_id AND a.tenant_id = current_tenant() AND a.status IN ('ACTIVE', 'DISPOSED')
    UNION ALL
    -- Depreciation run: per class, Dr expense / Cr accumulated depreciation.
    SELECT c.expense_account_id, 'DEBIT', sum(l.amount), NULL::uuid, NULL::uuid
      FROM depreciation_lines l JOIN fixed_assets a ON a.id = l.asset_id JOIN asset_classes c ON c.id = a.class_id
     WHERE p_doc = 'FA_DEP' AND l.run_id = p_id AND l.tenant_id = current_tenant() GROUP BY c.expense_account_id
    UNION ALL
    SELECT c.accum_account_id, 'CREDIT', sum(l.amount), NULL::uuid, NULL::uuid
      FROM depreciation_lines l JOIN fixed_assets a ON a.id = l.asset_id JOIN asset_classes c ON c.id = a.class_id
     WHERE p_doc = 'FA_DEP' AND l.run_id = p_id AND l.tenant_id = current_tenant() GROUP BY c.accum_account_id
    UNION ALL
    -- Disposal: Dr accumulated, Dr proceeds, Cr cost, and the gain (Cr) or loss (Dr).
    SELECT c.accum_account_id, 'DEBIT', d.accumulated, NULL::uuid, NULL::uuid
      FROM asset_disposals d JOIN fixed_assets a ON a.id = d.asset_id JOIN asset_classes c ON c.id = a.class_id WHERE p_doc = 'FA_DISP' AND d.id = p_id AND d.status = 'APPROVED' AND d.tenant_id = current_tenant()
    UNION ALL
    SELECT d.proceeds_account_id, 'DEBIT', d.proceeds, NULL::uuid, NULL::uuid
      FROM asset_disposals d WHERE p_doc = 'FA_DISP' AND d.id = p_id AND d.status = 'APPROVED' AND d.tenant_id = current_tenant()
    UNION ALL
    SELECT c.cost_account_id, 'CREDIT', a.cost, NULL::uuid, NULL::uuid
      FROM asset_disposals d JOIN fixed_assets a ON a.id = d.asset_id JOIN asset_classes c ON c.id = a.class_id WHERE p_doc = 'FA_DISP' AND d.id = p_id AND d.status = 'APPROVED' AND d.tenant_id = current_tenant()
    UNION ALL
    SELECT c.gain_loss_account_id, CASE WHEN d.gain > 0 THEN 'CREDIT' ELSE 'DEBIT' END, abs(d.gain), NULL::uuid, NULL::uuid
      FROM asset_disposals d JOIN fixed_assets a ON a.id = d.asset_id JOIN asset_classes c ON c.id = a.class_id WHERE p_doc = 'FA_DISP' AND d.id = p_id AND d.status = 'APPROVED' AND d.tenant_id = current_tenant())
  SELECT account_id, side, amount, project_id, cost_code_id FROM e WHERE amount > 0
$$;
CREATE FUNCTION fa_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(string_agg(concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')),
                             ';' ORDER BY concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')) COLLATE "C"), '')
    FROM fa_expected_lines(p_doc, p_id)
$$;

CREATE FUNCTION journals_fa_binding() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE bound boolean;
BEGIN
  bound := CASE NEW.source_type
           WHEN 'FA_CAP' THEN EXISTS (SELECT 1 FROM fixed_assets WHERE id::text = NEW.source_ref AND cap_journal_id = NEW.id)
           WHEN 'FA_DEP' THEN EXISTS (SELECT 1 FROM depreciation_runs WHERE id::text = NEW.source_ref AND journal_id = NEW.id)
           WHEN 'FA_DISP' THEN EXISTS (SELECT 1 FROM asset_disposals WHERE id::text = NEW.source_ref AND journal_id = NEW.id)
           ELSE false END;
  IF NOT bound THEN
    RAISE EXCEPTION 'journal % uses reserved source type % but no asset document references it', NEW.id, NEW.source_type USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER journals_fa_binding AFTER INSERT ON journals DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.source_type LIKE 'FA\_%') EXECUTE FUNCTION journals_fa_binding();

CREATE FUNCTION journals_fa_rebind() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE want text; got text;
BEGIN
  IF NEW.source_ref !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RAISE EXCEPTION 'subledger journal % has no document reference', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.source_type = 'FA_DEP' AND EXISTS (SELECT 1 FROM depreciation_runs WHERE id = NEW.source_ref::uuid AND status = 'CANCELLED') THEN
    RAISE EXCEPTION 'the depreciation run of journal % was cancelled', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  want := fa_expected_text(NEW.source_type, NEW.source_ref::uuid);
  got := p2p_lines_text(NEW.id);
  IF want IS NULL OR want = '' OR want IS DISTINCT FROM got THEN
    RAISE EXCEPTION 'subledger journal % no longer equals the accounting of % % (expected %, got %)', NEW.id, NEW.source_type, NEW.source_ref, want, got
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER journals_fa_rebind BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.source_type LIKE 'FA\_%' AND NEW.status IN ('APPROVED', 'POSTED') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION journals_fa_rebind();


-- Subledger entry points and protections (verbatim from 0055, FA added; an FA_DEP draft is cancelled with its run).
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
$$;
CREATE OR REPLACE FUNCTION subledger_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE WHEN p_doc LIKE 'P2P\_%' THEN p2p_expected_text(p_doc, p_id) WHEN p_doc LIKE 'AR\_%' THEN ar_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'STK\_%' THEN stk_expected_text(p_doc, p_id) WHEN p_doc LIKE 'REV\_%' THEN rev_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'SUB\_%' THEN sub_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'FA\_%' THEN fa_expected_text(p_doc, p_id) ELSE '' END
$$;
CREATE OR REPLACE FUNCTION journals_subledger_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.source_type ~ '^(P2P|AR|STK|REV|SUB|FA)_' AND TG_OP = 'UPDATE' AND NEW.status = 'CANCELLED' AND OLD.status <> 'CANCELLED' THEN
    IF NEW.source_type = 'FA_DEP' AND EXISTS (SELECT 1 FROM depreciation_runs r WHERE r.id::text = NEW.source_ref AND r.journal_id = NEW.id AND r.status = 'CANCELLED') THEN
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
  IF j.source_type ~ '^(P2P|AR|STK|REV|SUB|FA)_' AND j.created_txid IS DISTINCT FROM txid_current() THEN
    RAISE EXCEPTION 'lines of subledger journal % are written only by the transaction that generated it', jid USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END$$;

CREATE OR REPLACE FUNCTION journals_reserved_idempotency() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF lower(NEW.idempotency_key) LIKE 'p2p:%'
     AND (NEW.source_type !~ '^(P2P|AR|STK|REV|SUB|FA)_' OR NEW.idempotency_key IS DISTINCT FROM 'p2p:' || NEW.source_type || ':' || NEW.source_ref) THEN
    RAISE EXCEPTION 'idempotency keys beginning "p2p:" are reserved to the subledger journal of the named document' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

DROP TRIGGER journals_subledger_no_reversal ON journals;
CREATE TRIGGER journals_subledger_no_reversal BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.status = 'REVERSED' AND OLD.status <> 'REVERSED' AND NEW.source_type ~ '^(P2P|AR|STK|REV|SUB|FA)_')
  EXECUTE FUNCTION journals_subledger_no_reversal();

-- ---------------------------------------- shared functions (verbatim + 0057)
-- p2p_subject_text: verbatim from 0056 plus fixed assets and disposals (marked 0057).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0056; assets are entity-level (marked 0057).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy', 'fixed_asset', 'asset_disposal') THEN
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
  FOREACH tbl IN ARRAY ARRAY['asset_classes', 'fixed_assets', 'depreciation_runs', 'depreciation_lines', 'asset_disposals'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, approved_by, approved_at, decision_reason, cap_journal_id) ON fixed_assets TO serk_app;
GRANT UPDATE (status, journal_id, cancel_reason) ON depreciation_runs TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason, journal_id) ON asset_disposals TO serk_app;
REVOKE ALL ON FUNCTION fa_accumulated(uuid), fa_depreciation_compute(uuid, date), fa_expected_lines(text, uuid), fa_expected_text(text, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION fa_accumulated(uuid), fa_depreciation_compute(uuid, date), fa_expected_lines(text, uuid), fa_expected_text(text, uuid) TO serk_app;
