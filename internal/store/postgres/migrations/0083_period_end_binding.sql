-- 0083_period_end_binding.sql — period-end journals equal their computation; recurring templates are approved (DEF-013).
--
-- The carried period-end module (0064, new-version 0058) recorded what an overhead run or a recurring template
-- should produce but did not bind the journals to it: an OVERHEAD or RECURRING journal could be edited while DRAFT
-- (or written directly) with other amounts, bound to the run, and approved. A recurring template took effect
-- without anyone but its maker reviewing it, and its lines could be added by any transaction within a minute of
-- its creation. Now:
--   * OVERHEAD: binding an overhead line to its journal, and approving or posting that journal, require the
--     journal's lines to be exactly Dr the overhead-applied account / Cr the pool, for the line's computed share,
--     on the line's project. An OVERHEAD journal no run refers to cannot be approved.
--   * RECURRING: a template's lines are written by the transaction that created it (xmin, not a time window) and
--     only before it is approved. A template is approved by someone other than its maker, holding journal.approve,
--     with a fresh second factor, attested in the database; only an approved template runs. Recording a run, and
--     approving or posting the run's journal, require the journal's lines to equal the template's lines (amounts
--     rounded half-even to the entity currency, as the kernel rounds them), on the template's project. A RECURRING
--     journal no run refers to cannot be approved.
--   * ACCRUAL journals need no change: an accrual is the preparer's own journal, approved with segregation of
--     duties like any manual journal, and its reversal must mirror it line for line (0001/0002 reversal guard) and
--     carry the accrual's reversal date (0064 accruals_guard).
-- Templates created before this migration are unapproved and do not run until someone approves them: no approval
-- is assumed on anyone's behalf.
--
-- p2p_subject_text / serk_subject_project: verbatim from 0082 plus recurring_journal.

ALTER TABLE recurring_journals ADD COLUMN approved_by text, ADD COLUMN approved_at timestamptz;

CREATE OR REPLACE FUNCTION recurring_journals_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'ACTIVE' THEN
      RAISE EXCEPTION 'recurring journals are created ACTIVE' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.approved_by IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'a recurring journal is approved after it is created, by someone else' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'ENDED' THEN
    RAISE EXCEPTION 'an ended recurring journal never changes' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'next_run_on', 'approved_by', 'approved_at'], 'recurring journal');
  IF OLD.approved_by IS NOT NULL AND (NEW.approved_by, NEW.approved_at) IS DISTINCT FROM (OLD.approved_by, OLD.approved_at) THEN
    RAISE EXCEPTION 'the approval of a recurring journal never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.approved_by IS NULL AND NEW.approved_by IS NULL AND NEW.approved_at IS NOT NULL THEN
    RAISE EXCEPTION 'a recurring journal is approved by a named person' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.approved_by IS NULL AND NEW.approved_by IS NOT NULL THEN
    IF NEW.approved_by = OLD.created_by THEN
      RAISE EXCEPTION 'segregation of duties: the person who prepared this recurring journal cannot approve it' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status <> 'ACTIVE' OR NEW.next_run_on <> OLD.next_run_on THEN
      RAISE EXCEPTION 'a recurring journal is approved on its own, while active' USING ERRCODE = 'check_violation';
    END IF;
    NEW.approved_at := now();
  END IF;
  IF NEW.next_run_on < OLD.next_run_on THEN
    RAISE EXCEPTION 'a recurring journal never moves back in time' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.next_run_on > OLD.next_run_on AND NOT EXISTS (SELECT 1 FROM recurring_journal_runs WHERE template_id = NEW.id AND run_on = OLD.next_run_on) THEN
    RAISE EXCEPTION 'a recurring journal advances only after its run for % was recorded', OLD.next_run_on USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

-- The approval is attested for the approver over the template and its lines, with a fresh second factor.
CREATE FUNCTION recurring_journals_attest() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  PERFORM approval_use('recurring.approve', 'recurring_journal', NEW.id::text, NEW.approved_by, ARRAY['journal.approve'], true, 'APPROVED', NULL);
  RETURN NEW;
END$$;
CREATE TRIGGER recurring_journals_zattest BEFORE UPDATE ON recurring_journals FOR EACH ROW
  WHEN (OLD.approved_by IS NULL AND NEW.approved_by IS NOT NULL) EXECUTE FUNCTION recurring_journals_attest();
