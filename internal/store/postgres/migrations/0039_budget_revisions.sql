-- 0039 Budget revisions, transfers and contingency drawdowns (WP-1.3, SRS PCC-01, PCC-02, EST-07; invariant R8)
--
-- WHAT THIS MIGRATION DOES
--   A project's budget used to be only its approved BOQ. Real projects change:
--   scope is added, money moves between cost codes, and contingency is spent.
--   This migration makes every such change a controlled document:
--
--     budget_versions + budget_lines   ORIGINAL baseline (at most one approved per
--                                      project) and REVISIONs (+/- amounts per code)
--     budget_transfers                 move money from one cost code to another
--     contingency_drawdowns            move money out of a contingency cost code
--
--   Each is DRAFT -> SUBMITTED -> APPROVED | REJECTED, prepared by one person and
--   decided by another, with a signed attestation for every transition
--   (approval_use, ADR 0008). Approved documents never change; the current
--   budget is always computed, never edited:
--
--     current = original + approved revisions + transfers in - transfers out
--               + drawdowns in - drawdowns out
--
--   (project_budget_current). When a project has no approved ORIGINAL budget
--   version, its approved BOQ is the original, exactly as before this migration.
--
-- WHO MAY DO WHAT (pkg/auth seed roles)
--   budget.revise    project_manager      prepares revisions and drawdowns
--   budget.transfer  project_manager      prepares transfers
--   budget.approve   commercial_manager,  decides all three (never their own)
--                    cfo
--
-- GUARDS THE DATABASE ENFORCES (even against raw SQL as the tenant login)
--   * legal transitions only; content frozen once submitted; decided rows immutable;
--   * maker differs from checker (CHECK + trigger + attestation);
--   * lines and movements use ACTIVE cost codes of the same project's approved WBS;
--   * one approved ORIGINAL per project; a REVISION needs a baseline to revise;
--   * a transfer or drawdown never takes a code below what is already committed,
--     reserved or spent on it (p2p_cost_position.available);
--   * a drawdown comes only from a code that holds contingency;
--   * movements in one project are serialised by locking the project row, so two
--     approvals cannot both spend the same headroom (the kernel takes the same lock
--     before requisition and purchase-order approvals).
--
-- SHARED FUNCTIONS REDEFINED HERE (copied verbatim from their latest definition,
-- with only the budget additions marked "0039"):
--   p2p_cost_position (from 0030)   budget now = project_budget_current
--   p2p_subject_text (from 0037)    attestation digest for the three new documents
--   serk_subject_project (from 0037) project of the three new documents (scope checks)
--   p2p_project_cost_report (0013)  adds original and current columns

-- ------------------------------------------------------------ budget versions
CREATE TABLE budget_versions (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id            uuid NOT NULL,
  kind                  text NOT NULL CHECK (kind IN ('ORIGINAL', 'REVISION')),
  seq                   integer NOT NULL CHECK (seq >= 1),           -- 1, 2, 3 ... per project, for people
  source_boq_version_id uuid REFERENCES boq_versions(id),            -- optional: the BOQ it was derived from
  reason                text NOT NULL CHECK (length(btrim(reason)) BETWEEN 3 AND 500),
  status                text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  created_by            text NOT NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  submitted_at          timestamptz,
  approved_by           text,                                        -- the decider (approve or reject)
  approved_at           timestamptz,
  decision_reason       text,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, project_id, seq),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  CONSTRAINT budget_version_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE INDEX budget_versions_proj_status ON budget_versions (tenant_id, project_id, status);
-- A project has one approved baseline; later changes are REVISIONs.
CREATE UNIQUE INDEX budget_versions_one_original ON budget_versions (tenant_id, project_id)
  WHERE kind = 'ORIGINAL' AND status = 'APPROVED';

