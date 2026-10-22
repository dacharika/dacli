#!/usr/bin/env python3
"""Bounded Codex diagnostic/MCP identity probe; never starts a model turn.

Host/thread labels are correlation only. The native server authenticates each
explicit owner-selected connection. This probe never opens credential files.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import queue
import re
import shlex
import signal
import stat
import subprocess
import sys
import tempfile
import threading
import time

SCHEMA = 'dacli-codex-native-boundaries/v2'
VERSION = 'codex-cli 0.159.2'
CASES = ('observe', 'deny', 'disabled', 'untrusted', 'malformed', 'timeout', 'error')
EVENTS = ('SessionStart', 'PreToolUse', 'PostToolUse', 'SubagentStart', 'SubagentStop', 'SessionEnd')
ALLOWED_METHODS = {'initialize', 'hooks/list', 'thread/start', 'mcpServerStatus/list', 'mcpServer/tool/call'}


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def private_json(path):
    path = Path(path)
    if not path.is_absolute() or path.is_symlink():
        raise ValueError('explicit regular private JSON required')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if info.st_mode & 0o077 or not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
            raise ValueError('private owner file required')
        raw = stream.read(65537)
    if len(raw) > 65536:
        raise ValueError('private JSON too large')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate private field')
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=unique)


def fixture(path):
    value = private_json(path)
    required = {'schema_version', 'workspace', 'task_id', 'selection_a', 'selection_b', 'child_selection', 'expected_sessions'}
    if not isinstance(value, dict) or set(value) != required or type(value['schema_version']) is not int or value['schema_version'] != 1:
        raise ValueError('fixture schema mismatch')
    workspace = Path(value['workspace']).resolve(strict=True)
    temporary_roots = {Path(tempfile.gettempdir()).resolve(), Path('/tmp').resolve()}
    if not any(workspace != root and workspace.is_relative_to(root) for root in temporary_roots):
        raise ValueError('isolated temporary workspace required')
    if not (workspace / '.git').exists() or not re.fullmatch(r'[a-zA-Z0-9_-]{1,128}', value['task_id']):
        raise ValueError('fixture Git checkout and canonical task required')
    expected = value['expected_sessions']
    if not isinstance(expected, dict) or set(expected) != {'a', 'b', 'child'} or any(not re.fullmatch(r'[0-9a-f]{64}', x) for x in expected.values()):
        raise ValueError('exact enrolled session references required')
    if len(set(expected.values())) != 3:
        raise ValueError('three separately enrolled identities required')
    for key in ('selection_a', 'selection_b', 'child_selection'):
        selection = private_json(value[key])
        if not isinstance(selection, dict) or set(selection) != {'schema_version', 'binary', 'connection_file'} or type(selection['schema_version']) is not int or selection['schema_version'] != 1:
            raise ValueError('worker-only selection required')
    value['workspace'] = str(workspace)
    return value


def identity_boundary(left, right):
    """Authenticated session IDs decide shared authority, never host labels."""
    if not all(re.fullmatch(r'[0-9a-f]{64}', x.get('session_id', '')) for x in (left, right)):
        raise ValueError('authenticated native session ID missing')
    return 'shared' if left['session_id'] == right['session_id'] else 'distinct-session'


def tool_data(response):
    if response.get('isError'):
        raise ValueError('native tool refused')
    structured = response.get('structuredContent')
    content = response.get('content')
    if not isinstance(structured, dict) or not structured.get('ok') or not isinstance(content, list) or len(content) != 1:
        raise ValueError('native tool envelope invalid')
    if content[0].get('type') != 'text' or json.loads(content[0]['text']) != structured:
        raise ValueError('native text/structured parity failure')
    return structured['data']


def attributable(identity, attempt, task, checkout):
    return (attempt.get('SessionID') == identity['session_id'] and attempt.get('Request', {}).get('TaskID') == task
            and attempt.get('Request', {}).get('Checkout') == str(Path(checkout).resolve())
            and attempt.get('Observation', {}).get('Locator') == str(Path(checkout).resolve())
            and bool(re.fullmatch(r'[0-9a-f]{64}', attempt.get('ID', '')))
            and bool(re.fullmatch(r'[0-9a-f]{40}', attempt.get('Observation', {}).get('Commit', '')))
            and bool(re.fullmatch(r'[0-9a-f]{40}', attempt.get('Observation', {}).get('Tree', ''))))


def hook_verdict(records, action_ok):
    entered = [r for r in records if r.get('state') == 'entered']
    completed = [r for r in records if r.get('state') == 'completed']
    return {'callback_entered': bool(entered), 'callback_completed': bool(completed),
            'events_entered': sorted({r['event'] for r in entered}),
            'observed_action': 'completed' if action_ok else 'refused-or-failed',
            'hook_path_exercised': bool(entered),
            'tool_hook_path_exercised': any(r['event'] == 'PreToolUse' for r in entered),
            'blocking': ('nonblocking-observed' if action_ok else 'refusal-observed-cause-unverified') if entered else 'unexercised'}


def closed_notification(value):
    method = value.get('method', '')
    allowed = {'thread/started', 'thread/status/changed', 'hook/started', 'hook/completed', 'hooks/started', 'hooks/completed', 'mcpServer/startupStatus/updated'}
    return method if method in allowed else 'other'


class RPCError(RuntimeError):
    def __init__(self, error):
        super().__init__('host protocol refusal')
        self.code = error.get('code') if isinstance(error.get('code'), int) else None


class RPC:
    def __init__(self, argv, cwd, env, native=False):
        self.argv, self.notifications, self.calls, self.next_id, self.native = argv, set(), [], 0, native
        self.proc = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                     text=True, start_new_session=True)
        self.messages = queue.Queue(maxsize=1024)
        threading.Thread(target=self._read, daemon=True).start()
        try:
            if native:
                self.call('initialize', {'protocolVersion': '2025-06-18', 'capabilities': {},
                          'clientInfo': {'name': 'dacli-boundaries-child', 'version': '2'}})
            else:
                self.call('initialize', {'clientInfo': {'name': 'dacli-boundaries', 'version': '2'},
                                        'capabilities': {'experimentalApi': True}})
                self.proc.stdin.write('{"method":"initialized"}\n'); self.proc.stdin.flush()
        except BaseException:
            self.close()
            raise

    def _read(self):
        while True:
            line = self.proc.stdout.readline(1048577)
            if not line:
                break
            if len(line) > 1048576:
                self.messages.put(ValueError('oversized host response')); break
            try:
                self.messages.put(json.loads(line))
            except ValueError:
                self.messages.put(ValueError('malformed host response')); break
        self.messages.put(EOFError('host closed'))

    def call(self, method, params):
        allowed = {'initialize', 'tools/list', 'tools/call'} if self.native else ALLOWED_METHODS
        if method not in allowed:
            raise ValueError('probe method outside no-model allowlist')
        self.next_id += 1
        self.proc.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': self.next_id, 'method': method, 'params': params}) + '\n')
        self.proc.stdin.flush()
        deadline = time.monotonic() + 15
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise queue.Empty('bounded host response timeout')
            value = self.messages.get(timeout=remaining)
            if isinstance(value, Exception):
                raise value
            if value.get('id') == self.next_id:
                record = {'request_id': self.next_id, 'method': method, 'outcome': 'refused' if 'error' in value else 'response'}
                tool = params.get('tool') if not self.native else params.get('name')
                if tool in {'identity', 'task_get', 'register_attempt', 'unsupported_boundary_fixture_tool'}:
                    record['tool'] = tool
                self.calls.append(record)
                if 'error' in value:
                    raise RPCError(value['error'])
                return value['result']
            self.record_notification(value)

    def record_notification(self, value):
        # A closed enum set stays bounded even under duplicate notification storms.
        self.notifications.add(closed_notification(value))

    def close(self):
        if not self.proc.stdin.closed:
            try:
                self.proc.stdin.close()
            except BrokenPipeError:
                pass
        if self.proc.poll() is None:
            try:
                self.proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                pass
        # start_new_session=True gives this probe its own group. Parent exit does
        # not establish descendant exit; always clean that owned group, bounded.
        try:
            os.killpg(self.proc.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        else:
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                try:
                    os.killpg(self.proc.pid, 0)
                except ProcessLookupError:
                    break
                time.sleep(0.02)
            else:
                try:
                    os.killpg(self.proc.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        if self.proc.poll() is None:
            self.proc.wait(timeout=3)
        self.proc.stdout.close()
        return self.proc.returncode


HOOK = r'''
import hashlib,json,sys,time
from pathlib import Path
mode,log=sys.argv[1:]
q=json.load(sys.stdin)
event=q.get('hook_event_name')
if event not in ['SessionStart','PreToolUse','PostToolUse','SubagentStart','SubagentStop','SessionEnd']: event='other'
def record(state):
 with Path(log).open('a') as f: f.write(json.dumps({'event':event,'state':state,'session_present':isinstance(q.get('session_id'),str),'agent_present':isinstance(q.get('agent_id'),str),'tool_present':isinstance(q.get('tool_name'),str)})+'\n')
record('entered')
if mode=='timeout': time.sleep(4)
if mode=='malformed': print('{broken')
if mode=='error': sys.exit(1)
if mode=='deny' and event=='PreToolUse': print(json.dumps({'hookSpecificOutput':{'hookEventName':'PreToolUse','permissionDecision':'deny','permissionDecisionReason':'Bounded public fixture'}}))
record('completed')
'''


def environment(home):
    env = {k: os.environ[k] for k in ('PATH', 'TMPDIR', 'SystemRoot') if k in os.environ}
    env.update(HOME=str(home.parent), CODEX_HOME=str(home), GIT_CONFIG_NOSYSTEM='1')
    return env


def host_case(codex, source, spec, selection, mode, request_id):
    with tempfile.TemporaryDirectory(prefix='dacli-native-boundary-') as temporary:
        root = Path(temporary); home = root / 'codex'; home.mkdir(mode=0o700)
        script, log = root / 'hook.py', root / 'hooks.jsonl'
        script.write_text(HOOK)
        command = ' '.join(shlex.quote(x) for x in (sys.executable, str(script), mode, str(log)))
        (home / 'hooks.json').write_text(json.dumps({'hooks': {e: [{'hooks': [{'type': 'command', 'command': command, 'timeout': 2}]}] for e in EVENTS}}))
        (home / 'config.toml').write_text('[mcp_servers.dacli]\ncommand = ' + json.dumps(sys.executable)
                + '\nargs = ' + json.dumps([str(source / 'scripts/serve.py'), str(selection)]) + '\n')
        argv = [codex]
        if mode == 'disabled': argv += ['--disable', 'hooks']
        if mode != 'untrusted': argv += ['--dangerously-bypass-hook-trust']
        argv += ['app-server']
        rpc = RPC(argv, spec['workspace'], environment(home))
        started = time.monotonic(); result = {'case': mode, 'action_succeeded': False}
        stage = 'hook-inventory'
        try:
            inventory = rpc.call('hooks/list', {'cwds': [spec['workspace']]})
            # Inventory is configuration evidence only, not callback execution.
            result['hook_inventory_entries'] = len(inventory.get('data', []))
            stage = 'native-tool-discovery'
            status = rpc.call('mcpServerStatus/list', {})['data']
            if len(status) != 1 or status[0].get('toolsError') or 'identity' not in status[0].get('tools', {}):
                raise ValueError('exact native MCP server unavailable')
            stage = 'ephemeral-thread-start'
            thread = rpc.call('thread/start', {'cwd': spec['workspace'], 'ephemeral': True,
                             'approvalPolicy': 'never', 'sandbox': 'read-only'})['thread']['id']
            result['host_thread_correlation_sha256'] = digest(thread.encode())
            def call(tool, args):
                return rpc.call('mcpServer/tool/call', {'threadId': thread, 'server': status[0]['name'],
                                                     'tool': tool, 'arguments': {'schema_version': 1, **args}})
            stage = 'native-identity'
            identity = tool_data(call('identity', {}))
            result['session_id'] = identity['session_id']
            stage = 'canonical-task-read'
            task = tool_data(call('task_get', {'task': spec['task_id']}))['task']
            if task['id'] != spec['task_id']:
                raise ValueError('canonical task mismatch')
            stage = 'native-attempt-registration'
            attempt = tool_data(call('register_attempt', {'request': {'RequestID': request_id,
                    'TaskID': spec['task_id'], 'Checkout': spec['workspace'], 'HostLabel': 'diagnostic-' + mode}}))
            if not attributable(identity, attempt, spec['task_id'], spec['workspace']):
                raise ValueError('tool issuer attribution mismatch')
            result.update(attempt_id=attempt['ID'], base_commit=attempt['Observation']['Commit'],
                          base_tree=attempt['Observation']['Tree'], tool_text_structured_parity=True,
                          authenticated_tool_attribution=True, action_succeeded=True)
            stage = 'unsupported-tool-proxy'
            try:
                response = call('unsupported_boundary_fixture_tool', {})
                result['unsupported_tool'] = 'refused' if response.get('isError') else 'unexpected-success'
            except RPCError as error:
                result['unsupported_tool'] = 'protocol-refused'
                result['unsupported_tool_error_code'] = error.code
            if result['unsupported_tool'] == 'unexpected-success':
                raise ValueError('unsupported tool unexpectedly succeeded')
        except (RPCError, ValueError, EOFError, queue.Empty, BrokenPipeError) as error:
            result['diagnostic_failure'] = diagnostic_failure(error)
            result['failure_stage'] = stage
            result['action_succeeded'] = False
        finally:
            result['host_notifications'] = sorted(set(rpc.notifications))
            result['tool_calls'] = rpc.calls
            result['app_server_exit_code'] = rpc.close()
            result['elapsed_seconds'] = round(time.monotonic() - started, 3)
        records = [json.loads(x) for x in log.read_text().splitlines()] if log.exists() else []
        result['hooks'] = hook_verdict(records, result['action_succeeded'])
        result['model_turn_requested'] = False
        return result


def child_probe(source, spec):
    env = {k: os.environ[k] for k in ('PATH', 'TMPDIR', 'SystemRoot') if k in os.environ}
    rpc = RPC([sys.executable, str(source / 'scripts/serve.py'), spec['child_selection']], spec['workspace'], env, native=True)
    try:
        tools = rpc.call('tools/list', {})['tools']
        if not {'identity', 'task_get', 'register_attempt'} <= {x['name'] for x in tools}:
            raise ValueError('child native tools unavailable')
        def call(tool, args):
            return tool_data(rpc.call('tools/call', {'name': tool, 'arguments': {'schema_version': 1, **args}}))
        identity = call('identity', {})
        if identity['session_id'] != spec['expected_sessions']['child']:
            raise ValueError('child authenticated different session')
        task = call('task_get', {'task': spec['task_id']})['task']
        if task['id'] != spec['task_id']:
            raise ValueError('child canonical task mismatch')
        attempt = call('register_attempt', {'request': {'RequestID': 'boundary-child', 'TaskID': spec['task_id'],
                       'Checkout': spec['workspace'], 'HostLabel': 'actual-native-child-correlation-only'}})
        if not attributable(identity, attempt, spec['task_id'], spec['workspace']):
            raise ValueError('child issuer attribution mismatch')
        result = {'schema': SCHEMA, 'stage': 'child', 'session_id': identity['session_id'], 'attempt_id': attempt['ID'],
                  'own_stdio_authenticated': True, 'text_structured_parity': True, 'tool_calls': rpc.calls, 'host_identity_cryptographically_bound': False,
                  'automatic_child_profile_delivery': False}
    finally:
        code = rpc.close()
    result['stdio_exit_code'] = code
    return result


def host_probe(codex, dacli, source, spec):
    env = {k: os.environ[k] for k in ('PATH', 'TMPDIR', 'SystemRoot') if k in os.environ}
    version = subprocess.check_output([codex, '--version'], env=env, stderr=subprocess.DEVNULL, text=True, timeout=8).strip()
    if version != VERSION:
        raise ValueError('unmeasured Codex version')
    for key in ('selection_a', 'selection_b', 'child_selection'):
        if str(Path(private_json(spec[key])['binary']).resolve(strict=True)) != dacli:
            raise ValueError('selection runtime differs from measured binary')
    cases = [host_case(codex, source, spec, spec['selection_a'], mode, 'boundary-a-' + mode) for mode in CASES]
    independent = host_case(codex, source, spec, spec['selection_b'], 'observe', 'boundary-b')
    shared = host_case(codex, source, spec, spec['selection_a'], 'observe', 'boundary-shared-label')
    baseline = cases[0]
    if not all(x['action_succeeded'] for x in (baseline, independent, shared)):
        raise ValueError('required independent/shared host identity calls failed')
    if baseline['session_id'] != spec['expected_sessions']['a'] or independent['session_id'] != spec['expected_sessions']['b']:
        raise ValueError('actual session does not match owner enrollment reference')
    if identity_boundary(baseline, independent) != 'distinct-session' or identity_boundary(baseline, shared) != 'shared':
        raise ValueError('shared process labels incorrectly promoted into separate authority')
    return {'schema': SCHEMA, 'stage': 'host', 'host_version': version,
            'host_binary_sha256': digest(Path(codex).read_bytes()), 'dacli_binary_sha256': digest(Path(dacli).read_bytes()),
            'source_sha256': digest(Path(__file__).read_bytes()), 'fixture_only': True, 'model_turn_requested': False,
            'worker_credentials_read_by_probe': False, 'cases': cases, 'independent_host': independent,
            'shared_selection_control': shared, 'independent_boundary': 'distinct-session', 'shared_boundary': 'shared',
            'hook_matrix_complete': all(x['hooks']['tool_hook_path_exercised'] for x in cases),
            'reviewer_independence_certified_from_session_ids': False,
            'native_child_exercised': False, 'tool_route': 'diagnostic-mcpServer/tool/call',
            'limits': ['Direct RPC coverage is not model-issued tool hook coverage.',
                       'Caller labels and hook metadata do not authenticate native children.',
                       'Native child stage requires actual root collaboration and separately provisioned own process.',
                       'Unentered callbacks remain unexercised; hook enforcement is not certified.']}

def diagnostic_failure(error):
    """Closed classifications only; never serialize raw host/input diagnostics."""
    reasons = {
        'isolated temporary workspace required': 'fixture-not-temporary',
        'unmeasured Codex version': 'unmeasured-host-version',
        'exact native MCP server unavailable': 'native-server-discovery-failed',
        'native tool refused': 'native-tool-refused',
        'native text/structured parity failure': 'tool-envelope-parity-failed',
        'tool issuer attribution mismatch': 'tool-issuer-mismatch',
        'required independent/shared host identity calls failed': 'required-identity-exercise-failed',
        'unsupported tool unexpectedly succeeded': 'unsupported-tool-succeeded',
    }
    if isinstance(error, RPCError):
        return 'host-protocol-refusal'
    if isinstance(error, queue.Empty):
        return 'host-response-timeout'
    if isinstance(error, subprocess.TimeoutExpired):
        return 'subprocess-timeout'
    if isinstance(error, OSError):
        return 'filesystem-or-process-unavailable'
    return reasons.get(str(error), 'input-or-native-validation-failed')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--fixture', required=True)
    p.add_argument('--stage', required=True, choices=('host', 'child'))
    p.add_argument('--codex')
    p.add_argument('--dacli')
    p.add_argument('--output', required=True)
    args = p.parse_args()
    stage = 'fixture-validation'
    try:
        spec = fixture(args.fixture)
        source = Path(__file__).resolve().parents[1]
        if args.stage == 'host':
            if not args.codex or not args.dacli:
                p.error('host stage requires exact --codex and --dacli executables')
            stage = 'host-probe'
            result = host_probe(str(Path(args.codex).resolve(strict=True)), str(Path(args.dacli).resolve(strict=True)), source, spec)
        else:
            stage = 'child-stdio-probe'
            result = child_probe(source, spec)
        output = Path(args.output)
        with output.open('x') as stream:
            json.dump(result, stream, indent=2); stream.write('\n')
        output.chmod(0o600)
        print(json.dumps({'schema': SCHEMA, 'stage': args.stage, 'receipt_sha256': digest(output.read_bytes())}))
    except (OSError, ValueError, EOFError, RPCError, queue.Empty, subprocess.SubprocessError) as error:
        print(json.dumps({'schema': SCHEMA, 'stage': stage, 'failure': diagnostic_failure(error), 'certified': False}), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
