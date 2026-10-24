import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shlex
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
    def test_historical_04_receipt_preserves_actual_migration_observations(self):
        receipt = json.loads((ROOT / 'receipts/native-install-0.159.2.json').read_text())
        # This exact receipt binds the historical 0.4 package/artifacts. It cannot
        # certify newly changed source or silently acquire new current hashes.
        self.assertEqual(hashlib.sha256((ROOT / 'receipts/native-install-0.159.2.json').read_bytes()).hexdigest(),
                         'e924226d08ac0f66901b661fa1534cd8db42fe708478a00ab1114c9c5078efcb')
        self.assertEqual(set(receipt['plugin_source_sha256']), set(PACKAGE.SOURCE_FILES))
        self.assertEqual(PACKAGE.HISTORICAL_RECEIPTS, ('receipts/native-install-0.159.2.json',))
        for digest in (*receipt['plugin_source_sha256'].values(), receipt['smoke_script_sha256']):
            self.assertRegex(digest, r'^[0-9a-f]{64}$')
        self.assertTrue(receipt['genuine_schema5_profile_migrated_to_6'])
        self.assertTrue(receipt['migration_preserved_existing_profile_and_credentials'])
        for key in ('pre_migration_new_server_exit', 'forged_owner_upgrade_exit',
                    'already_target_upgrade_exit', 'old_schema5_writer_exit'):
            self.assertEqual(receipt[key], 3)
        self.assertFalse(receipt['model_turn_requested'])
        self.assertFalse(receipt['dacli_spawn_used'])
        self.assertEqual(receipt['installed_version'], '0.4.0')
        self.assertEqual(receipt['configured_async_completion_provenance'], 'trusted-local-runner')
        for key in ('configured_async_exact_replay', 'configured_async_unrelated_reads_responsive',
                    'configured_async_cancel_has_no_evidence', 'configured_async_restart_preserves_unknown_and_completed'):
            self.assertTrue(receipt[key])
        self.assertEqual(set(receipt['configured_async_tool_names']) - set(receipt['tool_names']),
                         {'request_verification', 'get_verification_status', 'cancel_verification'})
        self.assertEqual(set(receipt['configured_review_tool_names']),
                         {'identity','workspace_status','task_list','task_get','propose_review','evaluate_review'})
        for key in ('configured_review_separate_credentials',
                    'configured_review_exact_trusted_evidence_eligible',
                    'configured_review_exact_replay_is_historical',
                    'configured_review_fresh_policy_and_released_lease_refuse',
                    'configured_review_caller_grant_override_refused',
                    'configured_review_request_changes_ineligible',
                    'configured_review_preserves_task_status',
                    'configured_review_live_reviewer_revocation_refused'):
            self.assertTrue(receipt[key])

    def test_current_orchestration_version_has_no_fabricated_installation_receipt(self):
        portable = json.loads((ROOT / 'plugin.json').read_text())
        legacy = json.loads((ROOT / '.codex-plugin/plugin.json').read_text())
        self.assertEqual(portable['version'], '0.5.0')
        self.assertEqual(legacy['version'], portable['version'])
        self.assertIn('bounded', portable['description'])
        self.assertLessEqual(len(portable['extensions']['com.openai']['interface']['shortDescription']), 30)
        self.assertNotIn('receipts/orchestration-install-0.159.2.json', PACKAGE.FILES)

    def test_current_native_swarm_qualification_receipt_is_packaged(self):
        receipt_path = 'receipts/native-swarm-0.163.0-alpha.11.json'
        notes_path = 'receipts/native-swarm-0.163.0-alpha.11.md'
        receipt = json.loads((ROOT / receipt_path).read_text())
        terminal = receipt['terminal']
        self.assertTrue(receipt['actual_native_swarm_exercised'])
        self.assertEqual((terminal['Phase'], terminal['HaltClass'], terminal['Waves']),
                         ('completed', 'empty_backlog', 2))
        self.assertTrue(any('provider usage and billing remain unknown' in limit
                            for limit in receipt['limits']))
        with tempfile.TemporaryDirectory() as tmp:
            archive_path = PACKAGE.build(Path(tmp) / 'qualification.zip')
            with zipfile.ZipFile(archive_path) as archive:
                names = set(archive.namelist())
                self.assertIn('dacli-governance/' + receipt_path, names)
                self.assertIn('dacli-governance/' + notes_path, names)
                self.assertEqual(names, {'dacli-governance/' + name for name in PACKAGE.FILES})
                self.assertFalse(any('/private/' in name or '/probes/' in name or
                                     name.endswith(('.token', '.pem', '.key')) for name in names))

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

    def test_owner_selected_runner_path_is_forwarded_without_reading_it(self):
        runner = self.base / 'absent-private-runner.json'
        self.write_config(dict(self.config, runner_config_file=str(runner)))
        result = self.run_server()
        self.assertEqual(result.returncode, 0, result.stderr)
        response = json.loads(result.stdout)
        self.assertEqual(response['argv'], ['native', 'serve', '--connection', str(self.profile),
                                            '--runner-config', str(runner)])
        self.assertFalse(response['ambient_present'])
        self.assertFalse(runner.exists(), 'bootstrap created or read runner credentials')

    def test_runner_selection_rejects_invalid_paths_and_command_overrides(self):
        for value in ('', 'relative', None, True, 1, [], {}, '/private/\x00runner'):
            self.write_config(dict(self.config, runner_config_file=value))
            self.assert_refused()
        for key in ('command', 'credential_file', 'grant_id', 'worker_session'):
            self.write_config(dict(self.config, runner_config_file='/private/runner.json', **{key:'override'}))
            self.assert_refused()

    def test_owner_selected_orchestration_profile_is_forwarded_without_reading_it(self):
        profile = self.base / 'absent-private-orchestration.json'
        self.write_config(dict(self.config, orchestration_config_file=str(profile)))
        result = self.run_server()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)['argv'], ['native', 'serve', '--connection', str(self.profile),
                                                            '--orchestration-config', str(profile)])
        self.assertFalse(profile.exists(), 'bootstrap accessed child profiles')
        for key in ('runner_config_file', 'review_config_file'):
            self.write_config(dict(self.config, orchestration_config_file=str(profile), **{key:'/private/other.json'}))
            self.assert_refused()
        for key in ('command', 'credential_file', 'routes', 'grant_id', 'model'):
            self.write_config(dict(self.config, orchestration_config_file=str(profile), **{key:'override'}))
            self.assert_refused()

    def test_owner_selected_review_path_is_forwarded_without_reading_it(self):
        review = self.base / 'absent-private-review.json'
        self.write_config(dict(self.config, review_config_file=str(review)))
        result = self.run_server()
        self.assertEqual(result.returncode, 0, result.stderr)
        response = json.loads(result.stdout)
        self.assertEqual(response['argv'], ['native', 'serve', '--connection', str(self.profile),
                                            '--review-config', str(review)])
        self.assertFalse(response['ambient_present'])
        self.assertFalse(review.exists(), 'bootstrap accessed review credentials')

    def test_review_selection_rejects_invalid_paths_and_mixed_identities(self):
        for value in ('', 'relative', None, True, 1, [], {}, '/private/\x00review'):
            self.write_config(dict(self.config, review_config_file=value))
            self.assert_refused()
        self.write_config(dict(self.config, review_config_file='/private/review.json',
                               runner_config_file='/private/runner.json'))
        self.assert_refused()
        for key in ('command', 'credential_file', 'grant_id', 'policy_file', 'reviewer_session'):
            self.write_config(dict(self.config, review_config_file='/private/review.json', **{key:'override'}))
            self.assert_refused()

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


class SmokeProcessCommandTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        sys.path.insert(0, str(ROOT / 'scripts'))
        try:
            import smoke
        finally:
            sys.path.pop(0)
        cls.smoke = smoke

    def compare(self, argv, executable='/private/fixture/python', args=None):
        expected_args = argv[1:] if args is None else args
        return self.smoke._process_command_comparison(shlex.join(argv), executable, expected_args)

    def test_process_command_match_requires_exact_resolved_executable_and_arguments(self):
        executable = '/private/fixture/python'
        args = ['/private/fixture/check.py', '/private/fixture/started', 'fifo']
        matched, diagnostic = self.compare([executable, *args], executable, args)
        self.assertTrue(matched, diagnostic)
        self.assertEqual(diagnostic,
                         'argv_parse=ok; argv_count_match=true; executable_match=true; '
                         'argument_mismatch_indices=[]')

    def test_process_command_reports_independent_executable_argument_and_count_mismatches(self):
        executable = '/private/fixture/python'
        args = ['/private/fixture/check.py', 'one', 'two']
        wrong_executable, executable_diagnostic = self.compare(
            [executable + '-other', *args], executable, args)
        wrong_argument, argument_diagnostic = self.compare(
            [executable, args[0], 'changed', args[2]], executable, args)
        wrong_count, count_diagnostic = self.compare([executable, args[0]], executable, args)

        self.assertFalse(wrong_executable)
        self.assertIn('argv_count_match=true; executable_match=false; argument_mismatch_indices=[]',
                      executable_diagnostic)
        self.assertFalse(wrong_argument)
        self.assertIn('argv_count_match=true; executable_match=true; argument_mismatch_indices=[2]',
                      argument_diagnostic)
        self.assertFalse(wrong_count)
        self.assertIn('argv_count_match=false; executable_match=true; argument_mismatch_indices=[2,3]',
                      count_diagnostic)

    def test_process_command_rejects_empty_and_malformed_quoted_commands(self):
        empty, empty_diagnostic = self.smoke._process_command_comparison(
            '   ', '/private/fixture/python', ['arg'])
        malformed, malformed_diagnostic = self.smoke._process_command_comparison(
            "'/private/fixture/python", '/private/fixture/python', ['arg'])

        self.assertFalse(empty)
        self.assertIn('argv_parse=ok; argv_count_match=false; executable_match=false', empty_diagnostic)
        self.assertFalse(malformed)
        self.assertEqual(malformed_diagnostic,
                         'argv_parse=malformed_quoting; argv_count_match=false; '
                         'executable_match=false; argument_mismatch_indices=unavailable')

    def test_process_command_diagnostics_never_disclose_observed_arguments(self):
        secret_path = '/private/operator/fixture-secret-token'
        command_line = shlex.join(['/private/unexpected/python', secret_path, 'credential-value'])
        matched, diagnostic = self.smoke._process_command_comparison(
            command_line, '/private/fixture/python', ['/private/fixture/check.py'])

        self.assertFalse(matched)
        self.assertNotIn(secret_path, diagnostic)
        self.assertNotIn('credential-value', diagnostic)
        self.assertNotIn(command_line, diagnostic)


if __name__ == '__main__':
    unittest.main()
