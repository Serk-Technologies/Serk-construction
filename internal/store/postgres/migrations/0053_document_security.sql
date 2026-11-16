-- 0053 Document security: malware-scan state and quarantine, object-storage
-- references, per-tenant document policy (requirements CNX-01.4, CNX-01.5,
-- CNX-01.7 onwards; ADR 0019)
--
-- WHAT THIS MIGRATION DOES
--   * SCAN STATE on every revision: NOT_SCANNED, PENDING, CLEAN, INFECTED or
--     ERROR, with the engine that said so and a proof (a keyed digest only the
--     API can compute, so a verdict written by raw SQL is recognisable).
--     CLEAN and INFECTED are final.
--   * FAIL CLOSED. A tenant's document policy says whether scanning is
--     required; it IS required unless a person with tenant.settings.manage
--     turns it off with a fresh multi-factor sign-in and a reason (attested).
--     While it is required, a revision that is not CLEAN can be neither
--     submitted nor published. An INFECTED revision can never be submitted or
--     published, whatever the policy; its author can only discard it.
--     Existing revisions become NOT_SCANNED: they stay as they are, and are
--     scanned by a rescan before anything further is done with them.
--   * OBJECT STORAGE. A revision's bytes live either in this database (as
--     before, at most 10 MiB) or in an object store. The database fixes the
--     only key an object may have: t/<tenant>/rev/<revision>/<sha256>, so a
--     reference is tenant-scoped and can never be repointed.
--   * MIGRATION OF EXISTING FILES in two steps: the application copies a file
--     to the object store, reads it back, compares the hash and records the
--     key (the bytes stay here); only the schema owner, in a maintenance
--     session, may then release the bytes. Nothing a tenant login can do
--     removes a file.
--   * SIZE. size_bytes becomes bigint with no upper bound: the limit on a file
--     is a setting of the deployment, not of the schema.
--   * QUOTA. An optional per-tenant ceiling on the total bytes of documents.
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0052, additions marked 0053):
--   p2p_subject_text, serk_subject_project

