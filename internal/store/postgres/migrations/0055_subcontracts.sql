-- 0055 Subcontracts: schedule of values, applications, certificates (WP-2.5 part 1;
-- SRS SUB-01..05, PCC-02)
--
-- WHAT THIS MIGRATION DOES
--   * SUBCONTRACT POLICY (per legal entity): the subcontractor payable,
--     retention payable, advance to subcontractors, withholding tax payable and
--     back-charge recovery accounts. Proposed by one person, approved by
--     another (attested).
--   * SUBCONTRACT: a subcontractor (an ACTIVE supplier), a project, a schedule
--     of values (lines: quantity x rate on a cost code of the project),
--     retention % and cap, advance and its recovery %, withholding rate. Prepared
--     by one person, approved by another (fresh MFA, attested); approval is
--     refused when a line exceeds the available budget of its cost code. An
--     approved subcontract COMMITS its value (or what was certified, if more)
--     in p2p_cost_position, so budgets, the cost report and purchase order
--     budget checks all see it.
--   * BACK-CHARGES: costs the contractor recovers from the subcontractor;
--     approved by another person, deducted once by the next certificate.
--   * APPLICATION AND CERTIFICATE: the subcontractor's cumulative claim per line
--     (entered by a quantity surveyor), then certification by another person
--     (fresh MFA, attested) with the certified cumulative quantity per line. The
--     database computes this period's gross, retention (never above the cap),
--     advance recovery (never above what is outstanding), back-charges,
--     withholding and the net payable, and refuses a negative period or net.
--   * SUB_CERT JOURNAL: Dr cost per cost code / Cr payable, retention, advance,
--     withholding, back-charge recovery; bound to the certificate like every
--     subledger journal.
--
-- SHARED FUNCTIONS REDEFINED HERE:
--   p2p_cost_position (verbatim from 0039 plus subcontract commitments; SUB_CERT
--   journals are inside the commitment, like P2P_GRN), subledger_expected_lines /
--   subledger_expected_text, journals_subledger_guard, journal_lines_p2p_birth,
--   journals_reserved_idempotency, trigger journals_subledger_no_reversal
--   (verbatim from 0053, prefix SUB added), p2p_subject_text / serk_subject_project
--   (verbatim from 0053 plus the 0055 documents).

CREATE TABLE subcontract_policies (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id       uuid NOT NULL,
  version               integer NOT NULL CHECK (version >= 1),
  payable_account_id    uuid NOT NULL,
  retention_account_id  uuid NOT NULL,
  advance_account_id    uuid NOT NULL,
  wht_account_id        uuid NOT NULL,
  backcharge_account_id uuid NOT NULL,
  status                text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'APPROVED', 'REJECTED', 'RETIRED')),
  created_by            text NOT NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  approved_by           text,
  approved_at           timestamptz,
  decision_reason       text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, version),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id),
  CONSTRAINT subcontract_policy_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE UNIQUE INDEX subcontract_policies_one_approved ON subcontract_policies (tenant_id, legal_entity_id) WHERE status = 'APPROVED';
CREATE UNIQUE INDEX subcontract_policies_one_draft ON subcontract_policies (tenant_id, legal_entity_id) WHERE status = 'DRAFT';

