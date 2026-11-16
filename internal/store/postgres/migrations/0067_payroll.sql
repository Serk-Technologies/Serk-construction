-- 0061 Workforce and payroll (WP-2.8; SRS HCM-01, HCM-04, HCM-05, HCM-06)
--
-- WHAT THIS MIGRATION DOES
--   * PAYROLL POLICY (per legal entity): salary expense, employer pension
--     expense, net pay payable, income tax payable and pension payable
--     accounts; proposed by one person, approved by another (attested).
--   * EMPLOYEE: number, name, kind (SALARIED per month, DAILY, HOURLY), trade,
--     base rate, TIN, pension number; the bank account number is stored
--     encrypted with its keyed digest and last four digits only (like supplier
--     bank accounts).
--   * RULE SET: income tax bands (up to, rate, deduction: tax = gross x rate -
--     deduction in the band the gross falls into), employee and employer
--     pension rates on basic pay, overtime multiplier and hours per month;
--     effective from a date; prepared by one person and approved by another
--     (fresh MFA). The figures are the company's configuration (decision D-04:
--     the adviser-signed values), not a statement of law. An approved set never
--     changes; a pay run uses the set in force at its period end.
--   * PAY RUN: inputs per employee (units worked, overtime hours, taxable
--     allowances, project and cost code), then CALCULATED by the database (the
--     lines cannot be written by a client), SUBMITTED, APPROVED by another
--     person (payroll.approve, fresh MFA) which generates one PAY_RUN journal
--     per project section, and PAID by a third person (payment.release, fresh
--     MFA) which generates the PAY_PAY journal (Dr net pay payable / Cr bank
--     clearing). PAY_* journals are bound subledger journals.
--
-- SHARED FUNCTIONS REDEFINED HERE: the subledger entry points and protections
-- (verbatim from 0057, PAY added) and p2p_subject_text / serk_subject_project
-- (verbatim from 0057 plus the payroll documents).

CREATE TABLE payroll_policies (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id          uuid NOT NULL,
  version                  integer NOT NULL CHECK (version >= 1),
  salary_account_id        uuid NOT NULL,
  pension_expense_account_id uuid NOT NULL,
  net_payable_account_id   uuid NOT NULL,
  tax_payable_account_id   uuid NOT NULL,
  pension_payable_account_id uuid NOT NULL,
  status                   text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'APPROVED', 'REJECTED', 'RETIRED')),
  created_by               text NOT NULL,
  created_at               timestamptz NOT NULL DEFAULT now(),
  approved_by              text,
  approved_at              timestamptz,
  decision_reason          text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, version),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id),
  CONSTRAINT payroll_policy_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE UNIQUE INDEX payroll_policies_one_approved ON payroll_policies (tenant_id, legal_entity_id) WHERE status = 'APPROVED';
CREATE UNIQUE INDEX payroll_policies_one_draft ON payroll_policies (tenant_id, legal_entity_id) WHERE status = 'DRAFT';

