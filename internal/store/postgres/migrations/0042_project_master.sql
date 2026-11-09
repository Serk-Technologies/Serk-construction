-- 0042 Organisation structure and project master (WP-1.7; SRS ORG-01, ORG-02, ORG-09)
--
-- WHAT THIS MIGRATION DOES
--   Until now a project was a code and a name created by the operator. A
--   contractor needs to know, for every project, who the client is, where it
--   is, what kind of contract it runs under, who manages it, when it starts
--   and finishes, which region and business unit own it, and which phase of
--   its life it is in. This adds:
--
--     regions, business_units       reference data for roll-ups
--     projects (extended)           client, contract type, location, dates,
--                                   manager, region, business unit, phase
--     project_stage_gates           the controlled steps of a project's life:
--                                   BID -> MOBILISATION -> EXECUTION ->
--                                   PRACTICAL_COMPLETION -> DEFECTS -> CLOSED
--                                   (or BID -> CLOSED for a bid not pursued)
--     calendar_exceptions           company holidays and exceptional working
--                                   days (movable holidays differ every year,
--                                   so a person enters them; nothing is
--                                   assumed)
--
--   A project created by a person (not by the operator) starts PLANNED in
--   phase BID and takes part in no cost until it passes its first gate.
--
-- GUARDS THE DATABASE ENFORCES
--   * a project created by a tenant login is attested, PLANNED and in phase BID;
--     the operator's own tooling (maintenance session) keeps the old defaults
--     (ACTIVE, phase EXECUTION) so existing projects and seeds are unchanged;
--   * the phase moves only through an approved stage gate, in the same
--     transaction: prepared by one person, decided by another with fresh MFA,
--     each step attested; one open gate per project; the gate must start from
--     the project's current phase;
--   * approving a gate moves the project (phase, and status ACTIVE once it
--     mobilises, CLOSED when it closes) atomically with the approval;
--   * dates and coordinates are sane; region, business unit and client must
--     belong to the same company.
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0041, additions marked 0042):
--   p2p_subject_text, serk_subject_project

-- ---------------------------------------------------------- reference data
CREATE TABLE regions (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  code       text NOT NULL CHECK (code ~ '^[A-Za-z0-9._-]{1,20}$'),
  name       text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 120),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE business_units (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  code       text NOT NULL CHECK (code ~ '^[A-Za-z0-9._-]{1,20}$'),
  name       text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 120),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, code)
);

CREATE TABLE calendar_exceptions (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  day        date NOT NULL,
  name       text NOT NULL CHECK (length(btrim(name)) BETWEEN 2 AND 120),
  kind       text NOT NULL CHECK (kind IN ('HOLIDAY', 'WORKING')),   -- WORKING = a normally non-working day that is worked
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  removed_at timestamptz,                                  -- entries are removed, never deleted (the audit trail keeps them)
  removed_by text,
  UNIQUE (tenant_id, id)
);
CREATE UNIQUE INDEX calendar_one_entry_per_day ON calendar_exceptions (tenant_id, day) WHERE removed_at IS NULL;

CREATE FUNCTION calendar_exceptions_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF OLD.removed_at IS NOT NULL THEN
    RAISE EXCEPTION 'a removed calendar entry is final' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.removed_at IS NULL OR NEW.removed_by IS NULL THEN
    RAISE EXCEPTION 'a calendar entry changes only by being removed, with who removed it' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['removed_at', 'removed_by'], 'calendar entry');
  RETURN NEW;
END$$;
CREATE TRIGGER calendar_exceptions_guard BEFORE UPDATE ON calendar_exceptions FOR EACH ROW EXECUTE FUNCTION calendar_exceptions_guard();

