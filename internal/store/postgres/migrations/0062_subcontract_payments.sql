-- 0056 Paying subcontract certificates; subcontractor documents (WP-2.5 part 2;
-- SRS SUB-06, SUB-07, TRS-01)
--
-- WHAT THIS MIGRATION DOES
--   * SUBCONTRACTOR DOCUMENTS: insurance, tax clearance, licence, bond and
--     other documents of a subcontractor (a supplier) with their expiry. A
--     REQUIRED document that has expired by the value date blocks the approval
--     and the release of a payment to that subcontractor.
--   * SUBCONTRACT PAYMENT: pays certified net amounts. Prepared by a treasury
--     officer (payment.prepare), approved by finance (payment.approve, fresh
--     MFA), released by a different treasury officer (payment.release, fresh
--     MFA); the three people differ. Allocations are to CERTIFIED certificates of
--     the payment's subcontract and never exceed what is still open on each.
--     The payee is an APPROVED bank account of the subcontractor. When the
--     subcontract is "pay when certified", approval and release are refused
--     until the client has certified (CERTIFIED or INVOICED IPC on a contract of
--     the same project) a period at least as late as the paid certificates.
--   * SUB_PAY JOURNAL at release: Dr subcontractor payable / Cr the company
--     bank's clearing account, bound like every subledger journal. Matching the
--     payment to the bank statement is a later step (see WP-2.5 status).
--
-- SHARED FUNCTIONS REDEFINED HERE: sub_expected_lines and journals_sub_binding
-- (verbatim from 0055 plus SUB_PAY), p2p_subject_text / serk_subject_project
-- (verbatim from 0055 plus subcontract_payment).

CREATE TABLE subcontractor_documents (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  supplier_id uuid NOT NULL,
  doc_type    text NOT NULL CHECK (doc_type IN ('INSURANCE', 'TAX_CLEARANCE', 'LICENSE', 'BOND', 'OTHER')),
  reference   text NOT NULL CHECK (length(btrim(reference)) BETWEEN 2 AND 200),
  issued_on   date NOT NULL,
  expires_on  date NOT NULL,
  required    boolean NOT NULL DEFAULT true,
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES suppliers(tenant_id, id),
  CONSTRAINT subdoc_dates CHECK (expires_on > issued_on)
);

-- The required documents of a subcontractor that are expired at a date.
CREATE FUNCTION subcontractor_expired_docs(p_supplier uuid, p_at date) RETURNS text
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT string_agg(d.doc_type || ' ' || d.reference || ' (expired ' || d.expires_on || ')', ', ' ORDER BY d.doc_type)
    FROM subcontractor_documents d
   WHERE d.supplier_id = p_supplier AND d.tenant_id = current_tenant() AND d.required
     AND NOT EXISTS (SELECT 1 FROM subcontractor_documents n WHERE n.supplier_id = d.supplier_id AND n.doc_type = d.doc_type AND n.required AND n.expires_on >= p_at)
     AND d.expires_on < p_at
     AND d.expires_on = (SELECT max(m.expires_on) FROM subcontractor_documents m WHERE m.supplier_id = d.supplier_id AND m.doc_type = d.doc_type AND m.required)
$$;

CREATE TABLE subcontract_payments (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id    uuid NOT NULL,
  subcontract_id     uuid NOT NULL,
  number             text NOT NULL,
  supplier_bank_id   uuid NOT NULL,
  company_bank_id    uuid NOT NULL,
  currency           char(3) NOT NULL REFERENCES currencies(code),
  amount             numeric(24,6) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  value_date         date NOT NULL,
  status             text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'RELEASED', 'CANCELLED')),
  created_by         text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  approved_by        text,
  approved_at        timestamptz,
  decision_reason    text,
  released_by        text,
  released_at        timestamptz,
  release_journal_id uuid UNIQUE,
  cancel_reason      text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, subcontract_id) REFERENCES subcontracts(tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_bank_id) REFERENCES supplier_bank_accounts(tenant_id, id),
  FOREIGN KEY (tenant_id, company_bank_id) REFERENCES company_bank_accounts(tenant_id, id),
  CONSTRAINT subpay_three_people CHECK ((approved_by IS NULL OR approved_by <> created_by)
    AND (released_by IS NULL OR (released_by <> created_by AND released_by <> approved_by)))
);

CREATE TABLE subcontract_payment_allocations (
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  payment_id     uuid NOT NULL,
  application_id uuid NOT NULL,
  amount         numeric(24,6) NOT NULL CHECK (amount > 0),
  PRIMARY KEY (tenant_id, payment_id, application_id),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES subcontract_payments(tenant_id, id),
  FOREIGN KEY (tenant_id, application_id) REFERENCES subcontract_applications(tenant_id, id)
);

