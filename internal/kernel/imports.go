// Staged imports and the company setup checklist (WP-1.8; SRS INT-03, ADM-01,
// ADM-03, NFR-DAT-05). Database side: migration 0043.
//
// An import never creates anything while it is being read. A CSV file is
// STAGED (every row parsed and validated), a person reviews it, and a
// DIFFERENT person commits it, confirming the row count the source states
// (the reconciliation check). Commit creates each record through the ordinary
// command (CreateSupplier, CreateCustomer, CreateStockItem), so every record
// still goes through its own approval exactly as if typed in. Commit is not one
// transaction: each row is created on its own and its result is recorded, so a
// failure leaves a PARTIAL batch that can be committed again to retry only the
// rows that failed (a record that already exists is reported, never duplicated).
package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// ImportMaxBytes and ImportMaxRows bound an upload.
const (
	ImportMaxBytes = 1 << 20
	ImportMaxRows  = 2000
)

type importKind struct {
	perm    auth.Permission
	columns []string // required header, in order
	example []string
	help    string
}

var importKinds = map[string]importKind{
	"supplier": {auth.PermSupplierManage, []string{"code", "name", "tax_id"}, []string{"ABC-STEEL", "ABC Steel PLC", "0012345678"},
		"Suppliers: a short unique code, the legal name, and the tax identification number (may be empty)."},
	"customer": {auth.PermCustomerManage, []string{"code", "name", "tax_id"}, []string{"ERA", "Ethiopian Roads Administration", "0001234567"},
		"Customers (clients): a short unique code, the legal name, and the tax identification number (may be empty)."},
	"stock_item": {auth.PermStockItemManage, []string{"code", "name", "unit", "valuation_method", "inventory_account"}, []string{"CEM-OPC50", "OPC cement 50kg", "bag", "WAC", "1200"},
		"Stock items: code, name, unit, FIFO or WAC, and the code of the inventory (asset) account in the chosen company."},
}

var importCode = regexp.MustCompile(`^[A-Za-z0-9._/-]{2,30}$`)

// ImportTemplate returns the CSV template of a kind (header and one example row).
func ImportTemplate(kind string) (string, string, bool) {
	k, ok := importKinds[kind]
	if !ok {
		return "", "", false
	}
	return strings.Join(k.columns, ",") + "\n" + strings.Join(k.example, ",") + "\n", k.help, true
}

// ImportBatch is a staged file.
type ImportBatch struct {
	ID           string      `json:"id"`
	Kind         string      `json:"kind"`
	EntityID     string      `json:"legal_entity_id"`
	Filename     string      `json:"filename"`
	SHA256       string      `json:"file_sha256"`
	RowCount     int         `json:"row_count"`
	ValidCount   int         `json:"valid_count"`
	InvalidCount int         `json:"invalid_count"`
	Status       string      `json:"status"`
	CreatedBy    string      `json:"created_by"`
	CommittedBy  string      `json:"committed_by,omitempty"`
	Rows         []ImportRow `json:"rows,omitempty"`
}

