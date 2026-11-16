-- 0052 Controlled documents, revisions and requests for information
-- (CONNECT; requirements CNX-01, CNX-02, CNX-03 in docs/engineering; ADR 0018)
--
-- WHAT THIS MIGRATION DOES
--   * DOCUMENTS: the register entry of a controlled document of a project (a
--     drawing, a specification, a method statement...). Its number, reference,
--     type and title never change; what changes is its revisions.
--   * REVISIONS: each revision is stored with its file (document_contents).
--     The database computes the SHA-256 and the size of the bytes and refuses
--     a file whose hash or size differs from what the revision declares, so the hash an approver attests is the
--     hash of the bytes that are stored. A revision's file and description
--     never change. One person uploads and submits it; another publishes it
--     (APPROVED) with a fresh multi-factor sign-in, attested. Publishing a
--     revision supersedes the previous current revision in the same
--     transaction, and at most one revision of a document is ever current.
--     A document has at most one revision in preparation at a time.
--       Bible states -> stored status: work in progress = DRAFT, shared =
--       SUBMITTED, published = APPROVED, archived = SUPERSEDED.
--   * RFIs: a request for information is raised on a project (optionally
--     about one revision of one of its documents), answered once by a person
--     other than its author (attested: an answer is a human decision), and
--     then closed. An unanswered RFI can be withdrawn by closing it with a
--     reason.
--
-- NOT DONE HERE (recorded as open requirements): malware scanning and
--   quarantine, object storage, transmittals, retention and legal hold.
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0051, additions marked 0052):
--   p2p_subject_text, serk_subject_project

CREATE TABLE documents (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id  uuid NOT NULL,
  number      text NOT NULL,
  reference   text NOT NULL CHECK (reference ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,59}$'),
  doc_type    text NOT NULL CHECK (doc_type IN ('DRAWING', 'SPECIFICATION', 'METHOD_STATEMENT', 'REPORT', 'CORRESPONDENCE', 'CONTRACT', 'CERTIFICATE', 'OTHER')),
  title       text NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 200),
  discipline  text CHECK (discipline IS NULL OR length(btrim(discipline)) BETWEEN 1 AND 60),
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, project_id, reference),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id)
);

CREATE TABLE document_revisions (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  document_id     uuid NOT NULL,
  number          text NOT NULL,
  revision        text NOT NULL CHECK (revision ~ '^[A-Za-z0-9][A-Za-z0-9.-]{0,11}$'),
  file_name       text NOT NULL CHECK (file_name ~ '^[A-Za-z0-9][A-Za-z0-9 ._()-]{0,149}$'),
  media_type      text NOT NULL CHECK (media_type IN ('application/pdf', 'image/png', 'image/jpeg', 'text/plain', 'text/csv',
                    'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
                    'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
                    'image/vnd.dwg', 'image/vnd.dxf', 'application/x-step')),
  size_bytes      integer NOT NULL CHECK (size_bytes BETWEEN 1 AND 10485760),
  sha256          text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  change_note     text NOT NULL CHECK (length(btrim(change_note)) BETWEEN 3 AND 500),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'SUPERSEDED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  superseded_at   timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, document_id, revision),
  FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
  CONSTRAINT revision_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
-- At most one current revision, and at most one in preparation, per document.
CREATE UNIQUE INDEX document_revisions_one_current ON document_revisions (tenant_id, document_id) WHERE status = 'APPROVED';
CREATE UNIQUE INDEX document_revisions_one_open ON document_revisions (tenant_id, document_id) WHERE status IN ('DRAFT', 'SUBMITTED');

-- The file bytes live beside the revision, one row each, so that reading or
-- approving a revision never drags the file along. Written once, in the
-- transaction that creates the revision; never updated or deleted.
CREATE TABLE document_contents (
  revision_id uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  content     bytea NOT NULL,
  FOREIGN KEY (tenant_id, revision_id) REFERENCES document_revisions(tenant_id, id)
);
ALTER TABLE document_contents ALTER COLUMN content SET STORAGE EXTERNAL;

CREATE FUNCTION document_contents_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE v document_revisions;
BEGIN
  SELECT * INTO v FROM document_revisions WHERE id = NEW.revision_id AND tenant_id = current_tenant();
  IF v.id IS NULL OR v.status <> 'DRAFT' THEN
    RAISE EXCEPTION 'a file is stored only for a DRAFT revision' USING ERRCODE = 'check_violation';
  END IF;
  IF v.size_bytes <> octet_length(NEW.content) THEN
    RAISE EXCEPTION 'the declared size (%) is not the size of the file (%)', v.size_bytes, octet_length(NEW.content) USING ERRCODE = 'check_violation';
  END IF;
  IF v.sha256 <> encode(sha256(NEW.content), 'hex') THEN
    RAISE EXCEPTION 'the declared SHA-256 is not the hash of the file' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER document_contents_guard BEFORE INSERT ON document_contents FOR EACH ROW EXECUTE FUNCTION document_contents_guard();

-- A revision cannot be committed without its file.
CREATE FUNCTION document_revisions_has_file() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM document_contents WHERE revision_id = NEW.id AND tenant_id = NEW.tenant_id) THEN
    RAISE EXCEPTION 'a revision is stored together with its file' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER document_revisions_has_file AFTER INSERT ON document_revisions DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION document_revisions_has_file();

