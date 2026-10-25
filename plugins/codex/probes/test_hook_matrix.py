"""Deterministic guards for the live Codex hook qualification runner."""
import importlib.util
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
import unittest


MODULE = Path(__file__).with_name('hook_matrix.py')
spec = importlib.util.spec_from_file_location('hook_matrix', MODULE)
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)
COMMAND_HOOK = [{'event': 'preToolUse', 'handler': 'command', 'enabled': True, 'trust': 'untrusted'}]
MCP_HOOK = [{'event': 'preToolUse', 'handler': 'mcpTool', 'enabled': True, 'trust': 'untrusted'}]


class HookMatrixQualificationGuards(unittest.TestCase):
    def test_responses_fixture_reads_exact_content_length_without_deadlock(self):
        with tempfile.TemporaryDirectory(prefix='codex-hook-http-') as temporary:
            server = probe.FixtureServer(Path(temporary) / 'marker')
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                body = json.dumps({'tools': [{'name': 'exec_command'}]}).encode()
                request = urllib.request.Request(
                    f'http://127.0.0.1:{server.server_address[1]}/v1/responses',
                    data=body, headers={'Content-Type': 'application/json'}, method='POST')
                with urllib.request.urlopen(request, timeout=2) as response:
                    payload = response.read(4096)
                self.assertEqual(response.status, 200)
                self.assertIn(b'response.completed', payload)
                self.assertEqual(server.request_count, 1)
                self.assertEqual(server.tool_names, ['exec_command'])
                self.assertFalse(server.write_failed)
            finally:
                server.shutdown()
                server.server_close()

    def test_inventory_does_not_promote_configuration_to_callback_entry(self):
        inventory = probe.safe_inventory_record({
            'eventName': 'preToolUse', 'trustStatus': 'untrusted', 'handlerType': 'command',
            'enabled': True, 'sourcePath': '/private/fixture/hook.py', 'command': 'secret command',
        })
        self.assertEqual(inventory, {
            'event': 'preToolUse', 'trust': 'untrusted', 'handler': 'command', 'enabled': True,
        })
        self.assertNotIn('sourcePath', inventory)
        self.assertFalse(probe.classify_case('observe', {
            'callback_entered': False, 'turn_outcome': 'completed', 'marker_created': True,
            'hook_statuses': [],
        }), 'configured hook or successful tool was promoted into callback entry')

    def test_observe_requires_callback_terminal_status_and_completed_tool(self):
        good = {'callback_entered': True, 'turn_outcome': 'completed', 'marker_created': True,
                'hook_statuses': ['completed'], 'hook_inventory': COMMAND_HOOK}
        self.assertTrue(probe.classify_case('observe', good))
        for change in (
            {'callback_entered': False}, {'turn_outcome': 'unknown'},
            {'marker_created': False}, {'hook_statuses': ['failed']},
        ):
            with self.subTest(change=change):
                self.assertFalse(probe.classify_case('observe', {**good, **change}))

    def test_transport_and_deadline_failures_never_qualify(self):
        result = {'callback_entered': True, 'turn_outcome': 'completed', 'marker_created': True,
                  'hook_statuses': ['completed'], 'hook_inventory': COMMAND_HOOK,
                  'host_deadline_met': True, 'cleanup_deadline_met': True,
                  'total_deadline_met': True, 'cleanup_complete': True,
                  'response_limit_exceeded': False, 'notification_overflow': False,
                  'fixture_write_failed': False}
        self.assertTrue(probe.qualify_case('observe', result))
        for change in ({'host_deadline_met': False}, {'cleanup_deadline_met': False},
                       {'total_deadline_met': False}, {'cleanup_complete': False},
                       {'response_limit_exceeded': True}, {'notification_overflow': True},
                       {'fixture_write_failed': True}, {'transport_outcome': 'timeout'}):
            with self.subTest(change=change):
                self.assertFalse(probe.qualify_case('observe', {**result, **change}))

    def test_timeout_is_not_inferred_from_missing_marker_alone(self):
        no_entry = {'callback_entered': False, 'turn_outcome': 'completed', 'marker_created': False,
                    'hook_statuses': [], 'hook_inventory': COMMAND_HOOK}
        self.assertFalse(probe.classify_case('timeout', no_entry),
                         'absent marker was misreported as a callback timeout')
        entered_failed = {**no_entry, 'callback_entered': True, 'hook_statuses': ['failed'],
                          'marker_created': True, 'hook_durations_ms': [1000]}
        self.assertTrue(probe.classify_case('timeout', entered_failed))
        self.assertFalse(probe.classify_case('timeout', {**entered_failed, 'hook_durations_ms': [10]}))

    def test_deny_requires_blocked_callback_and_prevents_marker(self):
        denied = {'callback_entered': True, 'turn_outcome': 'completed', 'marker_created': False,
                  'hook_statuses': ['blocked'], 'hook_inventory': COMMAND_HOOK}
        self.assertTrue(probe.classify_case('deny', denied))
        self.assertFalse(probe.classify_case('deny', {**denied, 'marker_created': True}))
        self.assertFalse(probe.classify_case('deny', {**denied, 'hook_statuses': ['failed']}))

    def test_disabled_and_untrusted_are_control_observations_only(self):
        control = {'callback_entered': False, 'turn_outcome': 'completed', 'marker_created': True,
                   'hook_statuses': [], 'hook_inventory': COMMAND_HOOK}
        self.assertTrue(probe.classify_case('disabled', {**control, 'thread_controls': {
            'hooks_disabled': True, 'hook_trust_bypassed': True}}))
        self.assertTrue(probe.classify_case('untrusted', {**control, 'thread_controls': {
            'hooks_disabled': False, 'hook_trust_bypassed': False}}))
        self.assertFalse(probe.classify_case('disabled', {**control, 'turn_outcome': 'unknown', 'thread_controls': {
            'hooks_disabled': True, 'hook_trust_bypassed': True}}))
        self.assertFalse(probe.classify_case('untrusted', {**control, 'callback_entered': True, 'thread_controls': {
            'hooks_disabled': False, 'hook_trust_bypassed': False}}))
        self.assertFalse(probe.classify_case('disabled', {**control, 'hook_inventory': [], 'thread_controls': {
            'hooks_disabled': True, 'hook_trust_bypassed': True}}), 'empty inventory was promoted into disabled-hook evidence')

    def test_unknown_mcp_callback_tool_requires_healthy_advertising_server(self):
        result = {'callback_entered': False, 'hook_started': True, 'turn_outcome': 'completed',
                  'marker_created': True, 'hook_statuses': ['failed'],
                  'unsupported_scenario': 'unknown-mcp-callback-tool', 'fixture_mcp_server_healthy': True,
                  'fixture_mcp_noop_advertised': True, 'requested_mcp_tool_not_advertised': True,
                  'hook_inventory': MCP_HOOK}
        self.assertTrue(probe.classify_case('unsupported-callback', result))
        self.assertFalse(probe.classify_case('unsupported-callback', {**result, 'hook_statuses': []}))
        self.assertFalse(probe.classify_case('unsupported-callback', {**result, 'fixture_mcp_server_healthy': False}))
        self.assertFalse(probe.classify_case('unsupported-callback', {**result, 'requested_mcp_tool_not_advertised': False}))

    def test_turn_completed_notification_with_failed_status_is_not_success(self):
        rpc = object.__new__(probe.RPC)
        rpc.deadline = time.monotonic() + 1
        rpc.messages = queue.Queue()
        rpc.notifications = []
        rpc.notification_overflow = False
        rpc.read_overflow = False
        rpc.terminal_key = None
        rpc.messages.put({'method': 'turn/completed', 'params': {
            'threadId': 'fixture-thread', 'turn': {'id': 'other-turn', 'status': 'completed'},
        }})
        rpc.messages.put({'method': 'turn/completed', 'params': {
            'threadId': 'fixture-thread', 'turn': {'id': 'fixture-turn', 'status': 'failed'},
        }})
        self.assertEqual(rpc.terminal_turn('fixture-thread', 'fixture-turn'), 'failed')
        failed = {'callback_entered': True, 'turn_outcome': 'failed', 'marker_created': True,
                  'hook_statuses': ['completed'], 'hook_inventory': COMMAND_HOOK}
        self.assertFalse(probe.classify_case('observe', failed),
                         'turn/completed method promoted a failed terminal turn')

    def test_owned_group_cleanup_stops_descendant_after_parent_exit(self):
        if not hasattr(os, 'fork'):
            self.skipTest('POSIX process group fixture required')
        with tempfile.TemporaryDirectory(prefix='codex-hook-cleanup-') as temporary:
            marker = Path(temporary) / 'descendant-stopped'
            script = '''import os,signal,sys,time
marker=sys.argv[1]
child=os.fork()
if child==0:
 def stop(_signum,_frame):
  open(marker,"w").write("stopped")
  os._exit(0)
 signal.signal(signal.SIGTERM,stop)
 print(os.getpid(),flush=True)
 while True: time.sleep(1)
os._exit(0)
'''
            process = subprocess.Popen([sys.executable, '-c', script, str(marker)], stdout=subprocess.PIPE,
                                       stderr=subprocess.DEVNULL, text=True, start_new_session=True)
            try:
                child_pid = int(process.stdout.readline().strip())
                self.assertNotEqual(child_pid, process.pid)
                process.wait(timeout=2)
                outcome = probe.cleanup_owned_group(process, grace=0.5)
                self.assertIn(outcome, {'terminated', 'killed'})
                deadline = time.monotonic() + 0.5
                while not marker.exists() and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertTrue(marker.exists(), 'cleanup waited only for the exited parent')
            finally:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.stdout.close()


if __name__ == '__main__':
    unittest.main()
