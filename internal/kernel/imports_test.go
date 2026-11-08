package kernel

// Tests for staged imports and the setup checklist (WP-1.8, migration 0043,
// kernel/imports.go and setup.go). What is proven:
//  1. staging validates every row and creates nothing;
//  2. a different person commits, confirming the source's row count, and the
//     records are created through the ordinary commands (still unapproved);
//  3. a failed row leaves a PARTIAL batch that retries without duplicating;
//  4. the database refuses forged batch changes; scope and input checks;
//  5. the checklist reads real state.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func countRows(t *testing.T, f *fixture, sql string) int {
	t.Helper()
	var n int
	must(t, "count", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error { return tx.QueryRow(ctx, sql).Scan(&n) }))
	return n
}

func TestImports_StageReviewCommit(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast

	before := countRows(t, f, `SELECT count(*) FROM suppliers`)
	csv := "code,name,tax_id\nNILE-CEM,Nile Cement PLC,0011223344\nHABESHA-CEMENT,Duplicate of an existing supplier,\nbad code!,Bad Code PLC,\nNILE-CEM,Repeated In File,\nSHORT-NAME,Ab,\nGOOD-ONE,Good One Trading,0099887766\n"
	// Without the supplier permission nothing can be staged.
	if _, err := f.svc.StageImport(f.ctx, c.estimator, "supplier", f.entity, "s.csv", []byte(csv)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator staged an import: %v", err)
	}
	for name, bad := range map[string]string{
		"wrong header": "name,code\nA,B\n",
		"header only":  "code,name,tax_id\n",
		"not csv":      "code,name,tax_id\n\"unterminated,x,y\n",
		"empty":        "",
	} {
		if _, err := f.svc.StageImport(f.ctx, c.buyer, "supplier", f.entity, "s.csv", []byte(bad)); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := f.svc.StageImport(f.ctx, c.buyer, "galaxy", f.entity, "s.csv", []byte(csv)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown kind: %v", err)
	}
	id, err := f.svc.StageImport(f.ctx, c.buyer, "supplier", f.entity, "suppliers.csv", []byte("\xef\xbb\xbf"+csv))
	must(t, "stage", err)
	if n := countRows(t, f, `SELECT count(*) FROM suppliers`); n != before {
		t.Fatalf("staging created %d suppliers", n-before)
	}
	b, err := f.svc.GetImport(f.ctx, c.buyer, id)
	must(t, "get", err)
	if b.RowCount != 6 || b.ValidCount != 2 || b.InvalidCount != 4 || b.Status != "STAGED" || len(b.SHA256) != 64 {
		t.Fatalf("batch: %+v", b)
	}
	why := map[int]string{}
	for _, r := range b.Rows {
		why[r.Line] = strings.Join(r.Errors, "; ")
	}
	if !strings.Contains(why[3], "already exists") || !strings.Contains(why[4], "code must be") || !strings.Contains(why[5], "repeated") || !strings.Contains(why[6], "name must be") || why[2] != "" || why[7] != "" {
		t.Fatalf("validation messages: %v", why)
	}
	// Another person without the permission cannot read it.
	if _, err := f.svc.GetImport(f.ctx, c.estimator, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an estimator read an import: %v", err)
	}

	// Commit: not by the stager, with the right confirmed count.
	if _, err := f.svc.CommitImport(f.ctx, c.buyer, id, 6); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the stager committed their own file: %v", err)
	}
	buyer2 := auth.Principal{Kind: auth.KindHuman, UserID: "u-buyer-2", TenantID: f.tenant, Roles: []string{"procurement_officer"}, ProjectIDs: []string{f.project}}
	if _, err := f.svc.CommitImport(f.ctx, buyer2, id, 5); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a wrong confirmed row count was accepted: %v", err)
	}
	if n := countRows(t, f, `SELECT count(*) FROM suppliers`); n != before {
		t.Fatalf("a refused commit created %d suppliers", n-before)
	}
	done, err := f.svc.CommitImport(f.ctx, buyer2, id, 6)
	must(t, "commit", err)
	// 4 invalid rows were skipped: the batch is PARTIAL; the 2 valid rows were created, unapproved.
	if done.Status != "PARTIAL" || done.CommittedBy != "u-buyer-2" {
		t.Fatalf("after commit: %+v", done)
	}
	created := 0
	for _, r := range done.Rows {
		if r.Status == "CREATED" {
			created++
			if r.CreatedID == "" {
				t.Fatal("a created row has no record id")
			}
		}
	}
	if created != 2 || countRows(t, f, `SELECT count(*) FROM suppliers`) != before+2 {
		t.Fatalf("created %d rows, suppliers now %d (was %d)", created, countRows(t, f, `SELECT count(*) FROM suppliers`), before)
	}
	if n := countRows(t, f, `SELECT count(*) FROM suppliers WHERE code IN ('NILE-CEM','GOOD-ONE') AND status = 'PENDING' AND created_by = 'u-buyer-2'`); n != 2 {
		t.Fatalf("imported suppliers must stay PENDING (approval still required): %d", n)
	}
	// Committing again creates nothing new (created rows are not retried).
	again, err := f.svc.CommitImport(f.ctx, buyer2, id, 6)
	must(t, "commit again", err)
	if countRows(t, f, `SELECT count(*) FROM suppliers`) != before+2 || again.Status != "PARTIAL" {
		t.Fatalf("a second commit duplicated records: %+v", again)
	}
	must(t, "discard partial", f.svc.DiscardImport(f.ctx, c.buyer, id, "The remaining rows will be re-sent corrected"))
	if err := f.svc.DiscardImport(f.ctx, c.buyer, id, "again"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a discarded batch was discarded again: %v", err)
	}

	// A fully valid file commits to COMMITTED; the records go through their normal approval.
	id2, err := f.svc.StageImport(f.ctx, c.buyer, "supplier", f.entity, "ok.csv", []byte("code,name,tax_id\nOK-ONE,Ok One PLC,\nOK-TWO,Ok Two PLC,123\n"))
	must(t, "stage 2", err)
	d2, err := f.svc.CommitImport(f.ctx, buyer2, id2, 2)
	must(t, "commit 2", err)
	if d2.Status != "COMMITTED" {
		t.Fatalf("clean batch status %s", d2.Status)
	}
	var sup string
	must(t, "find", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM suppliers WHERE code = 'OK-ONE'`).Scan(&sup)
	}))
	if err := f.svc.ApproveSupplier(f.ctx, buyer2, sup); err == nil {
		t.Fatal("the person who imported a supplier approved it")
	}
	must(t, "approve imported supplier", f.svc.ApproveSupplier(f.ctx, c.procMgr, sup))

	// The database refuses forgery of batches and rows.
	id3, err := f.svc.StageImport(f.ctx, c.buyer, "supplier", f.entity, "f.csv", []byte("code,name,tax_id\nFORGE-ONE,Forge One PLC,\n"))
	must(t, "stage 3", err)
	for name, q := range map[string]string{
		"commit without confirming the count": `UPDATE import_batches SET status = 'COMMITTED', committed_by = 'u-x' WHERE id = '` + id3 + `'`,
		"commit with a wrong count":           `UPDATE import_batches SET status = 'COMMITTED', committed_by = 'u-x', expected_rows = 99 WHERE id = '` + id3 + `'`,
		"commit by the stager":                `UPDATE import_batches SET status = 'COMMITTED', committed_by = created_by, expected_rows = 1 WHERE id = '` + id3 + `'`,
		"edit the file digest":                `UPDATE import_batches SET file_sha256 = repeat('a', 64) WHERE id = '` + id3 + `'`,
		"edit a staged row":                   `UPDATE import_rows SET data = '{"code":"HACK"}' WHERE batch_id = '` + id3 + `'`,
		"mark an invalid row created":         `UPDATE import_rows SET status = 'CREATED' WHERE batch_id = '` + id + `' AND status = 'INVALID'`,
		"edit a committed batch":              `UPDATE import_batches SET discard_reason = 'x' WHERE id = '` + id2 + `'`,
		"add a row to a committed batch":      `INSERT INTO import_rows (batch_id, line_no, data, status) VALUES ('` + id2 + `', 99, '{}', 'VALID')`,
		"insert a committed batch":            `INSERT INTO import_batches (kind, legal_entity_id, filename, file_sha256, row_count, valid_count, invalid_count, status, created_by) VALUES ('supplier', '` + f.entity + `', 'x', repeat('a', 64), 1, 1, 0, 'COMMITTED', 'x')`,
	} {
		err := f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, q)
			if err == nil && ct.RowsAffected() == 0 {
				return errors.New("no rows")
			}
			return err
		})
		if err == nil {
			t.Fatalf("database accepted: %s", name)
		}
		t.Logf("held: %s: %v", name, err)
	}
	// The other company sees nothing.
	var n int
	must(t, "tenant2", f.app.WithTenant(f.ctx, f.tenant2, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM import_batches) + (SELECT count(*) FROM import_rows)`).Scan(&n)
	}))
	if n != 0 {
		t.Fatalf("tenant2 reads %d import rows of tenant1", n)
	}
}

