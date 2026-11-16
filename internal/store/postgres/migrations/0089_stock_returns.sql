-- 0089_stock_returns.sql — site returns (SUP-05.4, second part).
--
-- Material issued to a project and not used goes back to a store. A return names the approved stock issue it
-- reverses in part and is valued at that issue's cost (the issue's amount in proportion to the quantity; returning
-- everything still out gives back exactly what remains of the issue's amount). It is prepared by someone holding
-- stock.issue on the project and approved by someone else holding stock.issue.approve, attested; approval puts the
-- quantity back into the chosen store of the issue's entity (a new FIFO layer at that cost, or into the moving
-- average) and the approving transaction attaches the STK_RETURN journal: Dr inventory, Cr the issue's cost account
-- on its project and cost code, bound to the computed amount. More than was issued, less earlier returns, is never
-- returned (checked at approval with the issue locked).
--
-- Shared functions copied verbatim with returns added: stk_expected_lines, journals_stk_binding and
-- stock_gl_reconciliation from 0088; p2p_cost_position from 0061 (issued to a cost code is now net of returns, so a
-- return gives back the code's inventory commitment). p2p_subject_text / serk_subject_project: verbatim from 0088
-- plus stock_return.

ALTER TABLE stock_layers DROP CONSTRAINT stock_layers_source_type_check;
ALTER TABLE stock_layers ADD CONSTRAINT stock_layers_source_type_check CHECK (source_type IN ('RECEIPT', 'COUNT', 'TRANSFER', 'RETURN'));

CREATE TABLE stock_returns (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  legal_entity_id    uuid NOT NULL,
  number             text NOT NULL,
  issue_id           uuid NOT NULL,
  warehouse_id       uuid NOT NULL,
  stock_item_id      uuid NOT NULL,
  project_id         uuid NOT NULL,
  cost_code_id       uuid NOT NULL,
  expense_account_id uuid NOT NULL,
  quantity           numeric(20,4) NOT NULL CHECK (quantity > 0),
  return_date        date NOT NULL,
  reason             text NOT NULL CHECK (length(btrim(reason)) BETWEEN 5 AND 1000),
  status             text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'APPROVED', 'REJECTED', 'CANCELLED')),
  unit_cost          numeric(24,6),
  amount             numeric(24,6),
  currency           char(3) NOT NULL REFERENCES currencies(code),
  created_by         text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  approved_by        text,
  approved_at        timestamptz,
  approved_txid      bigint,
  decision_reason    text,
  journal_id         uuid UNIQUE,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, issue_id) REFERENCES stock_issues(tenant_id, id),
  FOREIGN KEY (tenant_id, warehouse_id) REFERENCES warehouses(tenant_id, id),
  CONSTRAINT stock_return_maker_checker CHECK (approved_by IS NULL OR approved_by <> created_by)
);
CREATE INDEX stock_returns_issue ON stock_returns (tenant_id, issue_id);