CREATE TABLE budget_lines (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  version_id       uuid NOT NULL,
  cost_code_id     uuid NOT NULL,
  amount           numeric(24,6) NOT NULL CHECK (amount <> 0),       -- REVISION lines may be negative (a reduction)
  currency         char(3) NOT NULL REFERENCES currencies(code),
  contingency_flag boolean NOT NULL DEFAULT false,                   -- this code holds contingency (drawdowns come from it)
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, version_id, cost_code_id),
  FOREIGN KEY (tenant_id, version_id) REFERENCES budget_versions(tenant_id, id),
  FOREIGN KEY (tenant_id, cost_code_id) REFERENCES cost_codes(tenant_id, id)
);
CREATE INDEX budget_lines_cost_code ON budget_lines (tenant_id, cost_code_id);

-- ------------------------------------------------------------ budget transfers
CREATE TABLE budget_transfers (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id      uuid NOT NULL,
  from_cost_code  uuid NOT NULL,
  to_cost_code    uuid NOT NULL,
  amount          numeric(24,6) NOT NULL CHECK (amount > 0),
  currency        char(3) NOT NULL REFERENCES currencies(code),
  reason          text NOT NULL CHECK (length(btrim(reason)) BETWEEN 3 AND 500),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  requested_by    text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  FOREIGN KEY (tenant_id, from_cost_code) REFERENCES cost_codes(tenant_id, id),
  FOREIGN KEY (tenant_id, to_cost_code) REFERENCES cost_codes(tenant_id, id),
  CONSTRAINT budget_transfer_distinct_codes CHECK (from_cost_code <> to_cost_code),
  CONSTRAINT budget_transfer_maker_checker CHECK (approved_by IS NULL OR approved_by <> requested_by)
);
CREATE INDEX budget_transfers_proj ON budget_transfers (tenant_id, project_id, status);

-- ------------------------------------------------------ contingency drawdowns
CREATE TABLE contingency_drawdowns (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  project_id      uuid NOT NULL,
  from_cost_code  uuid NOT NULL,                                     -- the contingency code drawn from
  to_cost_code    uuid NOT NULL,                                     -- the code that receives the money
  amount          numeric(24,6) NOT NULL CHECK (amount > 0),
  currency        char(3) NOT NULL REFERENCES currencies(code),
  reason          text NOT NULL CHECK (length(btrim(reason)) BETWEEN 3 AND 500),
  status          text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED')),
  requested_by    text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  approved_by     text,
  approved_at     timestamptz,
  decision_reason text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects(tenant_id, id),
  FOREIGN KEY (tenant_id, from_cost_code) REFERENCES cost_codes(tenant_id, id),
  FOREIGN KEY (tenant_id, to_cost_code) REFERENCES cost_codes(tenant_id, id),
  CONSTRAINT contingency_drawdown_distinct_codes CHECK (from_cost_code <> to_cost_code),
  CONSTRAINT contingency_drawdown_maker_checker CHECK (approved_by IS NULL OR approved_by <> requested_by)
);
CREATE INDEX contingency_drawdowns_proj ON contingency_drawdowns (tenant_id, project_id, status);