-- What is still owed on a certificate (net payable less live payments).
CREATE FUNCTION subcontract_certificate_open(p_app uuid, p_exclude uuid) RETURNS numeric
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT a.net_payable - COALESCE((SELECT sum(x.amount) FROM subcontract_payment_allocations x JOIN subcontract_payments p ON p.id = x.payment_id
                                    WHERE x.application_id = a.id AND p.status NOT IN ('CANCELLED', 'REJECTED') AND p.id IS DISTINCT FROM p_exclude), 0)
    FROM subcontract_applications a WHERE a.id = p_app AND a.tenant_id = current_tenant()
$$;

CREATE FUNCTION subcontract_payment_allocations_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE p subcontract_payments; a subcontract_applications; open numeric;
BEGIN
  SELECT * INTO p FROM subcontract_payments WHERE id = NEW.payment_id AND tenant_id = current_tenant() FOR UPDATE;
  IF p.status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'allocations are made while the payment is a draft' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO a FROM subcontract_applications WHERE id = NEW.application_id AND tenant_id = current_tenant() FOR UPDATE;
  IF a.status IS DISTINCT FROM 'CERTIFIED' OR a.subcontract_id <> p.subcontract_id THEN
    RAISE EXCEPTION 'a payment pays CERTIFIED certificates of its own subcontract' USING ERRCODE = 'check_violation';
  END IF;
  open := subcontract_certificate_open(a.id, NULL);
  IF NEW.amount > open THEN
    RAISE EXCEPTION 'certificate % has % still open, less than %', a.number, open, NEW.amount USING ERRCODE = 'check_violation';
  END IF;
  UPDATE subcontract_payments SET amount = amount + NEW.amount WHERE id = p.id;
  RETURN NEW;
END$$;
CREATE TRIGGER subcontract_payment_allocations_guard BEFORE INSERT ON subcontract_payment_allocations FOR EACH ROW EXECUTE FUNCTION subcontract_payment_allocations_guard();

-- Pay when certified: the latest client certificate of the project against the
-- latest certificate paid.
CREATE FUNCTION subcontract_payment_paid_when_certified(p_payment uuid) RETURNS boolean
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT NOT s.pay_when_certified OR EXISTS (
           SELECT 1 FROM ipcs i JOIN contracts c ON c.id = i.contract_id
            WHERE c.project_id = s.project_id AND i.status IN ('CERTIFIED', 'INVOICED')
              AND i.period_end >= (SELECT max(a.period_to) FROM subcontract_payment_allocations x JOIN subcontract_applications a ON a.id = x.application_id WHERE x.payment_id = p.id))
    FROM subcontract_payments p JOIN subcontracts s ON s.id = p.subcontract_id WHERE p.id = p_payment AND p.tenant_id = current_tenant()
$$;