GRANT UPDATE (approved_by, approved_at) ON recurring_journals TO serk_app;

-- A template's lines are written by the transaction that created it, before approval and before any run.
CREATE OR REPLACE FUNCTION recurring_journal_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r recurring_journals; born boolean;
BEGIN
  SELECT * INTO r FROM recurring_journals WHERE id = NEW.template_id AND tenant_id = current_tenant();
  SELECT x.xmin = pg_current_xact_id()::xid INTO born FROM recurring_journals x WHERE x.id = NEW.template_id AND x.tenant_id = current_tenant();
  IF r.id IS NULL OR NOT COALESCE(born, false) OR r.approved_by IS NOT NULL OR EXISTS (SELECT 1 FROM recurring_journal_runs WHERE template_id = r.id) THEN
    RAISE EXCEPTION 'the lines of a recurring journal are written with it' USING ERRCODE = 'check_violation';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = NEW.account_id AND legal_entity_id = r.legal_entity_id AND active) THEN
    RAISE EXCEPTION 'line % uses an account that is not an active account of the entity', NEW.line_no USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;

-- What each period-end journal must contain, in the canonical text of p2p_lines_text.
CREATE FUNCTION period_end_lines_text(p_rows jsonb) RETURNS text
LANGUAGE sql IMMUTABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE(string_agg(t, ';' ORDER BY t COLLATE "C"), '')
    FROM (SELECT concat_ws('|', e->>'account_id', e->>'side', (e->>'amount')::numeric(24,6), COALESCE(e->>'project_id', '-'), COALESCE(e->>'cost_code_id', '-')) AS t
            FROM jsonb_array_elements(p_rows) e) x
$$;

CREATE FUNCTION overhead_expected_text(p_journal uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT period_end_lines_text(jsonb_build_array(
           jsonb_build_object('account_id', r.target_account_id, 'side', 'DEBIT', 'amount', ol.share, 'project_id', ol.project_id),
           jsonb_build_object('account_id', r.pool_account_id, 'side', 'CREDIT', 'amount', ol.share, 'project_id', ol.project_id)))
    FROM overhead_lines ol JOIN overhead_runs r ON r.id = ol.run_id AND r.tenant_id = ol.tenant_id
   WHERE ol.journal_id = p_journal AND ol.tenant_id = current_tenant()
$$;

CREATE FUNCTION recurring_expected_text(p_template uuid) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT period_end_lines_text(COALESCE(jsonb_agg(jsonb_build_object('account_id', l.account_id, 'side', l.side,
           'amount', round_half_even(l.amount, currency_minor_units(e.functional_currency)), 'project_id', r.project_id, 'cost_code_id', l.cost_code_id)), '[]'::jsonb))
    FROM recurring_journals r JOIN legal_entities e ON e.id = r.legal_entity_id
    JOIN recurring_journal_lines l ON l.template_id = r.id AND l.tenant_id = r.tenant_id
   WHERE r.id = p_template AND r.tenant_id = current_tenant()
$$;

-- Binding an overhead line to its journal: the journal says exactly what the line computed.
CREATE FUNCTION overhead_lines_amounts() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE want text; got text;
BEGIN
  SELECT period_end_lines_text(jsonb_build_array(
           jsonb_build_object('account_id', r.target_account_id, 'side', 'DEBIT', 'amount', NEW.share, 'project_id', NEW.project_id),
           jsonb_build_object('account_id', r.pool_account_id, 'side', 'CREDIT', 'amount', NEW.share, 'project_id', NEW.project_id)))
    INTO want FROM overhead_runs r WHERE r.id = NEW.run_id AND r.tenant_id = current_tenant();
  got := p2p_lines_text(NEW.journal_id);
  IF want IS DISTINCT FROM got THEN
    RAISE EXCEPTION 'overhead journal % does not equal the computed share of project % (expected %, got %)', NEW.journal_id, NEW.project_id, want, got
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER overhead_lines_zamounts BEFORE UPDATE OF journal_id ON overhead_lines FOR EACH ROW
  WHEN (NEW.journal_id IS NOT NULL) EXECUTE FUNCTION overhead_lines_amounts();

-- Recording a run: the template is approved and the journal says exactly what the template says.
CREATE FUNCTION recurring_journal_runs_amounts() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE want text; got text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM recurring_journals WHERE id = NEW.template_id AND tenant_id = current_tenant() AND approved_by IS NOT NULL) THEN
    RAISE EXCEPTION 'a recurring journal runs only after it was approved' USING ERRCODE = 'check_violation';
  END IF;
  want := recurring_expected_text(NEW.template_id);
  got := p2p_lines_text(NEW.journal_id);
  IF want IS NULL OR want = '' OR want IS DISTINCT FROM got THEN
    RAISE EXCEPTION 'recurring journal % does not equal its template (expected %, got %)', NEW.journal_id, want, got USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER recurring_journal_runs_zamounts BEFORE INSERT ON recurring_journal_runs FOR EACH ROW EXECUTE FUNCTION recurring_journal_runs_amounts();

