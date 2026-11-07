-- 0040 Authority matrix, delegation of authority and segregation-of-duties exceptions
--      (WP-1.2; SRS IAM-09, IAM-13, IAM-14)
--
-- WHAT THIS MIGRATION DOES
--   Who may approve how much is a company policy, not code. Three governed
--   documents make it data:
--
--     authority_matrices + authority_rules
--         "A purchase order from 5,000,000 to 20,000,000 ETB needs the
--          approval.high_value permission (with fresh MFA)." One matrix is
--          APPROVED per company at a time; a new one retires the old one.
--     delegations
--         A person lends part of their approval authority to a colleague for a
--         limited time (leave, travel). Approved by a third person; the
--         delegator can revoke at any time. A delegate never exceeds the
--         delegator's own authority, and never approves what the delegator
--         prepared (the kernel enforces both).
--     sod_exceptions
--         A named person may hold two permissions that the company's
--         conflict rules (pkg/auth DefaultSoDConflicts) normally forbid, but
--         only with a written mitigating control, an expiry and approval.
--
--   All three go DRAFT -> SUBMITTED -> APPROVED | REJECTED, prepared by one
--   person and decided by another, with a signed attestation for every step
--   (approval_use, ADR 0008).
--
-- HOW LIMITS ARE ENFORCED
--   authority_check(doc type, amount, currency, permissions, fresh MFA) looks
--   up the approved matrix and says what is missing, or NULL when allowed.
--   * Budget documents: the database enforces it. The trigger
--     budget_zz_authority reads the permissions the decider's signed
--     attestation carries, so even raw SQL cannot approve above the limit.
--   * Requisitions, purchase orders, supplier invoices and payments: the
--     kernel calls the same function before it asks for the signature.
--   A company with no approved matrix has no limits beyond the roles
--   themselves (the behaviour before this migration).
--
-- SHARED FUNCTIONS REDEFINED HERE (verbatim from 0039, additions marked 0040):
--   p2p_subject_text, serk_subject_project

-- ------------------------------------------------------------ authority matrix
CREATE TABLE authority_matrices (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  version         integer NOT NULL CHECK (version >= 1),
  reason          text NOT NULL CHECK (length(btrim(reason)) BETWEEN 3 AND 500),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'RETIRED')),
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, version),
  CONSTRAINT authority_matrix_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
-- One matrix is in force per company.
CREATE UNIQUE INDEX authority_matrices_one_approved ON authority_matrices (tenant_id) WHERE status = 'APPROVED';

CREATE TABLE authority_rules (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  matrix_id     uuid NOT NULL,
  doc_type      text NOT NULL CHECK (doc_type IN ('budget_version', 'budget_transfer', 'contingency_drawdown',
                  'purchase_requisition', 'purchase_order', 'supplier_invoice', 'payment_instruction')),
  min_amount    numeric(24,6) NOT NULL CHECK (min_amount >= 0),         -- band is [min, max)
  max_amount    numeric(24,6) CHECK (max_amount IS NULL OR max_amount > min_amount),  -- NULL = no upper limit
  currency      char(3) NOT NULL REFERENCES currencies(code),
  required_perm text NOT NULL CHECK (length(required_perm) BETWEEN 3 AND 80 AND required_perm !~ '^(serk|principal)\.'),
  requires_mfa  boolean NOT NULL DEFAULT false,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, matrix_id) REFERENCES authority_matrices(tenant_id, id)
);
CREATE INDEX authority_rules_matrix ON authority_rules (tenant_id, matrix_id, doc_type);

-- ----------------------------------------------------------------- delegations
CREATE TABLE delegations (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  delegator       text NOT NULL,
  delegate        text NOT NULL,
  perms           text[] NOT NULL CHECK (cardinality(perms) BETWEEN 1 AND 30),
  valid_from      timestamptz NOT NULL,
  valid_to        timestamptz NOT NULL,
  reason          text NOT NULL CHECK (length(btrim(reason)) BETWEEN 5 AND 500),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'REVOKED')),
  created_by      text NOT NULL,                       -- always the delegator
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  revoked_by      text,
  revoked_at      timestamptz,
  UNIQUE (tenant_id, id),
  CONSTRAINT delegation_people CHECK (delegate <> delegator AND created_by = delegator),
  CONSTRAINT delegation_period CHECK (valid_to > valid_from AND valid_to <= valid_from + interval '90 days'),
  CONSTRAINT delegation_approver CHECK (approved_by IS NULL OR (approved_by <> delegator AND approved_by <> delegate)),
  CONSTRAINT delegation_perms_delegable CHECK (NOT (perms && ARRAY['scope.tenant_wide', 'identity.revoke_sessions', 'delegation.approve',
                  'authority.manage', 'authority.approve', 'budget.override', 'tenant.settings.manage'])
                  AND array_to_string(perms, ',') !~ '(^|,)(serk|principal)\.')
);
CREATE INDEX delegations_delegate_active ON delegations (tenant_id, delegate, status, valid_to);

