// Controlled documents, revisions and requests for information (CONNECT;
// requirements CNX-01..03; ADR 0018). Migration: 0052_document_control.sql.
//
// A document is a register entry of a project; its revisions carry the file.
// One person uploads and submits a revision, another publishes it with a fresh
// multi-factor sign-in. Publishing supersedes the previous current revision in
// the same transaction, so a document never has two current revisions and a
// superseded revision is never offered as current. The database hashes the
// stored bytes itself; the hash the approver attests is the hash of the file.
//
// An RFI is answered once, by a person other than its author, then closed.
package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// MaxDocumentBytes is the largest file a revision may carry (ADR 0018). It is
// a storage protection of this deployment profile, enforced again by the
// database, not a limit on how many documents or revisions a project has.
const MaxDocumentBytes = 10 << 20

// documentMedia is the allow-list of media types with the leading bytes a
// file of that type must have ("" = no signature to check).
var documentMedia = map[string][]string{
	"application/pdf": {"%PDF-"},
	"image/png":       {"\x89PNG\r\n\x1a\n"},
	"image/jpeg":      {"\xff\xd8\xff"},
	"text/plain":      nil,
	"text/csv":        nil,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {"PK\x03\x04"},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       {"PK\x03\x04"},
	"image/vnd.dwg":      {"AC10"},
	"image/vnd.dxf":      nil,
	"application/x-step": {"ISO-10303-21"},
}

var documentTypes = map[string]bool{"DRAWING": true, "SPECIFICATION": true, "METHOD_STATEMENT": true, "REPORT": true,
	"CORRESPONDENCE": true, "CONTRACT": true, "CERTIFICATE": true, "OTHER": true}

// DocumentInput registers a controlled document.
type DocumentInput struct {
	ProjectID  string `json:"project_id"`
	Reference  string `json:"reference"` // the project's own reference, e.g. a drawing number
	DocType    string `json:"doc_type"`
	Title      string `json:"title"`
	Discipline string `json:"discipline,omitempty"`
}

// RevisionInput uploads a revision of a document.
type RevisionInput struct {
	Revision   string `json:"revision"`
	FileName   string `json:"file_name"`
	MediaType  string `json:"media_type"`
	ChangeNote string `json:"change_note"`
	Content    []byte `json:"content"` // base64 in JSON
}

// DocumentRevision is a revision without its file.
type DocumentRevision struct {
	ID             string     `json:"id"`
	Number         string     `json:"number"`
	Revision       string     `json:"revision"`
	Status         string     `json:"status"`
	FileName       string     `json:"file_name"`
	MediaType      string     `json:"media_type"`
	SizeBytes      int        `json:"size_bytes"`
	SHA256         string     `json:"sha256"`
	ChangeNote     string     `json:"change_note"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	ApprovedBy     string     `json:"approved_by,omitempty"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
	DecisionReason string     `json:"decision_reason,omitempty"`
	SupersededAt   *time.Time `json:"superseded_at,omitempty"`
}

// Document is a register entry with its current revision (if one is
// published) and, in a detail read, every revision newest first.
type Document struct {
	ID         string             `json:"id"`
	Number     string             `json:"number"`
	ProjectID  string             `json:"project_id"`
	Reference  string             `json:"reference"`
	DocType    string             `json:"doc_type"`
	Title      string             `json:"title"`
	Discipline string             `json:"discipline,omitempty"`
	CreatedBy  string             `json:"created_by"`
	CreatedAt  time.Time          `json:"created_at"`
	Current    *DocumentRevision  `json:"current"`             // the one published revision, or null
	InReview   int                `json:"revisions_in_review"` // DRAFT or SUBMITTED (0 or 1)
	Revisions  []DocumentRevision `json:"revisions,omitempty"`
	Superseded int                `json:"superseded_revisions"`
}

// RevisionFile is a revision's file as stored.
type RevisionFile struct {
	DocumentRevision
	Content []byte `json:"-"`
}