CREATE FUNCTION subcontract_policies_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE ok boolean;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'subcontract policies are proposed as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    SELECT bool_and(a.found) INTO ok FROM (VALUES
      (EXISTS (SELECT 1 FROM accounts WHERE id = NEW.payable_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'LIABILITY' AND active)),
      (EXISTS (SELECT 1 FROM accounts WHERE id = NEW.retention_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'LIABILITY' AND active)),
      (EXISTS (SELECT 1 FROM accounts WHERE id = NEW.advance_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'ASSET' AND active)),
      (EXISTS (SELECT 1 FROM accounts WHERE id = NEW.wht_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'LIABILITY' AND active)),
      (EXISTS (SELECT 1 FROM accounts WHERE id = NEW.backcharge_account_id AND legal_entity_id = NEW.legal_entity_id AND type IN ('EXPENSE', 'REVENUE') AND active))) a(found);
    IF NOT ok THEN
      RAISE EXCEPTION 'subcontract policy accounts: payable, retention and withholding are LIABILITY, advance is ASSET, back-charge is EXPENSE or REVENUE, all active accounts of the entity'
        USING ERRCODE = 'check_violation';
    END IF;
    IF cardinality(ARRAY(SELECT DISTINCT x FROM unnest(ARRAY[NEW.payable_account_id, NEW.retention_account_id, NEW.advance_account_id, NEW.wht_account_id, NEW.backcharge_account_id]) x)) <> 5 THEN
      RAISE EXCEPTION 'the five subcontract accounts must all differ' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'RETIRED') OR (OLD.status = 'APPROVED' AND NEW.status <> 'RETIRED') THEN
    RAISE EXCEPTION 'a decided subcontract policy never changes (an approved one is only retired by its successor)' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'subcontract policy');
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'subcontract policy');
  IF NEW.status NOT IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'illegal subcontract policy transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
    RAISE EXCEPTION 'rejecting a subcontract policy requires a reason' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('subpolicy.decide', 'subcontract_policy', NEW.id::text, NEW.approved_by, ARRAY['contract.policy.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
  NEW.approved_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER subcontract_policies_guard BEFORE INSERT OR UPDATE ON subcontract_policies FOR EACH ROW EXECUTE FUNCTION subcontract_policies_guard();

CREATE TABLE subcontracts (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id      uuid NOT NULL,
  project_id           uuid NOT NULL,
  supplier_id          uuid NOT NULL,
  number               text NOT NULL,
  title                text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 200),
  currency             char(3) NOT NULL REFERENCES currencies(code),
  retention_pct        numeric(6,4) NOT NULL DEFAULT 0 CHECK (retention_pct >= 0 AND retention_pct <= 0.2),
  retention_cap        numeric(24,6) NOT NULL DEFAULT 0 CHECK (retention_cap >= 0),
  advance_amount       numeric(24,6) NOT NULL DEFAULT 0 CHECK (advance_amount >= 0),
  advance_recovery_pct numeric(6,4) NOT NULL DEFAULT 0 CHECK (advance_recovery_pct >= 0 AND advance_recovery_pct <= 1),
  wht_rate             numeric(6,4) NOT NULL DEFAULT 0 CHECK (wht_rate >= 0 AND wht_rate <= 0.3),
  pay_when_certified   boolean NOT NULL DEFAULT false,
  status               text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CLOSED')),
  created_by           text NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  approved_by          text,
  approved_at          timestamptz,
  decision_reason      text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, legal_entity_id, project_id) REFERENCES projects(tenant_id, legal_entity_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES suppliers(tenant_id, id),
  CONSTRAINT subcontract_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by),
  CONSTRAINT subcontract_advance_recovered CHECK (advance_amount = 0 OR advance_recovery_pct > 0)
);

CREATE TABLE subcontract_lines (
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  subcontract_id uuid NOT NULL,
  line_no        integer NOT NULL CHECK (line_no BETWEEN 1 AND 999),
  description    text NOT NULL CHECK (length(btrim(description)) BETWEEN 2 AND 300),
  unit           text NOT NULL CHECK (length(btrim(unit)) BETWEEN 1 AND 20),
  quantity       numeric(24,6) NOT NULL CHECK (quantity > 0),
  rate           numeric(24,6) NOT NULL CHECK (rate > 0),
  amount         numeric(24,6) NOT NULL,
  cost_code_id   uuid NOT NULL,
  account_id     uuid NOT NULL,
  PRIMARY KEY (tenant_id, subcontract_id, line_no),
  FOREIGN KEY (tenant_id, subcontract_id) REFERENCES subcontracts(tenant_id, id),
  FOREIGN KEY (tenant_id, cost_code_id) REFERENCES cost_codes(tenant_id, id)
);

CREATE FUNCTION subcontract_value(p_sub uuid) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(sum(amount), 0) FROM subcontract_lines WHERE subcontract_id = p_sub AND tenant_id = current_tenant()
$$;

