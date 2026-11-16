"""Tests for the CI result gate: it must reject every way a run can be
unacceptable, including the ones a log grep or a pipe's exit status misses."""
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(__file__))
import check_test_results as c  # noqa: E402

P = "example.com/m/internal/kernel"
REQ = [("internal/kernel", "TestA")]


def run(events, **kw):
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
        for e in events:
            fh.write(e if isinstance(e, str) else json.dumps(e))
            fh.write("\n")
    try:
        tests, packages, bad = c.load(fh.name)
    finally:
        os.unlink(fh.name)
    return c.evaluate(tests, packages, bad, REQ, **kw)


def ev(action, test=None, pkg=P):
    e = {"Action": action, "Package": pkg}
    if test:
        e["Test"] = test
    return e


GOOD = [ev("run", "TestA"), ev("pass", "TestA"), ev("pass", "TestB"), ev("pass")]


class Gate(unittest.TestCase):
    def test_clean_run_passes(self):
        s = run(GOOD, min_pass=2)
        self.assertEqual(s["verdict"], "PASS", s["problems"])
        self.assertEqual(s["top_level_tests"]["passed"], 2)

    def test_failed_test_is_rejected(self):
        s = run([ev("pass", "TestA"), ev("fail", "TestB"), ev("fail")])
        self.assertIn(f"FAILED: {P} TestB", s["problems"])

    def test_failed_subtest_is_rejected(self):
        s = run(GOOD[:-1] + [ev("fail", "TestB/case"), ev("pass")])
        self.assertEqual(s["verdict"], "FAIL")

    def test_any_skip_is_rejected(self):
        s = run(GOOD[:-1] + [ev("skip", "TestC"), ev("pass")])
        self.assertIn(f"SKIPPED: {P} TestC", s["problems"])
        self.assertEqual(run(GOOD[:-1] + [ev("skip", "TestC"), ev("pass")], allow_skips=True)["verdict"], "PASS")

    def test_skipped_mandatory_is_rejected_even_when_skips_are_allowed(self):
        s = run([ev("skip", "TestA"), ev("pass")], allow_skips=True)
        self.assertTrue(any("mandatory test did not pass (skip)" in p for p in s["problems"]))

    def test_missing_mandatory_is_rejected(self):
        s = run([ev("pass", "TestB"), ev("pass")])
        self.assertIn("mandatory test did not run: internal/kernel TestA", s["problems"])

    def test_mandatory_in_another_package_does_not_count(self):
        s = run([ev("pass", "TestA", "example.com/m/pkg/other"), ev("pass", pkg="example.com/m/pkg/other")])
        self.assertTrue(any("did not run" in p for p in s["problems"]))

    def test_nonzero_go_exit_is_rejected_even_if_events_look_clean(self):
        s = run(GOOD, go_exit=1)
        self.assertIn("go test exited with status 1", s["problems"])

    def test_package_without_result_is_rejected(self):
        s = run(GOOD + [ev("start", pkg="example.com/m/pkg/broken"), "# example.com/m/pkg/broken", "x.go:1: syntax error"])
        self.assertTrue(any("produced no result" in p for p in s["problems"]))
        self.assertEqual(s["non_json_lines"], 2)

    def test_empty_run_is_rejected(self):
        self.assertEqual(run([])["verdict"], "FAIL")

    def test_too_few_tests_is_rejected(self):
        self.assertTrue(any("only 2" in p for p in run(GOOD, min_pass=3)["problems"]))


if __name__ == "__main__":
    unittest.main()