// documentTx checks perm on the project, then runs fn in a tenant transaction.
func (s *Service) documentTx(ctx context.Context, p auth.Principal, perm auth.Permission, projectID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return err
	}
	if err := s.authz.RequireProject(p, perm, p.TenantID, projectID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, fn)
}

// projectOfDocument returns the project of a document, or ErrNotFound when it
// does not exist or is outside the principal's project scope.
func (s *Service) projectOfDocument(ctx context.Context, p auth.Principal, perm auth.Permission, documentID string) (string, error) {
	if err := s.authz.Require(p, perm, p.TenantID); err != nil {
		return "", err
	}
	var project string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT project_id::text FROM documents WHERE id = $1::uuid`, documentID).Scan(&project)
	})
	if err != nil {
		return "", mapNoRows(err)
	}
	if !s.authz.ScopeOf(p).Allows(project) {
		return "", ErrNotFound // out of scope looks like absence
	}
	return project, nil
}

// CreateDocument registers a controlled document in a project.
func (s *Service) CreateDocument(ctx context.Context, p auth.Principal, in DocumentInput) (string, error) {
	in.DocType = strings.ToUpper(strings.TrimSpace(in.DocType))
	in.Reference, in.Title, in.Discipline = strings.TrimSpace(in.Reference), strings.TrimSpace(in.Title), strings.TrimSpace(in.Discipline)
	if !documentTypes[in.DocType] {
		return "", fmt.Errorf("%w: doc_type must be one of DRAWING, SPECIFICATION, METHOD_STATEMENT, REPORT, CORRESPONDENCE, CONTRACT, CERTIFICATE, OTHER", ErrInvalidInput)
	}
	if in.Reference == "" || len(in.Title) < 3 {
		return "", fmt.Errorf("%w: a document needs a reference and a title", ErrInvalidInput)
	}
	var id string
	err := s.documentTx(ctx, p, auth.PermDocumentManage, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM documents WHERE project_id = $1::uuid AND reference = $2)`, in.ProjectID, in.Reference).Scan(&dup); err != nil {
			return err
		}
		if dup {
			return fmt.Errorf("%w: this project already has a document with reference %s; upload a new revision of it instead", ErrInvalidInput, in.Reference)
		}
		num, err := postgres.NextNumber(ctx, tx, "DOC")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO documents (project_id, number, reference, doc_type, title, discipline, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, ''), $7) RETURNING id::text`, in.ProjectID, num, in.Reference, in.DocType, in.Title, in.Discipline, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "document.create", "document", id, "document.registered",
			map[string]any{"document_id": id, "project_id": in.ProjectID, "number": num, "reference": in.Reference})
	})
	return id, err
}

// checkDocumentFile validates a file against the allow-list and its signature.
func checkDocumentFile(mediaType string, content []byte) error {
	sigs, ok := documentMedia[mediaType]
	if !ok {
		return fmt.Errorf("%w: files of type %q are not accepted", ErrInvalidInput, mediaType)
	}
	if len(content) == 0 {
		return fmt.Errorf("%w: the file is empty", ErrInvalidInput)
	}
	if len(content) > MaxDocumentBytes {
		return fmt.Errorf("%w: the file is larger than %d MiB", ErrInvalidInput, MaxDocumentBytes>>20)
	}
	if sigs == nil {
		// Text formats: refuse bytes that are plainly not text.
		if bytes.IndexByte(content, 0) >= 0 {
			return fmt.Errorf("%w: the file is not %s", ErrInvalidInput, mediaType)
		}
		return nil
	}
	for _, sig := range sigs {
		if bytes.HasPrefix(content, []byte(sig)) {
			return nil
		}
	}
	return fmt.Errorf("%w: the file's content is not %s", ErrInvalidInput, mediaType)
}

// AddRevision uploads a DRAFT revision (with its file) of a document.
func (s *Service) AddRevision(ctx context.Context, p auth.Principal, documentID string, in RevisionInput) (string, error) {
	in.Revision, in.FileName, in.MediaType, in.ChangeNote = strings.TrimSpace(in.Revision), strings.TrimSpace(in.FileName), strings.TrimSpace(in.MediaType), strings.TrimSpace(in.ChangeNote)
	if in.Revision == "" || in.FileName == "" || len(in.ChangeNote) < 3 {
		return "", fmt.Errorf("%w: a revision needs its revision code, a file name and a note of what changed", ErrInvalidInput)
	}
	if err := checkDocumentFile(in.MediaType, in.Content); err != nil {
		return "", err
	}
	project, err := s.projectOfDocument(ctx, p, auth.PermDocumentManage, documentID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(in.Content)
	var id string
	err = s.documentTx(ctx, p, auth.PermDocumentManage, project, func(ctx context.Context, tx pgx.Tx) error {
		// One revision in preparation at a time: checked here for a clear
		// message, and held under concurrency by the database's unique indexes.
		var docNumber string
		if err := tx.QueryRow(ctx, `SELECT number FROM documents WHERE id = $1::uuid`, documentID).Scan(&docNumber); err != nil {
			return mapNoRows(err)
		}
		var open, taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document_revisions WHERE document_id = $1::uuid AND status IN ('DRAFT', 'SUBMITTED')),
			EXISTS (SELECT 1 FROM document_revisions WHERE document_id = $1::uuid AND revision = $2)`, documentID, in.Revision).Scan(&open, &taken); err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%w: revision %s of this document already exists", ErrInvalidInput, in.Revision)
		}
		if open {
			return fmt.Errorf("%w: this document already has a revision in preparation; publish or reject it first", ErrInvalidInput)
		}
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO document_revisions (document_id, number, revision, file_name, media_type, size_bytes, sha256, change_note, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id::text`,
			documentID, docNumber+"/"+in.Revision, in.Revision, in.FileName, in.MediaType, len(in.Content), hex.EncodeToString(sum[:]), in.ChangeNote, p.UserID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document_contents (revision_id, content) VALUES ($1::uuid, $2)`, id, in.Content); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "document.revision.create", "document_revision", id, "document.revision_uploaded",
			map[string]any{"revision_id": id, "document_id": documentID, "revision": in.Revision, "sha256": hex.EncodeToString(sum[:]), "size_bytes": len(in.Content)})
	})
	return id, err
}