CREATE FUNCTION subcontract_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE s subcontracts;
BEGIN
  SELECT * INTO s FROM subcontracts WHERE id = NEW.subcontract_id AND tenant_id = current_tenant();
  IF s.status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'the schedule of values is set while the subcontract is a draft' USING ERRCODE = 'check_violation';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM cost_codes WHERE id = NEW.cost_code_id AND project_id = s.project_id AND tenant_id = current_tenant()) THEN
    RAISE EXCEPTION 'line % is charged to a cost code of another project', NEW.line_no USING ERRCODE = 'check_violation';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.account_id AND legal_entity_id = s.legal_entity_id AND type = 'EXPENSE' AND active AND tenant_id = current_tenant()) THEN
    RAISE EXCEPTION 'line % must be charged to an active EXPENSE account of the entity', NEW.line_no USING ERRCODE = 'check_violation';
  END IF;
  NEW.amount := p2p_round(NEW.quantity * NEW.rate, s.currency);
  RETURN NEW;
END$$;
CREATE TRIGGER subcontract_lines_guard BEFORE INSERT ON subcontract_lines FOR EACH ROW EXECUTE FUNCTION subcontract_lines_guard();

CREATE FUNCTION subcontracts_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE bad record; val numeric;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'subcontracts are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM suppliers WHERE id = NEW.supplier_id AND tenant_id = current_tenant() AND status = 'ACTIVE' AND legal_entity_id = NEW.legal_entity_id) THEN
      RAISE EXCEPTION 'the subcontractor must be an approved (ACTIVE) supplier of the project''s entity' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'CLOSED') THEN
    RAISE EXCEPTION 'a % subcontract never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'subcontract');
    val := subcontract_value(NEW.id);
    IF val = 0 THEN
      RAISE EXCEPTION 'a subcontract needs a schedule of values' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.advance_amount > val OR NEW.retention_cap > val THEN
      RAISE EXCEPTION 'the advance and the retention cap cannot exceed the subcontract value (%)', val USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('subcontract.submit', 'subcontract', NEW.id::text, NEW.created_by, ARRAY['subcontract.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'subcontract');
    IF NEW.status = 'APPROVED' THEN
      -- Budget control: each cost code must have room for the subcontract.
      SELECT c.code, sum(l.amount) AS need, (p2p_cost_position(l.cost_code_id, NULL, NULL)).available AS room INTO bad
        FROM subcontract_lines l JOIN cost_codes c ON c.id = l.cost_code_id WHERE l.subcontract_id = NEW.id
       GROUP BY c.code, l.cost_code_id HAVING sum(l.amount) > (p2p_cost_position(l.cost_code_id, NULL, NULL)).available LIMIT 1;
      IF bad.code IS NOT NULL THEN
        RAISE EXCEPTION 'cost code % has % available, less than the % this subcontract commits', bad.code, bad.room, bad.need USING ERRCODE = 'check_violation';
      END IF;
    ELSIF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a subcontract requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('subcontract.decide', 'subcontract', NEW.id::text, NEW.approved_by, ARRAY['subcontract.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal subcontract transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER subcontracts_guard BEFORE INSERT OR UPDATE ON subcontracts FOR EACH ROW EXECUTE FUNCTION subcontracts_guard();

CREATE TABLE subcontract_backcharges (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  subcontract_id  uuid NOT NULL,
  number          text NOT NULL,
  reason          text NOT NULL CHECK (length(btrim(reason)) BETWEEN 5 AND 500),
  evidence_ref    text NOT NULL CHECK (length(btrim(evidence_ref)) BETWEEN 2 AND 200),
  amount          numeric(24,6) NOT NULL CHECK (amount > 0),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  deducted_in     uuid,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, subcontract_id) REFERENCES subcontracts(tenant_id, id),
  CONSTRAINT backcharge_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by),
  CONSTRAINT backcharge_deducted_when_approved CHECK (deducted_in IS NULL OR status = 'APPROVED')
);

CREATE FUNCTION subcontract_backcharges_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.deducted_in IS NOT NULL THEN
      RAISE EXCEPTION 'back-charges are raised as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM subcontracts WHERE id = NEW.subcontract_id AND tenant_id = current_tenant() AND status = 'APPROVED') THEN
      RAISE EXCEPTION 'back-charges are raised on an APPROVED subcontract' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'APPROVED' THEN
    -- Only deduction, once, by a certificate of the same subcontract.
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['deducted_in'], 'back-charge');
    IF OLD.deducted_in IS NOT NULL OR NOT EXISTS (SELECT 1 FROM subcontract_applications a WHERE a.id = NEW.deducted_in AND a.subcontract_id = NEW.subcontract_id) THEN
      RAISE EXCEPTION 'a back-charge is deducted once, by a certificate of its subcontract' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'REJECTED' THEN
    RAISE EXCEPTION 'a rejected back-charge never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'back-charge');
    PERFORM approval_use('backcharge.submit', 'subcontract_backcharge', NEW.id::text, NEW.created_by, ARRAY['subcontract.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'back-charge');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a back-charge requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('backcharge.decide', 'subcontract_backcharge', NEW.id::text, NEW.approved_by, ARRAY['subcontract.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal back-charge transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER subcontract_backcharges_guard BEFORE INSERT OR UPDATE ON subcontract_backcharges FOR EACH ROW EXECUTE FUNCTION subcontract_backcharges_guard();

CREATE TABLE subcontract_applications (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  subcontract_id     uuid NOT NULL,
  number             text NOT NULL,
  period_to          date NOT NULL,
  status             text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'CERTIFIED', 'REJECTED')),
  created_by         text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  certified_by       text,
  certified_at       timestamptz,
  decision_reason    text,
  -- The certificate, computed by the database at certification.
  gross_cumulative   numeric(24,6),
  previous_certified numeric(24,6),
  this_gross         numeric(24,6),
  retention          numeric(24,6),
  advance_recovery   numeric(24,6),
  backcharges        numeric(24,6),
  wht                numeric(24,6),
  net_payable        numeric(24,6),
  journal_id         uuid,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, subcontract_id) REFERENCES subcontracts(tenant_id, id),
  CONSTRAINT subapp_maker_checker CHECK (certified_by IS NULL OR certified_by <> created_by),
  CONSTRAINT subapp_certificate_whole CHECK (status <> 'CERTIFIED' OR (net_payable IS NOT NULL AND this_gross = retention + advance_recovery + backcharges + wht + net_payable))
);
CREATE UNIQUE INDEX subcontract_applications_one_open ON subcontract_applications (tenant_id, subcontract_id) WHERE status IN ('DRAFT', 'SUBMITTED');
ALTER TABLE subcontract_backcharges ADD FOREIGN KEY (tenant_id, deducted_in) REFERENCES subcontract_applications(tenant_id, id);