CREATE FUNCTION stock_returns_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE i stock_issues; w warehouses; back_qty numeric; back_amt numeric;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'stock returns are never deleted' USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'INSERT' THEN
    SELECT * INTO i FROM stock_issues WHERE id = NEW.issue_id AND tenant_id = current_tenant();
    SELECT * INTO w FROM warehouses WHERE id = NEW.warehouse_id AND tenant_id = current_tenant();
    IF NEW.status <> 'DRAFT' OR NEW.approved_by IS NOT NULL OR NEW.amount IS NOT NULL OR NEW.journal_id IS NOT NULL OR NEW.decision_reason IS NOT NULL THEN
      RAISE EXCEPTION 'stock returns are prepared DRAFT' USING ERRCODE = 'check_violation';
    END IF;
    IF i.id IS NULL OR i.status <> 'APPROVED' THEN
      RAISE EXCEPTION 'a return reverses an approved stock issue' USING ERRCODE = 'check_violation';
    END IF;
    IF w.id IS NULL OR w.legal_entity_id <> i.legal_entity_id OR (w.project_id IS NOT NULL AND w.project_id <> i.project_id) THEN
      RAISE EXCEPTION 'stock is returned to a store of the issue''s entity (a project store of the issue''s project)' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.return_date < i.issue_date OR NEW.return_date > current_date THEN
      RAISE EXCEPTION 'a return is dated between its issue and today' USING ERRCODE = 'check_violation';
    END IF;
    -- What is returned, and where its cost goes, is the issue's, never the caller's.
    NEW.legal_entity_id := i.legal_entity_id; NEW.stock_item_id := i.stock_item_id; NEW.project_id := i.project_id;
    NEW.cost_code_id := i.cost_code_id; NEW.expense_account_id := i.expense_account_id; NEW.currency := i.currency;
    RETURN NEW;
  END IF;
  IF OLD.status = 'DRAFT' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'approved_by', 'approved_at', 'approved_txid', 'unit_cost', 'amount'], 'stock return');
    PERFORM approval_use('stock.return.approve', 'stock_return', NEW.id::text, NEW.approved_by, ARRAY['stock.issue.approve'], false, 'APPROVED', NULL);
    SELECT * INTO i FROM stock_issues WHERE id = NEW.issue_id FOR UPDATE;
    SELECT COALESCE(sum(quantity), 0), COALESCE(sum(amount), 0) INTO back_qty, back_amt
      FROM stock_returns WHERE issue_id = NEW.issue_id AND status = 'APPROVED' AND id <> NEW.id;
    IF NEW.quantity > i.quantity - back_qty THEN
      RAISE EXCEPTION 'only % of issue % is still out; % cannot be returned', i.quantity - back_qty, i.number, NEW.quantity USING ERRCODE = 'check_violation';
    END IF;
    NEW.amount := CASE WHEN NEW.quantity = i.quantity - back_qty THEN COALESCE(i.amount, 0) - back_amt
                       ELSE p2p_round(COALESCE(i.amount, 0) * NEW.quantity / i.quantity, NEW.currency) END;
    NEW.unit_cost := round(NEW.amount / NEW.quantity, 6);
    PERFORM stk_receive(NEW.stock_item_id, NEW.warehouse_id, NEW.quantity, NEW.amount, NEW.unit_cost, 'RETURN', NEW.id);
    NEW.approved_at := now();
    NEW.approved_txid := txid_current();
  ELSIF OLD.status = 'APPROVED' AND NEW.status = 'APPROVED' THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['journal_id'], 'stock return');
    IF OLD.journal_id IS NOT NULL OR OLD.approved_txid IS DISTINCT FROM txid_current() OR NEW.journal_id IS NULL THEN
      RAISE EXCEPTION 'the journal of a return is attached once, by the approving transaction' USING ERRCODE = 'insufficient_privilege';
    END IF;
    PERFORM stk_assert_journal(NEW.journal_id, 'STK_RETURN', NEW.id, NEW.legal_entity_id, NEW.currency, NEW.return_date);
  ELSIF OLD.status = 'DRAFT' AND NEW.status IN ('REJECTED', 'CANCELLED') THEN
    PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'decision_reason'], 'stock return');
    IF NEW.decision_reason IS NULL OR length(btrim(NEW.decision_reason)) < 5 THEN
      RAISE EXCEPTION 'rejecting or cancelling a return requires a reason' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.status = 'REJECTED' THEN
      PERFORM approval_use('stock.return.reject', 'stock_return', NEW.id::text, NULL, ARRAY['stock.issue.approve'], false, 'REJECTED', NULL);
    ELSE
      PERFORM approval_use('stock.return.cancel', 'stock_return', NEW.id::text, NEW.created_by, ARRAY['stock.issue'], false, 'CANCELLED', NULL);
    END IF;
  ELSE
    RAISE EXCEPTION 'illegal stock return transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER stock_returns_guard BEFORE INSERT OR UPDATE OR DELETE ON stock_returns FOR EACH ROW EXECUTE FUNCTION stock_returns_guard();