-- ------------------------------------------------- project_budget_current view
-- security_invoker: the view runs with the caller's rights, so row-level
-- security on every underlying table applies (a tenant sees only its rows).
CREATE VIEW project_budget_current WITH (security_invoker = true) AS
WITH has_original AS (
  SELECT DISTINCT tenant_id, project_id FROM budget_versions WHERE kind = 'ORIGINAL' AND status = 'APPROVED'
),
orig AS (
  -- The approved ORIGINAL budget version ...
  SELECT bv.tenant_id, bv.project_id, bl.cost_code_id, bl.amount, bl.contingency_flag
    FROM budget_lines bl JOIN budget_versions bv ON bv.id = bl.version_id
   WHERE bv.status = 'APPROVED' AND bv.kind = 'ORIGINAL'
  UNION ALL
  -- ... or, when there is none, the approved BOQ (the behaviour before 0039).
  SELECT bi.tenant_id, bv.project_id, bi.cost_code_id, sum(bi.amount), false
    FROM boq_items bi JOIN boq_versions bv ON bv.id = bi.version_id
   WHERE bv.status = 'APPROVED'
     AND NOT EXISTS (SELECT 1 FROM has_original h WHERE h.tenant_id = bi.tenant_id AND h.project_id = bv.project_id)
   GROUP BY bi.tenant_id, bv.project_id, bi.cost_code_id
),
revs AS (
  SELECT bv.tenant_id, bv.project_id, bl.cost_code_id, sum(bl.amount) AS amount, bool_or(bl.contingency_flag) AS contingency_flag
    FROM budget_lines bl JOIN budget_versions bv ON bv.id = bl.version_id
   WHERE bv.status = 'APPROVED' AND bv.kind = 'REVISION'
   GROUP BY bv.tenant_id, bv.project_id, bl.cost_code_id
),
moves AS (
  -- Every approved movement as two signed legs: (code, +in) and (code, -out).
  SELECT tenant_id, project_id, to_cost_code AS cost_code_id, amount AS xin, 0::numeric AS xout, 0::numeric AS din, 0::numeric AS dout FROM budget_transfers WHERE status = 'APPROVED'
  UNION ALL SELECT tenant_id, project_id, from_cost_code, 0, amount, 0, 0 FROM budget_transfers WHERE status = 'APPROVED'
  UNION ALL SELECT tenant_id, project_id, to_cost_code, 0, 0, amount, 0 FROM contingency_drawdowns WHERE status = 'APPROVED'
  UNION ALL SELECT tenant_id, project_id, from_cost_code, 0, 0, 0, amount FROM contingency_drawdowns WHERE status = 'APPROVED'
),
mv AS (
  SELECT tenant_id, project_id, cost_code_id, sum(xin) AS xin, sum(xout) AS xout, sum(din) AS din, sum(dout) AS dout
    FROM moves GROUP BY tenant_id, project_id, cost_code_id
),
codes AS (
  SELECT tenant_id, project_id, cost_code_id FROM orig
  UNION SELECT tenant_id, project_id, cost_code_id FROM revs
  UNION SELECT tenant_id, project_id, cost_code_id FROM mv
)
SELECT c.tenant_id, c.project_id, c.cost_code_id,
       COALESCE(o.amount, 0)                     AS original,
       COALESCE(r.amount, 0)                     AS approved_revisions,
       COALESCE(m.xin, 0)                        AS transfers_in,
       COALESCE(m.xout, 0)                       AS transfers_out,
       COALESCE(m.din, 0)                        AS drawdowns_in,
       COALESCE(m.dout, 0)                       AS drawdowns_out,
       COALESCE(o.amount, 0) + COALESCE(r.amount, 0) + COALESCE(m.xin, 0) - COALESCE(m.xout, 0)
         + COALESCE(m.din, 0) - COALESCE(m.dout, 0) AS current,
       COALESCE(o.contingency_flag, false) OR COALESCE(r.contingency_flag, false) AS is_contingency
  FROM codes c
  LEFT JOIN (SELECT tenant_id, project_id, cost_code_id, sum(amount) AS amount, bool_or(contingency_flag) AS contingency_flag
               FROM orig GROUP BY tenant_id, project_id, cost_code_id) o
         ON o.tenant_id = c.tenant_id AND o.project_id = c.project_id AND o.cost_code_id = c.cost_code_id
  LEFT JOIN revs r ON r.tenant_id = c.tenant_id AND r.project_id = c.project_id AND r.cost_code_id = c.cost_code_id
  LEFT JOIN mv m ON m.tenant_id = c.tenant_id AND m.project_id = c.project_id AND m.cost_code_id = c.cost_code_id;

-- ------------------------------------------------------------ helper checks
-- A code usable for budgets: same project, ACTIVE, in an APPROVED WBS version
-- (the same rule BOQ items follow).
CREATE FUNCTION budget_code_usable(p_code uuid, p_project uuid) RETURNS boolean
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT EXISTS (SELECT 1 FROM cost_codes c JOIN wbs_nodes n ON n.id = c.wbs_node_id JOIN wbs_versions w ON w.id = n.version_id
                  WHERE c.id = p_code AND c.tenant_id = current_tenant() AND c.project_id = p_project
                    AND c.status = 'ACTIVE' AND w.status = 'APPROVED')
$$;