-- -------------------------------------------------------------- SoD exceptions
CREATE TABLE sod_exceptions (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  user_id            text NOT NULL,
  perm_a             text NOT NULL,
  perm_b             text NOT NULL,
  mitigating_control text NOT NULL CHECK (length(btrim(mitigating_control)) BETWEEN 20 AND 1000),
  expires_at         timestamptz NOT NULL,
  status             text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by         text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  submitted_at       timestamptz,
  approved_by        text,
  approved_at        timestamptz,
  decision_reason    text,
  UNIQUE (tenant_id, id),
  CONSTRAINT sod_exception_pair CHECK (perm_a < perm_b),   -- stored in sorted order so a pair has one spelling
  CONSTRAINT sod_exception_checker CHECK (approved_by IS NULL OR (approved_by <> created_by AND approved_by <> user_id))
);
CREATE INDEX sod_exceptions_user ON sod_exceptions (tenant_id, user_id, status);

-- ------------------------------------------------------------- the limit check
-- Returns NULL when the permissions satisfy the approved matrix for this
-- document, otherwise a sentence saying what is missing. No matrix, or no
-- rule covering the amount, means no extra requirement.
CREATE FUNCTION authority_check(p_doc_type text, p_amount numeric, p_currency text, p_perms text[], p_mfa_fresh boolean)
RETURNS text LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE r authority_rules;
BEGIN
  SELECT ru.* INTO r FROM authority_rules ru JOIN authority_matrices m ON m.id = ru.matrix_id
   WHERE m.tenant_id = current_tenant() AND m.status = 'APPROVED' AND ru.doc_type = p_doc_type AND ru.currency = p_currency
     AND p_amount >= ru.min_amount AND (ru.max_amount IS NULL OR p_amount < ru.max_amount)
   ORDER BY ru.min_amount DESC LIMIT 1;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  IF NOT (r.required_perm = ANY (p_perms)) THEN
    RETURN format('the authority matrix requires %s to approve a %s of %s %s (band from %s%s)', r.required_perm, replace(p_doc_type, '_', ' '),
                  p_amount, p_currency, r.min_amount, COALESCE(' to below ' || r.max_amount, ' and above'));
  END IF;
  IF r.requires_mfa AND NOT COALESCE(p_mfa_fresh, false) THEN
    RETURN format('the authority matrix requires a fresh multi-factor sign-in for a %s of %s %s', replace(p_doc_type, '_', ' '), p_amount, p_currency);
  END IF;
  RETURN NULL;
END$$;

-- The amount and currency of a document, for limit checks (NULL row = unknown type).
CREATE FUNCTION authority_doc_amount(p_type text, p_id uuid, OUT amount numeric, OUT currency text)
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  CASE p_type
  WHEN 'purchase_requisition' THEN
    SELECT COALESCE((SELECT sum(l.est_amount) FROM pr_lines l WHERE l.pr_id = x.id), 0), x.currency INTO amount, currency
      FROM purchase_requisitions x WHERE x.id = p_id AND x.tenant_id = current_tenant();
  WHEN 'purchase_order' THEN SELECT x.total, x.currency INTO amount, currency FROM purchase_orders x WHERE x.id = p_id AND x.tenant_id = current_tenant();
  WHEN 'supplier_invoice' THEN SELECT x.total, x.currency INTO amount, currency FROM supplier_invoices x WHERE x.id = p_id AND x.tenant_id = current_tenant();
  WHEN 'payment_instruction' THEN SELECT x.amount, x.currency INTO amount, currency FROM payment_instructions x WHERE x.id = p_id AND x.tenant_id = current_tenant();
  WHEN 'budget_transfer' THEN SELECT x.amount, x.currency INTO amount, currency FROM budget_transfers x WHERE x.id = p_id AND x.tenant_id = current_tenant();
  WHEN 'contingency_drawdown' THEN SELECT x.amount, x.currency INTO amount, currency FROM contingency_drawdowns x WHERE x.id = p_id AND x.tenant_id = current_tenant();
  WHEN 'budget_version' THEN
    SELECT COALESCE(sum(abs(l.amount)), 0), min(l.currency) INTO amount, currency FROM budget_lines l WHERE l.version_id = p_id AND l.tenant_id = current_tenant();
  ELSE
    amount := NULL; currency := NULL;
  END CASE;
END$$;