CREATE FUNCTION stock_returns_journal_bound() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM stock_returns WHERE id = NEW.id AND status = 'APPROVED' AND amount > 0 AND journal_id IS NULL) THEN
    RAISE EXCEPTION 'return % was approved without its STK_RETURN journal', NEW.number USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;
CREATE CONSTRAINT TRIGGER stock_returns_journal_bound AFTER UPDATE ON stock_returns DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW WHEN (NEW.status = 'APPROVED') EXECUTE FUNCTION stock_returns_journal_bound();

ALTER TABLE stock_returns ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_returns FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON stock_returns USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
DO $$
BEGIN
  EXECUTE format('CREATE POLICY owner_maintenance ON stock_returns TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', current_user);
END$$;
REVOKE ALL ON stock_returns FROM PUBLIC;
GRANT SELECT, INSERT ON stock_returns TO serk_app;
GRANT UPDATE (status, approved_by, decision_reason, journal_id) ON stock_returns TO serk_app;
REVOKE ALL ON FUNCTION stock_returns_guard() FROM PUBLIC;

CREATE OR REPLACE FUNCTION stk_expected_lines(p_doc text, p_id uuid)
RETURNS TABLE (account_id uuid, side text, amount numeric, project_id uuid, cost_code_id uuid)
LANGUAGE sql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
  WITH e(account_id, side, amount, project_id, cost_code_id) AS (
    -- Issue: Dr project cost (cost code); Cr inventory.
    SELECT i.expense_account_id, 'DEBIT', i.amount, i.project_id, i.cost_code_id
      FROM stock_issues i WHERE p_doc = 'STK_ISSUE' AND i.id = p_id AND i.tenant_id = current_tenant() AND i.status = 'APPROVED'
    UNION ALL
    SELECT it.inventory_account_id, 'CREDIT', i.amount, i.project_id, NULL::uuid
      FROM stock_issues i JOIN stock_items it ON it.id = i.stock_item_id
     WHERE p_doc = 'STK_ISSUE' AND i.id = p_id AND i.tenant_id = current_tenant() AND i.status = 'APPROVED'
    UNION ALL
    -- Count: shortage Dr variance, Cr inventory; surplus the reverse.
    SELECT c.variance_account_id, CASE WHEN c.difference < 0 THEN 'DEBIT' ELSE 'CREDIT' END, c.amount, c.project_id, NULL::uuid
      FROM stock_counts c WHERE p_doc = 'STK_ADJUST' AND c.id = p_id AND c.tenant_id = current_tenant() AND c.status = 'APPROVED'
    UNION ALL
    SELECT it.inventory_account_id, CASE WHEN c.difference < 0 THEN 'CREDIT' ELSE 'DEBIT' END, c.amount, c.project_id, NULL::uuid
      FROM stock_counts c JOIN stock_items it ON it.id = c.stock_item_id
     WHERE p_doc = 'STK_ADJUST' AND c.id = p_id AND c.tenant_id = current_tenant() AND c.status = 'APPROVED'
    UNION ALL
    -- 0088 write-off (scrap): Dr the write-off expense account; Cr inventory; on the warehouse's project, if any.
    SELECT o.expense_account_id, 'DEBIT', o.amount, w.project_id, NULL::uuid
      FROM stock_writeoffs o JOIN warehouses w ON w.id = o.warehouse_id
     WHERE p_doc = 'STK_SCRAP' AND o.id = p_id AND o.tenant_id = current_tenant() AND o.status = 'APPROVED'
    UNION ALL
    SELECT it.inventory_account_id, 'CREDIT', o.amount, w.project_id, NULL::uuid
      FROM stock_writeoffs o JOIN stock_items it ON it.id = o.stock_item_id JOIN warehouses w ON w.id = o.warehouse_id
     WHERE p_doc = 'STK_SCRAP' AND o.id = p_id AND o.tenant_id = current_tenant() AND o.status = 'APPROVED'
    UNION ALL
    -- 0089 site return: Dr inventory (on the project); Cr the issue's cost account, project and cost code.
    SELECT it.inventory_account_id, 'DEBIT', r.amount, r.project_id, NULL::uuid
      FROM stock_returns r JOIN stock_items it ON it.id = r.stock_item_id
     WHERE p_doc = 'STK_RETURN' AND r.id = p_id AND r.tenant_id = current_tenant() AND r.status = 'APPROVED'
    UNION ALL
    SELECT r.expense_account_id, 'CREDIT', r.amount, r.project_id, r.cost_code_id
      FROM stock_returns r WHERE p_doc = 'STK_RETURN' AND r.id = p_id AND r.tenant_id = current_tenant() AND r.status = 'APPROVED'
  )
  SELECT account_id, side, amount, project_id, cost_code_id FROM e WHERE amount > 0