CREATE FUNCTION payroll_policies_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'payroll policies are proposed as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.salary_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'EXPENSE' AND active)
       OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.pension_expense_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'EXPENSE' AND active)
       OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.net_payable_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'LIABILITY' AND active)
       OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.tax_payable_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'LIABILITY' AND active)
       OR NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.pension_payable_account_id AND legal_entity_id = NEW.legal_entity_id AND type = 'LIABILITY' AND active) THEN
      RAISE EXCEPTION 'payroll accounts: salary and employer pension are EXPENSE accounts; net pay, income tax and pension payable are LIABILITY accounts; all active accounts of the entity'
        USING ERRCODE = 'check_violation';
    END IF;
    IF cardinality(ARRAY(SELECT DISTINCT x FROM unnest(ARRAY[NEW.salary_account_id, NEW.pension_expense_account_id, NEW.net_payable_account_id, NEW.tax_payable_account_id, NEW.pension_payable_account_id]) x)) <> 5 THEN
      RAISE EXCEPTION 'the five payroll accounts must all differ' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'RETIRED') OR (OLD.status = 'APPROVED' AND NEW.status <> 'RETIRED') THEN
    RAISE EXCEPTION 'a decided payroll policy never changes (an approved one is only retired by its successor)' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'payroll policy');
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'payroll policy');
  IF NEW.status NOT IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'illegal payroll policy transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
    RAISE EXCEPTION 'rejecting a payroll policy requires a reason' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM approval_use('paypolicy.decide', 'payroll_policy', NEW.id::text, NEW.approved_by, ARRAY['contract.policy.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
  NEW.approved_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER payroll_policies_guard BEFORE INSERT OR UPDATE ON payroll_policies FOR EACH ROW EXECUTE FUNCTION payroll_policies_guard();

CREATE TABLE employees (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id    uuid NOT NULL,
  number             text NOT NULL CHECK (number ~ '^[A-Z0-9][A-Z0-9-]{1,30}$'),
  name               text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 200),
  kind               text NOT NULL CHECK (kind IN ('SALARIED', 'DAILY', 'HOURLY')),
  trade              text,
  base_rate          numeric(24,6) NOT NULL CHECK (base_rate > 0),
  tin                text CHECK (tin IS NULL OR tin ~ '^[0-9A-Z-]{4,20}$'),
  pension_no         text,
  bank_name          text,
  account_number_enc bytea,
  account_digest     text,
  account_last4      text CHECK (account_last4 IS NULL OR account_last4 ~ '^[A-Z0-9]{1,4}$'),
  starts_on          date NOT NULL,
  ends_on            date,
  status             text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'LEFT')),
  created_by         text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id),
  CONSTRAINT employee_bank_whole CHECK ((account_number_enc IS NULL) = (account_digest IS NULL) AND (account_digest IS NULL) = (account_last4 IS NULL)),
  CONSTRAINT employee_dates CHECK (ends_on IS NULL OR ends_on >= starts_on)
);

CREATE FUNCTION employees_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'ACTIVE' THEN
      RAISE EXCEPTION 'employees are registered ACTIVE' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'LEFT' THEN
    RAISE EXCEPTION 'a leaver''s record never changes' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'ends_on'], 'employee');
  IF NEW.status <> 'LEFT' OR NEW.ends_on IS NULL THEN
    RAISE EXCEPTION 'an employee record changes only by recording the leaving date' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER employees_guard BEFORE INSERT OR UPDATE ON employees FOR EACH ROW EXECUTE FUNCTION employees_guard();

CREATE TABLE payroll_rule_sets (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id  uuid NOT NULL,
  number           text NOT NULL,
  effective_from   date NOT NULL,
  tax_bands        jsonb NOT NULL,   -- [{"upto": 2000 | null, "rate": 0.15, "deduction": 300}, ...] ascending
  pension_ee_rate  numeric(6,4) NOT NULL CHECK (pension_ee_rate >= 0 AND pension_ee_rate < 1),
  pension_er_rate  numeric(6,4) NOT NULL CHECK (pension_er_rate >= 0 AND pension_er_rate < 1),
  overtime_multiplier numeric(6,3) NOT NULL DEFAULT 1.5 CHECK (overtime_multiplier >= 1 AND overtime_multiplier <= 4),
  hours_per_month  numeric(6,2) NOT NULL DEFAULT 208 CHECK (hours_per_month > 0),
  hours_per_day    numeric(5,2) NOT NULL DEFAULT 8 CHECK (hours_per_day > 0),
  source_ref       text NOT NULL CHECK (length(btrim(source_ref)) BETWEEN 3 AND 300),
  status           text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by       text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  approved_by      text,
  approved_at      timestamptz,
  decision_reason  text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id),
  CONSTRAINT rule_set_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE UNIQUE INDEX payroll_rule_sets_one ON payroll_rule_sets (tenant_id, legal_entity_id, effective_from) WHERE status IN ('DRAFT', 'SUBMITTED', 'APPROVED');

