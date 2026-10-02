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

    def test_export_refuses_symlink_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'source'; root.mkdir()
            for name in PACKAGE.FILES:
                path = root / name; path.parent.mkdir(parents=True, exist_ok=True); path.write_text('fixture')
            scripts = root / 'scripts'; scripts.rename(Path(tmp) / 'scripts')
            scripts.symlink_to(Path(tmp) / 'scripts', target_is_directory=True)
            with self.assertRaisesRegex(ValueError, 'regular file'):
                PACKAGE.build(Path(tmp) / 'out.zip', root)


class BootstrapTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.binary = self.base / 'fake-dacli'
        self.binary.write_text('#!' + sys.executable + '\nimport os,sys,json\nprint(json.dumps({"argv":sys.argv[1:],"ambient_present":"DACLI_AGENT" in os.environ}))\n')
        self.binary.chmod(0o700)
        # Deliberately absent: the bootstrap must not read credentials/profile.
        self.profile = self.base / 'native-session.json'
        self.connection = self.base / 'selection.json'
        self.config = {'schema_version': 1, 'binary': str(self.binary), 'connection_file': str(self.profile)}
        self.write_config(self.config)

    def write_config(self, config):
        self.connection.write_text(json.dumps(config))
        self.connection.chmod(0o600)

    def run_server(self):
        return subprocess.run([sys.executable, str(ROOT / 'scripts/serve.py'), str(self.connection)],
                              env=dict(os.environ, DACLI_AGENT='ambient-secret'), capture_output=True, text=True, timeout=5)

    def assert_refused(self):
        result = self.run_server()
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertEqual(result.stdout, '')
        self.assertNotIn('ambient-secret', result.stderr)
        self.assertNotIn(str(self.base), result.stderr)

    def test_native_only_no_legacy_or_operator_fallback(self):
        result = self.run_server()
        self.assertEqual(result.returncode, 0, result.stderr)
        response = json.loads(result.stdout)
        self.assertEqual(response['argv'], ['native', 'serve', '--connection', str(self.profile)])
        self.assertFalse(response['ambient_present'])
        self.assertNotIn('ambient-secret', result.stdout + result.stderr)

    def test_missing_malformed_and_legacy_selection_refuse(self):
        self.connection.unlink(); self.assert_refused()
        for config in ({}, {'workspace':'/tmp', 'binary':str(self.binary), 'credential_file':'/tmp/token'},
                       dict(self.config, schema_version=True), dict(self.config, schema_version=2),
                       dict(self.config, operator=True), dict(self.config, connection_file='relative'),
                       dict(self.config, binary='missing'), dict(self.config, connection_file=None)):
            self.write_config(config); self.assert_refused()
        for raw in ('{', 'x' * 16385, '{"schema_version":1,"schema_version":1}'):
            self.connection.write_text(raw); self.assert_refused()

    def test_selection_authority_requires_private_regular_inode(self):
        self.connection.chmod(0o644); self.assert_refused()
        self.connection.unlink(); self.connection.symlink_to(self.binary); self.assert_refused()
        self.connection.unlink(); self.connection.mkdir(); self.assert_refused()
        self.connection.rmdir(); os.mkfifo(self.connection, 0o600); self.assert_refused()


class SmokeCleanupTests(unittest.TestCase):
    def test_revocation_requires_native_auth_refusal(self):
        sys.path.insert(0, str(ROOT / 'scripts'))
        try:
            import smoke
        finally:
            sys.path.pop(0)
        self.assertTrue(smoke.HostRPCError('identity', {'code':-32003,'message':'native session authority denied'}).native_refusal)
        for error in ({'code':-32603,'message':'transport disconnected'},
                      {'code':-32003,'message':'unrelated'}, {'message':'request refused'}):
            self.assertFalse(smoke.HostRPCError('identity', error).native_refusal)


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


if __name__ == '__main__':
    unittest.main()
