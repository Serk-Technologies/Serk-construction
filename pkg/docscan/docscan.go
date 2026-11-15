// Package docscan decides whether an uploaded file may be kept: structural
// inspection that needs no external service (type signatures, archive-bomb and
// path-traversal checks, active content), and malware scanning through a
// Scanner, of which the ClamAV daemon client is the one operational
// implementation (ADR 0019).
//
// Inspection is a filter, not a malware control: it refuses what is plainly
// unsafe or mislabelled. Only a Scanner verdict of Clean says a file was
// scanned.
package docscan

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Verdict is the outcome of a scan.
type Verdict struct {
	Clean     bool
	Signature string // what was found, when not clean
	Engine    string // scanner name and version, as reported by the scanner
}

// Scanner scans a stream for malware. An error means "not scanned": callers
// must fail closed.
type Scanner interface {
	Scan(ctx context.Context, r io.Reader) (Verdict, error)
	// Operational reports whether this scanner is a real security control.
	// Test doubles return false and must be refused by production configuration.
	Operational() bool
}

// ErrUnsafe is returned by Inspect for a file that must not be stored.
var ErrUnsafe = errors.New("docscan: file refused")

// Media is the allow-list of media types: the leading bytes a file of that
// type must have (nil = a text format), and whether it is a ZIP container.
var Media = map[string]struct {
	Sigs []string
	Zip  bool
}{
	"application/pdf": {Sigs: []string{"%PDF-"}},
	"image/png":       {Sigs: []string{"\x89PNG\r\n\x1a\n"}},
	"image/jpeg":      {Sigs: []string{"\xff\xd8\xff"}},
	"text/plain":      {},
	"text/csv":        {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {Sigs: []string{"PK\x03\x04"}, Zip: true},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       {Sigs: []string{"PK\x03\x04"}, Zip: true},
	"image/vnd.dwg":      {Sigs: []string{"AC10"}},
	"image/vnd.dxf":      {},
	"application/x-step": {Sigs: []string{"ISO-10303-21"}},
}

// Limits for ZIP containers (Office documents).
const (
	MaxZipEntries      = 5000
	MaxZipUncompressed = 512 << 20
	MaxZipRatio        = 200
)

func unsafe(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsafe, fmt.Sprintf(format, a...))
}

// Inspect checks a complete file of the declared media type. SVG, HTML,
// scripts, executables and bare archives are not on the allow-list at all.
func Inspect(mediaType string, f io.ReaderAt, size int64) error {
	m, ok := Media[mediaType]
	if !ok {
		return unsafe("files of type %q are not accepted", mediaType)
	}
	if size <= 0 {
		return unsafe("the file is empty")
	}
	head := make([]byte, 16)
	n, _ := f.ReadAt(head, 0)
	head = head[:n]
	if m.Sigs == nil {
		// Text: no NUL anywhere, and not something else in disguise.
		if err := scanTokens(f, size, []string{"\x00"}); err != nil {
			return unsafe("the file is not %s", mediaType)
		}
		for _, sig := range []string{"%PDF-", "PK\x03\x04", "MZ", "\x7fELF", "<svg", "<?xml", "<!DOCTYPE", "<html", "<script"} {
			if bytes.HasPrefix(bytes.ToLower(bytes.TrimLeft(head, " \t\r\n\xef\xbb\xbf")), bytes.ToLower([]byte(sig))) {
				return unsafe("the file's content is not %s", mediaType)
			}
		}
		return nil
	}
	match := false
	for _, sig := range m.Sigs {
		match = match || bytes.HasPrefix(head, []byte(sig))
	}
	if !match {
		return unsafe("the file's content is not %s", mediaType)
	}
	switch {
	case m.Zip:
		return inspectZip(f, size)
	case mediaType == "application/pdf":
		// Heuristic: names can be hex-escaped or hidden in compressed object
		// streams. This refuses the plain cases; the scanner is the control.
		if err := scanTokens(f, size, []string{"/JavaScript", "/JS ", "/JS(", "/JS<", "/Launch", "/EmbeddedFile"}); err != nil {
			return unsafe("the PDF contains active content (%v)", err)
		}
	}
	return nil
}