-- Tax bands are ascending, the last open-ended, rates between 0 and 1.
CREATE FUNCTION payroll_bands_valid(p jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE b jsonb; prev numeric := -1; n integer := 0; total integer;
BEGIN
  IF jsonb_typeof(p) <> 'array' THEN RETURN false; END IF;
  total := jsonb_array_length(p);
  IF total = 0 OR total > 20 THEN RETURN false; END IF;
  FOR b IN SELECT * FROM jsonb_array_elements(p) LOOP
    n := n + 1;
    IF jsonb_typeof(b->'rate') <> 'number' OR (b->>'rate')::numeric < 0 OR (b->>'rate')::numeric >= 1
       OR jsonb_typeof(b->'deduction') <> 'number' OR (b->>'deduction')::numeric < 0 THEN RETURN false; END IF;
    IF n < total THEN
      IF jsonb_typeof(b->'upto') <> 'number' OR (b->>'upto')::numeric <= prev THEN RETURN false; END IF;
      prev := (b->>'upto')::numeric;
    ELSIF jsonb_typeof(b->'upto') <> 'null' AND b ? 'upto' THEN
      RETURN false;
    END IF;
  END LOOP;
  RETURN true;
END$$;
ALTER TABLE payroll_rule_sets ADD CONSTRAINT payroll_rule_sets_bands CHECK (payroll_bands_valid(tax_bands));

-- Income tax of a monthly gross under a rule set.
CREATE FUNCTION payroll_tax(p_set uuid, p_gross numeric, p_ccy char(3)) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT greatest(0, p2p_round(p_gross * (b->>'rate')::numeric - (b->>'deduction')::numeric, p_ccy))
    FROM payroll_rule_sets r, jsonb_array_elements(r.tax_bands) WITH ORDINALITY AS t(b, i)
   WHERE r.id = p_set AND r.tenant_id = current_tenant() AND (jsonb_typeof(b->'upto') <> 'number' OR p_gross <= (b->>'upto')::numeric)
   ORDER BY i LIMIT 1
$$;

CREATE FUNCTION payroll_rule_sets_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL THEN
      RAISE EXCEPTION 'rule sets are prepared as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM pay_runs r WHERE r.legal_entity_id = NEW.legal_entity_id AND r.status IN ('APPROVED', 'PAID') AND r.period_end >= NEW.effective_from) THEN
      RAISE EXCEPTION 'a rule set cannot take effect inside a period already approved (retro-dating a posted pay run is refused)' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided rule set never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'rule set');
    PERFORM approval_use('payrules.submit', 'payroll_rule_set', NEW.id::text, NEW.created_by, ARRAY['payroll.prepare'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'rule set');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a rule set requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'APPROVED' AND EXISTS (SELECT 1 FROM pay_runs r WHERE r.legal_entity_id = NEW.legal_entity_id AND r.status IN ('APPROVED', 'PAID') AND r.period_end >= NEW.effective_from) THEN
      RAISE EXCEPTION 'a rule set cannot take effect inside a period already approved' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('payrules.decide', 'payroll_rule_set', NEW.id::text, NEW.approved_by, ARRAY['payroll.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal rule set transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

CREATE TABLE pay_runs (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id  uuid NOT NULL,
  number           text NOT NULL,
  period_start     date NOT NULL,
  period_end       date NOT NULL CHECK (period_end >= period_start),
  pay_date         date NOT NULL,
  rule_set_id      uuid,
  company_bank_id  uuid,
  status           text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'CALCULATED', 'SUBMITTED', 'APPROVED', 'REJECTED', 'PAID', 'CANCELLED')),
  created_by       text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  calculated_at    timestamptz,
  approved_by      text,
  approved_at      timestamptz,
  decision_reason  text,
  paid_by          text,
  paid_at          timestamptz,
  pay_journal_id   uuid UNIQUE,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id),
  FOREIGN KEY (tenant_id, rule_set_id) REFERENCES payroll_rule_sets(tenant_id, id),
  CONSTRAINT pay_run_three_people CHECK ((approved_by IS NULL OR approved_by <> created_by) AND (paid_by IS NULL OR (paid_by <> created_by AND paid_by <> approved_by)))
);
CREATE UNIQUE INDEX pay_runs_one_period ON pay_runs (tenant_id, legal_entity_id, period_start, period_end) WHERE status NOT IN ('REJECTED', 'CANCELLED');
CREATE TRIGGER payroll_rule_sets_guard BEFORE INSERT OR UPDATE ON payroll_rule_sets FOR EACH ROW EXECUTE FUNCTION payroll_rule_sets_guard();

