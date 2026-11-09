// Document control, RFIs, submittals and transmittals (WP-3.5; SRS DOC-01..09).
// Migration: 0066_documents.sql.
//
// Files are stored with their SHA-256 (the database re-checks it); a revision
// is WIP until someone other than the uploader publishes it, which supersedes
// the previous published revision. Drafts are visible only to those who keep
// or publish documents; everyone with project scope reads published revisions.
package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

const maxFileBytes = 10 << 20

// DocumentIn creates a document with its first revision.
type DocumentIn struct {
	ProjectID  string `json:"project_id"`
	Number     string `json:"number"`
	Title      string `json:"title"`
	Discipline string `json:"discipline"`
	DocType    string `json:"doc_type"`
	FileName   string `json:"file_name"`
	Mime       string `json:"mime"`
	ContentB64 string `json:"content_base64"`
	Note       string `json:"note"`
}

// RevisionIn uploads a further revision.
type RevisionIn struct {
	FileName   string `json:"file_name"`
	Mime       string `json:"mime"`
	ContentB64 string `json:"content_base64"`
	Note       string `json:"note"`
}

func decodeFile(b64 string) ([]byte, string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) == 0 {
		return nil, "", fmt.Errorf("%w: the file is empty or not valid base64", ErrInvalidInput)
	}
	if len(raw) > maxFileBytes {
		return nil, "", fmt.Errorf("%w: files are at most 10 MB", ErrInvalidInput)
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

// CreateDocument creates a document and uploads revision 1 (WIP).
func (s *Service) CreateDocument(ctx context.Context, p auth.Principal, in DocumentIn) (string, error) {
	raw, sum, err := decodeFile(in.ContentB64)
	if err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermDocManage, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO documents (project_id, number, title, discipline, doc_type, created_by) VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`,
			in.ProjectID, strings.ToUpper(strings.TrimSpace(in.Number)), strings.TrimSpace(in.Title), strings.TrimSpace(in.Discipline), strings.ToUpper(strings.TrimSpace(in.DocType)), p.UserID)
		if err != nil {
			return err
		}
		if err := insertRevision(ctx, tx, id, 1, in.FileName, in.Mime, raw, sum, in.Note, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "document.create", "document", id, "document.created", map[string]any{"id": id, "number": in.Number, "sha256": sum})
	})
	return id, err
}

func insertRevision(ctx context.Context, tx pgx.Tx, doc string, rev int, name, mime string, raw []byte, sum, note, by string) error {
	_, err := tx.Exec(ctx, `INSERT INTO document_revisions (document_id, rev, file_name, mime, size_bytes, sha256, content, note, uploaded_by) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9)`,
		doc, rev, strings.TrimSpace(name), strings.TrimSpace(mime), len(raw), sum, raw, strings.TrimSpace(note), by)
	return err
}

func (s *Service) documentProject(ctx context.Context, p auth.Principal, id string) (string, error) {
	var project string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return mapNoRows(tx.QueryRow(ctx, `SELECT project_id::text FROM documents WHERE id = $1::uuid`, id).Scan(&project))
	})
	return project, err
}

// UploadRevision uploads the next revision (WIP) of a document.
func (s *Service) UploadRevision(ctx context.Context, p auth.Principal, id string, in RevisionIn) (int, error) {
	raw, sum, err := decodeFile(in.ContentB64)
	if err != nil {
		return 0, err
	}
	project, err := s.documentProject(ctx, p, id)
	if err != nil {
		return 0, err
	}
	if err := s.authz.RequireProject(p, auth.PermDocManage, p.TenantID, project); err != nil {
		return 0, err
	}
	var rev int
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(rev), 0) + 1 FROM document_revisions WHERE document_id = $1::uuid`, id).Scan(&rev); err != nil {
			return err
		}
		if err := insertRevision(ctx, tx, id, rev, in.FileName, in.Mime, raw, sum, in.Note, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "document.revise", "document", id, "document.revised", map[string]any{"id": id, "rev": rev, "sha256": sum})
	})
	return rev, err
}