-- Serialises every budget movement of one project (and, from the kernel,
-- requisition and purchase-order approvals) so headroom is never spent twice.
CREATE FUNCTION budget_lock_project(p_project uuid) RETURNS void
LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  PERFORM 1 FROM projects WHERE id = p_project AND tenant_id = current_tenant() FOR UPDATE;
END$$;

-- Refuses a movement that would leave the source code below what is already
-- committed, reserved or spent on it.
CREATE FUNCTION budget_assert_headroom(p_code uuid, p_amount numeric, p_what text) RETURNS void
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE avail numeric;
BEGIN
  SELECT pos.available INTO avail FROM p2p_cost_position(p_code, NULL, NULL) pos;
  IF COALESCE(avail, 0) < p_amount THEN
    RAISE EXCEPTION '% of % exceeds the available budget % on cost code % (budget not yet committed, reserved or spent)',
      p_what, p_amount, COALESCE(avail, 0), (SELECT code FROM cost_codes WHERE id = p_code) USING ERRCODE = 'check_violation';
  END IF;
END$$;

-- ------------------------------------------------------------- state guards
CREATE FUNCTION budget_versions_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE bad text; ln record;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION 'budget versions are created DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided budget version is immutable; changes are a new REVISION' USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), '{}', 'budget version');
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], 'budget version');
    IF NOT EXISTS (SELECT 1 FROM budget_lines WHERE version_id = NEW.id) THEN
      RAISE EXCEPTION 'a budget version needs at least one line' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('budget.revision.submit', 'budget_version', NEW.id::text, NEW.created_by, ARRAY['budget.revise'], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'budget version');
    PERFORM budget_lock_project(NEW.project_id);
    SELECT c.code INTO bad FROM budget_lines l JOIN cost_codes c ON c.id = l.cost_code_id
     WHERE l.version_id = NEW.id AND NOT budget_code_usable(l.cost_code_id, NEW.project_id) LIMIT 1;
    IF bad IS NOT NULL THEN
      RAISE EXCEPTION 'budget line % is not an active code of the project''s approved WBS', bad USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.kind = 'ORIGINAL' AND EXISTS (SELECT 1 FROM budget_lines WHERE version_id = NEW.id AND amount < 0) THEN
      RAISE EXCEPTION 'an ORIGINAL budget cannot have negative lines' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.kind = 'REVISION' AND NOT EXISTS (SELECT 1 FROM project_budget_current WHERE project_id = NEW.project_id) THEN
      RAISE EXCEPTION 'a REVISION needs an approved baseline (an ORIGINAL budget or an approved BOQ)' USING ERRCODE = 'check_violation';
    END IF;
    -- A reduction may not take a code below what is already committed or spent.
    FOR ln IN SELECT cost_code_id, amount FROM budget_lines WHERE version_id = NEW.id AND amount < 0 LOOP
      PERFORM budget_assert_headroom(ln.cost_code_id, -ln.amount, 'a budget reduction');
    END LOOP;
    PERFORM approval_use('budget.revision.decide', 'budget_version', NEW.id::text, NEW.approved_by, ARRAY['budget.approve'], false, 'APPROVED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], 'budget version');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a budget version requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use('budget.revision.decide', 'budget_version', NEW.id::text, NEW.approved_by, ARRAY['budget.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal budget version transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER budget_versions_guard BEFORE INSERT OR UPDATE ON budget_versions FOR EACH ROW EXECUTE FUNCTION budget_versions_guard();

CREATE FUNCTION budget_lines_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE v budget_versions;
BEGIN
  SELECT * INTO v FROM budget_versions WHERE id = COALESCE(NEW.version_id, OLD.version_id) AND tenant_id = current_tenant() FOR SHARE;
  IF v.status IS DISTINCT FROM 'DRAFT' THEN
    RAISE EXCEPTION 'budget lines change only while the version is DRAFT' USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  IF TG_OP = 'UPDATE' AND (NEW.version_id <> OLD.version_id OR NEW.cost_code_id <> OLD.cost_code_id) THEN
    RAISE EXCEPTION 'a budget line cannot move between versions or codes' USING ERRCODE = 'check_violation';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM cost_codes c WHERE c.id = NEW.cost_code_id AND c.project_id = v.project_id) THEN
    RAISE EXCEPTION 'a budget line uses a cost code of another project' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER budget_lines_guard BEFORE INSERT OR UPDATE OR DELETE ON budget_lines FOR EACH ROW EXECUTE FUNCTION budget_lines_guard();

