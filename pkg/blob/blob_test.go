package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

const (
	tenA = "11111111-1111-4111-8111-111111111111"
	tenB = "22222222-2222-4222-8222-222222222222"
	rev  = "33333333-3333-4333-8333-333333333333"
	sha  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func master() []byte { return bytes.Repeat([]byte{7}, 32) }

func random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func TestObjectKeyIsTenantScopedAndStrict(t *testing.T) {
	k, err := ObjectKey(tenA, rev, sha)
	if err != nil || k != "t/"+tenA+"/rev/"+rev+"/"+sha {
		t.Fatalf("key %q %v", k, err)
	}
	for _, bad := range [][3]string{{"../etc", rev, sha}, {tenA, "x", sha}, {tenA, rev, "abc"}, {tenA + "/..", rev, sha}} {
		if _, err := ObjectKey(bad[0], bad[1], bad[2]); !errors.Is(err, ErrKey) {
			t.Fatalf("accepted %v", bad)
		}
	}
	fs := FS{Root: t.TempDir()}
	for _, key := range []string{"../x", "a/../../x", "/abs", "a//b", "a/./b", "", "a\x00b", "a b"} {
		if _, err := fs.Put(context.Background(), key, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrKey) {
			t.Fatalf("store accepted key %q: %v", key, err)
		}
	}
}

func TestSealedRoundTripAndTamperDetection(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := Sealed{Store: FS{Root: dir}, Master: master()}
	for i, n := range []int{0, 1, frameSize - 1, frameSize, frameSize + 1, 3*frameSize + 17} {
		key, _ := ObjectKey(tenA, rev, sha[:63]+string(rune('0'+i)))
		plain := random(n)
		if err := s.Put(ctx, tenA, key, bytes.NewReader(plain)); err != nil {
			t.Fatalf("put %d: %v", n, err)
		}
		// Stored bytes are not the plaintext.
		raw, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(key)))
		if n > 32 && bytes.Contains(raw, plain[:32]) {
			t.Fatalf("plaintext found at rest (%d bytes)", n)
		}
		r, err := s.Get(ctx, tenA, key)
		if err != nil {
			t.Fatalf("get %d: %v", n, err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("round trip %d: %v (%d bytes back)", n, err, len(got))
		}
		// Written once.
		if err := s.Put(ctx, tenA, key, bytes.NewReader(plain)); !errors.Is(err, ErrExists) {
			t.Fatalf("overwrite %d: %v", n, err)
		}
		read := func(tenant, k string) error {
			r, err := s.Get(ctx, tenant, k)
			if err != nil {
				return err
			}
			defer r.Close()
			_, err = io.ReadAll(r)
			return err
		}
		// Another tenant's key cannot open it.
		if err := read(tenB, key); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("other tenant read %d: %v", n, err)
		}
		path := filepath.Join(dir, filepath.FromSlash(key))
		// A flipped bit, a cut-off object, a dropped last frame and appended bytes are all detected.
		for name, mut := range map[string]func([]byte) []byte{
			"bit flip": func(b []byte) []byte { c := bytes.Clone(b); c[len(c)-1] ^= 1; return c },
			"cut":      func(b []byte) []byte { return b[:len(b)-1] },
			"appended": func(b []byte) []byte { return append(bytes.Clone(b), 0) },
			"header":   func(b []byte) []byte { c := bytes.Clone(b); c[6] ^= 1; return c },
		} {
			if err := os.WriteFile(path, mut(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := read(tenA, key); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s on %d bytes not detected: %v", name, n, err)
			}
		}
		if n > 2*frameSize { // drop the final frame at a frame boundary
			cut := len(sealMagic) + 8 + 3*(4+frameSize+16)
			if err := os.WriteFile(path, raw[:cut], 0o600); err != nil {
				t.Fatal(err)
			}
			if err := read(tenA, key); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("truncation at a frame boundary not detected: %v", err)
			}
		}
		// An object moved to another key (another revision) does not open.
		other, _ := ObjectKey(tenA, "44444444-4444-4444-8444-444444444444", sha[:63]+string(rune('0'+i)))
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, filepath.FromSlash(other))), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(other)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := read(tenA, other); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("object swapped between keys opened: %v", err)
		}
	}
	if _, err := s.Get(ctx, tenA, "t/none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	if err := (Sealed{Store: FS{Root: dir}, Master: []byte("short")}).Put(ctx, tenA, "k", bytes.NewReader(nil)); err == nil {
		t.Fatal("a short master key was accepted")
	}
}

// TestS3AgainstARealServer runs against an S3-compatible server when
// SERK_TEST_S3_ENDPOINT, SERK_TEST_S3_ACCESS_KEY and SERK_TEST_S3_SECRET_KEY
// are set (MinIO in the project's evidence runs). Without them it is skipped:
// the S3 client is then NOT verified by this run.
func TestS3AgainstARealServer(t *testing.T) {
	ep := os.Getenv("SERK_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("SERK_TEST_S3_ENDPOINT is not set: the S3 client was not exercised against a server")
	}
	ctx := context.Background()
	// A bucket of this test's own, created here: no other test and no earlier
	// run has to have prepared it.
	s := &S3{Endpoint: ep, Region: "us-east-1", Bucket: "serk-blobtest-" + sha[:12], AccessKey: os.Getenv("SERK_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("SERK_TEST_S3_SECRET_KEY"), PartSize: 5 << 20}
	if err := s.CreateBucket(ctx); err != nil {
		t.Fatal(err)
	}
	sealed := Sealed{Store: s, Master: master()}
	for _, n := range []int{0, 1000, 5<<20 - 1, 12<<20 + 5} { // single request and multipart (three parts)
		key, _ := ObjectKey(tenA, rev, hexSHA(random(16)))
		plain := random(n)
		if err := sealed.Put(ctx, tenA, key, bytes.NewReader(plain)); err != nil {
			t.Fatalf("put %d: %v", n, err)
		}
		r, err := sealed.Get(ctx, tenA, key)
		if err != nil {
			t.Fatalf("get %d: %v", n, err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("round trip %d: %v", n, err)
		}
		if err := sealed.Put(ctx, tenA, key, bytes.NewReader(plain)); !errors.Is(err, ErrExists) {
			t.Fatalf("overwrite %d: %v", n, err)
		}
		// The server holds ciphertext.
		raw, err := s.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := io.ReadAll(raw)
		raw.Close()
		if n > 64 && bytes.Contains(rb, plain[:64]) {
			t.Fatal("plaintext stored on the server")
		}
		// Anonymous access is refused: the bucket is private.
		anon, err := http.Get(ep + "/" + s.Bucket + "/" + key)
		if err != nil {
			t.Fatal(err)
		}
		anon.Body.Close()
		if anon.StatusCode != http.StatusForbidden {
			t.Fatalf("anonymous read answered %d", anon.StatusCode)
		}
		// A wrong secret is refused.
		wrong := *s
		wrong.SecretKey = "not-the-secret"
		if _, err := wrong.Get(ctx, key); err == nil {
			t.Fatal("a request signed with the wrong secret was served")
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("after delete: %v", err)
		}
	}
}