CREATE TABLE document_policies (
  tenant_id     uuid PRIMARY KEY DEFAULT current_tenant() REFERENCES tenants(id),
  scan_required boolean NOT NULL DEFAULT true,
  quota_bytes   bigint CHECK (quota_bytes IS NULL OR quota_bytes > 0),
  reason        text NOT NULL CHECK (length(btrim(reason)) BETWEEN 10 AND 500),
  updated_by    text NOT NULL,
  updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Scanning is required unless the tenant's policy says otherwise.
CREATE FUNCTION document_scan_required() RETURNS boolean
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT COALESCE((SELECT scan_required FROM document_policies WHERE tenant_id = current_tenant()), true)
$$;

CREATE FUNCTION document_policies_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND NEW.tenant_id <> OLD.tenant_id THEN
    RAISE EXCEPTION 'a document policy belongs to its tenant' USING ERRCODE = 'check_violation';
  END IF;
  -- A human decision with a fresh second factor, attested against the policy in force.
  PERFORM approval_use('document.policy.decide', 'document_policy', NEW.tenant_id::text, NEW.updated_by, ARRAY['tenant.settings.manage'], true, NULL, NULL);
  NEW.updated_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER document_policies_guard BEFORE INSERT OR UPDATE ON document_policies FOR EACH ROW EXECUTE FUNCTION document_policies_guard();

-- ------------------------------------------------------------ revisions ---
ALTER TABLE document_revisions DROP CONSTRAINT document_revisions_size_bytes_check;
ALTER TABLE document_revisions ALTER COLUMN size_bytes TYPE bigint;
ALTER TABLE document_revisions ADD CONSTRAINT document_revisions_size_positive CHECK (size_bytes >= 1);
ALTER TABLE document_revisions
  ADD COLUMN scan_status text NOT NULL DEFAULT 'NOT_SCANNED' CHECK (scan_status IN ('NOT_SCANNED', 'PENDING', 'CLEAN', 'INFECTED', 'ERROR')),
  ADD COLUMN scan_engine text CHECK (scan_engine IS NULL OR length(scan_engine) BETWEEN 1 AND 300),
  ADD COLUMN scan_detail text CHECK (scan_detail IS NULL OR length(scan_detail) <= 500),
  ADD COLUMN scan_proof  text CHECK (scan_proof IS NULL OR scan_proof ~ '^[0-9a-f]{64}$'),
  ADD COLUMN scanned_at  timestamptz,
  ADD CONSTRAINT revision_scan_whole CHECK (
    (scan_status IN ('NOT_SCANNED', 'PENDING') AND scan_engine IS NULL AND scan_proof IS NULL AND scanned_at IS NULL)
    OR (scan_status = 'ERROR' AND scanned_at IS NOT NULL AND scan_proof IS NULL)
    OR (scan_status IN ('CLEAN', 'INFECTED') AND scan_engine IS NOT NULL AND scan_proof IS NOT NULL AND scanned_at IS NOT NULL));
-- An infected draft does not hold the document's one slot for a revision in preparation.
DROP INDEX document_revisions_one_open;
CREATE UNIQUE INDEX document_revisions_one_open ON document_revisions (tenant_id, document_id) WHERE status IN ('DRAFT', 'SUBMITTED') AND scan_status <> 'INFECTED';
CREATE INDEX document_revisions_scan ON document_revisions (tenant_id, scan_status) WHERE scan_status <> 'CLEAN';

-- Verbatim from 0052 with the scan rules (marked 0053).
CREATE OR REPLACE FUNCTION document_revisions_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE scan_cols text[] := ARRAY['scan_status', 'scan_engine', 'scan_detail', 'scan_proof', 'scanned_at'];
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.approved_at IS NOT NULL OR NEW.decision_reason IS NOT NULL OR NEW.superseded_at IS NOT NULL THEN
      RAISE EXCEPTION 'a revision is uploaded as DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM documents WHERE id = NEW.document_id AND tenant_id = current_tenant()) THEN
      RAISE EXCEPTION 'the document of this revision does not exist' USING ERRCODE = 'check_violation';
    END IF;
    -- 0053: a revision arrives unscanned, or already quarantined (which only restricts it).
    IF NEW.scan_status NOT IN ('NOT_SCANNED', 'PENDING', 'INFECTED') THEN
      RAISE EXCEPTION 'a revision is uploaded unscanned or quarantined; a clean verdict is recorded afterwards' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.scan_status = 'INFECTED' THEN
      NEW.scanned_at := now();
    END IF;
    RETURN NEW;
  END IF;
  -- 0053: recording a scan outcome changes nothing else and never overturns a verdict.
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), scan_cols, 'revision');
    IF OLD.scan_status IN ('CLEAN', 'INFECTED') THEN
      RAISE EXCEPTION 'a scan verdict is final' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.scan_status = 'NOT_SCANNED' THEN
      RAISE EXCEPTION 'a scan outcome is PENDING, CLEAN, INFECTED or ERROR' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.scan_status <> 'PENDING' THEN
      NEW.scanned_at := now();
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'SUPERSEDED') THEN
    RAISE EXCEPTION 'a rejected or superseded revision never changes' USING ERRCODE = 'check_violation';
  END IF;
  -- 0053: quarantine. Infected files go nowhere; unscanned files go nowhere while scanning is required.
  IF NEW.status IN ('SUBMITTED', 'APPROVED') THEN
    IF OLD.scan_status = 'INFECTED' THEN
      RAISE EXCEPTION 'this revision is quarantined: the scanner found %', COALESCE(OLD.scan_detail, 'malware') USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF document_scan_required() AND OLD.scan_status <> 'CLEAN' THEN
      RAISE EXCEPTION 'this revision has not been scanned clean (scan status %); scanning is required', OLD.scan_status USING ERRCODE = 'insufficient_privilege';
    END IF;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'revision');
    PERFORM approval_use('document.submit', 'document_revision', NEW.id::text, NEW.created_by, ARRAY['document.manage'], false, NULL, NULL);
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'REJECTED' THEN
    -- 0053: an infected draft is discarded (it was never submitted, so there is no decision to attest).
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'decision_reason'], 'revision');
    IF OLD.scan_status <> 'INFECTED' THEN
      RAISE EXCEPTION 'only a quarantined draft is discarded; other drafts are submitted and decided' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'discarding a revision requires a reason' USING ERRCODE = 'check_violation';
    END IF;
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status IN ('APPROVED', 'REJECTED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'revision');
    IF NEW.status = 'REJECTED' AND (NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5) THEN
      RAISE EXCEPTION 'rejecting a revision requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('document.decide', 'document_revision', NEW.id::text, NEW.approved_by, ARRAY['document.approve'], NEW.status = 'APPROVED', NEW.status, NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'APPROVED' AND NEW.status = 'SUPERSEDED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'superseded_at'], 'revision');
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

-- ------------------------------------------------------------- contents ---
ALTER TABLE document_contents ALTER COLUMN content DROP NOT NULL;
ALTER TABLE document_contents
  ADD COLUMN storage            text NOT NULL DEFAULT 'postgres' CHECK (storage IN ('postgres', 'object')),
  ADD COLUMN object_key         text,
  ADD COLUMN object_store       text CHECK (object_store IS NULL OR object_store ~ '^[a-z0-9]{1,20}$'),
  ADD COLUMN object_verified_at timestamptz,
  ADD CONSTRAINT contents_where CHECK (
    (storage = 'postgres' AND content IS NOT NULL AND (object_key IS NULL) = (object_verified_at IS NULL))
    OR (storage = 'object' AND content IS NULL AND object_key IS NOT NULL AND object_store IS NOT NULL AND object_verified_at IS NOT NULL));