-- Transfers and drawdowns share one guard: they differ only in their action
-- names and in the drawdown's contingency rule.
CREATE FUNCTION budget_movement_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE
  doc text := CASE TG_TABLE_NAME WHEN 'budget_transfers' THEN 'budget_transfer' ELSE 'contingency_drawdown' END;
  act text := CASE TG_TABLE_NAME WHEN 'budget_transfers' THEN 'budget.transfer' ELSE 'contingency.drawdown' END;
  perm text := CASE TG_TABLE_NAME WHEN 'budget_transfers' THEN 'budget.transfer' ELSE 'budget.revise' END;
  what text := replace(doc, '_', ' ');
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.submitted_at IS NOT NULL OR NEW.approved_at IS NOT NULL THEN
      RAISE EXCEPTION '% is created DRAFT', what USING ERRCODE = 'check_violation';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM cost_codes WHERE id = NEW.from_cost_code AND project_id = NEW.project_id)
       OR NOT EXISTS (SELECT 1 FROM cost_codes WHERE id = NEW.to_cost_code AND project_id = NEW.project_id) THEN
      RAISE EXCEPTION 'both cost codes of a % must belong to its project', what USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('APPROVED', 'REJECTED') THEN
    RAISE EXCEPTION 'a decided % is immutable', what USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), '{}', what);
  ELSIF OLD.status = 'DRAFT' AND NEW.status = 'SUBMITTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'submitted_at'], what);
    PERFORM approval_use(act || '.submit', doc, NEW.id::text, NEW.requested_by, ARRAY[perm], false, NULL, NULL);
    NEW.submitted_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], what);
    PERFORM budget_lock_project(NEW.project_id);
    IF NOT budget_code_usable(NEW.from_cost_code, NEW.project_id) OR NOT budget_code_usable(NEW.to_cost_code, NEW.project_id) THEN
      RAISE EXCEPTION 'a % moves money only between active codes of the project''s approved WBS', what USING ERRCODE = 'check_violation';
    END IF;
    IF doc = 'contingency_drawdown' AND NOT EXISTS (
         SELECT 1 FROM project_budget_current WHERE project_id = NEW.project_id AND cost_code_id = NEW.from_cost_code AND is_contingency) THEN
      RAISE EXCEPTION 'a drawdown comes only from a cost code that holds contingency' USING ERRCODE = 'check_violation';
    END IF;
    PERFORM budget_assert_headroom(NEW.from_cost_code, NEW.amount, 'the ' || what);
    PERFORM approval_use(act || '.decide', doc, NEW.id::text, NEW.approved_by, ARRAY['budget.approve'], false, 'APPROVED', NULL);
    NEW.approved_at := now();
  ELSIF OLD.status = 'SUBMITTED' AND NEW.status = 'REJECTED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'decision_reason'], what);
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting a % requires a reason', what USING ERRCODE = 'check_violation';
    END IF;
    PERFORM approval_use(act || '.decide', doc, NEW.id::text, NEW.approved_by, ARRAY['budget.approve'], false, 'REJECTED', NULL);
    NEW.approved_at := now();
  ELSE
    RAISE EXCEPTION 'illegal % transition % -> %', what, OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER budget_transfers_guard BEFORE INSERT OR UPDATE ON budget_transfers FOR EACH ROW EXECUTE FUNCTION budget_movement_guard();
CREATE TRIGGER contingency_drawdowns_guard BEFORE INSERT OR UPDATE ON contingency_drawdowns FOR EACH ROW EXECUTE FUNCTION budget_movement_guard();

