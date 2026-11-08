-- 0046 Contract notices, claims and extensions of time (WP-2.6; SRS CON-04, CON-06)
--
-- WHAT THIS MIGRATION DOES
--   Construction contracts bar a claim that is not notified in time (often 28
--   days from the event). This migration keeps the NOTICE (the event, the
--   clause, the date the contractor must notify by, the date it actually did)
--   and the CLAIM that follows (time, cost or both), and ties them together:
--
--     * a notice's deadline is computed by the database (event date + days);
--     * a notice is "sent" once, never in the future and never before the event;
--     * a claim is prepared by one person, approved for sending to the client
--       by another (attested, fresh MFA), and the client's answer (agreed,
--       rejected, disputed, with the agreed amount and days) is recorded once
--       against a client reference;
--     * a claim whose notice was late or missing cannot be submitted without a
--       stated reason (for example "the engineer waived the time bar in writing");
--     * a claim has the money or days its type says: TIME needs days, COST
--       needs an amount, BOTH needs both.
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0045, additions marked 0046):
--   p2p_subject_text, serk_subject_project

CREATE TABLE contract_notices (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  contract_id uuid NOT NULL,
  number      text NOT NULL,
  clause_ref  text NOT NULL CHECK (length(btrim(clause_ref)) BETWEEN 1 AND 100),
  subject     text NOT NULL CHECK (length(btrim(subject)) BETWEEN 3 AND 300),
  event_date  date NOT NULL,
  due_days    integer NOT NULL DEFAULT 28 CHECK (due_days BETWEEN 1 AND 365),
  due_on      date GENERATED ALWAYS AS (event_date + due_days) STORED,
  sent_on     date,
  sent_by     text,
  status      text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'SENT')),
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES contracts(tenant_id, id)
);

CREATE TABLE contract_claims (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  contract_id     uuid NOT NULL,
  notice_id       uuid,
  number          text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('TIME', 'COST', 'BOTH')),
  title           text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 200),
  description     text NOT NULL CHECK (length(btrim(description)) BETWEEN 10 AND 4000),
  amount          numeric(24,6) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  days            integer NOT NULL DEFAULT 0 CHECK (days >= 0),
  waiver_reason   text,
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  -- The client's answer, recorded once after approval.
  client_outcome  text CHECK (client_outcome IN ('AGREED', 'REJECTED', 'DISPUTED')),
  client_reference text,
  agreed_amount   numeric(24,6),
  agreed_days     integer,
  outcome_on      date,
  outcome_by      text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES contracts(tenant_id, id),
  FOREIGN KEY (tenant_id, notice_id) REFERENCES contract_notices(tenant_id, id),
  CONSTRAINT claim_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by),
  CONSTRAINT claim_kind_amounts CHECK ((kind = 'TIME' AND days > 0 AND amount = 0) OR (kind = 'COST' AND amount > 0 AND days = 0) OR (kind = 'BOTH' AND days > 0 AND amount > 0)),
  CONSTRAINT claim_outcome_whole CHECK ((client_outcome IS NULL AND client_reference IS NULL AND outcome_on IS NULL AND outcome_by IS NULL AND agreed_amount IS NULL AND agreed_days IS NULL)
    OR (client_outcome IS NOT NULL AND client_reference IS NOT NULL AND outcome_on IS NOT NULL AND outcome_by IS NOT NULL AND agreed_amount IS NOT NULL AND agreed_days IS NOT NULL
        AND agreed_amount >= 0 AND agreed_days >= 0 AND agreed_amount <= amount AND agreed_days <= days
        AND (client_outcome = 'AGREED' OR (agreed_amount = 0 AND agreed_days = 0))))
);
CREATE INDEX contract_claims_contract ON contract_claims (tenant_id, contract_id, status);

