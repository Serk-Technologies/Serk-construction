package pinauth

import (
	"bytes"
	"testing"
	"time"
)

var pepper = bytes.Repeat([]byte{7}, 32)

func TestCheckPIN(t *testing.T) {
	for _, bad := range []string{"", "12345", "1234567", "12a456", "000000", "777777", "123456", "654321", "890123", "210987", "121212", "123123", "１２３４５６"} {
		if CheckPIN(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"482915", "100293", "730518"} {
		if err := CheckPIN(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	for i := 0; i < 200; i++ {
		p, err := NewPIN()
		if err != nil || CheckPIN(p) != nil {
			t.Fatalf("NewPIN gave %q (%v)", p, err)
		}
	}
}

func TestHashVerify(t *testing.T) {
	salt, _ := NewSalt()
	h, err := Hash(pepper, "482915", salt, 20000)
	if err != nil {
		t.Fatal(err)
	}
	params := "pbkdf2-sha256-hmac:20000"
	if !Verify(pepper, "482915", salt, h, params) {
		t.Fatal("the right PIN failed")
	}
	if Verify(pepper, "482916", salt, h, params) {
		t.Fatal("a wrong PIN passed")
	}
	if Verify(bytes.Repeat([]byte{8}, 32), "482915", salt, h, params) {
		t.Fatal("the hash verified without the pepper")
	}
	if _, err := Hash(pepper[:16], "482915", salt, 20000); err == nil {
		t.Fatal("a short pepper was accepted")
	}
	if Verify(pepper, "482915", salt, h, "bcrypt:10") {
		t.Fatal("an unknown scheme verified")
	}
}

// RFC 6238 appendix B, SHA-1 vectors (last six of the eight digits).
func TestTOTPVectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for _, c := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		got, err := TOTPCode(secret, c.unix/30)
		if err != nil || got != c.code {
			t.Errorf("T=%d: %s %v, want %s", c.unix, got, err, c.code)
		}
	}
	now := time.Unix(1234567890, 0)
	if step, ok := TOTPVerify(secret, "005924", now); !ok || step != 1234567890/30 {
		t.Fatal("current code refused")
	}
	if _, ok := TOTPVerify(secret, "005924", now.Add(5*time.Minute)); ok {
		t.Fatal("an old code was accepted five minutes later")
	}
}

func TestSealOpen(t *testing.T) {
	s, _ := NewTOTPSecret()
	sealed, err := Seal(pepper, s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(s)) {
		t.Fatal("the secret is visible in the sealed form")
	}
	if got, err := Open(pepper, sealed); err != nil || got != s {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := Open(bytes.Repeat([]byte{9}, 32), sealed); err == nil {
		t.Fatal("opened with the wrong pepper")
	}
}

func TestDerivePIN(t *testing.T) {
	sec := []byte("a-seed-secret-of-at-least-32-bytes!!")
	a, b := DerivePIN(sec, "t1", "u-ceo"), DerivePIN(sec, "t1", "u-cfo")
	if CheckPIN(a) != nil || CheckPIN(b) != nil || a == b {
		t.Fatalf("derived PINs %q %q", a, b)
	}
	if DerivePIN(sec, "t1", "u-ceo") != a {
		t.Fatal("not deterministic")
	}
	if DerivePIN([]byte("another-secret-of-at-least-32-bytes!"), "t1", "u-ceo") == a && DerivePIN([]byte("a third secret of at least 32 bytes!"), "t1", "u-ceo") == a {
		t.Fatal("the secret does not change the PIN")
	}
}