// PublishRevision publishes the WIP revision (not by its uploader); the
// previously published revision becomes SUPERSEDED.
func (s *Service) PublishRevision(ctx context.Context, p auth.Principal, id string) error {
	project, err := s.documentProject(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, auth.PermDocApprove, p.TenantID, project); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var rev string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM document_revisions WHERE document_id = $1::uuid AND status = 'WIP'`, id).Scan(&rev); err != nil {
			return fmt.Errorf("%w: the document has no revision awaiting publication", ErrInvalidInput)
		}
		if _, err := tx.Exec(ctx, `UPDATE document_revisions SET status = 'SUPERSEDED' WHERE document_id = $1::uuid AND status = 'PUBLISHED'`, id); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE document_revisions SET status = 'PUBLISHED', published_by = $2 WHERE id = $1::uuid`, rev, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "document.publish", "document", id, "document.published", map[string]any{"id": id, "revision_id": rev})
	})
}

// DownloadRevision returns a file after the access check; the download is
// audited. Published and superseded revisions are readable by anyone with the
// project; work in progress only by those who keep or publish documents.
func (s *Service) DownloadRevision(ctx context.Context, p auth.Principal, id string, rev int) (name, mime string, content []byte, err error) {
	project, err := s.documentProject(ctx, p, id)
	if err != nil {
		return "", "", nil, err
	}
	if !s.authz.ScopeOf(p).Allows(project) {
		return "", "", nil, ErrNotFound
	}
	reader := s.authz.Has(p, auth.PermDocManage) || s.authz.Has(p, auth.PermDocApprove) || s.authz.Has(p, auth.PermDocRaise) || s.authz.Has(p, auth.PermDocRespond)
	if !reader {
		return "", "", nil, auth.ErrForbidden
	}
	wip := s.authz.Has(p, auth.PermDocManage) || s.authz.Has(p, auth.PermDocApprove)
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var status, sum string
		if err := tx.QueryRow(ctx, `SELECT file_name, mime, content, status, sha256 FROM document_revisions WHERE document_id = $1::uuid AND rev = $2`, id, rev).Scan(&name, &mime, &content, &status, &sum); err != nil {
			return mapNoRows(err)
		}
		if status == "WIP" && !wip {
			return ErrNotFound
		}
		return s.record(ctx, tx, p, "document.download", "document", id, "document.downloaded", map[string]any{"id": id, "rev": rev, "sha256": sum})
	})
	return name, mime, content, err
}

// TransmittalIn sends published documents to a party.
type TransmittalIn struct {
	ProjectID   string   `json:"project_id"`
	Subject     string   `json:"subject"`
	ToParty     string   `json:"to_party"`
	Purpose     string   `json:"purpose"`
	DocumentIDs []string `json:"document_ids"`
}

// SendTransmittal records the current published revision of each document as sent.
func (s *Service) SendTransmittal(ctx context.Context, p auth.Principal, in TransmittalIn) (string, error) {
	if len(in.DocumentIDs) == 0 {
		return "", fmt.Errorf("%w: choose at least one document", ErrInvalidInput)
	}
	if err := s.authz.RequireProject(p, auth.PermDocManage, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "TRM")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO transmittals (project_id, number, subject, to_party, purpose, sent_by) VALUES ($1::uuid, $2, $3, $4, $5, $6) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.Subject), strings.TrimSpace(in.ToParty), strings.ToUpper(strings.TrimSpace(in.Purpose)), p.UserID)
		if err != nil {
			return err
		}
		for _, d := range in.DocumentIDs {
			var rev string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM document_revisions WHERE document_id = $1::uuid AND status = 'PUBLISHED'`, d).Scan(&rev); err != nil {
				return fmt.Errorf("%w: a document has no published revision", ErrInvalidInput)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO transmittal_items (transmittal_id, revision_id) VALUES ($1::uuid, $2::uuid)`, id, rev); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "transmittal.send", "transmittal", id, "transmittal.sent", map[string]any{"id": id, "number": num, "documents": len(in.DocumentIDs)})
	})
	return id, err
}