-- Budget approvals are limit-checked by the database, from the permissions
-- the decider's signed attestation carries. Runs after budget_*_guard
-- (triggers fire in name order) so the attestation is already recorded.
CREATE FUNCTION budget_authority_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE
  doc text := CASE TG_TABLE_NAME WHEN 'budget_versions' THEN 'budget_version' WHEN 'budget_transfers' THEN 'budget_transfer' ELSE 'contingency_drawdown' END;
  act text := CASE TG_TABLE_NAME WHEN 'budget_versions' THEN 'budget.revision.decide' WHEN 'budget_transfers' THEN 'budget.transfer.decide' ELSE 'contingency.drawdown.decide' END;
  att approval_attestations; amt numeric; ccy text; msg text;
BEGIN
  SELECT * INTO att FROM approval_attestations a
   WHERE a.txid = txid_current() AND a.tenant_id = current_tenant() AND a.action = act AND a.subject_type = doc
     AND a.subject_id = NEW.id::text AND a.ctx = 'APPROVED' ORDER BY a.recorded_at DESC LIMIT 1;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'approving a % needs its signed attestation', replace(doc, '_', ' ') USING ERRCODE = 'insufficient_privilege';
  END IF;
  SELECT d.amount, d.currency INTO amt, ccy FROM authority_doc_amount(doc, NEW.id) d;
  msg := authority_check(doc, amt, ccy, att.perms, att.mfa_fresh);
  IF msg IS NOT NULL THEN
    RAISE EXCEPTION '%', msg USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER budget_zz_authority BEFORE UPDATE ON budget_versions FOR EACH ROW
  WHEN (OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED') EXECUTE FUNCTION budget_authority_guard();
CREATE TRIGGER budget_zz_authority BEFORE UPDATE ON budget_transfers FOR EACH ROW
  WHEN (OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED') EXECUTE FUNCTION budget_authority_guard();
CREATE TRIGGER budget_zz_authority BEFORE UPDATE ON contingency_drawdowns FOR EACH ROW
  WHEN (OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED') EXECUTE FUNCTION budget_authority_guard();

-- ------------------------------------------------------------- state guards
CREATE FUNCTION authority_matrices_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'authority matrices are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'RETIRED') THEN
    RAISE EXCEPTION 'a % authority matrix is immutable', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status = 'APPROVED' AND NEW.status = 'RETIRED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status'], 'authority matrix');
    -- Retired only by the attested approval of its successor, in this transaction.
    IF NOT EXISTS (SELECT 1 FROM approval_attestations a JOIN authority_matrices m ON m.id::text = a.subject_id
                    WHERE a.txid = txid_current() AND a.tenant_id = current_tenant() AND a.action = 'authority.matrix.decide' AND a.ctx = 'APPROVED'
                      AND m.id <> NEW.id) THEN
      RAISE EXCEPTION 'an authority matrix is retired only by the attested approval of its successor' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status = 'APPROVED' THEN
    RAISE EXCEPTION 'an approved authority matrix is immutable; propose a new version' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), '{}', 'authority matrix');
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'authority matrix');
    IF NOT EXISTS (SELECT 1 FROM authority_rules WHERE matrix_id = NEW.id) THEN
      RAISE EXCEPTION 'an authority matrix needs at least one rule' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('authority.matrix.submit', 'authority_matrix', NEW.id::text, NEW.created_by, ARRAY['authority.manage'], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'authority matrix');
    PERFORM approval_use('authority.matrix.decide', 'authority_matrix', NEW.id::text, NEW.approved_by, ARRAY['authority.approve'], true, 'APPROVED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'authority matrix');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting an authority matrix requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('authority.matrix.decide', 'authority_matrix', NEW.id::text, NEW.approved_by, ARRAY['authority.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal authority matrix transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER authority_matrices_guard BEFORE INSERT OR UPDATE ON authority_matrices FOR EACH ROW EXECUTE FUNCTION authority_matrices_guard();

-- At commit, a retirement must leave an approved successor.
CREATE FUNCTION authority_matrices_successor_check() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM authority_matrices WHERE tenant_id = NEW.tenant_id AND status = 'APPROVED') THEN
    RAISE EXCEPTION 'retiring an authority matrix requires an approved successor at commit' USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER authority_matrices_successor_check AFTER UPDATE ON authority_matrices DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.status = 'RETIRED' AND OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION authority_matrices_successor_check();

