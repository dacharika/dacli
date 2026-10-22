"""Deterministic consumer assertions; source assembly does not execute these."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("conformance", Path(__file__).with_name("conformance.py"))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


class ContractTests(unittest.TestCase):
    def test_duplicate_fields_refused(self):
        with self.assertRaisesRegex(ValueError, "duplicate"):
            json.loads('{"a":1,"a":2}', object_pairs_hook=c.unique)

    def test_structured_text_parity_required(self):
        typed = {"schema_version": 1, "ok": False, "error": {"exit_code": 3}}
        with self.assertRaisesRegex(ValueError, "parity"):
            c.envelope({"structuredContent": typed, "isError": True,
                        "content": [{"type": "text", "text": '{}'}]})

    def test_private_input_refuses_symlink(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp)/"fixture"
            p.write_text('{}')
            p.chmod(0o600)
            alias = Path(tmp)/"alias"
            alias.symlink_to(p)
            with self.assertRaises(OSError):
                c.private(alias)

    def test_wrong_refusal_never_passes(self):
        class Fake:
            def __init__(self, *args): pass
            def tool(self, *args):
                return {"schema_version": 1, "ok": False, "error": {"exit_code": 1, "code": "internal"}}
            def close(self): pass
        definitions = {"cases": {"revoked-identity": {"mode": "backend", "exit_codes": [3],
            "required_fields": ["/error/exit_code"], "unsupported": "not supplied"}}}
        step = {"profile": "revoked_worker", "tool": "identity", "arguments": {"schema_version": 1},
            "restart": False, "expect": {"exit_code": 3, "fields": {"/error/exit_code": 3}, "equal_to": {}}}
        with patch.object(c, "Broker", Fake):
            result = c.run({"profiles": {"revoked_worker": "unused"}, "cases": {"revoked-identity": [step]}}, definitions, "unused", "fake")
        self.assertEqual(result["cases"][0]["status"], "failed-or-unknown", "wrong refusal was accepted")
        self.assertFalse(result["host_certified"])

    def test_hooks_never_promoted_from_stdio(self):
        definitions = {"cases": {"disabled-hooks": {"mode": "live-only", "unsupported": "unentered"}}}
        result = c.run({"profiles": {}, "cases": {"disabled-hooks": []}}, definitions, "unused", "codex")
        self.assertEqual(result["cases"][0]["status"], "unsupported", "unentered hook passed")
        self.assertFalse(result["host_certified"])


if __name__ == "__main__":
    unittest.main()