$$;

CREATE OR REPLACE FUNCTION journals_stk_binding() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE bound boolean;
BEGIN
  bound := CASE NEW.source_type
      WHEN 'STK_ISSUE' THEN EXISTS (SELECT 1 FROM stock_issues WHERE id::text = NEW.source_ref AND journal_id = NEW.id)
      WHEN 'STK_ADJUST' THEN EXISTS (SELECT 1 FROM stock_counts WHERE id::text = NEW.source_ref AND journal_id = NEW.id)
      WHEN 'STK_SCRAP' THEN EXISTS (SELECT 1 FROM stock_writeoffs WHERE id::text = NEW.source_ref AND journal_id = NEW.id)
      WHEN 'STK_RETURN' THEN EXISTS (SELECT 1 FROM stock_returns WHERE id::text = NEW.source_ref AND journal_id = NEW.id)
      ELSE false END;
  IF NOT bound THEN
    RAISE EXCEPTION 'journal % uses reserved source type % but no % document references it', NEW.id, NEW.source_type, NEW.source_type
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END$$;

CREATE OR REPLACE FUNCTION stock_gl_reconciliation(p_entity uuid)
RETURNS TABLE (check_name text, subledger numeric, gl numeric, difference numeric, unposted_documents bigint)
LANGUAGE plpgsql STABLE SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE acc record; grn_posted numeric; moves numeric; g numeric; un bigint; putaway numeric; bal numeric; docs numeric; layers numeric;
        pa_unposted numeric; un_grn bigint; un_moves numeric; un_docs bigint;
