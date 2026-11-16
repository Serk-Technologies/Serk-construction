package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// DocPage is one page of a document list (XCT-API.2). Next is an opaque
// cursor for the following page; it is empty on the last page.
type DocPage struct {
	Items []postgres.DocSummary `json:"items"`
	Next  string                `json:"next,omitempty"`
}

// listCursor is what a cursor carries. It binds the position to the tenant,
// the document type and the status filter it was issued for: a cursor used
// with another tenant or another filter is refused. The position itself is not
// secret and need not be: a hand-made position only chooses where a page
// starts; row-level security and the reader's project scope still decide which
// rows can be seen. The checksum catches damaged cursors.
type listCursor struct {
	V      int       `json:"v"`
	Tenant string    `json:"tn"`
	Type   string    `json:"t"`
	Status string    `json:"s"`
	At     time.Time `json:"at"`
	ID     string    `json:"id"`
	Sum    string    `json:"c"`
}

func (c listCursor) sum() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s|%s|%s", c.V, c.Tenant, c.Type, c.Status, c.At.UTC().Format(time.RFC3339Nano), c.ID)))
	return base64.RawURLEncoding.EncodeToString(h[:9])
}

func encodeListCursor(tenant, docType, status string, last postgres.DocSummary) string {
	return encodeCursorAt(tenant, docType, status, last.CreatedAt, last.ID)
}

// encodeCursorAt and decodeListCursor serve every cursor-paged list: list
// names a list (a document type, or e.g. "journal") and filter is its whole
// filter, so a cursor is refused with any other list or filter.
func encodeCursorAt(tenant, list, filter string, at time.Time, id string) string {
	c := listCursor{V: 1, Tenant: tenant, Type: list, Status: filter, At: at.UTC(), ID: id}
	c.Sum = c.sum()
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeListCursor(s, tenant, docType, status string) (*postgres.ListPosition, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	var c listCursor
	if err != nil || len(b) > 512 || json.Unmarshal(b, &c) != nil || c.V != 1 || c.Sum != c.sum() || c.ID == "" {
		return nil, fmt.Errorf("%w: invalid cursor", ErrInvalidInput)
	}
	if c.Tenant != tenant || c.Type != docType || c.Status != status {
		return nil, fmt.Errorf("%w: the cursor was issued for another list; start again without a cursor", ErrInvalidInput)
	}
	return &postgres.ListPosition{CreatedAt: c.At, ID: c.ID}, nil
}

// ListP2PDocsPage is ListP2PDocs a page at a time, in a stable newest-first
// order. limit is 1..500 (default 200).
func (s *Service) ListP2PDocsPage(ctx context.Context, p auth.Principal, docType, status, cursor string, limit int) (DocPage, error) {
	out := DocPage{Items: []postgres.DocSummary{}}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	after, err := decodeListCursor(cursor, p.TenantID, docType, status)
	if err != nil {
		return out, err
	}
	err = s.p2pTx(ctx, p, s.readPerm(p, docType), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := postgres.ListDocsAfter(ctx, tx, docType, status, s.listScope(p, docType), limit+1, after)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			out.Next = encodeListCursor(p.TenantID, docType, status, rows[limit-1])
		}
		if rows != nil {
			out.Items = rows
		}
		return nil
	})
	return out, err
}

// JournalPage is one page of the journal worklist.
type JournalPage struct {
	Journals []postgres.JournalSummary `json:"journals"`
	Next     string                    `json:"next,omitempty"`
}

// ListJournalsPage is ListJournals a page at a time, newest first (XCT-API.2.e).
// The cursor is bound to the tenant and to both filters. limit is 1..500
// (default 200).
func (s *Service) ListJournalsPage(ctx context.Context, p auth.Principal, status, sourcePrefix, cursor string, limit int) (JournalPage, error) {
	out := JournalPage{Journals: []postgres.JournalSummary{}}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if err := s.authz.Require(p, auth.PermJournalRead, p.TenantID); err != nil {
		return out, err
	}
	if !s.tenantWide(p) {
		return out, fmt.Errorf("%w: journal worklists need tenant-wide scope", auth.ErrForbidden)
	}
	filter := status + "|" + sourcePrefix
	after, err := decodeListCursor(cursor, p.TenantID, "journal", filter)
	if err != nil {
		return out, err
	}
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := postgres.ListJournalsAfter(ctx, tx, status, sourcePrefix, limit+1, after)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			last := rows[limit-1]
			out.Next = encodeCursorAt(p.TenantID, "journal", filter, last.CreatedAt, last.ID)
		}
		if rows != nil {
			out.Journals = rows
		}
		return nil
	})
	return out, err
}