-- Approving or posting an OVERHEAD or RECURRING journal: it is bound to its run and still equals the computation
-- (DRAFT lines can be edited after binding; this is the check that counts).
CREATE FUNCTION journals_period_end_rebind() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE want text; got text; tpl uuid;
BEGIN
  IF NEW.source_type = 'OVERHEAD' THEN
    want := overhead_expected_text(NEW.id);
  ELSE
    SELECT rr.template_id INTO tpl FROM recurring_journal_runs rr WHERE rr.journal_id = NEW.id AND rr.tenant_id = current_tenant();
    IF tpl IS NOT NULL AND EXISTS (SELECT 1 FROM recurring_journals WHERE id = tpl AND approved_by IS NOT NULL) THEN
      want := recurring_expected_text(tpl);
    END IF;
  END IF;
  IF want IS NULL OR want = '' THEN
    RAISE EXCEPTION '% journal % is not bound to the run that computed it', NEW.source_type, NEW.id USING ERRCODE = 'check_violation';
  END IF;
  got := p2p_lines_text(NEW.id);
  IF want IS DISTINCT FROM got THEN
    RAISE EXCEPTION '% journal % no longer equals its computation (expected %, got %)', NEW.source_type, NEW.id, want, got USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER journals_period_end_rebind BEFORE UPDATE OF status ON journals FOR EACH ROW
  WHEN (NEW.source_type IN ('OVERHEAD', 'RECURRING') AND NEW.status IN ('APPROVED', 'POSTED') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION journals_period_end_rebind();

REVOKE ALL ON FUNCTION period_end_lines_text(jsonb), overhead_expected_text(uuid), recurring_expected_text(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION period_end_lines_text(jsonb), overhead_expected_text(uuid), recurring_expected_text(uuid) TO serk_app;

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
  -- 0081: tax and intercompany account designations (what their approver attests).
  WHEN 'tax_account' THEN SELECT to_jsonb(x) INTO j FROM tax_accounts x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'intercompany_account' THEN SELECT to_jsonb(x) INTO j FROM intercompany_accounts x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0082: a role adjustment (role, withheld permissions, version) is what its maker attests.
  WHEN 'role_adjustment' THEN SELECT to_jsonb(x) - 'updated_at' INTO j FROM role_adjustments x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0083: a recurring journal template with its lines is what its approver attests.
  WHEN 'recurring_journal' THEN SELECT to_jsonb(x) || jsonb_build_object('lines',
         (SELECT jsonb_agg(to_jsonb(l) ORDER BY l.line_no) FROM recurring_journal_lines l WHERE l.tenant_id = t AND l.template_id = x.id))
       INTO j FROM recurring_journals x WHERE tenant_id = t AND id = p_id::uuid;
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
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy', 'fixed_asset', 'asset_disposal', 'payroll_policy', 'payroll_rule_set', 'pay_run', 'consolidation_group', 'consolidation_run', 'tax_return', 'document_policy', 'equipment_operator_auth', 'fleet_policy', 'employee', 'equipment_charge_rate', 'tax_account', 'intercompany_account', 'role_adjustment') THEN  -- 0082: role adjustments are tenant-wide
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
  -- 0083: a recurring journal belongs to its project when it has one; otherwise it is tenant-wide.
  WHEN 'recurring_journal' THEN SELECT project_id::text INTO r FROM recurring_journals x WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;