CREATE TABLE pay_inputs (
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id         uuid NOT NULL,
  employee_id    uuid NOT NULL,
  units          numeric(12,4) NOT NULL CHECK (units >= 0),   -- months (salaried), days (daily) or hours (hourly)
  overtime_hours numeric(10,2) NOT NULL DEFAULT 0 CHECK (overtime_hours >= 0),
  allowances     numeric(24,6) NOT NULL DEFAULT 0 CHECK (allowances >= 0),
  project_id     uuid,
  cost_code_id   uuid,
  PRIMARY KEY (tenant_id, run_id, employee_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES pay_runs(tenant_id, id),
  FOREIGN KEY (tenant_id, employee_id) REFERENCES employees(tenant_id, id)
);

CREATE TABLE pay_lines (
  tenant_id    uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id       uuid NOT NULL,
  employee_id  uuid NOT NULL,
  project_id   uuid,
  cost_code_id uuid,
  basic        numeric(24,6) NOT NULL,
  overtime     numeric(24,6) NOT NULL,
  allowances   numeric(24,6) NOT NULL,
  gross        numeric(24,6) NOT NULL,
  income_tax   numeric(24,6) NOT NULL,
  pension_ee   numeric(24,6) NOT NULL,
  pension_er   numeric(24,6) NOT NULL,
  net          numeric(24,6) NOT NULL,
  PRIMARY KEY (tenant_id, run_id, employee_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES pay_runs(tenant_id, id),
  CONSTRAINT pay_line_arithmetic CHECK (gross = basic + overtime + allowances AND net = gross - income_tax - pension_ee AND net >= 0)
);

-- One journal per project section of a run (a journal belongs to one project).
CREATE TABLE pay_run_sections (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  run_id     uuid NOT NULL,
  project_id uuid,
  journal_id uuid UNIQUE,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES pay_runs(tenant_id, id)
);
CREATE UNIQUE INDEX pay_run_sections_one ON pay_run_sections (tenant_id, run_id, COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE FUNCTION pay_inputs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r pay_runs; e employees;
BEGIN
  SELECT * INTO r FROM pay_runs WHERE id = NEW.run_id AND tenant_id = current_tenant();
  IF r.status <> 'DRAFT' THEN
    RAISE EXCEPTION 'pay inputs are entered while the run is a draft' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO e FROM employees WHERE id = NEW.employee_id AND tenant_id = current_tenant();
  IF e.legal_entity_id <> r.legal_entity_id OR e.starts_on > r.period_end OR (e.ends_on IS NOT NULL AND e.ends_on < r.period_start) THEN
    RAISE EXCEPTION 'employee % is not employed by the entity during the period', e.number USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.project_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id AND legal_entity_id = r.legal_entity_id) THEN
    RAISE EXCEPTION 'the project belongs to another entity' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.cost_code_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM cost_codes WHERE id = NEW.cost_code_id AND project_id = NEW.project_id) THEN
    RAISE EXCEPTION 'the cost code belongs to another project' USING ERRCODE = 'check_violation';
  END IF;
  IF e.kind = 'SALARIED' AND NEW.units > 1 THEN
    RAISE EXCEPTION 'a salaried employee is paid at most one month per run' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER pay_inputs_guard BEFORE INSERT ON pay_inputs FOR EACH ROW EXECUTE FUNCTION pay_inputs_guard();

-- The one computation of a run's lines.
CREATE FUNCTION payroll_compute(p_run uuid)
RETURNS TABLE (employee_id uuid, project_id uuid, cost_code_id uuid, basic numeric, overtime numeric, allowances numeric, gross numeric,
               income_tax numeric, pension_ee numeric, pension_er numeric, net numeric)
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r pay_runs; s payroll_rule_sets; ccy char(3); i record; hourly numeric; b numeric; o numeric; g numeric; t numeric;
BEGIN
  SELECT * INTO r FROM pay_runs WHERE id = p_run AND tenant_id = current_tenant();
  SELECT * INTO s FROM payroll_rule_sets WHERE id = r.rule_set_id;
  SELECT functional_currency INTO ccy FROM legal_entities WHERE id = r.legal_entity_id;
  FOR i IN SELECT x.*, e.kind, e.base_rate FROM pay_inputs x JOIN employees e ON e.id = x.employee_id WHERE x.run_id = p_run ORDER BY e.number LOOP
    b := p2p_round(i.base_rate * i.units, ccy);
    hourly := CASE i.kind WHEN 'SALARIED' THEN i.base_rate / s.hours_per_month WHEN 'DAILY' THEN i.base_rate / s.hours_per_day ELSE i.base_rate END;
    o := p2p_round(i.overtime_hours * hourly * s.overtime_multiplier, ccy);
    g := b + o + i.allowances;
    t := COALESCE(payroll_tax(s.id, g, ccy), 0);
    employee_id := i.employee_id; project_id := i.project_id; cost_code_id := i.cost_code_id;
    basic := b; overtime := o; allowances := i.allowances; gross := g; income_tax := t;
    pension_ee := p2p_round(b * s.pension_ee_rate, ccy); pension_er := p2p_round(b * s.pension_er_rate, ccy);
    net := g - t - p2p_round(b * s.pension_ee_rate, ccy);
    RETURN NEXT;
  END LOOP;
END$$;

CREATE FUNCTION pay_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r pay_runs; x record;
BEGIN
  SELECT * INTO r FROM pay_runs WHERE id = NEW.run_id AND tenant_id = current_tenant();
  IF r.status <> 'DRAFT' THEN
    RAISE EXCEPTION 'pay lines are written by the calculation of a draft run' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO x FROM payroll_compute(NEW.run_id) c WHERE c.employee_id = NEW.employee_id;
  IF NOT FOUND OR (x.basic, x.overtime, x.allowances, x.gross, x.income_tax, x.pension_ee, x.pension_er, x.net, x.project_id, x.cost_code_id)
     IS DISTINCT FROM (NEW.basic, NEW.overtime, NEW.allowances, NEW.gross, NEW.income_tax, NEW.pension_ee, NEW.pension_er, NEW.net, NEW.project_id, NEW.cost_code_id) THEN
    RAISE EXCEPTION 'the pay line of employee % is not the database''s computation', NEW.employee_id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER pay_lines_guard BEFORE INSERT ON pay_lines FOR EACH ROW EXECUTE FUNCTION pay_lines_guard();

CREATE FUNCTION pay_runs_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE set_id uuid;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.paid_by IS NOT NULL THEN
      RAISE EXCEPTION 'pay runs are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    SELECT id INTO set_id FROM payroll_rule_sets WHERE legal_entity_id = NEW.legal_entity_id AND status = 'APPROVED' AND effective_from <= NEW.period_end
     ORDER BY effective_from DESC LIMIT 1;
    IF set_id IS NULL THEN
      RAISE EXCEPTION 'no approved payroll rule set is in force at %', NEW.period_end USING ERRCODE = 'check_violation';
    END IF;
    NEW.rule_set_id := set_id;
    IF NOT EXISTS (SELECT 1 FROM payroll_policies WHERE legal_entity_id = NEW.legal_entity_id AND status = 'APPROVED') THEN
      RAISE EXCEPTION 'the entity has no approved payroll policy (accounts)' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'CANCELLED') THEN
    RAISE EXCEPTION 'a % pay run never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'PAID' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['pay_journal_id'], 'pay run');
    IF OLD.pay_journal_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.pay_journal_id AND source_type = 'PAY_PAY' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'a paid run is bound once to its own DRAFT PAY_PAY journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.status = 'CANCELLED' THEN
    IF OLD.status NOT IN ('DRAFT', 'CALCULATED') THEN
      RAISE EXCEPTION 'only a draft or calculated run is cancelled' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'decision_reason'], 'pay run');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'cancelling a pay run requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'CALCULATED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'calculated_at'], 'pay run');
    IF NOT EXISTS (SELECT 1 FROM pay_lines WHERE run_id = NEW.id) OR (SELECT count(*) FROM pay_lines WHERE run_id = NEW.id) <> (SELECT count(*) FROM pay_inputs WHERE run_id = NEW.id) THEN
      RAISE EXCEPTION 'a run is calculated when every input has its computed line' USING ERRCODE = 'check_violation';
    END IF;
    NEW.calculated_at := now();
  ELSIF OLD.status = 'CALCULATED' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'pay run');
    PERFORM approval_use('payrun.submit', 'pay_run', NEW.id::text, NEW.created_by, ARRAY['payroll.prepare'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'pay run');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a pay run requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('payrun.decide', 'pay_run', NEW.id::text, NEW.approved_by, ARRAY['payroll.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'APPROVED' AND NEW.status = 'PAID' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'paid_by', 'paid_at', 'company_bank_id'], 'pay run');
    IF NEW.company_bank_id IS NULL OR NOT EXISTS (SELECT 1 FROM company_bank_accounts WHERE id = NEW.company_bank_id AND legal_entity_id = NEW.legal_entity_id) THEN
      RAISE EXCEPTION 'pay from a bank account of the entity' USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM pay_run_sections WHERE run_id = NEW.id AND journal_id IS NULL) THEN
      RAISE EXCEPTION 'the run''s journals must exist before it is paid' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('payrun.release', 'pay_run', NEW.id::text, NEW.paid_by, ARRAY['payment.release'], true, 'PAID', NULL);
    NEW.paid_at := now();
  ELSE
    RAISE EXCEPTION 'illegal pay run transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER pay_runs_guard BEFORE INSERT OR UPDATE ON pay_runs FOR EACH ROW EXECUTE FUNCTION pay_runs_guard();

CREATE FUNCTION pay_run_sections_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.journal_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM pay_runs WHERE id = NEW.run_id AND status = 'APPROVED') THEN
      RAISE EXCEPTION 'pay sections are created when the run is approved' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['journal_id'], 'pay section');
  IF OLD.journal_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.journal_id AND source_type = 'PAY_RUN' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
    RAISE EXCEPTION 'a pay section is bound once to its own DRAFT PAY_RUN journal' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER pay_run_sections_guard BEFORE INSERT OR UPDATE ON pay_run_sections FOR EACH ROW EXECUTE FUNCTION pay_run_sections_guard();