CREATE FUNCTION contract_notices_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' OR NEW.sent_on IS NOT NULL OR NEW.sent_by IS NOT NULL THEN
      RAISE EXCEPTION 'notices are created OPEN; record sending afterwards' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.event_date > current_date THEN
      RAISE EXCEPTION 'the event of a notice cannot be in the future' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM contracts WHERE id = NEW.contract_id AND tenant_id = current_tenant() AND status = 'ACTIVE') THEN
      RAISE EXCEPTION 'notices are raised against an ACTIVE contract' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'SENT' THEN
    RAISE EXCEPTION 'a sent notice never changes' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'sent_on', 'sent_by', 'due_on'], 'notice');
  IF NEW.status <> 'SENT' OR NEW.sent_on IS NULL OR NEW.sent_by IS NULL THEN
    RAISE EXCEPTION 'a notice changes only by being sent (with the date and the sender)' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.sent_on > current_date OR NEW.sent_on < NEW.event_date THEN
    RAISE EXCEPTION 'a notice is sent on or after its event and not in the future' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER contract_notices_guard BEFORE INSERT OR UPDATE ON contract_notices FOR EACH ROW EXECUTE FUNCTION contract_notices_guard();

CREATE FUNCTION contract_claims_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE n contract_notices; late boolean;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL OR NEW.client_outcome IS NOT NULL THEN
      RAISE EXCEPTION 'claims are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM contracts WHERE id = NEW.contract_id AND tenant_id = current_tenant() AND status = 'ACTIVE') THEN
      RAISE EXCEPTION 'claims are made against an ACTIVE contract' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.notice_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM contract_notices WHERE id = NEW.notice_id AND tenant_id = current_tenant() AND contract_id = NEW.contract_id) THEN
      RAISE EXCEPTION 'the notice belongs to another contract' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'REJECTED' OR (OLD.status = 'APPROVED' AND OLD.client_outcome IS NOT NULL) THEN
    RAISE EXCEPTION 'a decided claim is immutable' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'APPROVED' THEN
    -- Only the client's answer, once.
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['client_outcome', 'client_reference', 'agreed_amount', 'agreed_days', 'outcome_on', 'outcome_by'], 'claim');
    IF NEW.client_outcome IS NULL THEN
      RAISE EXCEPTION 'an approved claim changes only by recording the client''s answer' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.outcome_on > current_date THEN
      RAISE EXCEPTION 'the client''s answer cannot be dated in the future' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), '{}', 'claim');
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'claim');
    SELECT * INTO n FROM contract_notices WHERE id = NEW.notice_id AND tenant_id = current_tenant();
    late := NEW.notice_id IS NULL OR n.sent_on IS NULL OR n.sent_on > n.due_on;
    IF late AND (NEW.waiver_reason IS NULL OR length(btrim(NEW.waiver_reason)) < 10) THEN
      RAISE EXCEPTION 'the notice for this claim is missing or was not sent in time; state why the claim still stands (10+ characters)' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('claim.submit', 'contract_claim', NEW.id::text, NEW.created_by, ARRAY['claim.manage'], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'claim');
    PERFORM approval_use('claim.decide', 'contract_claim', NEW.id::text, NEW.approved_by, ARRAY['claim.approve'], true, 'APPROVED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'claim');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a claim requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('claim.decide', 'contract_claim', NEW.id::text, NEW.approved_by, ARRAY['claim.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal claim transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER contract_claims_guard BEFORE INSERT OR UPDATE ON contract_claims FOR EACH ROW EXECUTE FUNCTION contract_claims_guard();

-- ---------------------------------------- shared functions (verbatim + 0046)
-- p2p_subject_text: verbatim from 0045 plus notices and claims (marked 0046).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0045 plus notices and claims (marked 0046).
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
  -- 0044: a forecast cycle belongs to its project.
  WHEN 'forecast_cycle' THEN SELECT project_id::text INTO r FROM forecast_cycles x WHERE x.id = v_id;
  -- 0045: a change event belongs to its project.
  WHEN 'change_event' THEN SELECT project_id::text INTO r FROM change_events x WHERE x.id = v_id;
  -- 0046: a notice or claim belongs to its contract's project.
  WHEN 'contract_notice' THEN SELECT c.project_id::text INTO r FROM contract_notices x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  WHEN 'contract_claim' THEN SELECT c.project_id::text INTO r FROM contract_claims x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['contract_notices', 'contract_claims'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, sent_on, sent_by) ON contract_notices TO serk_app;
GRANT UPDATE (status, submitted_at, approved_by, approved_at, decision_reason, waiver_reason,
              client_outcome, client_reference, agreed_amount, agreed_days, outcome_on, outcome_by) ON contract_claims TO serk_app;