// SubmitRevision submits a draft revision for publication (by its author).
func (s *Service) SubmitRevision(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermDocumentManage, "document_revision", "document_revisions", id, "document.submit", "document.revision_submitted")
}

// DecideRevision publishes a submitted revision (superseding the document's
// previous current revision) or rejects it. Fresh MFA, not the author.
func (s *Service) DecideRevision(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermDocumentApprove, "document_revision", "document_revisions", id, "document.decide", "document revision", approve, reason,
		func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE document_revisions SET status = 'SUPERSEDED' WHERE status = 'APPROVED' AND id <> $1::uuid
				AND document_id = (SELECT document_id FROM document_revisions WHERE id = $1::uuid)`, id)
			return err
		})
}

const revisionCols = `v.id::text, v.number, v.revision, v.status, v.file_name, v.media_type, v.size_bytes, v.sha256, v.change_note, v.created_by, v.created_at,
	COALESCE(v.approved_by, ''), v.approved_at, COALESCE(v.decision_reason, ''), v.superseded_at`

func scanRevision(row pgx.Row, r *DocumentRevision) error {
	return row.Scan(&r.ID, &r.Number, &r.Revision, &r.Status, &r.FileName, &r.MediaType, &r.SizeBytes, &r.SHA256, &r.ChangeNote, &r.CreatedBy, &r.CreatedAt,
		&r.ApprovedBy, &r.ApprovedAt, &r.DecisionReason, &r.SupersededAt)
}

// ListDocuments returns the register of the principal's projects (optionally
// one project), each with its current revision. limit is capped at 500.
func (s *Service) ListDocuments(ctx context.Context, p auth.Principal, projectID string, limit int) ([]Document, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	out := []Document{}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		sc := s.authz.ScopeOf(p)
		if sc.Projects == nil {
			sc.Projects = []string{}
		}
		rows, err := tx.Query(ctx, `SELECT d.id::text, d.number, d.project_id::text, d.reference, d.doc_type, d.title, COALESCE(d.discipline, ''), d.created_by, d.created_at,
			(SELECT count(*) FROM document_revisions o WHERE o.document_id = d.id AND o.status IN ('DRAFT', 'SUBMITTED')),
			(SELECT count(*) FROM document_revisions o WHERE o.document_id = d.id AND o.status = 'SUPERSEDED'),
			(SELECT v.id::text FROM document_revisions v WHERE v.document_id = d.id AND v.status = 'APPROVED')
			FROM documents d WHERE ($1 = '' OR d.project_id = $1::uuid) AND ($2 OR d.project_id = ANY($3::uuid[]))
			ORDER BY d.reference COLLATE "C" LIMIT $4`, projectID, sc.TenantWide, sc.Projects, limit)
		if err != nil {
			return err
		}
		var current []*string
		for rows.Next() {
			var d Document
			var cur *string
			if err := rows.Scan(&d.ID, &d.Number, &d.ProjectID, &d.Reference, &d.DocType, &d.Title, &d.Discipline, &d.CreatedBy, &d.CreatedAt, &d.InReview, &d.Superseded, &cur); err != nil {
				rows.Close()
				return err
			}
			out = append(out, d)
			current = append(current, cur)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, cur := range current {
			if cur == nil {
				continue
			}
			var r DocumentRevision
			if err := scanRevision(tx.QueryRow(ctx, `SELECT `+revisionCols+` FROM document_revisions v WHERE v.id = $1::uuid`, *cur), &r); err != nil {
				return err
			}
			out[i].Current = &r
		}
		return nil
	})
	return out, err
}

// GetDocument returns a register entry with every revision, newest first.
func (s *Service) GetDocument(ctx context.Context, p auth.Principal, id string) (Document, error) {
	var d Document
	if _, err := s.projectOfDocument(ctx, p, auth.PermP2PRead, id); err != nil {
		return d, err
	}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text, number, project_id::text, reference, doc_type, title, COALESCE(discipline, ''), created_by, created_at FROM documents WHERE id = $1::uuid`, id).
			Scan(&d.ID, &d.Number, &d.ProjectID, &d.Reference, &d.DocType, &d.Title, &d.Discipline, &d.CreatedBy, &d.CreatedAt); err != nil {
			return mapNoRows(err)
		}
		rows, err := tx.Query(ctx, `SELECT `+revisionCols+` FROM document_revisions v WHERE v.document_id = $1::uuid ORDER BY v.created_at DESC, v.id`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		d.Revisions = []DocumentRevision{}
		for rows.Next() {
			var r DocumentRevision
			if err := scanRevision(rows, &r); err != nil {
				return err
			}
			switch r.Status {
			case "APPROVED":
				c := r
				d.Current = &c
			case "DRAFT", "SUBMITTED":
				d.InReview++
			case "SUPERSEDED":
				d.Superseded++
			}
			d.Revisions = append(d.Revisions, r)
		}
		return rows.Err()
	})
	return d, err
}