CREATE FUNCTION document_revisions_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.approved_at IS NOT NULL OR NEW.decision_reason IS NOT NULL OR NEW.superseded_at IS NOT NULL THEN
      RAISE EXCEPTION 'a revision is uploaded as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM documents WHERE id = NEW.document_id AND tenant_id = current_tenant()) THEN
      RAISE EXCEPTION 'the document of this revision does not exist' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'SUPERSEDED') THEN
    RAISE EXCEPTION 'a rejected or superseded revision never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'revision');
    PERFORM approval_use('document.submit', 'document_revision', NEW.id::text, NEW.created_by, ARRAY['document.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'revision');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a revision requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('document.decide', 'document_revision', NEW.id::text, NEW.approved_by, ARRAY['document.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'APPROVED' AND NEW.status = 'SUPERSEDED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'superseded_at'], 'revision');
    -- Only while an attested publication of another revision of the same
    -- document is being made in this transaction.
    IF NOT EXISTS (SELECT 1 FROM approval_attestations a JOIN document_revisions v ON v.id::text = a.subject_id
                    WHERE a.txid = txid_current() AND a.tenant_id = current_tenant() AND a.action = 'document.decide' AND a.ctx = 'APPROVED'
                      AND a.subject_type = 'document_revision' AND v.document_id = NEW.document_id AND v.id <> NEW.id) THEN
      RAISE EXCEPTION 'a revision is superseded only by the attested publication of its successor' USING ERRCODE = 'insufficient_privilege';
    END IF;
    NEW.superseded_at := now();
  ELSE
    RAISE EXCEPTION 'illegal revision transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER document_revisions_guard BEFORE INSERT OR UPDATE ON document_revisions FOR EACH ROW EXECUTE FUNCTION document_revisions_guard();

CREATE FUNCTION document_revisions_successor_check() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM document_revisions WHERE tenant_id = NEW.tenant_id AND document_id = NEW.document_id AND status = 'APPROVED') THEN
    RAISE EXCEPTION 'superseding a revision requires a published successor at commit' USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER document_revisions_successor_check AFTER UPDATE ON document_revisions DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.status = 'SUPERSEDED' AND OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION document_revisions_successor_check();

CREATE TABLE rfis (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id  uuid NOT NULL,
  number      text NOT NULL,
  subject     text NOT NULL CHECK (length(btrim(subject)) BETWEEN 3 AND 200),
  question    text NOT NULL CHECK (length(btrim(question)) BETWEEN 10 AND 4000),
  revision_id uuid,
  assigned_to text NOT NULL CHECK (assigned_to ~ '^[A-Za-z0-9._@:+|-]{1,200}$'),
  due_on      date NOT NULL,
  status      text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'ANSWERED', 'CLOSED')),
  answer      text,
  answered_by text,
  answered_at timestamptz,
  closed_by   text,
  closed_at   timestamptz,
  close_note  text,
  created_by  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  FOREIGN KEY (tenant_id, revision_id) REFERENCES document_revisions(tenant_id, id),
  CONSTRAINT rfi_answer_whole CHECK ((answer IS NULL AND answered_by IS NULL AND answered_at IS NULL)
    OR (answer IS NOT NULL AND answered_by IS NOT NULL AND answered_at IS NOT NULL AND length(btrim(answer)) BETWEEN 3 AND 4000)),
  CONSTRAINT rfi_not_self_answered CHECK (answered_by IS NULL OR answered_by <> created_by),
  CONSTRAINT rfi_close_whole CHECK ((status <> 'CLOSED' AND closed_by IS NULL AND closed_at IS NULL AND close_note IS NULL)
    OR (status = 'CLOSED' AND closed_by IS NOT NULL AND closed_at IS NOT NULL)),
  CONSTRAINT rfi_status_answer CHECK ((status = 'OPEN' AND answer IS NULL) OR (status = 'ANSWERED' AND answer IS NOT NULL) OR status = 'CLOSED')
);
CREATE INDEX rfis_project_status ON rfis (tenant_id, project_id, status, due_on);