-- The accounting of PAY_* documents.
CREATE FUNCTION pay_expected_lines(p_doc text, p_id uuid)
RETURNS TABLE (account_id uuid, side text, amount numeric, project_id uuid, cost_code_id uuid)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  WITH sec AS (SELECT s.*, r.legal_entity_id FROM pay_run_sections s JOIN pay_runs r ON r.id = s.run_id
                WHERE p_doc = 'PAY_RUN' AND s.id = p_id AND s.tenant_id = current_tenant()),
  pol AS (SELECT p.* FROM payroll_policies p JOIN sec ON p.legal_entity_id = sec.legal_entity_id WHERE p.status = 'APPROVED'),
  l AS (SELECT pl.* FROM pay_lines pl JOIN sec ON pl.run_id = sec.run_id WHERE pl.project_id IS NOT DISTINCT FROM sec.project_id),
  run AS (SELECT r.*, (SELECT sum(net) FROM pay_lines WHERE run_id = r.id) AS total_net FROM pay_runs r WHERE p_doc = 'PAY_PAY' AND r.id = p_id AND r.tenant_id = current_tenant() AND r.status = 'PAID'),
  e(account_id, side, amount, project_id, cost_code_id) AS (
    SELECT pol.salary_account_id, 'DEBIT', sum(l.gross), l.project_id, l.cost_code_id FROM l, pol GROUP BY pol.salary_account_id, l.project_id, l.cost_code_id
    UNION ALL
    SELECT pol.pension_expense_account_id, 'DEBIT', sum(l.pension_er), l.project_id, l.cost_code_id FROM l, pol GROUP BY pol.pension_expense_account_id, l.project_id, l.cost_code_id
    UNION ALL
    SELECT pol.net_payable_account_id, 'CREDIT', sum(l.net), NULL::uuid, NULL::uuid FROM l, pol GROUP BY pol.net_payable_account_id
    UNION ALL
    SELECT pol.tax_payable_account_id, 'CREDIT', sum(l.income_tax), NULL::uuid, NULL::uuid FROM l, pol GROUP BY pol.tax_payable_account_id
    UNION ALL
    SELECT pol.pension_payable_account_id, 'CREDIT', sum(l.pension_ee + l.pension_er), NULL::uuid, NULL::uuid FROM l, pol GROUP BY pol.pension_payable_account_id
    UNION ALL
    SELECT p.net_payable_account_id, 'DEBIT', run.total_net, NULL::uuid, NULL::uuid FROM run JOIN payroll_policies p ON p.legal_entity_id = run.legal_entity_id AND p.status = 'APPROVED'
    UNION ALL
    SELECT b.clearing_account_id, 'CREDIT', run.total_net, NULL::uuid, NULL::uuid FROM run JOIN company_bank_accounts b ON b.id = run.company_bank_id)
  SELECT account_id, side, amount, project_id, cost_code_id FROM e WHERE amount > 0