func TestImports_StockItemsAndCustomers(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	buyer2 := auth.Principal{Kind: auth.KindHuman, UserID: "u-buyer-3", TenantID: f.tenant, Roles: []string{"procurement_officer"}, ProjectIDs: []string{f.project}}
	arClerk := auth.Principal{Kind: auth.KindHuman, UserID: "u-ar-clerk-i", TenantID: f.tenant, Roles: []string{"ar_clerk"}}
	// Stock items: account code, method and unit are validated.
	csv := "code,name,unit,valuation_method,inventory_account\nCEM-1,Cement 50kg,bag,wac,1200\nSTL-1,Steel,t,FIFO,9999\nBRK-1,Bricks,pc,AVG,1200\n"
	id, err := f.svc.StageImport(f.ctx, c.buyer, "stock_item", f.entity, "items.csv", []byte(csv))
	must(t, "stage items", err)
	b, err := f.svc.GetImport(f.ctx, c.buyer, id)
	must(t, "get", err)
	if b.ValidCount != 1 || b.InvalidCount != 2 {
		t.Fatalf("stock item validation: %+v", b)
	}
	if !strings.Contains(strings.Join(b.Rows[1].Errors, ";"), "inventory_account") || !strings.Contains(strings.Join(b.Rows[2].Errors, ";"), "FIFO or WAC") {
		t.Fatalf("messages: %+v", b.Rows)
	}
	d, err := f.svc.CommitImport(f.ctx, buyer2, id, 3)
	must(t, "commit items", err)
	if d.Status != "PARTIAL" || d.Rows[0].Status != "CREATED" {
		t.Fatalf("items commit: %+v", d)
	}
	// Customers need the customer permission, not the supplier one.
	if _, err := f.svc.StageImport(f.ctx, c.buyer, "customer", f.entity, "c.csv", []byte("code,name,tax_id\nERA,Ethiopian Roads Administration,1\n")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer staged customers: %v", err)
	}
	cid, err := f.svc.StageImport(f.ctx, arClerk, "customer", f.entity, "c.csv", []byte("code,name,tax_id\nERA,Ethiopian Roads Administration,1\n"))
	must(t, "stage customers", err)
	if b, _ := f.svc.GetImport(f.ctx, arClerk, cid); b.ValidCount != 1 {
		t.Fatalf("customer batch: %+v", b)
	}
	ls, err := f.svc.ListImports(f.ctx, arClerk)
	must(t, "list", err)
	for _, x := range ls {
		if x.Kind != "customer" {
			t.Fatalf("an AR clerk lists %s imports", x.Kind)
		}
	}
}