// ImportRow is one staged row.
type ImportRow struct {
	Line      int               `json:"line"`
	Data      map[string]string `json:"data"`
	Errors    []string          `json:"errors,omitempty"`
	Status    string            `json:"status"`
	CreatedID string            `json:"created_id,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// StageImport parses and validates a CSV file and stages it. Nothing is created.
func (s *Service) StageImport(ctx context.Context, p auth.Principal, kind, entityID, filename string, content []byte) (string, error) {
	k, ok := importKinds[kind]
	if !ok {
		return "", ErrNotFound
	}
	if err := s.authz.Require(p, k.perm, p.TenantID); err != nil {
		return "", err
	}
	if len(content) == 0 || len(content) > ImportMaxBytes {
		return "", fmt.Errorf("%w: the file must be between 1 byte and %d KB", ErrInvalidInput, ImportMaxBytes/1024)
	}
	content = []byte(strings.TrimPrefix(string(content), "\xef\xbb\xbf")) // spreadsheet exports start with a byte-order mark
	rd := csv.NewReader(strings.NewReader(string(content)))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	recs, err := rd.ReadAll()
	if err != nil {
		return "", fmt.Errorf("%w: the file is not valid CSV: %v", ErrInvalidInput, err)
	}
	if len(recs) < 2 {
		return "", fmt.Errorf("%w: the file needs a header row and at least one data row", ErrInvalidInput)
	}
	head := make([]string, len(recs[0]))
	for i, h := range recs[0] {
		head[i] = strings.ToLower(strings.TrimSpace(h))
	}
	if strings.Join(head, ",") != strings.Join(k.columns, ",") {
		return "", fmt.Errorf("%w: the header must be exactly: %s (download the template)", ErrInvalidInput, strings.Join(k.columns, ","))
	}
	if len(recs)-1 > ImportMaxRows {
		return "", fmt.Errorf("%w: at most %d rows per file", ErrInvalidInput, ImportMaxRows)
	}
	sum := sha256.Sum256(content)
	var batchID string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM legal_entities WHERE id = $1::uuid)`, entityID).Scan(&exists); err != nil || !exists {
			return ErrNotFound
		}
		existing := map[string]bool{}
		accounts := map[string]string{}
		switch kind {
		case "supplier", "customer":
			table := map[string]string{"supplier": "suppliers", "customer": "customers"}[kind]
			rows, err := tx.Query(ctx, `SELECT lower(code) FROM `+table+` WHERE legal_entity_id = $1::uuid`, entityID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var c string
				if err := rows.Scan(&c); err != nil {
					rows.Close()
					return err
				}
				existing[c] = true
			}
			rows.Close()
		case "stock_item":
			rows, err := tx.Query(ctx, `SELECT lower(code) FROM stock_items WHERE legal_entity_id = $1::uuid`, entityID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var c string
				if err := rows.Scan(&c); err != nil {
					rows.Close()
					return err
				}
				existing[c] = true
			}
			rows.Close()
			ar, err := tx.Query(ctx, `SELECT code, id::text FROM accounts WHERE legal_entity_id = $1::uuid AND active AND type = 'ASSET'`, entityID)
			if err != nil {
				return err
			}
			for ar.Next() {
				var code, id string
				if err := ar.Scan(&code, &id); err != nil {
					ar.Close()
					return err
				}
				accounts[code] = id
			}
			ar.Close()
		}
		seen := map[string]int{}
		type staged struct {
			data map[string]string
			errs []string
		}
		var rows []staged
		valid := 0
		for i, rec := range recs[1:] {
			line := i + 2
			d := map[string]string{}
			var errs []string
			if len(rec) != len(k.columns) {
				errs = append(errs, fmt.Sprintf("expected %d columns, found %d", len(k.columns), len(rec)))
			}
			for j, c := range k.columns {
				if j < len(rec) {
					d[c] = strings.TrimSpace(rec[j])
				}
			}
			code := d["code"]
			if !importCode.MatchString(code) {
				errs = append(errs, "code must be 2-30 letters, digits or . _ / -")
			} else if existing[strings.ToLower(code)] {
				errs = append(errs, "a record with this code already exists")
			} else if first, dup := seen[strings.ToLower(code)]; dup {
				errs = append(errs, fmt.Sprintf("code repeated (first on line %d)", first))
			} else {
				seen[strings.ToLower(code)] = line
			}
			if n := len([]rune(d["name"])); n < 3 || n > 200 {
				errs = append(errs, "name must be 3-200 characters")
			}
			switch kind {
			case "supplier", "customer":
				if len(d["tax_id"]) > 30 {
					errs = append(errs, "tax_id is at most 30 characters")
				}
			case "stock_item":
				if n := len(d["unit"]); n < 1 || n > 20 {
					errs = append(errs, "unit must be 1-20 characters")
				}
				d["valuation_method"] = strings.ToUpper(d["valuation_method"])
				if d["valuation_method"] != "FIFO" && d["valuation_method"] != "WAC" {
					errs = append(errs, "valuation_method must be FIFO or WAC")
				}
				if _, ok := accounts[d["inventory_account"]]; !ok {
					errs = append(errs, "inventory_account is not an active asset account of this company")
				}
			}
			if len(errs) == 0 {
				valid++
			}
			rows = append(rows, staged{d, errs})
		}
		var err error
		batchID, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO import_batches (kind, legal_entity_id, filename, file_sha256, row_count, valid_count, invalid_count, created_by)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8) RETURNING id::text`, kind, entityID, filename, hex.EncodeToString(sum[:]), len(rows), valid, len(rows)-valid, p.UserID)
		if err != nil {
			return err
		}
		for i, r := range rows {
			status := "VALID"
			if len(r.errs) > 0 {
				status = "INVALID"
			}
			if r.errs == nil {
				r.errs = []string{}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO import_rows (batch_id, line_no, data, errors, status) VALUES ($1::uuid, $2, $3, $4, $5)`, batchID, i+2, r.data, r.errs, status); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "import.stage", "import_batch", batchID, "import.staged", map[string]any{"import_batch_id": batchID, "kind": kind, "rows": len(rows), "valid": valid})
	})
	return batchID, err
}