CREATE FUNCTION authority_rules_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE m authority_matrices;
BEGIN
  SELECT * INTO m FROM authority_matrices WHERE id = NEW.matrix_id AND tenant_id = current_tenant() FOR SHARE;
  IF m.status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'authority rules are added only while the matrix is DRAFT' USING ERRCODE = 'check_violation';
  END IF;
  -- Bands of one document type and currency must not overlap (a band is [min, max)).
  IF EXISTS (SELECT 1 FROM authority_rules r WHERE r.matrix_id = NEW.matrix_id AND r.doc_type = NEW.doc_type AND r.currency = NEW.currency
              AND r.id IS DISTINCT FROM NEW.id
              AND NEW.min_amount < COALESCE(r.max_amount, 'Infinity'::numeric) AND r.min_amount < COALESCE(NEW.max_amount, 'Infinity'::numeric)) THEN
    RAISE EXCEPTION 'authority bands for % in % overlap', replace(NEW.doc_type, '_', ' '), NEW.currency USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER authority_rules_guard BEFORE INSERT ON authority_rules FOR EACH ROW EXECUTE FUNCTION authority_rules_guard();

CREATE FUNCTION delegations_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL OR NEW.revoked_at IS NOT NULL THEN
      RAISE EXCEPTION 'delegations are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.valid_to <= now() THEN
      RAISE EXCEPTION 'a delegation must end in the future' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('REJECTED', 'REVOKED') THEN
    RAISE EXCEPTION 'a % delegation is immutable', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), '{}', 'delegation');
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'delegation');
    PERFORM approval_use('delegation.submit', 'delegation', NEW.id::text, NEW.created_by, ARRAY['delegation.create'], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'delegation');
    IF NEW.valid_to <= now() THEN
      RAISE EXCEPTION 'a delegation that has already ended cannot be approved' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('delegation.decide', 'delegation', NEW.id::text, NEW.approved_by, ARRAY['delegation.approve'], true, 'APPROVED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'delegation');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a delegation requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('delegation.decide', 'delegation', NEW.id::text, NEW.approved_by, ARRAY['delegation.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSIF NEW.status = 'REVOKED' AND OLD.status IN ('SUBMITTED', 'APPROVED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'revoked_by', 'revoked_at', 'decision_reason'], 'delegation');
    IF NEW.revoked_by IS NULL THEN
      RAISE EXCEPTION 'a revocation records who revoked' USING ERRCODE = 'check_violation';
    END IF;
    -- The delegator may always revoke; anyone else needs the approver's authority.
    PERFORM approval_use('delegation.revoke', 'delegation', NEW.id::text, NEW.revoked_by,
                         CASE WHEN NEW.revoked_by = OLD.delegator THEN ARRAY['delegation.create'] ELSE ARRAY['delegation.approve'] END, false, NULL, NULL);
    NEW.revoked_at := now();
  ELSE
    RAISE EXCEPTION 'illegal delegation transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER delegations_guard BEFORE INSERT OR UPDATE ON delegations FOR EACH ROW EXECUTE FUNCTION delegations_guard();

CREATE FUNCTION sod_exceptions_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'SoD exceptions are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.expires_at <= now() OR NEW.expires_at > now() + interval '366 days' THEN
      RAISE EXCEPTION 'an SoD exception must expire within one year' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided SoD exception is immutable' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), '{}', 'SoD exception');
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'SoD exception');
    PERFORM approval_use('sod.exception.submit', 'sod_exception', NEW.id::text, NEW.created_by, ARRAY['authority.manage'], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'SoD exception');
    PERFORM approval_use('sod.exception.decide', 'sod_exception', NEW.id::text, NEW.approved_by, ARRAY['authority.approve'], true, 'APPROVED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'SoD exception');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting an SoD exception requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('sod.exception.decide', 'sod_exception', NEW.id::text, NEW.approved_by, ARRAY['authority.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal SoD exception transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER sod_exceptions_guard BEFORE INSERT OR UPDATE ON sod_exceptions FOR EACH ROW EXECUTE FUNCTION sod_exceptions_guard();

-- ---------------------------------------- shared functions (verbatim + 0040)
-- p2p_subject_text: verbatim from 0039 plus the governance documents (marked 0040).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0039; governance documents are company-level (no project).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception') THEN
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
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['authority_matrices', 'authority_rules', 'delegations', 'sod_exceptions'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, submitted_at, approved_by, approved_at, decision_reason) ON authority_matrices, sod_exceptions TO serk_app;
GRANT UPDATE (status, submitted_at, approved_by, approved_at, decision_reason, revoked_by, revoked_at) ON delegations TO serk_app;
REVOKE ALL ON FUNCTION authority_check(text, numeric, text, text[], boolean), authority_doc_amount(text, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION authority_check(text, numeric, text, text[], boolean), authority_doc_amount(text, uuid) TO serk_app;
