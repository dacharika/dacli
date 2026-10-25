#!/usr/bin/env python3
"""Qualify Codex PreToolUse callbacks against a local Responses fixture.

This starts the installed app-server, not a coding CLI or a remote model. The
fixture supplies at most one harmless exec_command and one fixed completion.
"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import queue
import re
import shlex
import signal
import socketserver
import stat
import subprocess
import sys
import tempfile
import threading
import time

SCHEMA = 'dacli-codex-hook-matrix/v1'
VERSION = 'codex-cli 0.164.0-alpha.11.1'
CASES = ('observe', 'deny', 'disabled', 'untrusted', 'malformed', 'timeout', 'error', 'unsupported-callback')
METHODS = {'initialize', 'hooks/list', 'thread/start', 'turn/start', 'mcpServerStatus/list'}
NOTIFICATIONS = {'hook/started', 'hook/completed', 'turn/started', 'turn/completed', 'turn/failed'}
HOOK_STATUSES = {'running', 'completed', 'failed', 'blocked', 'stopped'}
TRUST_STATES = {'trusted', 'untrusted', 'managed', 'disabled', 'unknown'}
MAX_SECONDS = 15
MAX_CLEANUP_SECONDS = 8
MAX_TOTAL_SECONDS = MAX_SECONDS + MAX_CLEANUP_SECONDS
MAX_RESPONSES = 2
MAX_NOTIFICATIONS = 64
MAX_HTTP_BYTES = 2_000_000


def sha256(raw):
    return hashlib.sha256(raw).hexdigest()


def closed_method(value):
    method = value.get('method')
    return method if method in NOTIFICATIONS else 'other'


def safe_hook_records(records):
    """Retain only enums and booleans; event IDs, paths, text and arguments drop."""
    result = []
    for record in records:
        run = record.get('run') or {}
        status = run.get('status')
        trust = run.get('trustStatus')
        result.append({
            'event': run.get('eventName') if run.get('eventName') in {
                'preToolUse', 'postToolUse', 'sessionStart', 'sessionEnd', 'other'
            } else 'other',
            'status': status if status in HOOK_STATUSES else 'unknown',
            'trust': trust if trust in TRUST_STATES else 'unknown',
            'handler': run.get('handlerType') if run.get('handlerType') in {'command', 'mcpTool', 'prompt', 'agent'} else 'unknown',
        })
    return result


def safe_inventory_record(hook):
    event = hook.get('eventName')
    trust = hook.get('trustStatus')
    handler = hook.get('handlerType')
    return {
        'event': event if event in {'preToolUse', 'postToolUse', 'sessionStart', 'sessionEnd'} else 'other',
        'trust': trust if trust in TRUST_STATES else 'unknown',
        'handler': handler if handler in {'command', 'mcpTool', 'prompt', 'agent'} else 'unknown',
        'enabled': hook.get('enabled') is True,
    }


def classify_case(name, result):
    """An inventory entry or completed tool is not callback execution evidence."""
    entered = result.get('callback_entered') is True
    terminal = result.get('turn_outcome') == 'completed'
    hooks = result.get('hook_inventory', [])
    if name in {'disabled', 'untrusted'}:
        configured = any(h.get('event') == 'preToolUse' and h.get('handler') == 'command'
                         and h.get('enabled') is True and h.get('trust') == 'untrusted' for h in hooks)
        controls = result.get('thread_controls', {})
        expected_bypass = name == 'disabled'
        return (configured and terminal and not entered and result.get('marker_created') is True
                and controls.get('hooks_disabled') is (name == 'disabled')
                and controls.get('hook_trust_bypassed') is expected_bypass)
    if name == 'deny':
        configured = any(h.get('event') == 'preToolUse' and h.get('handler') == 'command'
                         and h.get('enabled') is True and h.get('trust') == 'untrusted' for h in hooks)
        return (configured and terminal and entered and result.get('marker_created') is False
                and 'blocked' in result.get('hook_statuses', []))
    if name == 'unsupported-callback':
        configured = any(h.get('event') == 'preToolUse' and h.get('handler') == 'mcpTool'
                         and h.get('enabled') is True and h.get('trust') == 'untrusted' for h in hooks)
        return (configured and terminal and result.get('unsupported_scenario') == 'unknown-mcp-callback-tool'
                and result.get('fixture_mcp_server_healthy') is True
                and result.get('fixture_mcp_noop_advertised') is True
                and result.get('requested_mcp_tool_not_advertised') is True
                and result.get('hook_started') is True and result.get('marker_created') is True
                and 'failed' in result.get('hook_statuses', []))
    expected = {'observe': 'completed', 'malformed': 'failed', 'timeout': 'failed', 'error': 'failed'}
    configured = any(h.get('event') == 'preToolUse' and h.get('handler') == 'command'
                     and h.get('enabled') is True and h.get('trust') == 'untrusted' for h in hooks)
    if name == 'timeout':
        elapsed = result.get('hook_durations_ms', [])
        timed_out = any(isinstance(value, int) and 800 <= value < 4000 for value in elapsed)
        return (configured and terminal and entered and result.get('marker_created') is True and timed_out
                and expected[name] in result.get('hook_statuses', []))
    return (configured and terminal and entered and result.get('marker_created') is True
            and expected.get(name) in result.get('hook_statuses', []))


def qualify_case(name, result):
    return (classify_case(name, result)
            and result.get('host_deadline_met') is True
            and result.get('cleanup_deadline_met') is True
            and result.get('total_deadline_met') is True
            and result.get('cleanup_complete') is True
            and not result.get('response_limit_exceeded')
            and not result.get('notification_overflow')
            and not result.get('fixture_write_failed')
            and 'transport_outcome' not in result)


def hook_program(mode, receipt):
    """Create a vetted callback script. It records no host IDs, paths, or inputs."""
    return '''import json,sys,time
q=json.load(sys.stdin)
def safe(value, allowed): return value if value in allowed else "other"
event=safe(q.get("hook_event_name"), {"PreToolUse","preToolUse","SessionStart","sessionStart"})
with open(sys.argv[1], "a", encoding="utf-8") as out:
 out.write(json.dumps({"event":event,"state":"entered","tool_name":safe(q.get("tool_name"), {"Bash","exec_command"})})+"\\n")
mode=sys.argv[2]
if mode=="timeout": time.sleep(4)
elif mode=="error": sys.exit(1)
elif mode=="malformed": print("{malformed")
elif mode=="deny": print(json.dumps({"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"fixture policy"}}))
'''


class FixtureServer(http.server.ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, marker):
        self.marker = marker
        self.request_count = 0
        self.tool_names = []
        self.overflow = False
        self.write_failed = False
        super().__init__(('127.0.0.1', 0), self.handler())

    def handler(self):
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                if self.client_address[0] != '127.0.0.1':
                    self.send_error(403)
                    return
                fixture.request_count += 1
                if fixture.request_count > MAX_RESPONSES:
                    fixture.overflow = True
                    self.send_response(429)
                    self.send_header('Connection', 'close')
                    self.end_headers()
                    return
                try:
                    size = int(self.headers.get('Content-Length', '-1'))
                except ValueError:
                    size = -1
                if size < 0 or size > MAX_HTTP_BYTES:
                    self.send_error(413)
                    return
                request_body = self.rfile.read(size)
                if len(request_body) != size:
                    self.send_error(400)
                    return
                body = json.loads(request_body)
                fixture.tool_names = [
                    name for name in (x.get('name') for x in body.get('tools', []))
                    if name in {'exec_command', 'write_stdin', 'request_user_input', 'view_image'}
                ]
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.send_header('Cache-Control', 'no-cache')
                self.end_headers()
                if fixture.request_count == 1:
                    command = f"printf marker > {shlex.quote(str(fixture.marker))}"
                    arguments = json.dumps({'cmd': command})
                    output = [{
                        'id': 'fixture-call-item', 'type': 'function_call', 'status': 'completed',
                        'call_id': 'fixture-call', 'name': 'exec_command', 'arguments': arguments,
                    }]
                else:
                    output = [{
                        'id': 'fixture-final', 'type': 'message', 'status': 'completed', 'role': 'assistant',
                        'content': [{'type': 'output_text', 'text': 'fixture complete', 'annotations': []}],
                    }]
                response = {
                    'id': f'fixture-response-{fixture.request_count}', 'object': 'response',
                    'status': 'completed', 'model': 'fixture', 'output': output,
                }
                if fixture.request_count == 1:
                    events = [
                        {'type': 'response.created', 'response': {**response, 'status': 'in_progress', 'output': []}},
                        {'type': 'response.output_item.added', 'output_index': 0, 'item': {**output[0], 'status': 'in_progress', 'arguments': ''}},
                        {'type': 'response.function_call_arguments.delta', 'item_id': output[0]['id'], 'output_index': 0, 'delta': arguments},
                        {'type': 'response.function_call_arguments.done', 'item_id': output[0]['id'], 'output_index': 0, 'arguments': arguments},
                        {'type': 'response.output_item.done', 'output_index': 0, 'item': output[0]},
                        {'type': 'response.completed', 'response': response},
                    ]
                else:
                    events = [{'type': 'response.completed', 'response': response}]
                for event in events:
                    frame = f"event: {event['type']}\ndata: {json.dumps(event)}\n\n"
                    try:
                        self.wfile.write(frame.encode())
                        self.wfile.flush()
                    except (BrokenPipeError, ConnectionResetError):
                        fixture.write_failed = True
                        return

            def log_message(self, *_args):
                return

        return Handler


class RPC:
    def __init__(self, process, deadline):
        self.process = process
        self.deadline = deadline
        self.next_id = 0
        self.messages = queue.Queue(maxsize=128)
        self.read_overflow = False
        self.notifications = []
        self.notification_overflow = False
        self.terminal_key = None
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()

    def read(self):
        def enqueue(value):
            try:
                self.messages.put_nowait(value)
            except queue.Full:
                self.read_overflow = True

        while True:
            line = self.process.stdout.readline(1_048_577)
            if not line:
                break
            if len(line) > 1_048_576:
                enqueue(ValueError('oversized host response'))
                return
            try:
                enqueue(json.loads(line))
            except ValueError:
                enqueue(ValueError('malformed host response'))
                return
        enqueue(EOFError('host closed'))

    def call(self, method, params):
        if method not in METHODS:
            raise ValueError('host method outside fixture allowlist')
        self.next_id += 1
        request_id = self.next_id
        self.process.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': request_id, 'method': method, 'params': params}) + '\n')
        self.process.stdin.flush()
        deadline = self.deadline
        while time.monotonic() < deadline:
            if self.read_overflow:
                raise RuntimeError('host notification queue overflow')
            message = self.messages.get(timeout=max(0.01, deadline - time.monotonic()))
            if isinstance(message, Exception):
                raise message
            if message.get('id') == request_id:
                if 'error' in message:
                    raise RuntimeError('host protocol refusal')
                return message['result']
            self.record(message)
        raise TimeoutError('host request timed out')

    def record(self, message):
        method = closed_method(message)
        if method == 'other':
            return
        params = message.get('params') or {}
        item = {'method': method}
        if method in {'hook/started', 'hook/completed'}:
            run = params.get('run') or {}
            item.update(event=run.get('eventName') if run.get('eventName') in {
                'preToolUse', 'postToolUse', 'sessionStart', 'sessionEnd'
            } else 'other', status=run.get('status') if run.get('status') in HOOK_STATUSES else 'unknown')
            duration = run.get('durationMs')
            item['duration_ms'] = duration if isinstance(duration, int) and 0 <= duration <= MAX_SECONDS * 1000 else None
            item['entry_kinds'] = sorted({entry.get('kind') for entry in run.get('entries', [])
                                          if entry.get('kind') in {'warning', 'stop', 'feedback', 'context', 'error'}})
        elif method in {'turn/completed', 'turn/failed'}:
            turn = params.get('turn') or {}
            item['turn_status'] = turn.get('status') if turn.get('status') in {'completed', 'failed', 'inProgress', 'interrupted'} else 'unknown'
            thread_id = params.get('threadId') if isinstance(params.get('threadId'), str) else None
            turn_id = turn.get('id') if isinstance(turn.get('id'), str) else None
            self.terminal_key = (thread_id, turn_id, item['turn_status'])
        if len(self.notifications) < MAX_NOTIFICATIONS:
            self.notifications.append(item)
        else:
            self.notification_overflow = True

    def terminal_turn(self, thread_id, turn_id):
        deadline = self.deadline
        if self.terminal_key and self.terminal_key[0] == thread_id and self.terminal_key[1] == turn_id:
            return self.terminal_key[2] if self.terminal_key[2] == 'completed' else 'failed'
        while time.monotonic() < deadline:
            if self.read_overflow:
                raise RuntimeError('host notification queue overflow')
            message = self.messages.get(timeout=max(0.01, deadline - time.monotonic()))
            if isinstance(message, Exception):
                raise message
            self.record(message)
            if message.get('method') not in {'turn/completed', 'turn/failed'}:
                continue
            params = message.get('params') or {}
            turn = params.get('turn') or {}
            if params.get('threadId') != thread_id or turn.get('id') != turn_id:
                continue
            if message['method'] == 'turn/completed' and turn.get('status') == 'completed':
                return 'completed'
            return 'failed'
        return 'unknown'


def cleanup_owned_group(process, grace=1.0):
    """Terminate the owned group even when its original parent already exited."""
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return 'already-gone'
    deadline = time.monotonic() + grace
    while time.monotonic() < deadline:
        process.poll()
        try:
            os.killpg(process.pid, 0)
        except ProcessLookupError:
            return 'terminated'
        time.sleep(0.02)
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        return 'terminated'
    deadline = time.monotonic() + grace
    while time.monotonic() < deadline:
        process.poll()
        try:
            os.killpg(process.pid, 0)
        except ProcessLookupError:
            return 'killed'
        time.sleep(0.02)
    return 'cleanup-unknown'


def private_output(path):
    path = Path(path)
    if not path.is_absolute() or path.is_symlink() or path.exists():
        raise ValueError('new absolute output path required')
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    return path


def run_case(codex, case):
    case_started = time.monotonic()
    case_deadline = case_started + MAX_SECONDS
    result = {'case': case, 'callback_entered': False, 'turn_outcome': 'unknown', 'marker_created': False,
              'hook_statuses': [], 'hook_inventory': [], 'responses': 0, 'tool_names': [], 'qualified': False}
    result['unsupported_scenario'] = 'unknown-mcp-callback-tool' if case == 'unsupported-callback' else None
    result['thread_controls'] = {'hooks_disabled': case == 'disabled',
                                 'hook_trust_bypassed': case != 'untrusted'}
    with tempfile.TemporaryDirectory(prefix='dacli-hook-matrix-') as directory:
        root = Path(directory)
        home = root / 'codex-home'
        home.mkdir(mode=0o700)
        isolated_tmp = root / 'tmp'
        isolated_tmp.mkdir(mode=0o700)
        marker = root / 'marker'
        callback_receipt = root / 'callback.jsonl'
        script = root / 'hook.py'
        script.write_text(hook_program(case, callback_receipt), encoding='utf-8')
        mcp_script = root / 'mcp_fixture.py'
        mcp_receipt = root / 'mcp.jsonl'
        mcp_script.write_text('''import json,sys
receipt=sys.argv[1]
for line in sys.stdin:
 try: request=json.loads(line)
 except ValueError: continue
 method=request.get("method")
 if method in {"initialize","tools/list","tools/call","notifications/initialized"}:
  params=request.get("params") or {}
  with open(receipt,"a",encoding="utf-8") as out:
   out.write(json.dumps({"method":method,"noop":params.get("name")=="noop","other_tool":method=="tools/call" and params.get("name")!="noop"})+"\\n")
 if method=="notifications/initialized": continue
 if method=="initialize":
  params=request.get("params") or {}
  result={"protocolVersion":params.get("protocolVersion","2025-06-18"),"capabilities":{"tools":{}},"serverInfo":{"name":"hook-matrix-fixture","version":"1"}}
 elif method=="tools/list": result={"tools":[{"name":"noop","description":"bounded fixture no-op","inputSchema":{"type":"object","properties":{}}}]}
 elif method=="tools/call":
  params=request.get("params") or {}
  if params.get("name")=="noop": result={"content":[{"type":"text","text":"fixture no-op"}]}
  else: result={"isError":True,"content":[{"type":"text","text":"unknown fixture tool"}]}
 else: continue
 if "id" in request: print(json.dumps({"jsonrpc":"2.0","id":request["id"],"result":result}),flush=True)
''', encoding='utf-8')
        if case == 'unsupported-callback':
            hook_config = {'type': 'mcp_tool', 'server': 'hook_fixture', 'tool': 'missing-fixture-tool',
                           'input': {}, 'timeout': 2}
        else:
            hook_config = {'type': 'command',
                           'command': f'{shlex.quote(sys.executable)} {shlex.quote(str(script))} {shlex.quote(str(callback_receipt))} {case}',
                           'timeout': 1 if case == 'timeout' else 3}
        hooks = {'hooks': {'PreToolUse': [{'matcher': '.*', 'hooks': [hook_config]}]}}
        (home / 'hooks.json').write_text(json.dumps(hooks), encoding='utf-8')
        config = '[features]\nhooks = true\n\n[model_providers.fixture]\nname = "fixture"\nwire_api = "responses"\nrequires_openai_auth = false\nsupports_websockets = false\nmodel = "fixture"\n'
        server = FixtureServer(marker)
        config += f'base_url = "http://127.0.0.1:{server.server_address[1]}/v1"\n'
        if case == 'unsupported-callback':
            config += ('\n[mcp_servers.hook_fixture]\ncommand = ' + json.dumps(sys.executable)
                       + '\nargs = [' + json.dumps(str(mcp_script)) + ', ' + json.dumps(str(mcp_receipt))
                       + ']\nstartup_timeout_sec = 3\ntool_timeout_sec = 3\n')
        (home / 'config.toml').write_text(config, encoding='utf-8')
        threading.Thread(target=server.serve_forever, daemon=True).start()
        env = {key: os.environ[key] for key in ('PATH', 'TMPDIR') if key in os.environ}
        env.update({'HOME': str(root), 'CODEX_HOME': str(home), 'TMPDIR': str(isolated_tmp)})
        command = [codex, '--enable', 'hooks', 'app-server', '--listen', 'stdio://']
        process = subprocess.Popen(command, cwd=root, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                    stderr=subprocess.DEVNULL, text=True, start_new_session=True)
        rpc = RPC(process, case_deadline)
        try:
            rpc.call('initialize', {'protocolVersion': '2025-06-18', 'capabilities': {'experimentalApi': True},
                                    'clientInfo': {'name': 'dacli-hook-matrix', 'version': '1'}})
            inventory = rpc.call('hooks/list', {'cwds': [str(root)]})
            for workspace in inventory.get('data', []):
                result['hook_inventory'].extend(safe_inventory_record(x) for x in workspace.get('hooks', []))
            thread_config = {'features': {'hooks': False}} if case == 'disabled' else {}
            if case != 'untrusted':
                thread_config['bypass_hook_trust'] = True
            thread = rpc.call('thread/start', {'cwd': str(root), 'ephemeral': True, 'modelProvider': 'fixture',
                               'model': 'fixture', 'approvalPolicy': 'never', 'sandbox': 'workspace-write',
                               'runtimeWorkspaceRoots': [str(root)], 'allowProviderModelFallback': False,
                               'config': thread_config})
            thread_id = thread.get('thread', {}).get('id')
            if not isinstance(thread_id, str):
                raise ValueError('thread start omitted id')
            if case == 'unsupported-callback':
                status = rpc.call('mcpServerStatus/list', {'threadId': thread_id,
                                      'serverName': 'hook_fixture', 'detail': 'full'})
                servers = status.get('data', [])
                fixture_server = next((x for x in servers if x.get('name') == 'hook_fixture'), {})
                tools = fixture_server.get('tools', {})
                result['fixture_mcp_noop_advertised'] = isinstance(tools, dict) and 'noop' in tools
                result['requested_mcp_tool_not_advertised'] = isinstance(tools, dict) and 'missing-fixture-tool' not in tools
                result['fixture_mcp_server_healthy'] = (
                    fixture_server.get('runtimeStatus') == 'connected'
                    and fixture_server.get('toolsError') is None
                    and result['fixture_mcp_noop_advertised'])
                if not result['fixture_mcp_server_healthy']:
                    result['transport_outcome'] = 'fixture-mcp-server-unhealthy'
            if result.get('transport_outcome') != 'fixture-mcp-server-unhealthy':
                turn = rpc.call('turn/start', {'threadId': thread_id,
                                        'input': [{'type': 'text', 'text': 'Run the one supplied fixture tool, then finish.'}],
                                        'model': 'fixture'})
                turn_id = turn.get('turn', {}).get('id')
                if not isinstance(turn_id, str):
                    raise ValueError('turn start omitted id')
                result['turn_outcome'] = rpc.terminal_turn(thread_id, turn_id)
        except (TimeoutError, queue.Empty):
            result['transport_outcome'] = 'timeout'
        except EOFError:
            result['transport_outcome'] = 'host-closed'
        except (OSError, RuntimeError, ValueError, queue.Empty):
            result['transport_outcome'] = 'host-error'
        finally:
            host_elapsed = time.monotonic() - case_started
            result['host_elapsed_seconds'] = round(host_elapsed, 3)
            result['host_deadline_met'] = host_elapsed <= MAX_SECONDS
            cleanup_started = time.monotonic()
            try:
                process.stdin.close()
            except (BrokenPipeError, OSError):
                pass
            result['cleanup_outcome'] = cleanup_owned_group(process)
            if process.poll() is None:
                try:
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    result['cleanup_outcome'] = 'cleanup-unknown'
            result['notifications'] = [x for x in rpc.notifications if x['method'] in NOTIFICATIONS]
            result['notification_overflow'] = rpc.notification_overflow
            server.shutdown()
            server.server_close()
            cleanup_elapsed = time.monotonic() - cleanup_started
            result['cleanup_elapsed_seconds'] = round(cleanup_elapsed, 3)
            result['cleanup_deadline_met'] = cleanup_elapsed <= MAX_CLEANUP_SECONDS
        result['responses'] = server.request_count
        result['response_limit_exceeded'] = server.overflow or server.request_count > MAX_RESPONSES
        result['fixture_write_failed'] = server.write_failed
        mcp_records = []
        if case == 'unsupported-callback' and mcp_receipt.exists():
            for line in mcp_receipt.read_text(encoding='utf-8').splitlines()[:8]:
                try:
                    value = json.loads(line)
                except ValueError:
                    continue
                method = value.get('method')
                if method in {'initialize', 'tools/list', 'tools/call', 'notifications/initialized'}:
                    mcp_records.append({'method': method, 'noop': value.get('noop') is True,
                                        'other_tool': value.get('other_tool') is True})
        result['mcp_fixture_calls'] = mcp_records
        result['fixture_mcp_initialized'] = any(x['method'] == 'initialize' for x in mcp_records)
        result['fixture_mcp_catalog_seen'] = any(x['method'] == 'tools/list' for x in mcp_records)
        result['fixture_mcp_unknown_tool_called'] = any(x['method'] == 'tools/call' and x['other_tool'] for x in mcp_records)
        result['tool_names'] = server.tool_names
        result['marker_created'] = marker.exists()
        records = []
        if callback_receipt.exists():
            for line in callback_receipt.read_text(encoding='utf-8').splitlines()[:4]:
                try:
                    value = json.loads(line)
                except ValueError:
                    continue
                records.append({'event': value.get('event') if value.get('event') in {'PreToolUse', 'preToolUse'} else 'other',
                                'entered': value.get('state') == 'entered',
                                'tool_name': value.get('tool_name') if value.get('tool_name') in {'Bash', 'exec_command'} else 'other'})
        result['callback_records'] = records
        result['hook_statuses'] = sorted({x['status'] for x in result['notifications']
                                          if x['method'] == 'hook/completed' and x.get('event') == 'preToolUse'})
        result['hook_durations_ms'] = [x['duration_ms'] for x in result['notifications']
                                       if x['method'] == 'hook/completed' and x.get('event') == 'preToolUse'
                                       and x.get('duration_ms') is not None]
        result['hook_error_entry_kinds'] = sorted({kind for x in result['notifications']
                                                   if x['method'] == 'hook/completed' and x.get('event') == 'preToolUse'
                                                   for kind in x.get('entry_kinds', [])})
        result['hook_started'] = any(x['method'] == 'hook/started' and x.get('event') == 'preToolUse' for x in result['notifications'])
        result['callback_entered'] = any(x['entered'] and x['event'] in {'PreToolUse', 'preToolUse'} for x in records)
        if case == 'unsupported-callback':
            result['unsupported_scenario'] = 'unknown-mcp-callback-tool'
        total_elapsed = time.monotonic() - case_started
        result['total_elapsed_seconds'] = round(total_elapsed, 3)
        result['total_deadline_met'] = total_elapsed <= MAX_TOTAL_SECONDS
        result['cleanup_complete'] = result.get('cleanup_outcome') in {'already-gone', 'terminated', 'killed'}
        result['qualified'] = qualify_case(case, result)
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--codex', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--case', choices=CASES)
    args = parser.parse_args()
    output = private_output(args.output)
    codex = Path(args.codex).resolve(strict=True)
    if not codex.is_file():
        raise ValueError('Codex binary required')
    env = {key: os.environ[key] for key in ('PATH', 'TMPDIR') if key in os.environ}
    version = subprocess.check_output([str(codex), '--version'], env=env, stderr=subprocess.DEVNULL,
                                      text=True, timeout=5).strip()
    if version != VERSION:
        raise ValueError('unmeasured Codex version')
    cases = [run_case(str(codex), case) for case in ([args.case] if args.case else CASES)]
    result = {'schema': SCHEMA, 'host_version': version, 'host_binary_sha256': sha256(codex.read_bytes()),
              'source_sha256': sha256(Path(__file__).read_bytes()), 'fixture_only': True,
              'external_inference': False, 'credentials_loaded': False, 'max_responses_per_case': MAX_RESPONSES,
              'max_seconds_per_case': MAX_SECONDS, 'max_cleanup_seconds_per_case': MAX_CLEANUP_SECONDS,
              'max_total_seconds_per_case': MAX_TOTAL_SECONDS, 'cases': cases,
              'matrix_qualified': len(cases) == len(CASES) and all(x['qualified'] for x in cases)}
    fd = os.open(output, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, stat.S_IRUSR | stat.S_IWUSR)
    with os.fdopen(fd, 'w', encoding='utf-8') as stream:
        json.dump(result, stream, indent=2)
        stream.write('\n')
    print(json.dumps({'schema': SCHEMA, 'cases': len(cases), 'matrix_qualified': result['matrix_qualified'],
                      'receipt_sha256': sha256(output.read_bytes())}))
    return 0 if result['matrix_qualified'] else 1


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (OSError, ValueError, subprocess.SubprocessError):
        raise SystemExit(2)
