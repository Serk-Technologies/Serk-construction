-- 0043 Staged imports (WP-1.8; SRS INT-03, ADM-03, NFR-DAT-05)
--
-- WHAT THIS MIGRATION DOES
--   Loading suppliers, customers and stock items from a spreadsheet must not
--   bypass the controls those records normally go through. A file is first
--   STAGED: every row is parsed and validated and nothing is created. A
--   person reviews the result, and a DIFFERENT person commits it, confirming
--   the number of rows the source says it contains (the reconciliation
--   check). Committing creates each record through the ordinary commands, so
--   a supplier still needs its approval, and a stock item its approval, exactly
--   as if it had been typed in.
--
--     import_batches   one uploaded file: kind, company, counts, who staged,
--                      who committed, the rows the committer confirmed
--     import_rows      each row as received, its validation errors, and (after
--                      commit) the record created or why it failed
--
-- GUARDS THE DATABASE ENFORCES
--   * the person who commits is not the person who staged;
--   * a committer's confirmed row count must equal the staged row count;
--   * counts, the file digest and every row's data never change after staging;
--   * only valid rows are created; a batch is final once COMMITTED or DISCARDED
--     (PARTIAL batches may be committed again to retry the failed rows).

CREATE TABLE import_batches (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  kind           text NOT NULL CHECK (kind IN ('supplier', 'customer', 'stock_item')),
  legal_entity_id uuid NOT NULL,
  filename       text NOT NULL CHECK (length(btrim(filename)) BETWEEN 1 AND 200),
  file_sha256    text NOT NULL CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
  row_count      integer NOT NULL CHECK (row_count BETWEEN 1 AND 2000),
  valid_count    integer NOT NULL CHECK (valid_count >= 0),
  invalid_count  integer NOT NULL CHECK (invalid_count >= 0),
  status         text NOT NULL DEFAULT 'STAGED' CHECK (status IN ('STAGED', 'COMMITTED', 'PARTIAL', 'DISCARDED')),
  created_by     text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  committed_by   text,
  committed_at   timestamptz,
  expected_rows  integer,
  discard_reason text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entities(tenant_id, id),
  CONSTRAINT import_counts CHECK (valid_count + invalid_count = row_count),
  CONSTRAINT import_maker_checker CHECK (committed_by IS NULL OR committed_by <> created_by)
);

CREATE TABLE import_rows (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL DEFAULT current_tenant() REFERENCES tenants(id),
  batch_id   uuid NOT NULL,
  line_no    integer NOT NULL CHECK (line_no >= 1),
  data       jsonb NOT NULL,
  errors     text[] NOT NULL DEFAULT '{}',
  status     text NOT NULL CHECK (status IN ('VALID', 'INVALID', 'CREATED', 'FAILED')),
  created_id text,
  error      text,
  UNIQUE (tenant_id, batch_id, line_no),
  FOREIGN KEY (tenant_id, batch_id) REFERENCES import_batches(tenant_id, id)
);

CREATE FUNCTION import_batches_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'STAGED' OR NEW.committed_by IS NOT NULL OR NEW.committed_at IS NOT NULL OR NEW.expected_rows IS NOT NULL THEN
      RAISE EXCEPTION 'import batches are created STAGED' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('COMMITTED', 'DISCARDED') THEN
    RAISE EXCEPTION 'a % import batch is final', lower(OLD.status) USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'committed_by', 'committed_at', 'expected_rows', 'discard_reason'], 'import batch');
  IF NEW.status IN ('COMMITTED', 'PARTIAL') THEN
    IF NEW.committed_by IS NULL THEN
      RAISE EXCEPTION 'a commit records who committed' USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.expected_rows IS DISTINCT FROM OLD.row_count THEN
      RAISE EXCEPTION 'the confirmed row count (%) does not match the staged file (% rows)', NEW.expected_rows, OLD.row_count USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.valid_count = 0 THEN
      RAISE EXCEPTION 'a batch with no valid rows cannot be committed' USING ERRCODE = 'check_violation';
    END IF;
    NEW.committed_at := now();
  ELSIF NEW.status = 'DISCARDED' THEN
    IF NEW.discard_reason IS NULL OR length(btrim(NEW.discard_reason)) < 3 THEN
      RAISE EXCEPTION 'discarding a batch records why' USING ERRCODE = 'check_violation';
    END IF;
  ELSIF NEW.status <> OLD.status THEN
    RAISE EXCEPTION 'illegal import batch transition % -> %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER import_batches_guard BEFORE INSERT OR UPDATE ON import_batches FOR EACH ROW EXECUTE FUNCTION import_batches_guard();

CREATE FUNCTION import_rows_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE b import_batches;
BEGIN
  SELECT * INTO b FROM import_batches WHERE id = NEW.batch_id AND tenant_id = current_tenant();
  IF TG_OP = 'INSERT' THEN
    IF b.status IS DISTINCT FROM 'STAGED' OR NEW.status NOT IN ('VALID', 'INVALID') OR (NEW.status = 'INVALID') <> (cardinality(NEW.errors) > 0) THEN
      RAISE EXCEPTION 'rows are staged with the batch, valid or invalid according to their errors' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF b.status NOT IN ('STAGED', 'PARTIAL') THEN
    RAISE EXCEPTION 'rows of a % batch are final', lower(b.status) USING ERRCODE = 'check_violation';
  END IF;
  PERFORM p2p_only_changed(to_jsonb(OLD), to_jsonb(NEW), ARRAY['status', 'created_id', 'error'], 'import row');
  IF OLD.status NOT IN ('VALID', 'FAILED') OR NEW.status NOT IN ('CREATED', 'FAILED') THEN
    RAISE EXCEPTION 'only a valid or failed row can be created or fail again' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER import_rows_guard BEFORE INSERT OR UPDATE ON import_rows FOR EACH ROW EXECUTE FUNCTION import_rows_guard();

DO $$
DECLARE tbl text; owner text := current_user;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['import_batches', 'import_rows'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, owner);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (status, committed_by, committed_at, expected_rows, discard_reason) ON import_batches TO serk_app;
GRANT UPDATE (status, created_id, error) ON import_rows TO serk_app;