-- Verbatim from 0052 with object references and the two-step release (marked 0053).
CREATE OR REPLACE FUNCTION document_contents_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE v document_revisions; want text;
BEGIN
  SELECT * INTO v FROM document_revisions WHERE id = NEW.revision_id AND tenant_id = NEW.tenant_id;
  IF v.id IS NULL THEN
    RAISE EXCEPTION 'a file belongs to a revision' USING ERRCODE = 'check_violation';
  END IF;
  -- 0053: the one key an object of this revision may have.
  want := 't/' || v.tenant_id || '/rev/' || v.id || '/' || v.sha256;
  IF NEW.object_key IS NOT NULL AND NEW.object_key <> want THEN
    RAISE EXCEPTION 'an object reference is fixed by tenant, revision and content hash' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF TG_OP = 'INSERT' THEN
    IF v.status <> 'DRAFT' THEN
      RAISE EXCEPTION 'a file is stored only for a DRAFT revision' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.storage = 'postgres' THEN
      IF NEW.object_key IS NOT NULL THEN
        RAISE EXCEPTION 'a file is stored in one place when it is uploaded' USING ERRCODE = 'check_violation';
      END IF;
      IF octet_length(NEW.content) > 10485760 THEN
        RAISE EXCEPTION 'files over 10 MiB are kept in object storage' USING ERRCODE = 'check_violation';
      END IF;
      IF v.size_bytes <> octet_length(NEW.content) THEN
        RAISE EXCEPTION 'the declared size (%) is not the size of the file (%)', v.size_bytes, octet_length(NEW.content) USING ERRCODE = 'check_violation';
      END IF;
      IF v.sha256 <> encode(sha256(NEW.content), 'hex') THEN
        RAISE EXCEPTION 'the declared SHA-256 is not the hash of the file' USING ERRCODE = 'check_violation';
      END IF;
    ELSE
      NEW.object_verified_at := now();
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.revision_id <> OLD.revision_id OR NEW.tenant_id <> OLD.tenant_id OR OLD.storage = 'object' THEN
    RAISE EXCEPTION 'a stored file never changes' USING ERRCODE = 'insufficient_privilege';
  END IF;
  IF NEW.storage = 'postgres' THEN
    -- Step 1 (application): the verified copy in the object store is recorded; the bytes stay.
    IF NEW.content IS DISTINCT FROM OLD.content OR OLD.object_key IS NOT NULL OR NEW.object_key IS NULL OR NEW.object_store IS NULL THEN
      RAISE EXCEPTION 'a stored file changes only by recording its verified copy in the object store, once' USING ERRCODE = 'insufficient_privilege';
    END IF;
    NEW.object_verified_at := now();
  ELSE
    -- Step 2 (schema owner, maintenance session): the bytes are released.
    IF NOT serk_maintenance_session() THEN
      RAISE EXCEPTION 'only the schema owner releases a migrated file from the database' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF OLD.object_key IS NULL OR NEW.object_key IS DISTINCT FROM OLD.object_key OR NEW.object_store IS DISTINCT FROM OLD.object_store
       OR NEW.object_verified_at IS DISTINCT FROM OLD.object_verified_at OR NEW.content IS NOT NULL THEN
      RAISE EXCEPTION 'a file is released only after its copy in the object store was verified' USING ERRCODE = 'insufficient_privilege';
    END IF;
  END IF;
  RETURN NEW;
END$$;
DROP TRIGGER document_contents_guard ON document_contents;
CREATE TRIGGER document_contents_guard BEFORE INSERT OR UPDATE ON document_contents FOR EACH ROW EXECUTE FUNCTION document_contents_guard();

-- ---------------------------------------- shared functions (verbatim + 0053)
-- p2p_subject_text: verbatim from 0052 plus the tenant document policy (marked 0053).
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
  -- 0053: the tenant's document policy. Its id is the tenant id; a tenant with no row has the default policy.
  WHEN 'document_policy' THEN
    IF p_id::uuid <> t THEN RETURN NULL; END IF;
    SELECT COALESCE((SELECT to_jsonb(x) FROM document_policies x WHERE x.tenant_id = t), jsonb_build_object('scan_required', true, 'quota_bytes', NULL, 'default', true)) INTO j;
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0052 plus the tenant document policy (marked 0053).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'document_policy') THEN
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
DECLARE owner text := current_user;
BEGIN
  ALTER TABLE document_policies ENABLE ROW LEVEL SECURITY;
  ALTER TABLE document_policies FORCE ROW LEVEL SECURITY;
  CREATE POLICY tenant_isolation ON document_policies USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
  EXECUTE format('CREATE POLICY owner_maintenance ON document_policies TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', owner);
  REVOKE ALL ON document_policies FROM PUBLIC;
  GRANT SELECT, INSERT ON document_policies TO serk_app;
END$$;
GRANT UPDATE (scan_required, quota_bytes, reason, updated_by, updated_at) ON document_policies TO serk_app;
GRANT UPDATE (scan_status, scan_engine, scan_detail, scan_proof, scanned_at) ON document_revisions TO serk_app;
-- Step 1 of the storage migration only; content and storage are not writable by the application.
GRANT UPDATE (object_key, object_store, object_verified_at) ON document_contents TO serk_app;
REVOKE ALL ON FUNCTION document_scan_required() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION document_scan_required() TO serk_app;