// scanTokens streams the file once and reports the first token found.
func scanTokens(f io.ReaderAt, size int64, tokens []string) error {
	const block = 256 << 10
	keep := 0
	for _, t := range tokens {
		keep = max(keep, len(t)-1)
	}
	buf := make([]byte, 0, block+keep)
	for off := int64(0); off < size; {
		n, err := f.ReadAt(buf[len(buf):min(cap(buf), len(buf)+block)], off)
		buf = buf[:len(buf)+n]
		off += int64(n)
		for _, t := range tokens {
			if bytes.Contains(buf, []byte(t)) {
				return fmt.Errorf("%q", strings.TrimSpace(t))
			}
		}
		if n == 0 || (err != nil && err != io.EOF) {
			break
		}
		tail := buf[max(0, len(buf)-keep):]
		buf = append(buf[:0], tail...)
	}
	return nil
}

func inspectZip(f io.ReaderAt, size int64) error {
	z, err := zip.NewReader(f, size)
	if err != nil {
		return unsafe("the document is not a readable Office file")
	}
	if len(z.File) > MaxZipEntries {
		return unsafe("the document has %d parts (limit %d)", len(z.File), MaxZipEntries)
	}
	var total uint64
	types := false
	for _, e := range z.File {
		name := e.Name
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, ":") {
			return unsafe("the document contains an absolute path")
		}
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				return unsafe("the document contains a path that leaves its folder")
			}
		}
		low := strings.ToLower(name)
		if strings.HasSuffix(low, "vbaproject.bin") || strings.HasSuffix(low, ".exe") || strings.HasSuffix(low, ".dll") || strings.Contains(low, "/activex/") || strings.Contains(low, "/embeddings/") {
			return unsafe("the document contains macros or embedded active content (%s)", name)
		}
		if e.Flags&0x1 != 0 {
			return unsafe("the document is encrypted and cannot be inspected")
		}
		types = types || name == "[Content_Types].xml"
		total += e.UncompressedSize64
		if total > MaxZipUncompressed {
			return unsafe("the document expands to more than %d MiB", MaxZipUncompressed>>20)
		}
	}
	if !types {
		return unsafe("the document is not an Office file")
	}
	if total > uint64(size)*MaxZipRatio {
		return unsafe("the document's compression ratio is implausible (archive bomb)")
	}
	return nil
}

// ---------------------------------------------------------------- clamd

// Clamd scans through a ClamAV daemon (clamd) using its INSTREAM command.
type Clamd struct {
	Network string        // "tcp" or "unix"
	Address string        // host:port or socket path
	Timeout time.Duration // whole scan; 0 = 2 minutes
}

func (c Clamd) Operational() bool { return true }

func (c Clamd) dial(ctx context.Context) (net.Conn, error) {
	t := c.Timeout
	if t == 0 {
		t = 2 * time.Minute
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, c.Network, c.Address)
	if err != nil {
		return nil, fmt.Errorf("docscan: clamd unreachable: %w", err)
	}
	deadline := time.Now().Add(t)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	return conn, nil
}

func (c Clamd) command(ctx context.Context, cmd string) (string, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("z" + cmd + "\x00")); err != nil {
		return "", err
	}
	s, err := bufio.NewReader(conn).ReadString(0)
	return strings.TrimRight(s, "\x00"), err
}

// Version returns the engine and signature database version clamd reports.
func (c Clamd) Version(ctx context.Context) (string, error) { return c.command(ctx, "VERSION") }

// Scan streams r to clamd. Any answer other than "OK" or "<name> FOUND" is an
// error (for example a file over clamd's StreamMaxLength), never Clean.
func (c Clamd) Scan(ctx context.Context, r io.Reader) (Verdict, error) {
	ver, err := c.Version(ctx)
	if err != nil {
		return Verdict{}, err
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return Verdict{}, err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return Verdict{}, err
	}
	buf := make([]byte, 64<<10)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, err := conn.Write(binary.BigEndian.AppendUint32(nil, uint32(n))); err != nil {
				return Verdict{}, fmt.Errorf("docscan: clamd closed the stream: %w", err)
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return Verdict{}, fmt.Errorf("docscan: clamd closed the stream: %w", err)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return Verdict{}, rerr
		}
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return Verdict{}, err
	}
	reply, err := bufio.NewReader(conn).ReadString(0)
	if err != nil && reply == "" {
		return Verdict{}, fmt.Errorf("docscan: no verdict from clamd: %w", err)
	}
	reply = strings.TrimSpace(strings.TrimRight(reply, "\x00"))
	body := strings.TrimSpace(strings.TrimPrefix(reply, "stream:"))
	switch {
	case body == "OK":
		return Verdict{Clean: true, Engine: ver}, nil
	case strings.HasSuffix(body, " FOUND"):
		return Verdict{Signature: strings.TrimSuffix(body, " FOUND"), Engine: ver}, nil
	}
	return Verdict{}, fmt.Errorf("docscan: clamd did not scan the file: %s", reply)
}
