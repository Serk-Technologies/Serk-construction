package testdb

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/serk-group/serk-construction/pkg/docscan"
)

// DocumentKey is the document key (scan proofs, download links, object
// encryption) used by tests only.
var DocumentKey = []byte("serk-test-document-key-0123456789abcdefABCDEF")

// EICAR is the industry-standard antivirus test file: harmless, and detected
// by every real scanner.
const EICAR = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// FakeScanner is a TEST DOUBLE, not a security control: it "detects" only the
// EICAR test string and fails when the file contains the word SCANNER-DOWN.
// Operational() is false, so the service never reports it as a configured
// scanner and production configuration cannot select it.
type FakeScanner struct{}

func (FakeScanner) Operational() bool { return false }

func (FakeScanner) Scan(ctx context.Context, r io.Reader) (docscan.Verdict, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return docscan.Verdict{}, err
	}
	switch {
	case bytes.Contains(b, []byte("SCANNER-DOWN")):
		return docscan.Verdict{}, errors.New("test scanner: simulated outage")
	case bytes.Contains(b, []byte(EICAR)):
		return docscan.Verdict{Signature: "Test.EICAR", Engine: "serk test double (not a scanner)"}, nil
	}
	return docscan.Verdict{Clean: true, Engine: "serk test double (not a scanner)"}, nil
}