BEGIN
  FOR acc IN SELECT DISTINCT a.id, a.code, a.name FROM stock_items i JOIN accounts a ON a.id = i.inventory_account_id
              WHERE i.tenant_id = current_tenant() AND i.legal_entity_id = p_entity ORDER BY a.code LOOP
    SELECT COALESCE(sum(p2p_round(gl.qty_received * pl.unit_price, o.currency)), 0) INTO grn_posted
      FROM grns gr JOIN journals j ON j.id = gr.journal_id JOIN purchase_orders o ON o.id = gr.po_id
      JOIN grn_lines gl ON gl.grn_id = gr.id JOIN po_lines pl ON pl.po_id = o.id AND pl.line_no = gl.po_line_no
     WHERE o.legal_entity_id = p_entity AND pl.account_id = acc.id AND j.status = 'POSTED';
    SELECT COALESCE((SELECT sum(CASE WHEN c.difference > 0 THEN c.amount ELSE -c.amount END) FROM stock_counts c JOIN journals j ON j.id = c.journal_id
                      JOIN stock_items i ON i.id = c.stock_item_id WHERE i.inventory_account_id = acc.id AND j.status = 'POSTED'), 0)
         - COALESCE((SELECT sum(s.amount) FROM stock_issues s JOIN journals j ON j.id = s.journal_id JOIN stock_items i ON i.id = s.stock_item_id
                      WHERE i.inventory_account_id = acc.id AND j.status = 'POSTED'), 0)
         - COALESCE((SELECT sum(o.amount) FROM stock_writeoffs o JOIN journals j ON j.id = o.journal_id JOIN stock_items i ON i.id = o.stock_item_id
                      WHERE i.inventory_account_id = acc.id AND j.status = 'POSTED'), 0)
         + COALESCE((SELECT sum(x.amount) FROM stock_returns x JOIN journals j ON j.id = x.journal_id JOIN stock_items i ON i.id = x.stock_item_id
                      WHERE i.inventory_account_id = acc.id AND j.status = 'POSTED'), 0) INTO moves;
    SELECT COALESCE(sum(CASE WHEN jl.side = 'DEBIT' THEN jl.amount ELSE -jl.amount END), 0) INTO g
      FROM journal_lines jl JOIN journals j ON j.id = jl.journal_id WHERE jl.account_id = acc.id AND j.status IN ('POSTED', 'REVERSED');
    -- Unposted documents on the account: stock issues and counts, and goods receipts.
    SELECT (SELECT count(*) FROM stock_issues s JOIN journals j ON j.id = s.journal_id JOIN stock_items i ON i.id = s.stock_item_id
             WHERE i.inventory_account_id = acc.id AND j.status <> 'POSTED')
         + (SELECT count(*) FROM stock_counts c JOIN journals j ON j.id = c.journal_id JOIN stock_items i ON i.id = c.stock_item_id
             WHERE i.inventory_account_id = acc.id AND j.status <> 'POSTED')
         + (SELECT count(*) FROM stock_writeoffs o JOIN journals j ON j.id = o.journal_id JOIN stock_items i ON i.id = o.stock_item_id
             WHERE i.inventory_account_id = acc.id AND j.status <> 'POSTED')
         + (SELECT count(*) FROM stock_returns x JOIN journals j ON j.id = x.journal_id JOIN stock_items i ON i.id = x.stock_item_id
             WHERE i.inventory_account_id = acc.id AND j.status <> 'POSTED') INTO un_docs;
    SELECT count(DISTINCT gr.id) INTO un_grn FROM grns gr JOIN journals j ON j.id = gr.journal_id JOIN purchase_orders o ON o.id = gr.po_id
      JOIN grn_lines gl ON gl.grn_id = gr.id JOIN po_lines pl ON pl.po_id = o.id AND pl.line_no = gl.po_line_no
     WHERE o.legal_entity_id = p_entity AND pl.account_id = acc.id AND j.status <> 'POSTED';
    check_name := 'Inventory: ' || acc.code || ' ' || acc.name; subledger := grn_posted + moves; gl := g; difference := g - (grn_posted + moves);
    unposted_documents := un_docs + un_grn; RETURN NEXT;
    -- Received on the account (posted) but not yet put away; negative is an error.
    SELECT COALESCE(sum(r.amount), 0) INTO putaway FROM stock_receipts r JOIN grns gr ON gr.id = r.grn_id JOIN journals j ON j.id = gr.journal_id
      JOIN stock_items i ON i.id = r.stock_item_id WHERE i.inventory_account_id = acc.id AND j.status = 'POSTED';
    check_name := 'Received, not yet put away: ' || acc.code; subledger := grn_posted - putaway; gl := NULL; difference := least(grn_posted - putaway, 0);
    unposted_documents := (SELECT count(*) FROM grns gr JOIN purchase_orders o ON o.id = gr.po_id JOIN grn_lines gl ON gl.grn_id = gr.id
                            JOIN po_lines pl ON pl.po_id = o.id AND pl.line_no = gl.po_line_no
                           WHERE pl.account_id = acc.id AND gr.status = 'CONFIRMED'
                             AND gl.qty_received > COALESCE((SELECT sum(quantity) FROM stock_receipts r WHERE r.grn_id = gr.id AND r.po_line_no = gl.po_line_no), 0));
    RETURN NEXT;
    -- Stock balances against the GL with explicit reconciling items:
    --   GL = stock - put away from unposted receipts + posted receipts not yet put away
    --        + approved issues and shortages not yet posted - approved surpluses not yet posted
    SELECT COALESCE(sum(b.value), 0) INTO bal FROM stock_balances b JOIN stock_items i ON i.id = b.stock_item_id WHERE i.inventory_account_id = acc.id;
    SELECT COALESCE(sum(r.amount), 0) INTO pa_unposted FROM stock_receipts r JOIN grns gr ON gr.id = r.grn_id JOIN stock_items i ON i.id = r.stock_item_id
      LEFT JOIN journals j ON j.id = gr.journal_id WHERE i.inventory_account_id = acc.id AND j.status IS DISTINCT FROM 'POSTED';
    SELECT COALESCE((SELECT sum(s.amount) FROM stock_issues s JOIN stock_items i ON i.id = s.stock_item_id LEFT JOIN journals j ON j.id = s.journal_id
                      WHERE i.inventory_account_id = acc.id AND s.status = 'APPROVED' AND s.amount > 0 AND j.status IS DISTINCT FROM 'POSTED'), 0)
         + COALESCE((SELECT sum(CASE WHEN c.difference < 0 THEN c.amount ELSE -c.amount END) FROM stock_counts c JOIN stock_items i ON i.id = c.stock_item_id
                      LEFT JOIN journals j ON j.id = c.journal_id
                      WHERE i.inventory_account_id = acc.id AND c.status = 'APPROVED' AND c.amount > 0 AND j.status IS DISTINCT FROM 'POSTED'), 0)
         + COALESCE((SELECT sum(o.amount) FROM stock_writeoffs o JOIN stock_items i ON i.id = o.stock_item_id LEFT JOIN journals j ON j.id = o.journal_id
                      WHERE i.inventory_account_id = acc.id AND o.status = 'APPROVED' AND o.amount > 0 AND j.status IS DISTINCT FROM 'POSTED'), 0)
         - COALESCE((SELECT sum(x.amount) FROM stock_returns x JOIN stock_items i ON i.id = x.stock_item_id LEFT JOIN journals j ON j.id = x.journal_id
                      WHERE i.inventory_account_id = acc.id AND x.status = 'APPROVED' AND x.amount > 0 AND j.status IS DISTINCT FROM 'POSTED'), 0) INTO un_moves;
    check_name := 'Stock balances vs GL: ' || acc.code; subledger := bal - pa_unposted + (grn_posted - putaway) + un_moves; gl := g;
    difference := g - (bal - pa_unposted + (grn_posted - putaway) + un_moves); unposted_documents := un_docs + un_grn; RETURN NEXT;
    check_name := 'Put away from goods receipts not yet posted: ' || acc.code; subledger := pa_unposted; gl := NULL; difference := NULL;
    unposted_documents := un_grn; RETURN NEXT;
    -- Integrity: running balances = documents (all approved, posted or not), and FIFO layers = balances.
    SELECT COALESCE((SELECT sum(r.amount) FROM stock_receipts r JOIN stock_items i ON i.id = r.stock_item_id WHERE i.inventory_account_id = acc.id), 0)
         + COALESCE((SELECT sum(CASE WHEN c.difference > 0 THEN c.amount ELSE -c.amount END) FROM stock_counts c JOIN stock_items i ON i.id = c.stock_item_id
                      WHERE i.inventory_account_id = acc.id AND c.status = 'APPROVED'), 0)
         - COALESCE((SELECT sum(s.amount) FROM stock_issues s JOIN stock_items i ON i.id = s.stock_item_id
                      WHERE i.inventory_account_id = acc.id AND s.status = 'APPROVED'), 0)
         - COALESCE((SELECT sum(o.amount) FROM stock_writeoffs o JOIN stock_items i ON i.id = o.stock_item_id
                      WHERE i.inventory_account_id = acc.id AND o.status = 'APPROVED'), 0)
         + COALESCE((SELECT sum(x.amount) FROM stock_returns x JOIN stock_items i ON i.id = x.stock_item_id
                      WHERE i.inventory_account_id = acc.id AND x.status = 'APPROVED'), 0) INTO docs;
    SELECT COALESCE(sum(l.value_remaining), 0) + COALESCE((SELECT sum(b.value) FROM stock_balances b JOIN stock_items i ON i.id = b.stock_item_id
                                                             WHERE i.inventory_account_id = acc.id AND i.valuation_method = 'WAC'), 0) INTO layers
      FROM stock_layers l JOIN stock_items i ON i.id = l.stock_item_id WHERE i.inventory_account_id = acc.id AND i.valuation_method = 'FIFO';
    check_name := 'Stock balances vs documents: ' || acc.code; subledger := docs; gl := bal; difference := bal - docs;
    unposted_documents := 0; RETURN NEXT;
    check_name := 'Cost layers vs stock balances: ' || acc.code; subledger := layers; gl := bal; difference := layers - bal;
    unposted_documents := 0; RETURN NEXT;
  END LOOP;