-- ---------------------------------------- shared functions (verbatim + 0039)
-- p2p_cost_position: verbatim from 0030 except the budget source (marked 0039).
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
     AND j.source_type <> 'P2P_GRN';
  -- Stock issued to the code consumes the code's own inventory commitment
  -- first, but never more than the code's orders put away: the code bears
  -- commitment + max(0, issued - put away).
  committed := committed - least(greatest(issued, 0), put_away);
  available := budget - committed - reserved - actual_other;
END$$;

-- p2p_subject_text: verbatim from 0037 plus the budget documents (marked 0039).
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
  ELSE
    RETURN NULL;
  END CASE;
  RETURN j::text;
END$function$;

-- serk_subject_project: verbatim from 0037 plus the budget documents (marked 0039).
CREATE OR REPLACE FUNCTION public.serk_subject_project(p_type text, p_id text)
 RETURNS text
 LANGUAGE plpgsql
 STABLE
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_id uuid; r text;
BEGIN
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device') THEN
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

-- Cost report: the 0013 report plus the original and current budget columns.
DROP FUNCTION p2p_project_cost_report(uuid);
CREATE FUNCTION p2p_project_cost_report(p_project uuid)
RETURNS TABLE (cost_code_id uuid, cost_code text, budget numeric, committed numeric, reserved numeric, received numeric,
               invoiced numeric, actual_posted numeric, actual_other numeric, available numeric, original numeric, current numeric)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT c.id, c.code, pos.budget, pos.committed, pos.reserved,
         COALESCE((SELECT sum(p2p_round(gl.qty_received * pl.unit_price, o.currency)) FROM grn_lines gl JOIN grns g ON g.id = gl.grn_id
                    JOIN purchase_orders o ON o.id = g.po_id JOIN po_lines pl ON pl.po_id = o.id AND pl.line_no = gl.po_line_no
                   WHERE g.status = 'CONFIRMED' AND pl.cost_code_id = c.id), 0),
         COALESCE((SELECT sum(il.amount) FROM invoice_lines il JOIN supplier_invoices i ON i.id = il.invoice_id
                    JOIN po_lines pl ON pl.po_id = i.po_id AND pl.line_no = il.po_line_no
                   WHERE i.status = 'APPROVED' AND pl.cost_code_id = c.id), 0),
         COALESCE((SELECT sum(CASE WHEN jl.side = 'DEBIT' THEN jl.amount ELSE -jl.amount END) FROM journal_lines jl JOIN journals j ON j.id = jl.journal_id
                   WHERE jl.cost_code_id = c.id AND j.status IN ('POSTED', 'REVERSED')), 0),
         pos.actual_other, pos.available,
         COALESCE(pbc.original, pos.budget), COALESCE(pbc.current, pos.budget)
    FROM cost_codes c CROSS JOIN LATERAL p2p_cost_position(c.id, NULL, NULL) pos
    LEFT JOIN project_budget_current pbc ON pbc.project_id = c.project_id AND pbc.cost_code_id = c.id
   WHERE c.tenant_id = current_tenant() AND c.project_id = p_project
     AND (pos.budget <> 0 OR pos.committed <> 0 OR pos.reserved <> 0 OR pos.actual_other <> 0)
   ORDER BY c.code
$$;

-- ------------------------------------------------- row security, grants ---
DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['budget_versions', 'budget_lines', 'budget_transfers', 'contingency_drawdowns'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
-- Only the decision columns of the documents change after insert; line amounts
-- change only while DRAFT (line guard). The application role never deletes.
GRANT UPDATE (status, submitted_at, approved_by, approved_at, decision_reason) ON budget_versions, budget_transfers, contingency_drawdowns TO serk_app;
GRANT UPDATE (amount, contingency_flag) ON budget_lines TO serk_app;
REVOKE ALL ON project_budget_current FROM PUBLIC;
GRANT SELECT ON project_budget_current TO serk_app;
REVOKE ALL ON FUNCTION budget_code_usable(uuid, uuid), budget_lock_project(uuid), budget_assert_headroom(uuid, numeric, text),
  p2p_project_cost_report(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION budget_code_usable(uuid, uuid), budget_lock_project(uuid), budget_assert_headroom(uuid, numeric, text),
  p2p_project_cost_report(uuid) TO serk_app;
