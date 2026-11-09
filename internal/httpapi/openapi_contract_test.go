package httpapi

// Contract test: api/openapi.yaml and the routes the API serves must agree.
//
//   - every route registered in this package is described in the spec
//     (run `python3 tools/gen_openapi.py` to add stubs for new ones);
//   - every operation in the spec is a route that is served (a stale entry is a
//     promise the API no longer keeps).
//
// Routes are read from the source (Handle("METHOD /path", ...)); a spec path with
// a templated {action} parameter covers each value of its enum.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	routeRe  = regexp.MustCompile(`(?:Handle|HandleFunc)\(\s*"((?:GET|POST|PUT|PATCH|DELETE) [^"]+)"`)
	paramRe  = regexp.MustCompile(`\{[^}]+\}`)
	pathLn   = regexp.MustCompile(`^  (/[^:]*):\s*$`)
	methodLn = regexp.MustCompile(`^    (get|post|put|patch|delete):`)
	actionRe = regexp.MustCompile(`name: action[^}]*enum: \[([^\]]+)\]`)
)

// routesNotInContract are served but deliberately outside the API contract.
var routesNotInContract = map[string]bool{"/console/": true, "/{$}": true, "/staging/personas": true, "/staging/token": true}

// registeredInLoops are registered with a computed path, which the source scan cannot see.
var registeredInLoops = map[string]bool{
	"GET /v1/org/regions": true, "POST /v1/org/regions": true, "GET /v1/org/business-units": true, "POST /v1/org/business-units": true,
}

func norm(p string) string { return paramRe.ReplaceAllString(p, "{}") }

func TestOpenAPIMatchesRoutes(t *testing.T) {
	served := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range routeRe.FindAllStringSubmatch(string(b), -1) {
			parts := strings.SplitN(m[1], " ", 2)
			if !routesNotInContract[parts[1]] {
				served[parts[0]+" "+norm(parts[1])] = true
			}
		}
	}
	for r := range registeredInLoops {
		parts := strings.SplitN(r, " ", 2)
		served[parts[0]+" "+norm(parts[1])] = true
	}
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	documented := map[string]bool{}
	templated := map[string]bool{} // "{action}" entries: each enum value is checked on its own
	cur := ""
	for i, l := range lines {
		if m := pathLn.FindStringSubmatch(l); m != nil {
			cur = m[1]
			continue
		}
		if m := methodLn.FindStringSubmatch(l); m != nil && cur != "" {
			method := strings.ToUpper(m[1])
			documented[method+" "+norm(cur)] = true
			if strings.Contains(cur, "{action}") {
				templated[method+" "+norm(cur)] = true
			}
			end := i + 8
			if end > len(lines) {
				end = len(lines)
			}
			if e := actionRe.FindStringSubmatch(strings.Join(lines[i:end], " ")); e != nil && strings.Contains(cur, "{action}") {
				for _, v := range strings.Split(e[1], ",") {
					documented[method+" "+norm(strings.Replace(cur, "{action}", strings.TrimSpace(v), 1))] = true
				}
			}
		}
	}
	var undocumented, stale []string
	for r := range served {
		if !documented[r] {
			undocumented = append(undocumented, r)
		}
	}
	for r := range documented {
		if !served[r] && !templated[r] {
			stale = append(stale, r)
		}
	}
	sort.Strings(undocumented)
	sort.Strings(stale)
	if len(undocumented) > 0 {
		t.Errorf("%d served route(s) are missing from api/openapi.yaml (run python3 tools/gen_openapi.py):\n  %s", len(undocumented), strings.Join(undocumented, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d operation(s) in api/openapi.yaml are not served:\n  %s", len(stale), strings.Join(stale, "\n  "))
	}
	t.Logf("%d routes served, %d operations documented", len(served), len(documented))
}