END$$;

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
         COALESCE(sum(CASE WHEN j.source_type IN ('STK_ISSUE', 'STK_RETURN') THEN CASE WHEN jl.side = 'DEBIT' THEN jl.amount ELSE -jl.amount END ELSE 0 END), 0)
    INTO actual_other, issued
    FROM journal_lines jl JOIN journals j ON j.id = jl.journal_id
   WHERE jl.tenant_id = current_tenant() AND jl.cost_code_id = p_cost_code AND j.status IN ('POSTED', 'REVERSED')
     AND j.source_type NOT IN ('P2P_GRN', 'SUB_CERT'); -- 0055: certificates are inside the subcontract commitment
  -- 0089: issued is net of site returns (a return credits the code and gives back its inventory commitment).
  -- Stock issued to the code consumes the code's own inventory commitment
  -- first, but never more than the code's orders put away: the code bears
  -- commitment + max(0, issued - put away).
  committed := committed - least(greatest(issued, 0), put_away);
  available := budget - committed - reserved - actual_other;
END$$;

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
  -- 0085: a correspondence item is a record; registered so it is listed and read like the other documents.
  WHEN 'correspondence' THEN SELECT to_jsonb(x) INTO j FROM correspondence x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0086: a certification or training record is what its verifier attests.
  WHEN 'certification' THEN SELECT to_jsonb(x) INTO j FROM certifications x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0088: a stock write-off (its journal is attached after the decision).
  WHEN 'stock_writeoff' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM stock_writeoffs x WHERE tenant_id = t AND id = p_id::uuid;
  -- 0089: a site return (its journal is attached after the decision).
  WHEN 'stock_return' THEN SELECT to_jsonb(x) - 'journal_id' INTO j FROM stock_returns x WHERE tenant_id = t AND id = p_id::uuid;
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
  IF p_type IN ('supplier', 'customer', 'stock_item', 'supplier_bank_account', 'workflow_definition', 'workflow_definition_body', 'site_device', 'authority_matrix', 'delegation', 'sod_exception', 'uom_conversion', 'project', 'price_index_value', 'revenue_policy', 'subcontract_policy', 'fixed_asset', 'asset_disposal', 'payroll_policy', 'payroll_rule_set', 'pay_run', 'consolidation_group', 'consolidation_run', 'tax_return', 'document_policy', 'equipment_operator_auth', 'fleet_policy', 'employee', 'equipment_charge_rate', 'tax_account', 'intercompany_account', 'role_adjustment', 'certification') THEN  -- 0086: certifications belong to people, not projects
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
  -- 0085: correspondence belongs to its project.
  WHEN 'correspondence' THEN SELECT project_id::text INTO r FROM correspondence x WHERE x.id = v_id;
  -- 0088: a write-off is scoped by its warehouse (a central warehouse needs tenant-wide scope).
  WHEN 'stock_writeoff' THEN SELECT COALESCE(w.project_id::text, '') INTO r FROM stock_writeoffs x JOIN warehouses w ON w.id = x.warehouse_id WHERE x.id = v_id;
  -- 0089: a site return belongs to the project it returns from.
  WHEN 'stock_return' THEN SELECT project_id::text INTO r FROM stock_returns x WHERE x.id = v_id;
  ELSE RETURN '';
  END CASE;
  RETURN COALESCE(r, '');
END$function$;