CREATE TABLE subcontract_application_lines (
  tenant_id         uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  application_id    uuid NOT NULL,
  line_no           integer NOT NULL,
  claimed_cum_qty   numeric(24,6) NOT NULL CHECK (claimed_cum_qty >= 0),
  certified_cum_qty numeric(24,6) CHECK (certified_cum_qty >= 0),
  PRIMARY KEY (tenant_id, application_id, line_no),
  FOREIGN KEY (tenant_id, application_id) REFERENCES subcontract_applications(tenant_id, id)
);

-- Certified cumulative quantity of a line before an application.
CREATE FUNCTION subcontract_prev_qty(p_sub uuid, p_line integer, p_before uuid) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE((SELECT l.certified_cum_qty FROM subcontract_application_lines l JOIN subcontract_applications a ON a.id = l.application_id
                    WHERE a.subcontract_id = p_sub AND a.status = 'CERTIFIED' AND a.id IS DISTINCT FROM p_before AND l.line_no = p_line AND a.tenant_id = current_tenant()
                    ORDER BY a.period_to DESC, a.certified_at DESC LIMIT 1), 0)
$$;

-- Certified value to date of a subcontract (for the commitment).
CREATE FUNCTION subcontract_certified_value(p_sub uuid, p_line integer) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT p2p_round(subcontract_prev_qty(p_sub, p_line, NULL) * l.rate, s.currency)
    FROM subcontract_lines l JOIN subcontracts s ON s.id = l.subcontract_id WHERE l.subcontract_id = p_sub AND l.line_no = p_line
$$;

