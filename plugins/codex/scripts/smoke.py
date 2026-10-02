#!/usr/bin/env python3
"""Install and exercise the plugin in a disposable home; no model or user auth.

Only a deliberately minted test fixture credential is used. No native governance
session enrollment is claimed. Codex app-server performs actual MCP discovery
and a harmless whoami call on the installed package.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import tempfile
import threading
import time

from package import build


def command(argv, cwd, env, decode=True):
    p = subprocess.run(argv, cwd=cwd, env=env, capture_output=True, text=True, timeout=30)
    if p.returncode:
        raise RuntimeError('command failed: ' + ' '.join(argv[:3]))
    return json.loads(p.stdout) if decode else None


class Host:
    def __init__(self, binary, cwd, env):
        self.proc = subprocess.Popen([binary, 'app-server'], cwd=cwd, env=env,
                                     stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=subprocess.DEVNULL, text=True, start_new_session=True)
        self.messages = queue.Queue()
        self.next_id = 0
        threading.Thread(target=self.read, daemon=True).start()
        try:
            self.call('initialize', {'clientInfo': {'name': 'dacli-plugin-smoke', 'version': '1'},
                                    'capabilities': {'experimentalApi': True}})
            self.proc.stdin.write('{"method":"initialized"}\n'); self.proc.stdin.flush()
        except BaseException:
            self.close()
            raise

    def read(self):
        for line in self.proc.stdout:
            try:
                self.messages.put(json.loads(line))
            except ValueError:
                pass

    def call(self, method, params):
        self.next_id += 1
        self.proc.stdin.write(json.dumps({'id': self.next_id, 'method': method, 'params': params}) + '\n')
        self.proc.stdin.flush()
        deadline = time.monotonic() + 30
        while True:
            result = self.messages.get(timeout=max(0, deadline - time.monotonic()))
            if result.get('id') == self.next_id:
                if 'error' in result:
                    raise RuntimeError('host refused ' + method)
                return result['result']

    def close(self):
        try:
            os.killpg(self.proc.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(self.proc.pid, signal.SIGKILL); self.proc.wait(timeout=5)
        self.proc.stdin.close(); self.proc.stdout.close()


def smoke(codex, dacli):
    version = subprocess.check_output([codex, '--version'], text=True, timeout=10).strip()
    with tempfile.TemporaryDirectory(prefix='dacli-plugin-smoke-') as tmp:
        base = Path(tmp).resolve(); home = base / 'os-home'; home.mkdir()
        codex_home = base / 'codex-home'; codex_home.mkdir()
        workspace = base / 'workspace'; workspace.mkdir()
        # No user config, workspace credentials, model login or MCP servers copied.
        env = {k: os.environ[k] for k in ('PATH', 'TMPDIR', 'SystemRoot') if k in os.environ}
        env.update(HOME=str(home), CODEX_HOME=str(codex_home))
        command([dacli, 'init', '--name', 'public-plugin-fixture'], workspace, env, decode=False)
        token = 'dacli-public-fixture-only-credential'
        agent = workspace / '.dacli/agents/a-plugin-fixture.md'
        digest = 'sha256:' + hashlib.sha256(token.encode()).hexdigest()
        agent.write_text('---\nid: a-plugin-fixture\nkind: agent\nrole: fixture-reader\ngrant: ro\ntoken_hash: ' + digest + '\n---\n# Public plugin test fixture\n')
        credential = base / 'fixture.token'; credential.write_text(token); credential.chmod(0o600)
        archive = build(base / 'package.zip')
        import zipfile
        with zipfile.ZipFile(archive) as packed:
            packed.extractall(base / 'marketplace')
        market = base / 'marketplace'
        catalogue = market / '.agents/plugins/marketplace.json'; catalogue.parent.mkdir(parents=True)
        catalogue.write_text(json.dumps({'name': 'dacli-smoke', 'plugins': [{
            'name': 'dacli-governance', 'source': {'source': 'local', 'path': './dacli-governance'},
            'policy': {'installation': 'AVAILABLE', 'authentication': 'ON_USE'}, 'category': 'Productivity'}]}))
        command([codex, 'plugin', 'marketplace', 'add', str(market), '--json'], workspace, env)
        installed = command([codex, 'plugin', 'add', 'dacli-governance@dacli-smoke', '--json'], workspace, env)
        server = Host(codex, workspace, env)
        try:
            missing = server.call('mcpServerStatus/list', {})['data']
            assert len(missing) == 1 and missing[0]['toolsError'] and not missing[0]['tools']
        finally:
            server.close()
        connection = home / '.config/dacli/plugin/connection.json'; connection.parent.mkdir(parents=True)
        connection.write_text(json.dumps({'workspace': str(workspace), 'binary': dacli, 'credential_file': str(credential)}))
        connection.chmod(0o600)
        server = Host(codex, workspace, env)
        try:
            status = server.call('mcpServerStatus/list', {})['data']
            assert len(status) == 1 and not status[0]['toolsError'], status[0].get('toolsError')
            tools = sorted(status[0]['tools'])
            assert 'whoami' in tools and 'get_context' in tools
            skills = server.call('skills/list', {'cwds': [str(workspace)], 'forceReload': True})
            assert 'governance-workflow' in json.dumps(skills)
            thread = server.call('thread/start', {'cwd': str(workspace), 'ephemeral': True,
                                                'approvalPolicy': 'never', 'sandbox': 'read-only'})
            tid = thread['thread']['id']
            identity = server.call('mcpServer/tool/call', {'threadId': tid, 'server': status[0]['name'],
                                                         'tool': 'whoami', 'arguments': {'schema_version': 1}})
            content = identity.get('content', [])
            assert not identity.get('isError')
            assert content == [{'type': 'text', 'text': 'a-plugin-fixture (grant: ro, role: fixture-reader)\n'}], 'fixture identity mismatch'
        finally:
            server.close()
        # Verify the installed bootstrap directly refuses an empty credential.
        credential.write_text('')
        result = subprocess.run(['python3', str(Path(installed['installedPath']) / 'scripts/serve.py')],
                                env=env, cwd=workspace, capture_output=True, text=True, timeout=10)
        assert result.returncode == 3 and not result.stdout
        credential.write_text('unrecognized-public-fixture')
        invalid = subprocess.run(['python3', str(Path(installed['installedPath']) / 'scripts/serve.py')],
                                 env=env, cwd=workspace, input='', capture_output=True, text=True, timeout=10)
        assert invalid.returncode == 3 and not invalid.stdout
        assert 'unrecognized-public-fixture' not in invalid.stderr
        return {'schema': 'dacli-plugin-smoke/v1', 'host_version': version,
                'installed_version': installed['version'], 'native_host_tool_discovery': True,
                'native_host_skill_discovery': True, 'tool_names': tools,
                'native_host_whoami': {'id': 'a-plugin-fixture', 'grant': 'ro'},
                'missing_connection_tools_unavailable': True, 'empty_credential_exit': result.returncode,
                'invalid_credential_exit': invalid.returncode,
                'fixture_only': True, 'model_turn_requested': False,
                'native_governance_session_enrollment': False, 'assurance': 'cooperative/advisory'}


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--codex', required=True); p.add_argument('--dacli', required=True)
    a = p.parse_args()
    print(json.dumps(smoke(str(Path(a.codex).resolve()), str(Path(a.dacli).resolve())), indent=2))