// GetImport returns a batch with its rows.
func (s *Service) GetImport(ctx context.Context, p auth.Principal, id string) (ImportBatch, error) {
	var b ImportBatch
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text, kind, legal_entity_id::text, filename, file_sha256, row_count, valid_count, invalid_count, status, created_by, COALESCE(committed_by, '')
			FROM import_batches WHERE id = $1::uuid`, id).Scan(&b.ID, &b.Kind, &b.EntityID, &b.Filename, &b.SHA256, &b.RowCount, &b.ValidCount, &b.InvalidCount, &b.Status, &b.CreatedBy, &b.CommittedBy); err != nil {
			return mapNoRows(err)
		}
		if !s.authz.Has(p, importKinds[b.Kind].perm) {
			return ErrNotFound // indistinguishable from absence
		}
		rows, err := tx.Query(ctx, `SELECT line_no, data, errors, status, COALESCE(created_id, ''), COALESCE(error, '') FROM import_rows WHERE batch_id = $1::uuid ORDER BY line_no`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ImportRow
			if err := rows.Scan(&r.Line, &r.Data, &r.Errors, &r.Status, &r.CreatedID, &r.Error); err != nil {
				return err
			}
			b.Rows = append(b.Rows, r)
		}
		return rows.Err()
	})
	return b, err
}

// ListImports lists batches the caller may see (kinds they may manage).
func (s *Service) ListImports(ctx context.Context, p auth.Principal) ([]ImportBatch, error) {
	var kinds []string
	for name, k := range importKinds {
		if s.authz.Has(p, k.perm) {
			kinds = append(kinds, name)
		}
	}
	if len(kinds) == 0 {
		return nil, auth.ErrForbidden
	}
	out := []ImportBatch{}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, kind, legal_entity_id::text, filename, file_sha256, row_count, valid_count, invalid_count, status, created_by, COALESCE(committed_by, '')
			FROM import_batches WHERE kind = ANY($1) ORDER BY created_at DESC LIMIT 100`, kinds)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b ImportBatch
			if err := rows.Scan(&b.ID, &b.Kind, &b.EntityID, &b.Filename, &b.SHA256, &b.RowCount, &b.ValidCount, &b.InvalidCount, &b.Status, &b.CreatedBy, &b.CommittedBy); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// CommitImport creates the valid rows through the ordinary commands. The
