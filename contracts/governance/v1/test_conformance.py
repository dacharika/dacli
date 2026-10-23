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
NATIVE_CATALOGS = Path(__file__).parents[3] / "internal/mcp/native/testdata"


def catalog_names(role):
    fixture = json.loads((NATIVE_CATALOGS / f"{role}_catalog.json").read_text())
    return {tool["name"] for tool in fixture["tools"]}


class ContractTests(unittest.TestCase):
    def test_current_native_review_verification_and_lifecycle_tools_are_allowed(self):
        # These checked-in catalogs are the actual role catalogs emitted by the
        # native server; each broker remains scoped to one of them.
        current = {
            "reviewer": {"propose_review", "evaluate_review"},
            "runner": {"get_verification_status"},
            "bridge": {"observe_dispatch", "acknowledge_cancellation"},
        }
        for role, expected in current.items():
            names = catalog_names(role)
            self.assertTrue(expected <= names, f"{role} fixture lacks {expected - names}")
            self.assertTrue(expected <= c.TOOLS, f"allowlist lacks {expected - c.TOOLS}")
            broker = c.Broker.__new__(c.Broker)
            broker.catalog = names
            broker.sequence = 0
            broker.call = lambda method, params: {
                "structuredContent": {"schema_version": 1, "ok": True, "data": {"tool": params["name"]}},
                "isError": False,
                "content": [{"type": "text", "text": json.dumps({"schema_version": 1, "ok": True, "data": {"tool": params["name"]}})}],
            }
            for name in sorted(expected):
                with self.subTest(role=role, tool=name):
                    self.assertEqual(broker.tool(name, {"schema_version": 1})["data"]["tool"], name)

    def test_obsolete_native_tool_names_are_refused_even_if_catalog_advertises_them(self):
        obsolete = {
            "submit_review", "get_verification", "controller_status", "controller_step",
            "start_verification", "poll_verification",
        }
        self.assertTrue(obsolete.isdisjoint(c.TOOLS))
        actual = set().union(*(catalog_names(role) for role in ("worker", "runner", "reviewer", "bridge", "controller")))
        self.assertTrue(obsolete.isdisjoint(actual), f"obsolete names present in native catalogs: {obsolete & actual}")
        broker = c.Broker.__new__(c.Broker)
        broker.catalog = set().union(*(catalog_names(role) for role in ("worker", "runner", "reviewer", "bridge", "controller"))) | obsolete
        broker.sequence = 0
        broker.call = lambda *_: self.fail("obsolete tool reached transport")
        for name in sorted(obsolete):
            with self.subTest(tool=name), self.assertRaisesRegex(ValueError, "unsupported exact tool request"):
                broker.tool(name, {"schema_version": 1})

    def test_tool_in_global_ceiling_but_absent_from_selected_catalog_is_refused(self):
        name = "propose_review"
        selected_catalog = catalog_names("worker")
        self.assertIn(name, c.TOOLS)
        self.assertNotIn(name, selected_catalog)
        broker = c.Broker.__new__(c.Broker)
        broker.catalog = selected_catalog
        broker.sequence = 0
        broker.call = lambda *_: self.fail("tool outside selected catalog reached transport")
        with self.assertRaisesRegex(ValueError, "unsupported exact tool request"):
            broker.tool(name, {"schema_version": 1})

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