-- --------------------------------------------------------- project master
-- Tenant logins create projects now, so the company comes from the login, like every other table.
ALTER TABLE projects ALTER COLUMN tenant_id SET DEFAULT current_tenant();
ALTER TABLE projects
  ADD COLUMN region_id uuid,
  ADD COLUMN business_unit_id uuid,
  ADD COLUMN customer_id uuid,
  ADD COLUMN contract_type text CHECK (contract_type IS NULL OR contract_type IN ('LUMP_SUM', 'REMEASURE', 'COST_PLUS', 'DESIGN_BUILD', 'FRAMEWORK', 'OTHER')),
  ADD COLUMN latitude numeric(9,6) CHECK (latitude IS NULL OR latitude BETWEEN -90 AND 90),
  ADD COLUMN longitude numeric(9,6) CHECK (longitude IS NULL OR longitude BETWEEN -180 AND 180),
  ADD COLUMN start_date date,
  ADD COLUMN planned_finish date,
  ADD COLUMN project_manager text,
  ADD COLUMN description text CHECK (description IS NULL OR length(description) <= 1000),
  ADD COLUMN phase text NOT NULL DEFAULT 'EXECUTION' CHECK (phase IN ('BID', 'MOBILISATION', 'EXECUTION', 'PRACTICAL_COMPLETION', 'DEFECTS', 'CLOSED')),
  ADD COLUMN created_by text,
  ADD CONSTRAINT projects_dates CHECK (planned_finish IS NULL OR start_date IS NULL OR planned_finish >= start_date),
  ADD CONSTRAINT projects_region_fk FOREIGN KEY (tenant_id, region_id) REFERENCES regions(tenant_id, id),
  ADD CONSTRAINT projects_bu_fk FOREIGN KEY (tenant_id, business_unit_id) REFERENCES business_units(tenant_id, id),
  ADD CONSTRAINT projects_customer_fk FOREIGN KEY (tenant_id, customer_id) REFERENCES customers(tenant_id, id);

