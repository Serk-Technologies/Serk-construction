package webui

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAURATokensMatchStylesheet runs the AURA contrast checker: every token
// pair meets its WCAG target and the console stylesheet's theme blocks are
// exactly the approved token sets. The checker is the single implementation;
// this test makes its verdict part of the recorded test evidence.
func TestAURATokensMatchStylesheet(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "tools/design/aura_contrast.py", "--check-css")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("AURA contrast check failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