func TestSetup_ChecklistReadsRealState(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	steps, err := f.svc.SetupChecklist(f.ctx, c.cfo)
	must(t, "checklist", err)
	state := map[string]SetupStep{}
	for _, s := range steps {
		state[s.ID] = s
		if s.Title == "" || s.Why == "" || s.Who == "" || s.Href == "" {
			t.Fatalf("step %s is not self-explanatory: %+v", s.ID, s)
		}
	}
	// The P2P world has accounts, an approved P2P policy, a bank, suppliers, a project and an approved WBS.
	for _, id := range []string{"accounts", "p2p_policy", "bank", "suppliers", "project", "wbs"} {
		if !state[id].Done {
			t.Fatalf("step %s should be done: %+v", id, state[id])
		}
	}
	for _, id := range []string{"boq", "authority", "regions", "stock_items"} {
		if state[id].Done {
			t.Fatalf("step %s should not be done yet: %+v", id, state[id])
		}
	}
	w.approvedBOQ(t)
	steps, _ = f.svc.SetupChecklist(f.ctx, c.cfo)
	for _, s := range steps {
		if s.ID == "boq" && !s.Done {
			t.Fatal("approving a BOQ did not tick the step")
		}
	}
	// A person with no procurement or company permissions cannot read the checklist.
	if _, err := f.svc.SetupChecklist(f.ctx, auth.Principal{Kind: auth.KindHuman, UserID: "u-n", TenantID: f.tenant, Roles: []string{"workflow_admin"}}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a workflow admin read the setup checklist: %v", err)
	}
}