CREATE FUNCTION subcontract_payments_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE s subcontracts; expired text;
BEGIN
  SELECT * INTO s FROM subcontracts WHERE id = NEW.subcontract_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.amount <> 0 OR NEW.approved_by IS NOT NULL OR NEW.released_by IS NOT NULL OR NEW.release_journal_id IS NOT NULL THEN
      RAISE EXCEPTION 'subcontract payments are prepared as empty DRAFTs; allocations set the amount' USING ERRCODE = 'check_violation';
    END IF;
    IF s.status NOT IN ('APPROVED', 'CLOSED') THEN
      RAISE EXCEPTION 'payments are made under an approved subcontract' USING ERRCODE = 'check_violation';
    END IF;
    NEW.legal_entity_id := s.legal_entity_id;
    NEW.currency := s.currency;
    IF NOT EXISTS (SELECT 1 FROM supplier_bank_accounts WHERE id = NEW.supplier_bank_id AND supplier_id = s.supplier_id AND status = 'APPROVED' AND tenant_id = current_tenant()) THEN
      RAISE EXCEPTION 'the payee must be an APPROVED bank account of the subcontractor' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM company_bank_accounts WHERE id = NEW.company_bank_id AND legal_entity_id = s.legal_entity_id AND currency = s.currency AND tenant_id = current_tenant()) THEN
      RAISE EXCEPTION 'pay from a bank account of the subcontract''s entity in its currency' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'CANCELLED') THEN
    RAISE EXCEPTION 'a % payment never changes', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status AND OLD.status = 'DRAFT' THEN
    -- The allocation trigger keeps the amount.
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['amount'], 'subcontract payment');
    RETURN NEW;
  END IF;
  IF OLD.status = 'RELEASED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['release_journal_id'], 'subcontract payment');
    IF OLD.release_journal_id IS NOT NULL OR NOT EXISTS (SELECT 1 FROM journals WHERE id = NEW.release_journal_id AND source_type = 'SUB_PAY' AND source_ref = NEW.id::text AND status = 'DRAFT') THEN
      RAISE EXCEPTION 'a released payment is bound once to its own DRAFT SUB_PAY journal' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.status = 'CANCELLED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'cancel_reason'], 'subcontract payment');
    IF NEW.cancel_reason IS NULL OR length(btrim(NEW.cancel_reason)) < 5 THEN
      RAISE EXCEPTION 'cancelling a payment requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'subcontract payment');
    IF NEW.amount <= 0 THEN
      RAISE EXCEPTION 'a payment needs at least one allocation' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('subpay.submit', 'subcontract_payment', NEW.id::text, NEW.created_by, ARRAY['payment.prepare'], false, NULL, NULL);
    RETURN NEW;
  END IF;
  IF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'subcontract payment');
    IF NEW.status = 'APPROVED' THEN
      expired := subcontractor_expired_docs(s.supplier_id, NEW.value_date);
      IF expired IS NOT NULL THEN
        RAISE EXCEPTION 'the subcontractor''s required documents have expired: %', expired USING ERRCODE = 'check_violation';
      END IF;
      IF NOT subcontract_payment_paid_when_certified(NEW.id) THEN
        RAISE EXCEPTION 'pay when certified: the client has not yet certified the period of these certificates' USING ERRCODE = 'check_violation';
      END IF;
    ELSIF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a payment requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('subpay.decide', 'subcontract_payment', NEW.id::text, NEW.approved_by, ARRAY['payment.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
    RETURN NEW;
  END IF;
  IF OLD.status = 'APPROVED' AND NEW.status = 'RELEASED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'released_by', 'released_at'], 'subcontract payment');
    expired := subcontractor_expired_docs(s.supplier_id, NEW.value_date);
    IF expired IS NOT NULL THEN
      RAISE EXCEPTION 'the subcontractor''s required documents have expired: %', expired USING ERRCODE = 'check_violation';
    END IF;
    IF NOT subcontract_payment_paid_when_certified(NEW.id) THEN
      RAISE EXCEPTION 'pay when certified: the client has not yet certified the period of these certificates' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('subpay.release', 'subcontract_payment', NEW.id::text, NEW.released_by, ARRAY['payment.release'], true, 'RELEASED', NULL);
    NEW.released_at := now();
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'illegal subcontract payment transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER subcontract_payments_guard BEFORE INSERT OR UPDATE ON subcontract_payments FOR EACH ROW EXECUTE FUNCTION subcontract_payments_guard();

-- Accounting of SUB_* (verbatim from 0055 plus SUB_PAY).
CREATE OR REPLACE FUNCTION sub_expected_lines(p_doc text, p_id uuid)
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
    UNION ALL SELECT pol.backcharge_account_id, 'CREDIT', a.backcharges, a.project_id, NULL::uuid FROM a, pol
    -- 0056: payment release: Dr subcontractor payable / Cr the bank's clearing account.
    UNION ALL SELECT sp.payable_account_id, 'DEBIT', p.amount, NULL::uuid, NULL::uuid
      FROM subcontract_payments p JOIN subcontract_policies sp ON sp.legal_entity_id = p.legal_entity_id AND sp.status = 'APPROVED'
     WHERE p_doc = 'SUB_PAY' AND p.id = p_id AND p.tenant_id = current_tenant() AND p.status = 'RELEASED'
    UNION ALL SELECT b.clearing_account_id, 'CREDIT', p.amount, NULL::uuid, NULL::uuid
      FROM subcontract_payments p JOIN company_bank_accounts b ON b.id = p.company_bank_id
     WHERE p_doc = 'SUB_PAY' AND p.id = p_id AND p.tenant_id = current_tenant() AND p.status = 'RELEASED')
  SELECT account_id, side, amount, project_id, cost_code_id FROM e WHERE amount > 0
$$;

CREATE OR REPLACE FUNCTION journals_sub_binding() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM subcontract_applications WHERE id::text = NEW.source_ref AND journal_id = NEW.id)
     AND NOT EXISTS (SELECT 1 FROM subcontract_payments WHERE id::text = NEW.source_ref AND release_journal_id = NEW.id) THEN
    RAISE EXCEPTION 'journal % uses reserved source type % but no certificate or payment references it', NEW.id, NEW.source_type USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;

-- ---------------------------------------- shared functions (verbatim + 0056)
-- p2p_subject_text: verbatim from 0055 plus subcontract payments (marked 0056).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0055 plus subcontract payments (marked 0056).
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
  WHEN 'subcontract_payment' THEN SELECT s.project_id::text INTO r FROM subcontract_payments x JOIN subcontracts s ON s.id = x.subcontract_id WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['subcontractor_documents', 'subcontract_payments', 'subcontract_payment_allocations'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (amount, status, approved_by, approved_at, decision_reason, released_by, released_at, release_journal_id, cancel_reason) ON subcontract_payments TO serk_app;
REVOKE ALL ON FUNCTION subcontractor_expired_docs(uuid, date), subcontract_certificate_open(uuid, uuid), subcontract_payment_paid_when_certified(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION subcontractor_expired_docs(uuid, date), subcontract_certificate_open(uuid, uuid), subcontract_payment_paid_when_certified(uuid) TO serk_app;
