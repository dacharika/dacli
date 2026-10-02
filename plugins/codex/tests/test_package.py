import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('package', ROOT / 'scripts/package.py')
PACKAGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PACKAGE)


class PackageTests(unittest.TestCase):
    def test_manifests_and_transport_match(self):
        portable = json.loads((ROOT / 'plugin.json').read_text())
        legacy = json.loads((ROOT / '.codex-plugin/plugin.json').read_text())
        for field in ('name', 'version', 'description', 'author', 'repository', 'license'):
            self.assertEqual(portable[field], legacy[field])
        self.assertEqual(portable['extensions']['com.openai']['interface'], legacy['interface'])
        self.assertLessEqual(len(legacy['interface']['shortDescription']), 30)
        servers = json.loads((ROOT / 'mcp.json').read_text())['mcpServers']
        expected = json.loads((ROOT / '.mcp.json').read_text())['mcpServers']
        for name, server in servers.items():
            self.assertEqual(server['type'], 'stdio')
            self.assertEqual({k: v for k, v in server.items() if k != 'type'}, expected[name])
            self.assertNotIn('--operator', server['args'])

    def test_export_is_deterministic_and_excludes_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            first = PACKAGE.build(Path(tmp) / 'one.zip')
            second = PACKAGE.build(Path(tmp) / 'two.zip')
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with zipfile.ZipFile(first) as archive:
                self.assertEqual(set(archive.namelist()), {'dacli-governance/' + x for x in PACKAGE.FILES})
                self.assertFalse(any('probes/' in x or 'tests/' in x or '__pycache__' in x for x in archive.namelist()))
        with self.assertRaisesRegex(ValueError, 'outside'):
            PACKAGE.build(ROOT / 'out.zip')

    def test_export_refuses_symlink(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'source'; root.mkdir()
            for name in PACKAGE.FILES:
                path = root / name; path.parent.mkdir(parents=True, exist_ok=True); path.write_text('fixture')
            file = root / 'plugin.json'; file.unlink(); file.symlink_to(ROOT / 'plugin.json')
            with self.assertRaisesRegex(ValueError, 'regular file'):
                PACKAGE.build(Path(tmp) / 'out.zip', root)


class BootstrapTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.workspace = self.base / 'workspace'; self.workspace.mkdir()
        (self.workspace / '.dacli').mkdir()
        self.binary = self.base / 'fake-dacli'
        self.binary.write_text('#!' + sys.executable + '\nimport os,sys,json\nprint(json.dumps({"argv":sys.argv[1:],"cwd":os.getcwd(),"token_present":bool(os.environ.get("DACLI_AGENT")),"ambient_replaced":os.environ.get("DACLI_AGENT")=="fixture-secret"}))\n')
        self.binary.chmod(0o700)
        self.credential = self.base / 'credential'; self.credential.write_text('fixture-secret\n'); self.credential.chmod(0o600)
        self.connection = self.base / 'connection.json'
        self.connection.write_text(json.dumps({'workspace':str(self.workspace), 'binary':str(self.binary), 'credential_file':str(self.credential)}))

    def run_server(self):
        return subprocess.run([sys.executable, str(ROOT / 'scripts/serve.py'), str(self.connection)],
                              env=dict(os.environ, DACLI_AGENT='ambient-wrong'), capture_output=True, text=True, timeout=5)

    def test_explicit_connection_replaces_ambient_identity(self):
        result = self.run_server()
        self.assertEqual(result.returncode, 0, result.stderr)
        response = json.loads(result.stdout)
        self.assertEqual(response['argv'], ['mcp', 'serve'])
        self.assertTrue(os.path.samefile(response['cwd'], self.workspace))
        self.assertTrue(response['token_present']); self.assertTrue(response['ambient_replaced'])
        self.assertNotIn('fixture-secret', result.stdout + result.stderr)

    def test_empty_credential_cannot_start_server(self):
        self.credential.write_text('')
        result = self.run_server()
        self.assertEqual(result.returncode, 3)
        self.assertEqual(result.stdout, '')
        self.assertIn('nonempty token', result.stderr)

    def test_missing_connection_and_token_refuse(self):
        self.connection.unlink()
        result = self.run_server(); self.assertEqual(result.returncode, 3); self.assertEqual(result.stdout, '')
        self.connection.write_text(json.dumps({'workspace':str(self.workspace), 'binary':str(self.binary), 'credential_file':str(self.credential)}))
        self.credential.unlink()
        result = self.run_server(); self.assertEqual(result.returncode, 3); self.assertEqual(result.stdout, '')

    def test_public_and_symlink_credentials_refuse(self):
        self.credential.chmod(0o644)
        result = self.run_server(); self.assertEqual(result.returncode, 3); self.assertEqual(result.stdout, '')
        self.credential.unlink(); self.credential.symlink_to(self.binary)
        result = self.run_server(); self.assertEqual(result.returncode, 3); self.assertEqual(result.stdout, '')

    def test_large_and_multiline_credentials_refuse_without_content(self):
        for value in ('x' * 4097, 'fixture-secret\nsecond-secret'):
            self.credential.write_text(value)
            result = self.run_server(); self.assertEqual(result.returncode, 3); self.assertEqual(result.stdout, '')
            self.assertNotIn('fixture-secret', result.stderr)


if __name__ == '__main__':
    unittest.main()


class SmokeCleanupTests(unittest.TestCase):
    def test_failed_host_initialization_reaps_process(self):
        from unittest.mock import patch
        sys.path.insert(0, str(ROOT / 'scripts'))
        try:
            import smoke
        finally:
            sys.path.pop(0)
        with tempfile.TemporaryDirectory() as tmp:
            fake = Path(tmp) / 'fake-codex'
            fake.write_text('#!' + sys.executable + '\nimport time\ntime.sleep(60)\n')
            fake.chmod(0o700)
            processes = []
            real_popen = subprocess.Popen

            def record(*args, **kwargs):
                process = real_popen(*args, **kwargs)
                processes.append(process)
                return process

            try:
                with patch.object(smoke.subprocess, 'Popen', side_effect=record), \
                     patch.object(smoke.Host, 'call', side_effect=RuntimeError('fixture initialization refused')):
                    with self.assertRaisesRegex(RuntimeError, 'fixture initialization refused'):
                        smoke.Host(str(fake), tmp, dict(os.environ))
                self.assertEqual(len(processes), 1)
                self.assertIsNotNone(processes[0].poll(), 'constructor leaked app-server process')
                self.assertTrue(processes[0].stdin.closed)
                self.assertTrue(processes[0].stdout.closed)
            finally:
                for process in processes:
                    if process.poll() is None:
                        process.kill(); process.wait(timeout=5)
                    process.stdin.close(); process.stdout.close()
