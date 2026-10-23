"""Package and explicit MCP installation safety checks."""
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


PACKAGE = load_module("antigravity_package", ROOT / "scripts/package.py")
INSTALL = load_module("antigravity_install", ROOT / "scripts/install.py")


class PackageInstallTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.package = self.base / "plugin"
        self.selection = self.base / "selection.json"
        self.secret = "credential-content-must-never-be-copied"
        self.connection = self.base / "native-connection.json"
        self.connection.write_text(self.secret)
        self.connection.chmod(0o600)
        self.write_selection()

    def write_selection(self):
        self.selection.write_text(json.dumps({
            "schema_version": 1,
            "binary": sys.executable,
            "connection_file": str(self.connection),
        }))
        self.selection.chmod(0o600)

    def test_configured_package_pins_final_bootstrap_and_excludes_connection_contents(self):
        PACKAGE.package(self.package, self.selection)

        config = json.loads((self.package / "mcp_config.json").read_text())
        entry = config["mcpServers"]["dacli"]
        self.assertEqual(entry["command"], sys.executable)
        self.assertEqual(entry["args"], [str(self.package / "scripts/serve.py"), str(self.selection)])
        manifest = json.loads((self.package / "package-manifest.json").read_text())
        self.assertEqual(manifest["shared_bootstrap_sha256"], manifest["files"]["scripts/serve.py"])
        self.assertFalse(manifest["host_validated"])
        self.assertFalse(manifest["orchestration_supported"])
        for path in self.package.rglob("*"):
            if path.is_file():
                self.assertNotIn(self.secret, path.read_text(errors="ignore"))
        self.assertFalse((self.package / self.connection.name).exists())

    def test_unconfigured_package_keeps_inert_template(self):
        PACKAGE.package(self.package)

        config = json.loads((self.package / "mcp_config.json").read_text())
        self.assertEqual(config["mcpServers"]["dacli"], {
            "command": "python3",
            "args": ["@DACLI_PACKAGE_ROOT@/scripts/serve.py", "@DACLI_SELECTION_FILE@"],
        })
        manifest = json.loads((self.package / "package-manifest.json").read_text())
        self.assertFalse(manifest["configured"])

    def test_render_preserves_settings_and_install_backups_exact_private_bytes(self):
        PACKAGE.package(self.package)
        config_path = self.base / "mcp_config.json"
        original = b'{"theme":"dark","mcpServers":{"other":{"command":"tool"}}}\n'
        config_path.write_bytes(original)
        config_path.chmod(0o600)

        old, updated = INSTALL.render(self.package, self.selection, config_path)
        result = json.loads(updated)
        self.assertEqual(old, original)
        self.assertEqual(result["theme"], "dark")
        self.assertEqual(result["mcpServers"]["other"], {"command": "tool"})
        self.assertEqual(result["mcpServers"]["dacli"]["command"], sys.executable)

        backup = Path(INSTALL.atomic_install(config_path, old, updated))
        self.assertEqual(backup.read_bytes(), original)
        self.assertEqual(backup.stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(config_path.read_bytes()), result)

    def test_render_refuses_conflicting_server_and_modified_package_source(self):
        PACKAGE.package(self.package)
        conflicting = self.base / "conflicting.json"
        conflicting.write_text(json.dumps({"mcpServers": {"dacli": {"command": "other"}}}))
        with self.assertRaisesRegex(ValueError, "conflicting dacli"):
            INSTALL.render(self.package, self.selection, conflicting)

        source = self.package / "README.md"
        source.write_text(source.read_text() + "tampered\n")
        with self.assertRaisesRegex(ValueError, "package source changed"):
            INSTALL.render(self.package, self.selection, self.base / "fresh.json")

    def test_atomic_install_refuses_configuration_changed_after_render(self):
        PACKAGE.package(self.package)
        config_path = self.base / "mcp_config.json"
        original = b'{"mcpServers":{}}\n'
        config_path.write_bytes(original)
        config_path.chmod(0o600)
        old, updated = INSTALL.render(self.package, self.selection, config_path)
        changed = b'{"mcpServers":{"newer":{"command":"keep"}}}\n'
        real_read = INSTALL.read

        def change_before_compare(path, private=False):
            if Path(path) == config_path:
                config_path.write_bytes(changed)
            return real_read(path, private=private)

        with mock.patch.object(INSTALL, "read", side_effect=change_before_compare):
            with self.assertRaisesRegex(ValueError, "changed during preparation"):
                INSTALL.atomic_install(config_path, old, updated)
        self.assertEqual(config_path.read_bytes(), changed)


if __name__ == "__main__":
    unittest.main()