// RFIIn raises a request for information.
type RFIIn struct {
	ProjectID   string `json:"project_id"`
	Subject     string `json:"subject"`
	Question    string `json:"question"`
	BallInCourt string `json:"ball_in_court"`
	DueOn       string `json:"due_on"`
	DocumentID  string `json:"document_id"`
}

// RaiseRFI raises an RFI.
func (s *Service) RaiseRFI(ctx context.Context, p auth.Principal, in RFIIn) (string, error) {
	due, err := parseDay("due_on", in.DueOn)
	if err != nil {
		return "", err
	}
	if err := s.authz.RequireProject(p, auth.PermDocRaise, p.TenantID, in.ProjectID); err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "RFI")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO rfis (project_id, number, subject, question, ball_in_court, due_on, document_id, raised_by) VALUES ($1::uuid, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid, $8) RETURNING id::text`,
			in.ProjectID, num, strings.TrimSpace(in.Subject), strings.TrimSpace(in.Question), strings.TrimSpace(in.BallInCourt), due, in.DocumentID, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rfi.raise", "rfi", id, "rfi.raised", map[string]any{"id": id, "number": num})
	})
	return id, err
}

// AnswerRFI answers an RFI (not by its raiser); the answer may flag a cost or
// schedule impact and link a change event.
func (s *Service) AnswerRFI(ctx context.Context, p auth.Principal, id, response string, cost, sched bool, changeEvent string) error {
	return s.stepByID(ctx, p, "rfis", "rfi", id, `UPDATE rfis SET status = 'ANSWERED', response = $2, responded_by = $3, cost_impact = $4, schedule_impact = $5, change_event_id = NULLIF($6, '')::uuid WHERE id = $1::uuid`,
		"rfi.answer", []auth.Permission{auth.PermDocRespond}, strings.TrimSpace(response), p.UserID, cost, sched, changeEvent)
}

// CloseRFI closes an answered RFI (not by its responder).
func (s *Service) CloseRFI(ctx context.Context, p auth.Principal, id string) error {
	return s.stepByID(ctx, p, "rfis", "rfi", id, `UPDATE rfis SET status = 'CLOSED', closed_by = $2 WHERE id = $1::uuid`, "rfi.close", []auth.Permission{auth.PermDocRaise}, p.UserID)
}

// SubmitSubmittal and ReviewSubmittal: review codes A to D.
func (s *Service) SubmitSubmittal(ctx context.Context, p auth.Principal, project, section, title string) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermDocRaise, p.TenantID, project); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "SUB-M")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO submittals (project_id, number, spec_section, title, submitted_by) VALUES ($1::uuid, $2, $3, $4, $5) RETURNING id::text`, project, num, strings.TrimSpace(section), strings.TrimSpace(title), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "submittal.submit", "submittal", id, "submittal.submitted", map[string]any{"id": id, "number": num})
	})
	return id, err
}
func (s *Service) ReviewSubmittal(ctx context.Context, p auth.Principal, id, code, note string) error {
	return s.stepByID(ctx, p, "submittals", "submittal", id, `UPDATE submittals SET status = 'REVIEWED', review_code = $2, review_note = NULLIF($3, ''), reviewed_by = $4 WHERE id = $1::uuid`,
		"submittal.review", []auth.Permission{auth.PermDocRespond}, strings.ToUpper(strings.TrimSpace(code)), strings.TrimSpace(note), p.UserID)
}

