// Package pinauth holds the pure parts of the e-mail + PIN sign-in (migration
// 0095): PIN rules and hashing, and the optional authenticator (TOTP, RFC
// 6238). It does no I/O; the pepper and keys are passed in.
//
// A 6-digit PIN has only 1,000,000 values, so its strength comes from the
// lockout (15 attempts, then 15 minutes) and from a server-side pepper: the
// PIN is HMAC-ed with the pepper before PBKDF2, so a copy of the database
// alone cannot be searched offline.
package pinauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is defined over HMAC-SHA1; every authenticator app uses it.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Iterations is the PBKDF2 work factor recorded with each hash.
const Iterations = 100000

// Params names the hashing scheme stored beside the hash.
var Params = "pbkdf2-sha256-hmac:" + strconv.Itoa(Iterations)

// ErrWeakPIN is returned for a PIN that is not 6 digits or is easily guessed.
var ErrWeakPIN = errors.New("a PIN is 6 digits and not an easy pattern (all the same digit, a run such as 123456, or a repeated pair or triple)")

// CheckPIN enforces the PIN rules: exactly six digits, not all the same
// digit, not an ascending or descending run, not a repeated pair (121212) or
// triple (123123).
func CheckPIN(pin string) error {
	if len(pin) != 6 {
		return ErrWeakPIN
	}
	d := make([]int, 6)
	for i, r := range pin {
		if r < '0' || r > '9' {
			return ErrWeakPIN
		}
		d[i] = int(r - '0')
	}
	same, up, down := true, true, true
	for i := 1; i < 6; i++ {
		same = same && d[i] == d[0]
		up = up && d[i] == (d[i-1]+1)%10
		down = down && d[i] == (d[i-1]+9)%10
	}
	pair := pin[0:2] == pin[2:4] && pin[2:4] == pin[4:6]
	triple := pin[0:3] == pin[3:6]
	if same || up || down || pair || triple {
		return ErrWeakPIN
	}
	return nil
}

// NewPIN returns a random PIN that passes CheckPIN.
func NewPIN() (string, error) {
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(1000000))
		if err != nil {
			return "", err
		}
		pin := fmt.Sprintf("%06d", n.Int64())
		if CheckPIN(pin) == nil {
			return pin, nil
		}
	}
}

// NewSalt returns 16 random bytes.
func NewSalt() ([]byte, error) {
	s := make([]byte, 16)
	_, err := rand.Read(s)
	return s, err
}

// Hash is PBKDF2-SHA256 over HMAC-SHA256(pepper, pin).
func Hash(pepper []byte, pin string, salt []byte, iterations int) ([]byte, error) {
	if len(pepper) < 32 {
		return nil, errors.New("pinauth: the pepper is at least 32 bytes")
	}
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte("serk-pin-v1\x00" + pin))
	return pbkdf2.Key(sha256.New, string(m.Sum(nil)), salt, iterations, 32)
}

// IterationsOf reads the work factor from a stored Params value.
func IterationsOf(params string) (int, error) {
	const p = "pbkdf2-sha256-hmac:"
	if !strings.HasPrefix(params, p) {
		return 0, fmt.Errorf("pinauth: unknown hash scheme %q", params)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(params, p))
	if err != nil || n < 10000 || n > 9999999 {
		return 0, fmt.Errorf("pinauth: bad work factor in %q", params)
	}
	return n, nil
}

// Verify compares a PIN with a stored hash in constant time.
func Verify(pepper []byte, pin string, salt, want []byte, params string) bool {
	n, err := IterationsOf(params)
	if err != nil {
		return false
	}
	got, err := Hash(pepper, pin, salt, n)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// ---------------------------------------------------------------- authenticator (TOTP)

// Step is the TOTP time step.
const Step = 30 * time.Second

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret in base32 (what an
// authenticator app is given).
func NewTOTPSecret() (string, error) {
	k := make([]byte, 20)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	return b32.EncodeToString(k), nil
}

// TOTPURI is the otpauth:// link an authenticator app reads.
func TOTPURI(issuer, account, secret string) string {
	v := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

// TOTPCode is the 6-digit code of a time step.
func TOTPCode(secret string, step int64) (string, error) {
	k, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", fmt.Errorf("pinauth: bad authenticator secret")
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, k)
	m.Write(msg[:])
	h := m.Sum(nil)
	off := h[len(h)-1] & 0x0f
	v := (uint32(h[off])&0x7f)<<24 | uint32(h[off+1])<<16 | uint32(h[off+2])<<8 | uint32(h[off+3])
	return fmt.Sprintf("%06d", v%1000000), nil
}

// TOTPVerify accepts a code for the current step or one step either side
// (clock drift) and returns the step it matched; the caller refuses a step
// at or below the last one accepted (replay).
func TOTPVerify(secret, code string, now time.Time) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	cur := now.Unix() / int64(Step/time.Second)
	for _, s := range []int64{cur - 1, cur, cur + 1} {
		want, err := TOTPCode(secret, s)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

// sealKey derives the key that encrypts authenticator secrets at rest.
func sealKey(pepper []byte) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte("serk-totp-seal-v1"))
	return m.Sum(nil)
}

// Seal encrypts an authenticator secret (AES-256-GCM, random nonce first).
func Seal(pepper []byte, secret string) ([]byte, error) {
	block, err := aes.NewCipher(sealKey(pepper))
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, []byte(secret), []byte("serk-totp")), nil
}

// Open decrypts what Seal produced.
func Open(pepper, sealed []byte) (string, error) {
	block, err := aes.NewCipher(sealKey(pepper))
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < g.NonceSize() {
		return "", errors.New("pinauth: sealed secret too short")
	}
	out, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte("serk-totp"))
	if err != nil {
		return "", errors.New("pinauth: sealed secret does not open")
	}
	return string(out), nil
}