// DownloadRevision returns a revision's file to a reader of its project. The
// download counts against the person's daily export budget and is recorded;
// the caller must present the revision's status with the file, because a
// superseded or unpublished revision is not the current one.
func (s *Service) DownloadRevision(ctx context.Context, p auth.Principal, id string) (RevisionFile, error) {
	var f RevisionFile
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "document_revision", id, false); err != nil {
			return err
		}
		return scanRevision(tx.QueryRow(ctx, `SELECT `+revisionCols+` FROM document_revisions v WHERE v.id = $1::uuid`, id), &f.DocumentRevision)
	})
	if err != nil {
		return f, mapNoRows(err)
	}
	if err := s.RecordExport(ctx, p, "document_revision:"+id, int64(f.SizeBytes)); err != nil {
		return RevisionFile{}, err
	}
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT content FROM document_contents WHERE revision_id = $1::uuid`, id).Scan(&f.Content)
	})
	if err != nil {
		return RevisionFile{}, mapNoRows(err)
	}
	// Defence in depth: never hand out bytes that are not what was attested.
	if sum := sha256.Sum256(f.Content); hex.EncodeToString(sum[:]) != f.SHA256 {
		return RevisionFile{}, fmt.Errorf("kernel: stored file of revision %s does not match its recorded hash", id)
	}
	return f, nil
}

// ------------------------------------------------------------------ RFIs

// RFIInput raises a request for information.
type RFIInput struct {
	ProjectID  string `json:"project_id"`
	Subject    string `json:"subject"`
	Question   string `json:"question"`
	RevisionID string `json:"revision_id,omitempty"` // the document revision the question is about
	AssignedTo string `json:"assigned_to"`           // who is expected to answer
	DueOn      string `json:"due_on"`
}

// RFI is a request for information with its computed standing.
type RFI struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	ProjectID  string `json:"project_id"`
	Subject    string `json:"subject"`
	Question   string `json:"question"`
	RevisionID string `json:"revision_id,omitempty"`
	AssignedTo string `json:"assigned_to"`
	DueOn      string `json:"due_on"`
	Status     string `json:"status"`
	Standing   string `json:"standing"` // OPEN | DUE_SOON | OVERDUE | ANSWERED | ANSWERED_LATE | CLOSED
	Answer     string `json:"answer,omitempty"`
	AnsweredBy string `json:"answered_by,omitempty"`
	CreatedBy  string `json:"created_by"`
}

// CreateRFI raises an RFI on a project.
func (s *Service) CreateRFI(ctx context.Context, p auth.Principal, in RFIInput) (string, error) {
	due, err := time.Parse("2006-01-02", strings.TrimSpace(in.DueOn))
	if err != nil {
		return "", fmt.Errorf("%w: due_on must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	in.Subject, in.Question, in.AssignedTo = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Question), strings.TrimSpace(in.AssignedTo)
	if len(in.Subject) < 3 || len(in.Question) < 10 || in.AssignedTo == "" {
		return "", fmt.Errorf("%w: an RFI needs a subject, a question (10+ characters) and the person expected to answer", ErrInvalidInput)
	}
	var id string
	err = s.documentTx(ctx, p, auth.PermDocumentManage, in.ProjectID, func(ctx context.Context, tx pgx.Tx) error {
		if in.RevisionID != "" {
			var same bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM document_revisions v JOIN documents d ON d.id = v.document_id WHERE v.id = $1::uuid AND d.project_id = $2::uuid)`,
				in.RevisionID, in.ProjectID).Scan(&same); err != nil {
				return err
			}
			if !same {
				return fmt.Errorf("%w: the revision is not a document of this project", ErrInvalidInput)
			}
		}
		num, err := postgres.NextNumber(ctx, tx, "RFI")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO rfis (project_id, number, subject, question, revision_id, assigned_to, due_on, created_by)
			VALUES ($1::uuid, $2, $3, $4, NULLIF($5, '')::uuid, $6, $7, $8) RETURNING id::text`, in.ProjectID, num, in.Subject, in.Question, in.RevisionID, in.AssignedTo, due, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rfi.create", "rfi", id, "rfi.raised", map[string]any{"rfi_id": id, "project_id": in.ProjectID, "number": num, "due_on": in.DueOn})
	})
	return id, err
}

// AnswerRFI records the answer to an open RFI, once, by a person other than
// its author (attested: an answer is a human decision).
func (s *Service) AnswerRFI(ctx context.Context, p auth.Principal, id, answer string) error {
	answer = strings.TrimSpace(answer)
	if len(answer) < 3 {
		return fmt.Errorf("%w: the answer is required", ErrInvalidInput)
	}
	return s.p2pTx(ctx, p, auth.PermDocumentManage, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermDocumentManage, "rfi", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "OPEN"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "RFI"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "rfi.answer", "rfi", id, ""); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE rfis SET status = 'ANSWERED', answer = $2, answered_by = $3, answered_at = now() WHERE id = $1::uuid AND status = 'OPEN'`, id, answer, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "rfi.answer", "rfi", id, "rfi.answered", map[string]any{"rfi_id": id})
	})
}