// DocsView lists documents (with search), transmittals, RFIs and submittals.
func (s *Service) DocsView(ctx context.Context, p auth.Principal, q string) (map[string]any, error) {
	out := map[string]any{}
	if !s.authz.Has(p, auth.PermDocManage) && !s.authz.Has(p, auth.PermDocApprove) && !s.authz.Has(p, auth.PermDocRaise) && !s.authz.Has(p, auth.PermDocRespond) {
		return out, auth.ErrForbidden
	}
	wide, projects := s.scopeArgs(p)
	wip := s.authz.Has(p, auth.PermDocManage) || s.authz.Has(p, auth.PermDocApprove)
	like := "%" + strings.ToLower(strings.TrimSpace(q)) + "%"
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out["projects"], err = rowsAsMaps(ctx, tx, `SELECT id, code, name FROM projects WHERE $1 OR id = ANY($2::uuid[]) ORDER BY code`, wide, projects); err != nil {
			return err
		}
		if out["documents"], err = rowsAsMaps(ctx, tx, `SELECT d.id, d.number, pr.code AS project, d.project_id, d.title, d.discipline, d.doc_type, d.created_by,
			(SELECT json_agg(json_build_object('rev', r.rev, 'file_name', r.file_name, 'size', r.size_bytes, 'sha256', r.sha256, 'status', r.status, 'uploaded_by', r.uploaded_by, 'uploaded_at', r.uploaded_at, 'published_by', r.published_by, 'note', r.note) ORDER BY r.rev DESC)
			   FROM document_revisions r WHERE r.document_id = d.id AND ($4 OR r.status <> 'WIP')) AS revisions
			FROM documents d JOIN projects pr ON pr.id = d.project_id WHERE ($1 OR d.project_id = ANY($2::uuid[]))
			  AND ($3 = '%%' OR lower(d.number || ' ' || d.title || ' ' || d.discipline || ' ' || d.doc_type) LIKE $3) ORDER BY d.number LIMIT 300`, wide, projects, like, wip); err != nil {
			return err
		}
		if out["transmittals"], err = rowsAsMaps(ctx, tx, `SELECT t.id, t.number, pr.code AS project, t.subject, t.to_party, t.purpose, t.sent_by, t.sent_at,
			(SELECT string_agg(d.number || ' rev ' || r.rev, ', ' ORDER BY d.number) FROM transmittal_items i JOIN document_revisions r ON r.id = i.revision_id JOIN documents d ON d.id = r.document_id WHERE i.transmittal_id = t.id) AS items
			FROM transmittals t JOIN projects pr ON pr.id = t.project_id WHERE ($1 OR t.project_id = ANY($2::uuid[])) ORDER BY t.sent_at DESC LIMIT 100`, wide, projects); err != nil {
			return err
		}
		if out["rfis"], err = rowsAsMaps(ctx, tx, `SELECT r.id, r.number, pr.code AS project, r.subject, r.question, r.ball_in_court, r.due_on, r.status, r.response, r.cost_impact, r.schedule_impact, r.raised_by, r.responded_by,
			(r.status = 'OPEN' AND r.due_on < current_date) AS overdue, (SELECT number FROM documents WHERE id = r.document_id) AS document, (SELECT number FROM change_events WHERE id = r.change_event_id) AS change_event
			FROM rfis r JOIN projects pr ON pr.id = r.project_id WHERE ($1 OR r.project_id = ANY($2::uuid[])) ORDER BY (r.status = 'CLOSED'), r.due_on LIMIT 200`, wide, projects); err != nil {
			return err
		}
		if out["submittals"], err = rowsAsMaps(ctx, tx, `SELECT x.id, x.number, pr.code AS project, x.spec_section, x.title, x.status, x.review_code, x.review_note, x.submitted_by, x.reviewed_by
			FROM submittals x JOIN projects pr ON pr.id = x.project_id WHERE ($1 OR x.project_id = ANY($2::uuid[])) ORDER BY x.submitted_at DESC LIMIT 200`, wide, projects); err != nil {
			return err
		}
		out["change_events"], err = rowsAsMaps(ctx, tx, `SELECT c.id, c.number, c.title FROM change_events c WHERE ($1 OR c.project_id = ANY($2::uuid[])) ORDER BY c.number DESC LIMIT 100`, wide, projects)
		return err
	})
	return out, err
}
