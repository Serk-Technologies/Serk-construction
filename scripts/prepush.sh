#!/usr/bin/env bash
# Checks to run before every push. They are the fast subset of CI that needs no
# services: formatting, vet, the CI gate's own tests, the repository's supply
# chain script and the SAME secret scanner CI uses (Gitleaks 8.21.2, default
# rules, whole history, this repository's .gitleaks.toml and .gitleaksignore).
#
# A push that fails the secret scan on GitHub cannot be taken back: history is
# not rewritten. Run this first.
#
# Gitleaks: set GITLEAKS to the binary, or have `gitleaks` on PATH, or install
# with: go install github.com/zricethezav/gitleaks/v8@v8.21.2
set -euo pipefail
cd "$(dirname "$0")/.."
WANT=8.21.2
GL="${GITLEAKS:-$(command -v gitleaks || true)}"
if [ -z "$GL" ] || [ ! -x "$GL" ]; then
  echo "prepush: FAIL: gitleaks $WANT not found (set GITLEAKS=/path/to/gitleaks)" >&2; exit 1
fi
HAVE="$("$GL" version 2>/dev/null | tr -d 'v' | head -1)"
# A binary built with `go install` reports "(devel)" or a module version; accept the pinned one or say so plainly.
case "$HAVE" in
  "$WANT"*) ;;
  *) echo "prepush: note: gitleaks reports version '$HAVE'; CI uses $WANT" >&2 ;;
esac
test -z "$(gofmt -l .)" || { echo "prepush: FAIL: gofmt"; gofmt -l .; exit 1; }
go vet ./...
# The console's scripts are ES modules; a plain `node --check` on them can miss a syntax error.
if command -v node > /dev/null; then
  for f in internal/webui/static/*.js; do node --input-type=module --check < "$f" || { echo "prepush: FAIL: syntax error in $f"; exit 1; }; done
fi
python3 tools/ci/test_check_test_results.py
python3 tools/design/aura_contrast.py --check-css > /dev/null || { echo "prepush: FAIL: AURA tokens (run tools/design/aura_contrast.py --check-css)"; exit 1; }
python3 tools/final_completion.py --check > /dev/null || { echo "prepush: FAIL: completion register (run tools/final_completion.py --check)"; exit 1; }
# A workflow file that does not parse is only discovered on GitHub, after the push.
python3 - <<'PYEOF'
import glob, sys
try:
    import yaml
except ImportError:
    print("prepush: note: PyYAML not installed; workflow files not parsed", file=sys.stderr); sys.exit(0)
for f in glob.glob(".github/workflows/*.yml"):
    yaml.safe_load(open(f))
PYEOF
"$GL" git . --redact --no-banner --exit-code 1
OUT="${TMPDIR:-/tmp}/serk-supply-chain" scripts/supply_chain_local.sh
echo "prepush: PASS"