$$;
CREATE FUNCTION pay_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(string_agg(concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')),
                             ';' ORDER BY concat_ws('|', account_id, side, amount::numeric(24,6), COALESCE(project_id::text, '-'), COALESCE(cost_code_id::text, '-')) COLLATE "C"), '')
    FROM pay_expected_lines(p_doc, p_id)
$$;

CREATE FUNCTION journals_pay_binding() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE bound boolean;
BEGIN
  bound := CASE NEW.source_type
           WHEN 'PAY_RUN' THEN EXISTS (SELECT 1 FROM pay_run_sections WHERE id::text = NEW.source_ref AND journal_id = NEW.id)
           WHEN 'PAY_PAY' THEN EXISTS (SELECT 1 FROM pay_runs WHERE id::text = NEW.source_ref AND pay_journal_id = NEW.id)
           ELSE false END;
  IF NOT bound THEN
    RAISE EXCEPTION 'journal % uses reserved source type % but no payroll document references it', NEW.id, NEW.source_type USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER journals_pay_binding AFTER INSERT ON journals DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.source_type LIKE 'PAY\_%') EXECUTE FUNCTION journals_pay_binding();

CREATE FUNCTION journals_pay_rebind() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE want text; got text;
BEGIN
  IF NEW.source_ref !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RAISE EXCEPTION 'subledger journal % has no document reference', NEW.id USING ERRCODE = 'check_violation';
  END IF;
  want := pay_expected_text(NEW.source_type, NEW.source_ref::uuid);
  got := p2p_lines_text(NEW.id);
  IF want IS NULL OR want = '' OR want IS DISTINCT FROM got THEN
    RAISE EXCEPTION 'subledger journal % no longer equals the accounting of % % (expected %, got %)', NEW.id, NEW.source_type, NEW.source_ref, want, got
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER journals_pay_rebind BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.source_type LIKE 'PAY\_%' AND NEW.status IN ('APPROVED', 'POSTED') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION journals_pay_rebind();