CREATE FUNCTION rfis_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'OPEN' OR NEW.answer IS NOT NULL OR NEW.closed_by IS NOT NULL THEN
      RAISE EXCEPTION 'an RFI is raised OPEN' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.due_on < current_date THEN
      RAISE EXCEPTION 'the answer to an RFI cannot be due in the past' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.revision_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM document_revisions v JOIN documents d ON d.id = v.document_id
         WHERE v.id = NEW.revision_id AND v.tenant_id = current_tenant() AND d.project_id = NEW.project_id) THEN
      RAISE EXCEPTION 'the revision an RFI is about belongs to a document of the same project' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'CLOSED' THEN
    RAISE EXCEPTION 'a closed RFI never changes' USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'OPEN' AND NEW.status = 'ANSWERED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'answer', 'answered_by', 'answered_at'], 'RFI');
    -- An answer is a decision by a person: attested, never by the author.
    PERFORM approval_use('rfi.answer', 'rfi', NEW.id::text, NEW.answered_by, ARRAY['document.manage'], false, NULL, NULL);
    NEW.answered_at := now();
  ELSIF NEW.status = 'CLOSED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'closed_by', 'closed_at', 'close_note'], 'RFI');
    IF OLD.status = 'OPEN' AND (NEW.close_note IS NULL OR length(btrim(NEW.close_note)) < 5) THEN
      RAISE EXCEPTION 'withdrawing an unanswered RFI requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    NEW.closed_at := now();
  ELSE
    RAISE EXCEPTION 'illegal RFI transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER rfis_guard BEFORE INSERT OR UPDATE ON rfis FOR EACH ROW EXECUTE FUNCTION rfis_guard();

-- ---------------------------------------- shared functions (verbatim + 0052)
-- p2p_subject_text: verbatim from 0051 plus document revisions and RFIs (marked 0052).
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
  -- 0052: a document revision (with its register entry; the file is covered by its sha256) and an RFI.
  WHEN 'document_revision' THEN
    SELECT jsonb_build_object('h', to_jsonb(x), 'doc', (SELECT to_jsonb(d) FROM documents d WHERE d.id = x.document_id))
      INTO j FROM document_revisions x WHERE tenant_id = t AND id = p_id::uuid;
  WHEN 'rfi' THEN SELECT to_jsonb(x) INTO j FROM rfis x WHERE tenant_id = t AND id = p_id::uuid;
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0051 plus document revisions and RFIs (marked 0052).
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
  -- 0051: damages terms belong to their contract's project.
  WHEN 'ld_terms' THEN SELECT c.project_id::text INTO r FROM contract_ld_terms x JOIN contracts c ON c.id = x.contract_id WHERE x.id = v_id;
  -- 0052: a document revision belongs to its document's project; an RFI to its project.
  WHEN 'document_revision' THEN SELECT d.project_id::text INTO r FROM document_revisions x JOIN documents d ON d.id = x.document_id WHERE x.id = v_id;
  WHEN 'rfi' THEN SELECT project_id::text INTO r FROM rfis x WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['documents', 'document_revisions', 'document_contents', 'rfis'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
-- A register entry and a stored file never change: no UPDATE is granted on documents or document_contents.
GRANT UPDATE (status, approved_by, approved_at, decision_reason, superseded_at) ON document_revisions TO serk_app;
GRANT UPDATE (status, answer, answered_by, answered_at, closed_by, closed_at, close_note) ON rfis TO serk_app;