CREATE FUNCTION subcontract_application_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE a subcontract_applications; l subcontract_lines;
BEGIN
  SELECT * INTO a FROM subcontract_applications WHERE id = NEW.application_id AND tenant_id = current_tenant();
  SELECT * INTO l FROM subcontract_lines WHERE subcontract_id = a.subcontract_id AND line_no = NEW.line_no AND tenant_id = current_tenant();
  IF l.line_no IS NULL THEN
    RAISE EXCEPTION 'line % is not in the schedule of values', NEW.line_no USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'INSERT' THEN
    IF a.status <> 'DRAFT' OR NEW.certified_cum_qty IS NOT NULL THEN
      RAISE EXCEPTION 'the claim is entered while the application is a draft' USING ERRCODE = 'check_violation';
    END IF;
  ELSE
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['certified_cum_qty'], 'application line');
    IF a.status <> 'SUBMITTED' THEN
      RAISE EXCEPTION 'certified quantities are entered while the application is submitted' USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  IF greatest(NEW.claimed_cum_qty, COALESCE(NEW.certified_cum_qty, 0)) > l.quantity THEN
    RAISE EXCEPTION 'line % : a cumulative quantity above the subcontract quantity (%)', NEW.line_no, l.quantity USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER subcontract_application_lines_guard BEFORE INSERT OR UPDATE ON subcontract_application_lines FOR EACH ROW EXECUTE FUNCTION subcontract_application_lines_guard();

CREATE FUNCTION subcontract_applications_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE s subcontracts; retained numeric; recovered numeric; bc numeric; t numeric;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.certified_by IS NOT NULL OR NEW.net_payable IS NOT NULL OR NEW.journal_id IS NOT NULL THEN
      RAISE EXCEPTION 'applications are entered as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM subcontracts WHERE id = NEW.subcontract_id AND tenant_id = current_tenant() AND status = 'APPROVED') THEN
      RAISE EXCEPTION 'applications are made under an APPROVED subcontract' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.period_to > current_date THEN
      RAISE EXCEPTION 'an application cannot be for a future period' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM subcontract_applications WHERE subcontract_id = NEW.subcontract_id AND status = 'CERTIFIED' AND period_to >= NEW.period_to) THEN
      RAISE EXCEPTION 'a later or equal period is already certified' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'REJECTED' THEN
    RAISE EXCEPTION 'a rejected application never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'CERTIFIED' THEN
    -- Binding the generated journal, once.
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['journal_id'], 'certificate');
    IF OLD.journal_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.journal_id AND source_type = 'SUB_CERT' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'a certificate is bound once to its own DRAFT SUB_CERT journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'application');
    IF NOT EXISTS (SELECT 1 FROM subcontract_application_lines WHERE application_id = NEW.id) THEN
      RAISE EXCEPTION 'an application needs at least one line' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('subapp.submit', 'subcontract_application', NEW.id::text, NEW.created_by, ARRAY['subcontract.apply'], false, NULL, NULL);
    RETURN NEW;
  END IF;
  IF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'certified_by', 'certified_at', 'decision_reason'], 'application');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting an application requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('subapp.certify', 'subcontract_application', NEW.id::text, NEW.certified_by, ARRAY['subcontract.certify'], false, 'REJECTED', NULL);
    NEW.certified_at := now();
    RETURN NEW;
  END IF;
  IF NOT (OLD.status = 'SUBMITTED' AND NEW.status = 'CERTIFIED') THEN
    RAISE EXCEPTION 'illegal application transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'certified_by', 'certified_at', 'decision_reason',
    'gross_cumulative', 'previous_certified', 'this_gross', 'retention', 'advance_recovery', 'backcharges', 'wht', 'net_payable'], 'application');
  IF EXISTS (SELECT 1 FROM subcontract_lines sl WHERE sl.subcontract_id = NEW.subcontract_id
               AND NOT EXISTS (SELECT 1 FROM subcontract_application_lines l WHERE l.application_id = NEW.id AND l.line_no = sl.line_no AND l.certified_cum_qty IS NOT NULL)
               AND subcontract_prev_qty(NEW.subcontract_id, sl.line_no, NULL) > 0) THEN
    RAISE EXCEPTION 'every line certified before must be certified again (cumulative)' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('subapp.certify', 'subcontract_application', NEW.id::text, NEW.certified_by, ARRAY['subcontract.certify'], true, 'CERTIFIED', NULL);
  SELECT * INTO s FROM subcontracts WHERE id = NEW.subcontract_id FOR UPDATE;
  -- Every figure is the database's.
  SELECT COALESCE(sum(p2p_round(COALESCE(l.certified_cum_qty, subcontract_prev_qty(s.id, sl.line_no, NEW.id)) * sl.rate, s.currency)), 0)
    INTO NEW.gross_cumulative FROM subcontract_lines sl LEFT JOIN subcontract_application_lines l ON l.application_id = NEW.id AND l.line_no = sl.line_no
   WHERE sl.subcontract_id = s.id;
  SELECT COALESCE(sum(this_gross), 0), COALESCE(sum(retention), 0), COALESCE(sum(advance_recovery), 0) INTO NEW.previous_certified, retained, recovered
    FROM subcontract_applications WHERE subcontract_id = s.id AND status = 'CERTIFIED' AND id <> NEW.id;
  t := NEW.gross_cumulative - NEW.previous_certified;
  IF t < 0 THEN
    RAISE EXCEPTION 'the certified value went down by %: correct it through a back-charge, not a negative certificate', -t USING ERRCODE = 'check_violation';
  END IF;
  NEW.this_gross := t;
  NEW.retention := greatest(0, least(p2p_round(t * s.retention_pct, s.currency), CASE WHEN s.retention_cap > 0 THEN s.retention_cap - retained ELSE p2p_round(t * s.retention_pct, s.currency) END));
  NEW.advance_recovery := greatest(0, least(p2p_round(t * s.advance_recovery_pct, s.currency), s.advance_amount - recovered, t - NEW.retention));
  SELECT COALESCE(sum(amount), 0) INTO bc FROM subcontract_backcharges WHERE subcontract_id = s.id AND status = 'APPROVED' AND deducted_in IS NULL;
  NEW.backcharges := bc;
  NEW.wht := p2p_round(t * s.wht_rate, s.currency);
  NEW.net_payable := t - NEW.retention - NEW.advance_recovery - NEW.backcharges - NEW.wht;
  IF NEW.net_payable < 0 THEN
    RAISE EXCEPTION 'the deductions (%) exceed this period''s gross (%): certify more work or defer a back-charge', t - NEW.net_payable, t USING ERRCODE = 'check_violation';
  END IF;
  UPDATE subcontract_backcharges SET deducted_in = NEW.id WHERE subcontract_id = s.id AND status = 'APPROVED' AND deducted_in IS NULL;
  NEW.certified_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER subcontract_applications_guard BEFORE INSERT OR UPDATE ON subcontract_applications FOR EACH ROW EXECUTE FUNCTION subcontract_applications_guard();

