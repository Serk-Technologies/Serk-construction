#!/usr/bin/env python3
"""Appends a stub entry to api/openapi.yaml for every route the API serves that the spec does not yet
describe. Hand-written entries are never touched. Run after adding a route:

    python3 tools/gen_openapi.py

The contract test (internal/httpapi/openapi_contract_test.go) fails if any route is missing, so the
spec cannot silently fall behind the code. A generated stub says what the route does in words taken
from its path; improve it by hand (it will then no longer be regenerated)."""
import re, glob, collections, sys, os

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..')
SPEC = os.path.join(ROOT, 'api', 'openapi.yaml')
SKIP = {'/console/', '/{$}', '/staging/personas', '/staging/token'}  # the web console and the test-environment sign-in are not API contract

def routes():
    out = set()
    for f in glob.glob(os.path.join(ROOT, 'internal/httpapi/*.go')):
        if f.endswith('_test.go'):
            continue
        for m in re.finditer(r'(?:Handle|HandleFunc)\(\s*"((?:GET|POST|PUT|PATCH|DELETE) [^"]+)"', open(f).read()):
            method, path = m.group(1).split(' ', 1)
            if path not in SKIP:
                out.add((method, path))
    return out

def norm(p):
    return re.sub(r'\{[^}]+\}', '{}', p)

def spec_coverage(text):
    cov = set(); cur = None; block = []
    def flush():
        pass
    lines = text.splitlines()
    for i, l in enumerate(lines):
        m = re.match(r'^  (/[^:]*):\s*$', l)
        if m:
            cur = m.group(1); continue
        m = re.match(r'^    (get|post|put|patch|delete):', l)
        if m and cur:
            method = m.group(1).upper()
            cov.add((method, norm(cur)))
            # a templated {action} path lists its values in an enum: it covers each
            seg = ' '.join(lines[i:i + 8])
            e = re.search(r'name: action[^}]*enum: \[([^\]]+)\]', seg)
            if e and '{action}' in cur:
                for v in [x.strip() for x in e.group(1).split(',')]:
                    cov.add((method, norm(cur.replace('{action}', v))))
    return cov

VERBS = {'submit': 'Submit the draft for approval (its author)', 'approve': 'Approve (a human decision by a different person; maker/checker)',
         'reject': 'Reject with a reason (a human decision)', 'cancel': 'Cancel with a reason', 'confirm': 'Confirm (a human decision by a different person)',
         'release': 'Release (a human decision by a different person)', 'activate': 'Activate (a human decision by a different person)',
         'retire': 'Retire', 'revoke': 'Revoke', 'issue': 'Issue', 'award': 'Award', 'evaluate': 'Record the technical evaluation',
         'certify': 'Certify (a human decision)', 'verify': 'Verify (a human decision)', 'post': 'Post (a human decision)', 'reverse': 'Reverse (a human decision)',
         'reconcile': 'Reconcile', 'close': 'Close (a human decision)', 'reopen': 'Reopen (a human decision with fresh MFA)'}

def summary(method, path):
    segs = [s for s in path.split('/') if s]
    last = segs[-1]
    if method == 'GET':
        return 'Read ' + re.sub(r'[-_]', ' ', last if not last.startswith('{') else segs[-2]) + ' (scoped to what the caller may see)'
    if last in VERBS:
        return f'{VERBS[last]}: {re.sub(r"[-_]", " ", segs[-2] if segs[-2].startswith("{") is False else segs[-3])}'
    return ('Create' if method == 'POST' else 'Change') + ' ' + re.sub(r'[-_]', ' ', last if not last.startswith('{') else segs[-2]) + ' (a draft; decisions need a different person)'

def main():
    text = open(SPEC).read()
    have = spec_coverage(text)
    missing = sorted((m, p) for (m, p) in routes() if (m, norm(p)) not in have)
    if not missing:
        print('openapi.yaml already describes every route'); return 0
    by = collections.OrderedDict()
    for m, p in missing:
        by.setdefault(p, []).append(m)
    add = []
    for p, ms in by.items():
        add.append(f'  {p}:')
        for m in ms:
            params = re.findall(r'\{([^}$]+)\}', p)
            add.append(f'    {m.lower()}:')
            add.append('      summary: ' + '"' + summary(m, p).replace('"', "'") + '"')
            add.append('      x-generated: true  # stub: replace with a hand-written description')
            if params:
                add.append('      parameters: [' + ', '.join(f'{{ name: {x}, in: path, required: true, schema: {{ type: string }} }}' for x in params) + ']')
            add.append("      responses: { '200': { description: ok }, '401': { description: bearer token required }, '403': { description: forbidden }, '404': { description: not found or outside your scope } }")
    if not text.endswith('\n'):
        text += '\n'
    open(SPEC, 'w').write(text + '\n'.join(add) + '\n')
    print(f'added {len(missing)} route stub(s) in {len(by)} path(s)')
    return 0

if __name__ == '__main__':
    sys.exit(main())