-- Subledger entry points and protections (verbatim from 0057, PAY added).
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
$$;
CREATE OR REPLACE FUNCTION subledger_expected_text(p_doc text, p_id uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT CASE WHEN p_doc LIKE 'P2P\_%' THEN p2p_expected_text(p_doc, p_id) WHEN p_doc LIKE 'AR\_%' THEN ar_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'STK\_%' THEN stk_expected_text(p_doc, p_id) WHEN p_doc LIKE 'REV\_%' THEN rev_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'SUB\_%' THEN sub_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'FA\_%' THEN fa_expected_text(p_doc, p_id)
              WHEN p_doc LIKE 'PAY\_%' THEN pay_expected_text(p_doc, p_id) ELSE '' END
$$;
CREATE OR REPLACE FUNCTION journals_subledger_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.source_type ~ '^(P2P|AR|STK|REV|SUB|FA|PAY)_' AND TG_OP = 'UPDATE' AND NEW.status = 'CANCELLED' AND OLD.status <> 'CANCELLED' THEN
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
  IF j.source_type ~ '^(P2P|AR|STK|REV|SUB|FA|PAY)_' AND j.created_txid IS DISTINCT FROM txid_current() THEN
    RAISE EXCEPTION 'lines of subledger journal % are written only by the transaction that generated it', jid USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END$$;

CREATE OR REPLACE FUNCTION journals_reserved_idempotency() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF lower(NEW.idempotency_key) LIKE 'p2p:%'
     AND (NEW.source_type !~ '^(P2P|AR|STK|REV|SUB|FA|PAY)_' OR NEW.idempotency_key IS DISTINCT FROM 'p2p:' || NEW.source_type || ':' || NEW.source_ref) THEN
    RAISE EXCEPTION 'idempotency keys beginning "p2p:" are reserved to the subledger journal of the named document' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

DROP TRIGGER journals_subledger_no_reversal ON journals;
CREATE TRIGGER journals_subledger_no_reversal BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.status = 'REVERSED' AND OLD.status <> 'REVERSED' AND NEW.source_type ~ '^(P2P|AR|STK|REV|SUB|FA|PAY)_')
  EXECUTE FUNCTION journals_subledger_no_reversal();