-- The accounting of a certificate.
CREATE FUNCTION sub_expected_lines(p_doc text, p_id uuid)
RETURNS TABLE (account_id uuid, side text, amount numeric, project_id uuid, cost_code_id uuid)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  WITH a AS (SELECT x.*, s.project_id, s.currency, s.legal_entity_id FROM subcontract_applications x JOIN subcontracts s ON s.id = x.subcontract_id
              WHERE p_doc = 'SUB_CERT' AND x.id = p_id AND x.tenant_id = current_tenant() AND x.status = 'CERTIFIED'),
  pol AS (SELECT p.* FROM subcontract_policies p JOIN a ON p.legal_entity_id = a.legal_entity_id WHERE p.status = 'APPROVED'),
  e(account_id, side, amount, project_id, cost_code_id) AS (
    -- Dr the cost of the work certified this period, per cost code.
    SELECT sl.account_id, 'DEBIT',
           sum(p2p_round(COALESCE(l.certified_cum_qty, subcontract_prev_qty(a.subcontract_id, sl.line_no, a.id)) * sl.rate, a.currency)
               - p2p_round(subcontract_prev_qty(a.subcontract_id, sl.line_no, a.id) * sl.rate, a.currency)), a.project_id, sl.cost_code_id
      FROM a JOIN subcontract_lines sl ON sl.subcontract_id = a.subcontract_id
      LEFT JOIN subcontract_application_lines l ON l.application_id = a.id AND l.line_no = sl.line_no
     GROUP BY sl.account_id, sl.cost_code_id, a.project_id
    UNION ALL SELECT pol.payable_account_id, 'CREDIT', a.net_payable, NULL::uuid, NULL::uuid FROM a, pol
    UNION ALL SELECT pol.retention_account_id, 'CREDIT', a.retention, NULL::uuid, NULL::uuid FROM a, pol
    UNION ALL SELECT pol.advance_account_id, 'CREDIT', a.advance_recovery, NULL::uuid, NULL::uuid FROM a, pol
    UNION ALL SELECT pol.wht_account_id, 'CREDIT', a.wht, NULL::uuid, NULL::uuid FROM a, pol
    UNION ALL SELECT pol.backcharge_account_id, 'CREDIT', a.backcharges, a.project_id, NULL::uuid FROM a, pol)
  SELECT account_id, side, amount, project_id, cost_code_id FROM e WHERE amount > 0
