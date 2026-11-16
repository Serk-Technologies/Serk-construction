package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

func readAll(t *testing.T, s Sealed, tenant, key string) ([]byte, error) {
	t.Helper()
	r, err := s.Get(context.Background(), tenant, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// TestKeyVersionsRotationAndRecovery: objects name the key version that opens
// them; after a rotation old objects stay readable while their key is listed,
// new objects use the new key, a missing key is reported as that (not as a
// damaged file), and supplying the key again recovers the object.
func TestKeyVersionsRotationAndRecovery(t *testing.T) {
	ctx := context.Background()
	store := FS{Root: t.TempDir()}
	k0, k1, k2 := bytes.Repeat([]byte{0xA0}, 32), bytes.Repeat([]byte{0xA1}, 32), bytes.Repeat([]byte{0xA2}, 32)
	key := func() string { k, _ := ObjectKey(tenA, rev, hexSHA(random(16))); return k }
	small, large := random(100), random(3*frameSize+17)

	// Before key versions existed: unversioned objects under k0.
	legacy := Sealed{Store: store, Master: k0}
	kLegacy := key()
	if err := legacy.Put(ctx, tenA, kLegacy, bytes.NewReader(large)); err != nil {
		t.Fatal(err)
	}
	// Versions introduced with the same key material, id d1.
	v1 := Sealed{Store: store, Master: k0, KeyID: "d1"}
	kV1small, kV1large := key(), key()
	for k, b := range map[string][]byte{kV1small: small, kV1large: large} {
		if err := v1.Put(ctx, tenA, k, bytes.NewReader(b)); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := store.Get(ctx, kV1small)
	head := make([]byte, 8)
	_, _ = io.ReadFull(raw, head)
	raw.Close()
	if string(head) != "SRKB2\x02d1" {
		t.Fatalf("a versioned object must name its key: %q", head)
	}
	if got, err := readAll(t, v1, tenA, kLegacy); err != nil || !bytes.Equal(got, large) {
		t.Fatalf("unversioned object after versions were introduced: %v", err)
	}

	// Rotation 1: k1 becomes current (d2); k0 is kept for reading.
	r1 := Sealed{Store: store, Master: k1, KeyID: "d2", Previous: map[string][]byte{"d1": k0}}
	kR1 := key()
	if err := r1.Put(ctx, tenA, kR1, bytes.NewReader(large)); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		k    string
		want []byte
	}{"unversioned": {kLegacy, large}, "d1 small": {kV1small, small}, "d1 large": {kV1large, large}, "d2": {kR1, large}} {
		if got, err := readAll(t, r1, tenA, c.k); err != nil || !bytes.Equal(got, c.want) {
			t.Fatalf("after rotation, %s: %v", name, err)
		}
	}
	// The new object is not readable with the old key alone, and says which key it needs.
	if _, err := readAll(t, v1, tenA, kR1); !errors.Is(err, ErrKeyUnavailable) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("object under a key the service lacks: %v", err)
	}

	// Rotation 2 with the oldest key dropped: its objects are unavailable, not corrupt...
	r2 := Sealed{Store: store, Master: k2, KeyID: "d3", Previous: map[string][]byte{"d2": k1}}
	if _, err := readAll(t, r2, tenA, kV1large); !errors.Is(err, ErrKeyUnavailable) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("d1 object without the d1 key: %v", err)
	}
	if got, err := readAll(t, r2, tenA, kR1); err != nil || !bytes.Equal(got, large) {
		t.Fatalf("d2 object under rotation 2: %v", err)
	}
	// ...an unversioned object cannot name its key, so it fails authentication...
	if _, err := readAll(t, r2, tenA, kLegacy); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unversioned object without its key: %v", err)
	}
	// ...and recovery is supplying the key again: nothing was lost.
	rec := Sealed{Store: store, Master: k2, KeyID: "d3", Previous: map[string][]byte{"d2": k1, "d1": k0}}
	for _, k := range []string{kLegacy, kV1large} {
		if got, err := readAll(t, rec, tenA, k); err != nil || !bytes.Equal(got, large) {
			t.Fatalf("recovery with the old key restored: %v", err)
		}
	}

	// The wrong material under the right id does not open the object.
	wrong := Sealed{Store: store, Master: k2, KeyID: "d3", Previous: map[string][]byte{"d1": k1}}
	if _, err := readAll(t, wrong, tenA, kV1small); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong key material under the named id: %v", err)
	}
	// Another tenant cannot open it under any version.
	if _, err := readAll(t, rec, tenB, kV1small); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("cross-tenant read: %v", err)
	}
	// A header rewritten to name another listed key does not open the object.
	path, _ := store.path(kV1small)
	b := mustRead(t, path)
	b[7] = '2' // "d1" -> "d2"
	mustWrite(t, path, b)
	if _, err := readAll(t, rec, tenA, kV1small); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("object with a rewritten key id: %v", err)
	}
	// Malformed ids are refused on write and on read.
	if err := (Sealed{Store: store, Master: k0, KeyID: "bad id!"}).Put(ctx, tenA, key(), bytes.NewReader(small)); err == nil {
		t.Fatal("an invalid key id was accepted")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
