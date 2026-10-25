"""Probe guard tests; these do not certify actual host hook coverage."""
import json
import io
import itertools
import queue
import signal
import subprocess
import sys
import time
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import native_boundaries as probe


class NativeBoundaryGuards(unittest.TestCase):
    def test_shared_credentials_labels_never_distinct_child_identity(self):
        a = {'session_id': 'a' * 64, 'host_label': 'root', 'thread_id': 'one'}
        b = {'session_id': 'a' * 64, 'host_label': 'child', 'thread_id': 'two'}
        self.assertEqual(probe.identity_boundary(a, b), 'shared', 'host labels promoted shared credentials into distinct child authority')
        self.assertEqual(probe.identity_boundary(a, {'session_id': 'b' * 64}), 'distinct-session')
        with self.assertRaises(ValueError):
            probe.identity_boundary({'host_label': 'one'}, {'host_label': 'two'})

    def test_startup_callback_cannot_certify_unrelated_successful_tool(self):
        result = probe.hook_verdict([], True)
        self.assertFalse(result['hook_path_exercised'], 'configured hook promoted into exercised enforcement')
        self.assertEqual(result['blocking'], 'unexercised')
        result = probe.hook_verdict([{'event': 'SessionStart', 'state': 'entered'}], True)
        self.assertFalse(result['callback_completed'])
        self.assertFalse(result['tool_hook_path_exercised'], 'startup callback promoted into tool authorization coverage')
        self.assertEqual(result['events_entered'], ['SessionStart'])
        self.assertEqual(result['events_completed'], [])
        self.assertEqual(result['observed_action'], 'completed')
        self.assertEqual(result['blocking'], 'unexercised', 'startup callback certified an unrelated direct-RPC tool success')

    def test_pre_tool_callback_on_diagnostic_route_does_not_certify_blocking(self):
        records = [{'event': 'PreToolUse', 'state': 'entered'}, {'event': 'PreToolUse', 'state': 'completed'}]
        result = probe.hook_verdict(records, True)
        self.assertTrue(result['tool_hook_path_exercised'], 'observed PreToolUse callback presence was lost')
        self.assertEqual(result['events_completed'], ['PreToolUse'])
        self.assertEqual(result['observed_action'], 'completed')
        self.assertEqual(result['blocking'], 'unexercised', 'diagnostic direct-RPC success was treated as a hook-protected action')

    def test_explicit_host_version_pin_rejects_mismatch_before_cases(self):
        with mock.patch.object(probe.subprocess, 'check_output', return_value='codex-cli 0.159.2\n') as version, \
                mock.patch.object(probe, 'host_case') as run_case:
            with self.assertRaisesRegex(ValueError, 'unmeasured Codex version'):
                probe.host_probe('codex', 'dacli', Path('/source'), {}, 'codex-cli 0.164.0-alpha.11.1')
        version.assert_called_once()
        run_case.assert_not_called()

    def test_native_tool_text_structured_parity(self):
        typed = {'ok': True, 'data': {'session_id': 'a' * 64}}
        result = {'structuredContent': typed, 'content': [{'type': 'text', 'text': json.dumps(typed)}]}
        self.assertEqual(probe.tool_data(result), typed['data'])
        result['content'][0]['text'] = json.dumps({'ok': True, 'data': {'session_id': 'b' * 64}})
        with self.assertRaisesRegex(ValueError, 'parity'):
            probe.tool_data(result)
        with self.assertRaises(ValueError):
            probe.tool_data({'isError': True})

    def test_attempt_attribution_uses_authenticated_issuer(self):
        identity = {'session_id': 'a' * 64}
        attempt = {'ID': 'c' * 64, 'SessionID': 'a' * 64, 'Request': {'TaskID': 'task', 'Checkout': str(Path('/tmp/checkout').resolve())}, 'Observation': {'Locator': str(Path('/tmp/checkout').resolve()), 'Commit': 'd' * 40, 'Tree': 'e' * 40}}
        self.assertTrue(probe.attributable(identity, attempt, 'task', '/tmp/checkout'))
        attempt['SessionID'] = 'b' * 64
        self.assertFalse(probe.attributable(identity, attempt, 'task', '/tmp/checkout'), 'foreign native tool issuer attributed to selected principal')

    def test_no_model_method_allowlist(self):
        self.assertNotIn('turn/start', probe.ALLOWED_METHODS)
        self.assertNotIn('thread/shellCommand', probe.ALLOWED_METHODS)
        self.assertNotIn('command/exec', probe.ALLOWED_METHODS)
        self.assertNotIn('thread/resume', probe.ALLOWED_METHODS)

    def test_private_fixture_rejects_alias_duplicates_and_permissions(self):
        with tempfile.TemporaryDirectory() as temporary:
            p = Path(temporary) / 'fixture'
            p.write_text('{"key":1,"key":2}')
            p.chmod(0o600)
            with self.assertRaisesRegex(ValueError, 'duplicate'):
                probe.private_json(p)
            p.write_text('{}'); p.chmod(0o644)
            with self.assertRaisesRegex(ValueError, 'private owner'):
                probe.private_json(p)
            alias = Path(temporary) / 'alias'; alias.symlink_to(p)
            with self.assertRaises(ValueError):
                probe.private_json(alias)

    def test_system_tmp_and_user_tmp_allowed_repository_rejected(self):
        with tempfile.TemporaryDirectory(dir='/tmp', prefix='dacli583-test-system-') as system, tempfile.TemporaryDirectory(prefix='dacli583-test-user-') as user:
            root = Path(system).resolve()
            workspace = root / 'workspace'; workspace.mkdir(); (workspace / '.git').mkdir()
            selections = {}
            for name in ('a', 'b', 'child'):
                selected = root / (name + '.json')
                selected.write_text(json.dumps({'schema_version': 1, 'binary': '/fixture/dacli', 'connection_file': '/fixture/native.json'}))
                selected.chmod(0o600); selections[name] = str(selected)
            value = {'schema_version': 1, 'workspace': str(workspace), 'task_id': 'fixture-task', 'selection_a': selections['a'], 'selection_b': selections['b'], 'child_selection': selections['child'], 'expected_sessions': {'a': 'a' * 64, 'b': 'b' * 64, 'child': 'c' * 64}}
            config = root / 'fixture.json'; config.write_text(json.dumps(value)); config.chmod(0o600)
            with mock.patch.object(probe.tempfile, 'gettempdir', return_value=user):
                self.assertEqual(probe.fixture(config)['workspace'], str(workspace), 'system temporary root rejected when per-user tempfile differs')
                user_workspace = Path(user).resolve() / 'workspace'; user_workspace.mkdir(); (user_workspace / '.git').mkdir()
                value['workspace'] = str(user_workspace); config.write_text(json.dumps(value))
                self.assertEqual(probe.fixture(config)['workspace'], str(user_workspace))
                value['workspace'] = str(Path.cwd()); config.write_text(json.dumps(value))
                with self.assertRaisesRegex(ValueError, 'isolated temporary workspace'):
                    probe.fixture(config)

    def test_failure_diagnostics_are_closed_and_stage_safe(self):
        self.assertEqual(probe.diagnostic_failure(ValueError('isolated temporary workspace required')), 'fixture-not-temporary')
        self.assertEqual(probe.diagnostic_failure(ValueError('credential PRIVATE-TOKEN')), 'input-or-native-validation-failed')
        self.assertEqual(probe.diagnostic_failure(OSError('secret-private-path')), 'filesystem-or-process-unavailable')

    def test_notification_storm_deadline_precedes_bounded_sentinel(self):
        class Storm:
            calls = 0
            def get(self, timeout):
                self.calls += 1
                if self.calls > 6:
                    raise AssertionError('notification storm reached bounded sentinel after deadline')
                return {'method': 'thread/started'}
        rpc = object.__new__(probe.RPC)
        rpc.native = False; rpc.next_id = 0; rpc.notifications = set(); rpc.calls = []
        rpc.proc = mock.Mock(stdin=io.StringIO()); rpc.messages = Storm()
        ticks = itertools.count(0, 5)
        with mock.patch.object(probe.time, 'monotonic', side_effect=lambda: next(ticks)):
            with self.assertRaises(queue.Empty, msg='notification storm escaped explicit deadline'):
                rpc.call('hooks/list', {})
        self.assertLessEqual(rpc.messages.calls, 2, 'deadline did not stop before bounded sentinel')

    def test_notification_retention_is_bounded_under_duplicates(self):
        process = mock.Mock(stdin=io.StringIO(), stdout=io.StringIO(''))
        with mock.patch.object(probe.subprocess, 'Popen', return_value=process), mock.patch.object(probe.RPC, 'call', return_value={}):
            rpc = probe.RPC(['owned-fixture'], '.', {})
        for i in range(1000):
            rpc.record_notification({'method': 'thread/started'})
            rpc.record_notification({'method': 'private-unbounded-' + str(i)})
        self.assertLessEqual(len(rpc.notifications), 8, 'notification retention grew beyond closed enum bound')
        self.assertEqual(set(rpc.notifications), {'thread/started', 'other'})

    def test_close_cleans_owned_group_after_parent_exited(self):
        if not hasattr(os, 'fork'):
            self.skipTest('owned process-group fixture requires POSIX fork')
        with tempfile.TemporaryDirectory(prefix='dacli583-cleanup-') as temporary:
            marker = Path(temporary) / 'terminated'
            script = """import os,signal,sys,time
pid=os.fork()
if pid==0:
 def stop(signum,frame):
  with open(sys.argv[1],'w') as f: f.write('owned descendant received TERM')
  os._exit(0)
 signal.signal(signal.SIGTERM,stop)
 print(os.getpid(),flush=True)
 time.sleep(60)
 os._exit(0)
os._exit(0)
"""
            process = subprocess.Popen([sys.executable, '-c', script, str(marker)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, start_new_session=True)
            try:
                child_pid = int(process.stdout.readline())
                self.assertNotEqual(child_pid, process.pid)
                process.wait(timeout=3)
                rpc = object.__new__(probe.RPC); rpc.proc = process
                started = time.monotonic(); self.assertEqual(rpc.close(), 0)
                self.assertLess(time.monotonic()-started, 10)
                deadline = time.monotonic()+0.2
                while not marker.exists() and time.monotonic()<deadline:
                    time.sleep(0.01)
                self.assertTrue(marker.exists(), 'exited parent skipped owned descendant cleanup')
            finally:
                try: os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError: pass
                if not process.stdin.closed: process.stdin.close()
                if not process.stdout.closed: process.stdout.close()
                process.wait(timeout=3)

    def test_unknown_lifecycle_content_is_not_retained(self):
        self.assertEqual(probe.closed_notification({'method': 'secret-user-message', 'params': {'secret': 'private'}}), 'other')
        self.assertEqual(probe.closed_notification({'method': 'hook/started', 'params': {'secret': 'private'}}), 'hook/started')


if __name__ == '__main__':
    unittest.main()