$$;
CREATE FUNCTION sub_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(string_agg(concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')),
                             ';' ORDER BY concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')) COLLATE "C"), '')
    FROM sub_expected_lines(p_doc, p_id)
$$;

-- Commitments (verbatim from 0039 plus subcontracts, marked 0055).
CREATE OR REPLACE FUNCTION p2p_cost_position(p_cost_code uuid, p_exclude_pr uuid, p_exclude_po uuid,
  OUT budget numeric, OUT committed numeric, OUT reserved numeric, OUT actual_other numeric, OUT available numeric)
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE put_away numeric; issued numeric;
BEGIN
  -- 0039: the budget is the current budget (original + approved revisions +
  -- transfers and drawdowns), or the approved BOQ for a project without budget versions.
  SELECT COALESCE((SELECT pbc.current FROM project_budget_current pbc WHERE pbc.cost_code_id = p_cost_code), 0) INTO budget;
  -- Commitment of a line: the order, or what was received if more.
  SELECT COALESCE(sum(greatest(l.amount, p2p_round(p2p_received_qty(l.po_id, l.line_no) * l.unit_price, o.currency))), 0),
         COALESCE(sum(CASE WHEN EXISTS (SELECT 1 FROM stock_items si WHERE si.inventory_account_id = l.account_id)
                           THEN p2p_put_away_value(l.po_id, l.line_no) ELSE 0 END), 0)
    INTO committed, put_away
    FROM po_lines l JOIN purchase_orders o ON o.id = l.po_id
   WHERE l.tenant_id = current_tenant() AND l.cost_code_id = p_cost_code AND o.status IN ('APPROVED', 'CLOSED')
     AND o.id IS DISTINCT FROM p_exclude_po;
  -- 0055: approved subcontracts commit their value, or what was certified if more.
  committed := committed + COALESCE((SELECT sum(greatest(l.amount, subcontract_certified_value(l.subcontract_id, l.line_no)))
    FROM subcontract_lines l JOIN subcontracts s ON s.id = l.subcontract_id
   WHERE l.tenant_id = current_tenant() AND l.cost_code_id = p_cost_code AND s.status IN ('APPROVED', 'CLOSED')), 0);
  SELECT COALESCE(sum(l.est_amount), 0) INTO reserved
    FROM pr_lines l JOIN purchase_requisitions r ON r.id = l.pr_id
   WHERE l.tenant_id = current_tenant() AND l.cost_code_id = p_cost_code AND r.status = 'APPROVED'
     AND r.id IS DISTINCT FROM p_exclude_pr
     AND NOT EXISTS (SELECT 1 FROM rfqs q JOIN purchase_orders o ON o.rfq_id = q.id
                      WHERE q.pr_id = r.id AND o.status IN ('APPROVED', 'CLOSED'));
  -- Posted cost charged to the code (receipt journals are inside the
  -- commitment above).
  SELECT COALESCE(sum(CASE WHEN jl.side = 'DEBIT' THEN jl.amount ELSE -jl.amount END), 0),
         COALESCE(sum(CASE WHEN j.source_type = 'STK_ISSUE' THEN CASE WHEN jl.side = 'DEBIT' THEN jl.amount ELSE -jl.amount END ELSE 0 END), 0)
    INTO actual_other, issued
    FROM journal_lines jl JOIN journals j ON j.id = jl.journal_id
   WHERE jl.tenant_id = current_tenant() AND jl.cost_code_id = p_cost_code AND j.status IN ('POSTED', 'REVERSED')
     AND j.source_type NOT IN ('P2P_GRN', 'SUB_CERT'); -- 0055: certificates are inside the subcontract commitment
  -- Stock issued to the code consumes the code's own inventory commitment
  -- first, but never more than the code's orders put away: the code bears
  -- commitment + max(0, issued - put away).
  committed := committed - least(greatest(issued, 0), put_away);
  available := budget - committed - reserved - actual_other;
END$$;

-- Subledger entry points and protections (verbatim from 0053, SUB added).
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
$$;
CREATE OR REPLACE FUNCTION subledger_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE WHEN p_doc LIKE 'P2P\_%' THEN p2p_expected_text(p_doc, p_id) WHEN p_doc LIKE 'AR\_%' THEN ar_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'STK\_%' THEN stk_expected_text(p_doc, p_id) WHEN p_doc LIKE 'REV\_%' THEN rev_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'SUB\_%' THEN sub_expected_text(p_doc, p_id) ELSE '' END
$$;
CREATE OR REPLACE FUNCTION journals_subledger_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.source_type ~ '^(P2P|AR|STK|REV|SUB)_' AND TG_OP = 'UPDATE' AND NEW.status = 'CANCELLED' AND OLD.status <> 'CANCELLED' THEN
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
  IF j.source_type ~ '^(P2P|AR|STK|REV|SUB)_' AND j.created_txid IS DISTINCT FROM txid_current() THEN
    RAISE EXCEPTION 'lines of subledger journal % are written only by the transaction that generated it', jid USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END$$;

CREATE OR REPLACE FUNCTION journals_reserved_idempotency() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF lower(NEW.idempotency_key) LIKE 'p2p:%'
     AND (NEW.source_type !~ '^(P2P|AR|STK|REV|SUB)_' OR NEW.idempotency_key IS DISTINCT FROM 'p2p:' || NEW.source_type || ':' || NEW.source_ref) THEN
    RAISE EXCEPTION 'idempotency keys beginning "p2p:" are reserved to the subledger journal of the named document' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

DROP TRIGGER journals_subledger_no_reversal ON journals;
CREATE TRIGGER journals_subledger_no_reversal BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.status = 'REVERSED' AND OLD.status <> 'REVERSED' AND NEW.source_type ~ '^(P2P|AR|STK|REV|SUB)_')
  EXECUTE FUNCTION journals_subledger_no_reversal();

CREATE FUNCTION journals_sub_binding() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM subcontract_applications WHERE id::text = NEW.source_ref AND journal_id = NEW.id) THEN
    RAISE EXCEPTION 'journal % uses reserved source type % but no certificate references it', NEW.id, NEW.source_type USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER journals_sub_binding AFTER INSERT ON journals DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.source_type LIKE 'SUB\_%') EXECUTE FUNCTION journals_sub_binding();

