#!/usr/bin/env python3
"""Bounded real-host startup probe; never imports credentials or private history."""
import argparse
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import time

MODES = ['observe', 'second-session', 'disabled', 'untrusted', 'malformed-output', 'timeout',
         'malformed-config', 'plugin-observe', 'plugin-disabled']
EVENTS = ['SessionStart', 'PreToolUse', 'PostToolUse', 'SubagentStart', 'SubagentStop', 'Stop', 'SessionEnd']


def run(cmd, env, cwd, seconds=8):
    started = time.monotonic()
    p = subprocess.Popen(cmd, env=env, cwd=cwd, stdin=subprocess.DEVNULL,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    timed_out = False
    try:
        out, err = p.communicate(timeout=seconds)
    except subprocess.TimeoutExpired:
        timed_out = True
        os.killpg(p.pid, signal.SIGKILL)
        out, err = p.communicate()
    return p.returncode, out.decode(errors='replace'), err.decode(errors='replace'), timed_out, round(time.monotonic()-started, 2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--codex', required=True, help='Exact installed executable path')
    parser.add_argument('--expected-version', required=True)
    parser.add_argument('--case', choices=MODES, help='Run one named case instead of the full matrix')
    args = parser.parse_args()
    binary = str(Path(args.codex).resolve())
    actual = subprocess.check_output([binary, '--version'], stderr=subprocess.DEVNULL, text=True, timeout=8).strip()
    if actual != args.expected_version:
        parser.error('host version mismatch')
    result = {'schema': 'codex-governance-probe/v1', 'host_version': actual,
              'simulated': False, 'credentials_loaded': False, 'cases': []}
    with tempfile.TemporaryDirectory(prefix='dacli-codex-probe-') as tmp:
        root = Path(tmp)
        session_ids = []
        for mode in MODES:
            if args.case and args.case != mode:
                continue
            base = root / mode
            home = base / 'home'
            work = base / 'work'
            home.mkdir(parents=True)
            work.mkdir()
            log = base / 'events.jsonl'
            script = base / 'hook.py'
            script.write_text('''import json,sys,time
from pathlib import Path
value=json.load(sys.stdin)
with Path(sys.argv[1]).open('a') as f:
 f.write(json.dumps({k:value[k] for k in ['hook_event_name','session_id','turn_id','agent_id','tool_name','tool_use_id'] if k in value})+'\\n')
if sys.argv[2]=='timeout': time.sleep(5)
Path(sys.argv[1]+'.complete').touch()
if sys.argv[2]=='malformed-output': print('{broken')
''')
            command = ' '.join(shlex.quote(s) for s in [sys.executable, str(script), str(log), mode])
            hooks = {'hooks': {event: [{'hooks': [{'type': 'command', 'command': command, 'timeout': 2}]}] for event in EVENTS}}
            (home / 'config.toml').write_text('')
            (home / 'hooks.json').write_text('{broken' if mode == 'malformed-config' else json.dumps(hooks))
            env = {k: os.environ[k] for k in ['PATH', 'TMPDIR', 'SystemRoot'] if k in os.environ}
            env.update(HOME=str(base), CODEX_HOME=str(home))
            installation = None
            if mode.startswith('plugin-'):
                (home / 'hooks.json').unlink()
                plugin = work / 'plugins' / 'probe'
                (plugin / 'hooks').mkdir(parents=True)
                (plugin / 'plugin.json').write_text(json.dumps({'name': 'governance-probe', 'version': '1.0.0',
                    'description': 'Bounded startup attribution probe'}))
                (plugin / 'hooks' / 'hooks.json').write_text(json.dumps(hooks))
                market = work / '.agents' / 'plugins'
                market.mkdir(parents=True)
                (market / 'marketplace.json').write_text(json.dumps({'name': 'probe-local', 'plugins': [{
                    'name': 'governance-probe', 'source': {'source': 'local', 'path': './plugins/probe'},
                    'policy': {'installation': 'AVAILABLE', 'authentication': 'ON_INSTALL'}, 'category': 'Productivity'}]}))
                installation = []
                for argv in [['plugin', 'marketplace', 'add', str(work), '--json'],
                             ['plugin', 'add', 'governance-probe@probe-local', '--json']]:
                    installed = run([binary] + argv, env, work)
                    installation.append({'exit_code': installed[0], 'bounded': installed[3]})
            cmd = [binary, '-a', 'never', 'exec', '--json', '--ephemeral', '--skip-git-repo-check', '-C', str(work)]
            if mode != 'untrusted':
                cmd.append('--dangerously-bypass-hook-trust')
            if mode == 'plugin-disabled':
                cmd.extend(['-c', 'plugins."governance-probe@probe-local".enabled=false'])
            if mode == 'disabled':
                cmd.extend(['--disable', 'hooks'])
            cmd.append('Reply OK without using tools.')
            code, out, err, bounded, elapsed = run(cmd, env, work)
            stream = []
            for line in out.splitlines():
                try:
                    stream.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
            records = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
            ids = [v['thread_id'] for v in stream if v.get('type') == 'thread.started']
            hook_ids = [v.get('session_id') for v in records]
            session_ids.extend(ids)
            result['cases'].append({'mode': mode, 'status': 'observed', 'installation': installation, 'exit_code': code,
                'runner_killed_at_bound': bounded, 'elapsed_seconds': elapsed,
                'stream_event_types': sorted(set(v.get('type', '') for v in stream)),
                'hook_completed': Path(str(log)+'.complete').exists(),
                'hook_events': [v.get('hook_event_name') for v in records],
                'hook_field_names': sorted(set(k for v in records for k in v)),
                'hook_session_matches_thread': bool(ids and hook_ids) and all(v == ids[0] for v in hook_ids),
                'network_lookup_failure': 'failed to lookup address' in out+err,
                'hook_failure_reported': any('hook' in (v.get('message', '') + str(v.get('item', {}))).lower() and
                    any(s in (v.get('message', '') + str(v.get('item', {}))).lower() for s in ['fail', 'timed out', 'invalid']) for v in stream),
                'turn_started': any(v.get('type') == 'turn.started' for v in stream)})
        result['distinct_started_sessions'] = len(set(session_ids))
    result['unavailable'] = {
        'authenticated_principal': 'No credentials loaded; a thread ID does not authenticate a principal.',
        'native_subagents': 'No successful model turn; no delegation observed.',
        'tool_call_attribution': 'No model tool calls observed.',
        'independent_review': 'Separate thread IDs are not independent authenticated reviewers.'}
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