-- ---------------------------------------- shared functions (verbatim + 0061)
-- p2p_subject_text: verbatim from 0057 plus payroll documents (marked 0061).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0057; payroll documents are entity-level (marked 0061).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy', 'fixed_asset', 'asset_disposal', 'payroll_policy', 'payroll_rule_set', 'pay_run') THEN
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
  FOREACH tbl IN ARRAY ARRAY['payroll_policies', 'employees', 'payroll_rule_sets', 'pay_runs', 'pay_inputs', 'pay_lines', 'pay_run_sections'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, approved_by, approved_at, decision_reason) ON payroll_policies, payroll_rule_sets TO serk_app;
GRANT UPDATE (status, ends_on) ON employees TO serk_app;
GRANT UPDATE (status, calculated_at, approved_by, approved_at, decision_reason, paid_by, paid_at, company_bank_id, pay_journal_id) ON pay_runs TO serk_app;
GRANT UPDATE (journal_id) ON pay_run_sections TO serk_app;
REVOKE ALL ON FUNCTION payroll_bands_valid(jsonb), payroll_tax(uuid, numeric, char), payroll_compute(uuid), pay_expected_lines(text, uuid), pay_expected_text(text, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION payroll_bands_valid(jsonb), payroll_tax(uuid, numeric, char), payroll_compute(uuid), pay_expected_lines(text, uuid), pay_expected_text(text, uuid) TO serk_app;