CREATE FUNCTION journals_sub_rebind() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE want text; got text;
BEGIN
  IF NEW.source_ref !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RAISE EXCEPTION 'subledger journal % has no document reference', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  want := sub_expected_text(NEW.source_type, NEW.source_ref::uuid);
  got := p2p_lines_text(NEW.id);
  IF want IS NULL OR want = '' OR want IS DISTINCT FROM got THEN
    RAISE EXCEPTION 'subledger journal % no longer equals the accounting of % % (expected %, got %)', NEW.id, NEW.source_type, NEW.source_ref, want, got
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER journals_sub_rebind BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.source_type LIKE 'SUB\_%' AND NEW.status IN ('APPROVED', 'POSTED') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION journals_sub_rebind();

-- ---------------------------------------- shared functions (verbatim + 0055)
-- p2p_subject_text: verbatim from 0053 plus the subcontract documents (marked 0055).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0053 plus the subcontract documents (marked 0055).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy') THEN
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
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['subcontract_policies', 'subcontracts', 'subcontract_lines', 'subcontract_backcharges', 'subcontract_applications', 'subcontract_application_lines'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON subcontract_policies, subcontracts TO serk_app;
GRANT UPDATE (status, approved_by, approved_at, decision_reason, deducted_in) ON subcontract_backcharges TO serk_app;
GRANT UPDATE (status, certified_by, certified_at, decision_reason, journal_id) ON subcontract_applications TO serk_app;
GRANT UPDATE (certified_cum_qty) ON subcontract_application_lines TO serk_app;
REVOKE ALL ON FUNCTION subcontract_value(uuid), subcontract_prev_qty(uuid, integer, uuid), subcontract_certified_value(uuid, integer),
  sub_expected_lines(text, uuid), sub_expected_text(text, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION subcontract_value(uuid), subcontract_prev_qty(uuid, integer, uuid), subcontract_certified_value(uuid, integer),
  sub_expected_lines(text, uuid), sub_expected_text(text, uuid) TO serk_app;
