// Package blob stores large immutable objects (document files) outside the
// relational database: an S3-compatible store for deployments, a directory
// for development, and an envelope that encrypts every object before it
// leaves the process (ADR 0019).
//
// Keys are chosen by the caller and are tenant-scoped by construction
// (ObjectKey); a store never lists, so a caller that holds one tenant's
// context cannot enumerate another tenant's objects through this package.
package blob

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var (
	// ErrNotFound: no object under that key.
	ErrNotFound = errors.New("blob: object not found")
	// ErrExists: objects are immutable; a key is written once.
	ErrExists = errors.New("blob: object already exists")
	// ErrCorrupt: the object failed authentication (tampered, truncated or
	// written under another key or tenant).
	ErrCorrupt = errors.New("blob: object failed integrity verification")
	// ErrBucketNotFound: the configured bucket does not exist. A deployment
	// fault, never "the file is missing".
	ErrBucketNotFound = errors.New("blob: bucket not found")
	// ErrAccessDenied: the store refused the credentials or the signature.
	ErrAccessDenied = errors.New("blob: access denied by the object store")
	// ErrUnavailable: the store could not be reached or failed (5xx, throttling).
	// The only class worth retrying.
	ErrUnavailable = errors.New("blob: object store unavailable")
	// ErrKeyUnavailable: the object was encrypted under a key version this
	// service has not been given. The object is intact; supply the named key
	// (recovery) rather than treating the file as damaged.
	ErrKeyUnavailable = errors.New("blob: encryption key version not available")
	// ErrKey: malformed object key.
	ErrKey = errors.New("blob: invalid object key")
)

// Store is a write-once object store.
type Store interface {
	// Put stores r under key. It fails with ErrExists if the key is taken.
	// Memory use is bounded regardless of the object's size.
	Put(ctx context.Context, key string, r io.Reader) (stored int64, err error)
	// Get opens the object for streaming.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object (quarantine disposal and failed uploads only).
	Delete(ctx context.Context, key string) error
	// Kind names the backend ("s3", "fs") for records and health output.
	Kind() string
}

var (
	keyRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,400}$`)
	uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	shaRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func checkKey(key string) error {
	if !keyRe.MatchString(key) || regexp.MustCompile(`(^|/)\.\.?(/|$)|//`).MatchString(key) {
		return fmt.Errorf("%w: %q", ErrKey, key)
	}
	return nil
}

// ObjectKey is the one way document object keys are built: under the tenant,
// then the revision, named by the content hash. The revision and the hash
// make the reference immutable; the tenant prefix makes it tenant-scoped.
func ObjectKey(tenant, revision, sha256hex string) (string, error) {
	if !uuidRe.MatchString(tenant) || !uuidRe.MatchString(revision) || !shaRe.MatchString(sha256hex) {
		return "", fmt.Errorf("%w: tenant and revision must be canonical UUIDs and the hash 64 hex digits", ErrKey)
	}
	return "t/" + tenant + "/rev/" + revision + "/" + sha256hex, nil
}

// ------------------------------------------------------------------ FS

// FS stores objects as files under Root. For development and tests; a
// deployment uses S3.
type FS struct{ Root string }

func (f FS) Kind() string { return "fs" }

func (f FS) path(key string) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	return filepath.Join(f.Root, filepath.FromSlash(key)), nil
}

func (f FS) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	p, err := f.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return 0, ErrExists
	}
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(p)
		return 0, err
	}
	return n, nil
}

func (f FS) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	p, err := f.path(key)
	if err != nil {
		return nil, err
	}
	in, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return in, err
}