// committer must differ from the person who staged the file, and must confirm
// the number of rows the source states (expectedRows).
func (s *Service) CommitImport(ctx context.Context, p auth.Principal, id string, expectedRows int) (ImportBatch, error) {
	b, err := s.GetImport(ctx, p, id)
	if err != nil {
		return ImportBatch{}, err
	}
	k := importKinds[b.Kind]
	if err := s.authz.Require(p, k.perm, p.TenantID); err != nil {
		return ImportBatch{}, err
	}
	if b.Status != "STAGED" && b.Status != "PARTIAL" {
		return ImportBatch{}, fmt.Errorf("%w: the batch is %s", ErrInvalidInput, strings.ToLower(b.Status))
	}
	if err := auth.CheckMakerChecker(p, b.CreatedBy, "staged import"); err != nil {
		return ImportBatch{}, err
	}
	if expectedRows != b.RowCount {
		return ImportBatch{}, fmt.Errorf("%w: you confirmed %d rows but the staged file has %d; check the source before committing", ErrInvalidInput, expectedRows, b.RowCount)
	}
	accounts := map[string]string{}
	if b.Kind == "stock_item" {
		_ = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT code, id::text FROM accounts WHERE legal_entity_id = $1::uuid AND type = 'ASSET'`, b.EntityID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var code, aid string
				if err := rows.Scan(&code, &aid); err != nil {
					return err
				}
				accounts[code] = aid
			}
			return rows.Err()
		})
	}
	failed := 0
	for _, r := range b.Rows {
		if r.Status != "VALID" && r.Status != "FAILED" {
			continue
		}
		var createdID string
		var cerr error
		switch b.Kind {
		case "supplier":
			createdID, cerr = s.CreateSupplier(ctx, p, b.EntityID, r.Data["code"], r.Data["name"], r.Data["tax_id"])
		case "customer":
			createdID, cerr = s.CreateCustomer(ctx, p, b.EntityID, r.Data["code"], r.Data["name"], r.Data["tax_id"])
		case "stock_item":
			createdID, cerr = s.CreateStockItem(ctx, p, StockItemInput{LegalEntityID: b.EntityID, Code: r.Data["code"], Name: r.Data["name"], Unit: r.Data["unit"],
				ValuationMethod: r.Data["valuation_method"], InventoryAccountID: accounts[r.Data["inventory_account"]]})
		}
		rowErr := ""
		status := "CREATED"
		if cerr != nil {
			status, rowErr = "FAILED", cerr.Error()
			failed++
		}
		if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
			return postgres.Exec1(ctx, tx, `UPDATE import_rows SET status = $3, created_id = NULLIF($4, ''), error = NULLIF($5, '') WHERE batch_id = $1::uuid AND line_no = $2`,
				id, r.Line, status, createdID, rowErr)
		}); err != nil {
			return ImportBatch{}, err
		}
	}
	final := "COMMITTED"
	if failed > 0 || b.InvalidCount > 0 {
		final = "PARTIAL" // invalid rows were skipped; failed rows can be retried
	}
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE import_batches SET status = $2, committed_by = $3, expected_rows = $4 WHERE id = $1::uuid`, id, final, p.UserID, expectedRows); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "import.commit", "import_batch", id, "import.committed", map[string]any{"import_batch_id": id, "kind": b.Kind, "status": final, "failed": failed, "invalid": b.InvalidCount})
	}); err != nil {
		return ImportBatch{}, err
	}
	return s.GetImport(ctx, p, id)
}

// DiscardImport abandons a batch that has not been committed (or is partial), with a reason.
func (s *Service) DiscardImport(ctx context.Context, p auth.Principal, id, reason string) error {
	b, err := s.GetImport(ctx, p, id)
	if err != nil {
		return err
	}
	if err := requireReason(reason); err != nil {
		return err
	}
	if b.Status != "STAGED" && b.Status != "PARTIAL" {
		return fmt.Errorf("%w: the batch is %s", ErrInvalidInput, strings.ToLower(b.Status))
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE import_batches SET status = 'DISCARDED', discard_reason = $2 WHERE id = $1::uuid`, id, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "import.discard", "import_batch", id, "import.discarded", map[string]any{"import_batch_id": id, "reason": reason})
	})
}