// CloseRFI closes an answered RFI, or withdraws an unanswered one with a
// reason. Only its author closes an RFI.
func (s *Service) CloseRFI(ctx context.Context, p auth.Principal, id, note string) error {
	note = strings.TrimSpace(note)
	return s.p2pTx(ctx, p, auth.PermDocumentManage, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermDocumentManage, "rfi", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "OPEN", "ANSWERED"); err != nil {
			return err
		}
		if d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the person who raised an RFI closes it", auth.ErrForbidden)
		}
		if d.Status == "OPEN" {
			if err := requireReason(note); err != nil {
				return err
			}
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE rfis SET status = 'CLOSED', closed_by = $2, closed_at = now(), close_note = NULLIF($3, '') WHERE id = $1::uuid AND status <> 'CLOSED'`, id, p.UserID, note); err != nil {
			return err
		}
		event := map[bool]string{true: "rfi.withdrawn", false: "rfi.closed"}[d.Status == "OPEN"]
		return s.record(ctx, tx, p, "rfi.close", "rfi", id, event, map[string]any{"rfi_id": id, "note": note})
	})
}

// ListRFIs returns the RFIs of the principal's projects with their standing.
func (s *Service) ListRFIs(ctx context.Context, p auth.Principal, projectID, status string, limit int) ([]RFI, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	status = strings.ToUpper(strings.TrimSpace(status))
	switch status {
	case "", "OPEN", "ANSWERED", "CLOSED":
	default:
		return nil, fmt.Errorf("%w: status must be OPEN, ANSWERED or CLOSED", ErrInvalidInput)
	}
	out := []RFI{}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		sc := s.authz.ScopeOf(p)
		if sc.Projects == nil {
			sc.Projects = []string{}
		}
		rows, err := tx.Query(ctx, `SELECT r.id::text, r.number, r.project_id::text, r.subject, r.question, COALESCE(r.revision_id::text, ''), r.assigned_to, r.due_on::text, r.status,
			CASE WHEN r.status = 'CLOSED' THEN 'CLOSED'
			     WHEN r.status = 'ANSWERED' AND r.answered_at::date > r.due_on THEN 'ANSWERED_LATE' WHEN r.status = 'ANSWERED' THEN 'ANSWERED'
			     WHEN current_date > r.due_on THEN 'OVERDUE' WHEN r.due_on - current_date <= 3 THEN 'DUE_SOON' ELSE 'OPEN' END,
			COALESCE(r.answer, ''), COALESCE(r.answered_by, ''), r.created_by
			FROM rfis r WHERE ($1 = '' OR r.project_id = $1::uuid) AND ($2 = '' OR r.status = $2) AND ($3 OR r.project_id = ANY($4::uuid[]))
			ORDER BY r.due_on, r.number LIMIT $5`, projectID, status, sc.TenantWide, sc.Projects, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RFI
			if err := rows.Scan(&r.ID, &r.Number, &r.ProjectID, &r.Subject, &r.Question, &r.RevisionID, &r.AssignedTo, &r.DueOn, &r.Status, &r.Standing, &r.Answer, &r.AnsweredBy, &r.CreatedBy); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