func (f FS) Delete(ctx context.Context, key string) error {
	p, err := f.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// -------------------------------------------------------------- Sealed

// Sealed encrypts every object before it reaches the store and authenticates
// it on the way back, so the storage provider holds ciphertext only and a
// swapped, truncated or cross-tenant object is detected.
//
// Format: "SRKB1" | 8 random bytes | frames. Each frame is a 4-byte big-endian
// ciphertext length and an AES-256-GCM ciphertext of at most 64 KiB of
// plaintext. The nonce is the 8 random bytes and the frame counter; the
// additional data binds the object key, the counter and whether the frame is
// the last, so frames cannot be reordered, dropped or cut off.
// The key is HKDF-SHA256(master, info = "serk-blob-v1|" + tenant).
//
// Key versions (ADR 0021). With KeyID set, objects are written as
// "SRKB2" | 1-byte id length | key id | 8 random bytes | frames, under
// HKDF-SHA256(master, info = "serk-blob-v2|" + key id + "|" + tenant), so
// every object names the key version that opens it. Rotation is: make a new
// key current and list the old one in Previous. New objects use the new key;
// old objects stay readable for as long as their key is listed. An object
// whose key is not listed fails with ErrKeyUnavailable, never ErrCorrupt.
// Objects written before key versions existed ("SRKB1") carry no id and are
// opened with whichever listed key authenticates their first frame.
type Sealed struct {
	Store    Store
	Master   []byte            // current key, at least 32 bytes
	KeyID    string            // version id of Master; "" writes the unversioned format
	Previous map[string][]byte // earlier keys by version id, for reading only
}

const (
	sealMagic  = "SRKB1"
	sealMagic2 = "SRKB2"
	frameSize  = 64 << 10
)

func gcmFor(master []byte, info string) (cipher.AEAD, error) {
	if len(master) < 32 {
		return nil, errors.New("blob: master key must be at least 32 bytes")
	}
	k, err := hkdf.Key(sha256.New, master, nil, info, 32)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func validKeyID(id string) bool {
	if len(id) == 0 || len(id) > 32 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// writer returns the cipher and header for a new object.
func (s Sealed) writer(tenant string) (cipher.AEAD, []byte, error) {
	if s.KeyID == "" {
		g, err := gcmFor(s.Master, "serk-blob-v1|"+tenant)
		return g, []byte(sealMagic), err
	}
	if !validKeyID(s.KeyID) {
		return nil, nil, errors.New("blob: invalid key id")
	}
	g, err := gcmFor(s.Master, "serk-blob-v2|"+s.KeyID+"|"+tenant)
	return g, append(append([]byte(sealMagic2), byte(len(s.KeyID))), s.KeyID...), err
}

// legacy returns the ciphers that may open an unversioned object: the current
// key first, then every previous key in id order.
func (s Sealed) legacy(tenant string) ([]cipher.AEAD, error) {
	var out []cipher.AEAD
	masters := [][]byte{s.Master}
	ids := make([]string, 0, len(s.Previous))
	for id := range s.Previous {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		masters = append(masters, s.Previous[id])
	}
	for _, m := range masters {
		g, err := gcmFor(m, "serk-blob-v1|"+tenant)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func frameAAD(key string, n uint32, last bool) []byte {
	aad := make([]byte, 0, len(key)+5)
	aad = append(aad, key...)
	aad = binary.BigEndian.AppendUint32(aad, n)
	if last {
		return append(aad, 1)
	}
	return append(aad, 0)
}

// Put encrypts r for tenant and stores it under key.
func (s Sealed) Put(ctx context.Context, tenant, key string, r io.Reader) error {
	g, header, err := s.writer(tenant)
	if err != nil {
		return err
	}
	prefix := make([]byte, 8)
	if _, err := rand.Read(prefix); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(func() error {
			if _, err := pw.Write(append(append([]byte{}, header...), prefix...)); err != nil {
				return err
			}
			cur, next := make([]byte, frameSize), make([]byte, frameSize)
			n, err := io.ReadFull(r, cur)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return err
			}
			for i := uint32(0); ; i++ {
				// Read ahead one frame to know whether this one is the last.
				m, rerr := 0, io.EOF
				if n == frameSize {
					m, rerr = io.ReadFull(r, next)
					if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
						return rerr
					}
				}
				last := m == 0
				nonce := binary.BigEndian.AppendUint32(append([]byte{}, prefix...), i)
				ct := g.Seal(nil, nonce, cur[:n], frameAAD(key, i, last))
				if _, err := pw.Write(binary.BigEndian.AppendUint32(nil, uint32(len(ct)))); err != nil {
					return err
				}
				if _, err := pw.Write(ct); err != nil {
					return err
				}
				if last {
					return nil
				}
				if i == 1<<32-2 {
					return errors.New("blob: object too large")
				}
				cur, next, n = next, cur, m
			}
		}())
	}()
	_, err = s.Store.Put(ctx, key, pr)
	_ = pr.CloseWithError(err) // unblock the encrypting goroutine if the store stopped reading
	return err
}

// Get opens the object and returns its plaintext as a stream. A read returns
// ErrCorrupt if any frame fails authentication or the object is cut short;
// callers must treat everything already read as untrusted in that case.
func (s Sealed) Get(ctx context.Context, tenant, key string) (io.ReadCloser, error) {
	if len(s.Master) < 32 {
		return nil, errors.New("blob: master key must be at least 32 bytes")
	}
	in, err := s.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (io.ReadCloser, error) { in.Close(); return nil, e }
	magic := make([]byte, len(sealMagic))
	if _, err := io.ReadFull(in, magic); err != nil {
		return fail(ErrCorrupt)
	}
	var cands []cipher.AEAD
	switch string(magic) {
	case sealMagic:
		if cands, err = s.legacy(tenant); err != nil {
			return fail(err)
		}
	case sealMagic2:
		var n [1]byte
		if _, err := io.ReadFull(in, n[:]); err != nil || n[0] == 0 || n[0] > 32 {
			return fail(ErrCorrupt)
		}
		idb := make([]byte, n[0])
		if _, err := io.ReadFull(in, idb); err != nil || !validKeyID(string(idb)) {
			return fail(ErrCorrupt)
		}
		id := string(idb)
		master, ok := s.Previous[id]
		if id == s.KeyID {
			master, ok = s.Master, true
		}
		if !ok {
			return fail(fmt.Errorf("%w: object needs key %q", ErrKeyUnavailable, id))
		}
		g, err := gcmFor(master, "serk-blob-v2|"+id+"|"+tenant)
		if err != nil {
			return fail(err)
		}
		cands = []cipher.AEAD{g}
	default:
		return fail(ErrCorrupt)
	}
	prefix := make([]byte, 8)
	if _, err := io.ReadFull(in, prefix); err != nil {
		return fail(ErrCorrupt)
	}
	return &unsealer{in: in, cands: cands, key: key, prefix: prefix}, nil
}

// Delete removes the object.
func (s Sealed) Delete(ctx context.Context, key string) error { return s.Store.Delete(ctx, key) }

type unsealer struct {
	in     io.ReadCloser
	g      cipher.AEAD
	cands  []cipher.AEAD // keys that may open the object; the first frame settles which
	key    string
	prefix []byte
	n      uint32
	buf    []byte
	done   bool
	err    error
}

func (u *unsealer) Close() error { return u.in.Close() }

func (u *unsealer) Read(p []byte) (int, error) {
	for len(u.buf) == 0 {
		if u.err != nil {
			return 0, u.err
		}
		if u.done {
			return 0, io.EOF
		}
		var lb [4]byte
		if _, err := io.ReadFull(u.in, lb[:]); err != nil {
			u.err = ErrCorrupt // the last frame was never seen
			return 0, u.err
		}
		l := binary.BigEndian.Uint32(lb[:])
		if u.g == nil {
			u.g = u.cands[0]
		}
		if l < uint32(u.g.Overhead()) || l > frameSize+uint32(u.g.Overhead()) {
			u.err = ErrCorrupt
			return 0, u.err
		}
		ct := make([]byte, l)
		if _, err := io.ReadFull(u.in, ct); err != nil {
			u.err = ErrCorrupt
			return 0, u.err
		}
		nonce := binary.BigEndian.AppendUint32(append([]byte{}, u.prefix...), u.n)
		if u.n == 0 && len(u.cands) > 1 {
			// An unversioned object: the key that authenticates its first frame is its key.
			for _, g := range u.cands {
				if _, e1 := g.Open(nil, nonce, ct, frameAAD(u.key, 0, false)); e1 == nil {
					u.g = g
					break
				}
				if _, e2 := g.Open(nil, nonce, ct, frameAAD(u.key, 0, true)); e2 == nil {
					u.g = g
					break
				}
			}
		}
		// A frame is the last one exactly when it authenticates as such.
		pt, err := u.g.Open(nil, nonce, ct, frameAAD(u.key, u.n, false))
		if err != nil {
			if pt, err = u.g.Open(nil, nonce, ct, frameAAD(u.key, u.n, true)); err != nil {
				u.err = ErrCorrupt
				return 0, u.err
			}
			u.done = true
			var one [1]byte
			if n, _ := u.in.Read(one[:]); n != 0 {
				u.err = ErrCorrupt // bytes after the last frame
				return 0, u.err
			}
		}
		u.n++
		u.buf = pt
	}
	n := copy(p, u.buf)
	u.buf = u.buf[n:]
	return n, nil
}