CREATE TABLE project_stage_gates (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id      uuid NOT NULL,
  from_phase      text NOT NULL,
  to_phase        text NOT NULL,
  notes           text NOT NULL CHECK (length(btrim(notes)) BETWEEN 5 AND 1000),   -- the evidence for passing this gate
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  CONSTRAINT gate_path CHECK ((from_phase, to_phase) IN (('BID', 'MOBILISATION'), ('BID', 'CLOSED'), ('MOBILISATION', 'EXECUTION'),
     ('EXECUTION', 'PRACTICAL_COMPLETION'), ('PRACTICAL_COMPLETION', 'DEFECTS'), ('DEFECTS', 'CLOSED'))),
  CONSTRAINT gate_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
-- One open gate per project.
CREATE UNIQUE INDEX project_one_open_gate ON project_stage_gates (tenant_id, project_id) WHERE status IN ('DRAFT', 'SUBMITTED');

-- --------------------------------------------------------------- guards
-- True when the session is one of the tenants' own logins (never the
-- operator's tooling). SECURITY DEFINER because the tenant login may not read
-- tenant_db_roles; it reveals nothing but this one fact about the caller.
CREATE FUNCTION serk_tenant_login() RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT EXISTS (SELECT 1 FROM public.tenant_db_roles WHERE role_name = session_user)
$$;
REVOKE ALL ON FUNCTION serk_tenant_login() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION serk_tenant_login() TO serk_app;

-- A tenant login creates a PLANNED project in phase BID with its creation
-- attested (checked at commit); the operator's tooling is not
-- restricted (existing projects and the seed keep ACTIVE / EXECUTION).
CREATE FUNCTION projects_insert_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF serk_tenant_login() THEN
    IF NEW.status <> 'PLANNED' OR NEW.phase <> 'BID' THEN
      RAISE EXCEPTION 'a project created by a person starts PLANNED in phase BID and moves through stage gates' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.created_by IS NULL THEN
      RAISE EXCEPTION 'a project records who created it' USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER projects_insert_guard BEFORE INSERT ON projects FOR EACH ROW EXECUTE FUNCTION projects_insert_guard();

CREATE FUNCTION projects_created_attested() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF serk_tenant_login() THEN
    PERFORM approval_use('project.create', 'project', NEW.id::text, NEW.created_by, ARRAY['project.create'], false, NULL, NULL);
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER projects_created_attested AFTER INSERT ON projects DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION projects_created_attested();

-- The phase moves only through an approved gate in this transaction; the
-- code, entity and creator never change.
CREATE FUNCTION projects_update_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.phase IS DISTINCT FROM OLD.phase THEN
    IF NOT EXISTS (SELECT 1 FROM project_stage_gates g WHERE g.project_id = NEW.id AND g.tenant_id = current_tenant() AND g.status = 'APPROVED'
                    AND g.from_phase = OLD.phase AND g.to_phase = NEW.phase AND g.approved_at = (SELECT max(approved_at) FROM project_stage_gates WHERE project_id = NEW.id)
                    AND g.approved_txid = txid_current()) THEN
      RAISE EXCEPTION 'a project changes phase only through an approved stage gate' USING ERRCODE = 'insufficient_privilege';
    END IF;
  END IF;
  IF NEW.code <> OLD.code OR NEW.legal_entity_id <> OLD.legal_entity_id OR NEW.created_by IS DISTINCT FROM OLD.created_by OR NEW.tenant_id <> OLD.tenant_id THEN
    RAISE EXCEPTION 'a project''s code, company and creator never change' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER projects_update_guard BEFORE UPDATE ON projects FOR EACH ROW EXECUTE FUNCTION projects_update_guard();

ALTER TABLE project_stage_gates ADD COLUMN approved_txid bigint;

CREATE FUNCTION project_stage_gates_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE pr projects;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL OR NEW.approved_txid IS NOT NULL THEN
      RAISE EXCEPTION 'stage gates are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    SELECT * INTO pr FROM projects WHERE id = NEW.project_id AND tenant_id = current_tenant();
    IF pr.phase IS DISTINCT FROM NEW.from_phase THEN
      RAISE EXCEPTION 'the project is in phase %, not %', pr.phase, NEW.from_phase USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided stage gate is immutable' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), '{}', 'stage gate');
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'stage gate');
    PERFORM approval_use('project.gate.submit', 'project_stage_gate', NEW.id::text, NEW.created_by, ARRAY['project.gate.submit'], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'approved_txid', 'decision_reason'], 'stage gate');
    SELECT * INTO pr FROM projects WHERE id = NEW.project_id AND tenant_id = current_tenant() FOR UPDATE;
    IF pr.phase IS DISTINCT FROM NEW.from_phase THEN
      RAISE EXCEPTION 'the project is no longer in phase %', NEW.from_phase USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('project.gate.decide', 'project_stage_gate', NEW.id::text, NEW.approved_by, ARRAY['project.gate.decide'], true, 'APPROVED', NULL);
    NEW.approved_at := now();
    NEW.approved_txid := txid_current();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'stage gate');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a stage gate requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('project.gate.decide', 'project_stage_gate', NEW.id::text, NEW.approved_by, ARRAY['project.gate.decide'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal stage gate transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER project_stage_gates_guard BEFORE INSERT OR UPDATE ON project_stage_gates FOR EACH ROW EXECUTE FUNCTION project_stage_gates_guard();

-- Approving a gate moves the project, atomically.
CREATE FUNCTION project_stage_gates_apply() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  UPDATE projects SET phase = NEW.to_phase,
         status = CASE NEW.to_phase WHEN 'CLOSED' THEN 'CLOSED' WHEN 'BID' THEN status ELSE 'ACTIVE' END
   WHERE id = NEW.project_id AND tenant_id = current_tenant() AND phase = NEW.from_phase;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'the project could not move to %', NEW.to_phase USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE TRIGGER project_stage_gates_apply AFTER UPDATE ON project_stage_gates FOR EACH ROW
  WHEN (OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED') EXECUTE FUNCTION project_stage_gates_apply();

-- ---------------------------------------- shared functions (verbatim + 0042)
-- p2p_subject_text: verbatim from 0041 plus projects and stage gates (marked 0042).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0041 plus company-level project creation and stage gates (marked 0042).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project') THEN
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
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['regions', 'business_units', 'calendar_exceptions', 'project_stage_gates'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (removed_at, removed_by) ON calendar_exceptions TO serk_app;
GRANT UPDATE (name) ON regions, business_units TO serk_app;
GRANT UPDATE (status, submitted_at, approved_by, approved_at, approved_txid, decision_reason) ON project_stage_gates TO serk_app;
GRANT INSERT ON projects TO serk_app;
GRANT UPDATE (phase, region_id, business_unit_id, customer_id, contract_type, latitude, longitude, start_date, planned_finish, project_manager, description) ON projects TO serk_app;
