#!/usr/bin/env python3
"""Opt-in public-lab native action probe. Never reads the supplied auth file."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile

CASES = ('observe', 'deny', 'disabled', 'untrusted', 'malformed', 'timeout', 'error')


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def feature_declarations(raw):
    names = ('hooks','plugins','multi_agent','use_agent_identity','executed_tool_call_metadata')
    result = {}
    for line in raw.splitlines():
        fields = line.split()
        if len(fields) >= 2 and fields[0] in names and fields[-1] in ('true','false'):
            result[fields[0]] = fields[-1] == 'true'
    return result


def error_category(value):
    # Classification is intentionally coarse; original diagnostics never escape.
    text = json.dumps(value).lower()
    for category, needles in (
        ('ephemeral_tasks_unavailable', ('ephemeral tasks cannot create inspectable background tasks',)),
        ('permission_denied', ('permission denied', 'not permitted', 'denied by', 'approval')),
        ('timeout', ('timed out', 'timeout')),
        ('unavailable', ('not found', 'unavailable', 'unknown tool', 'not supported')),
        ('authentication', ('unauthorized', 'authentication', 'credential')),
        ('transport', ('connection', 'transport', 'broken pipe', 'handshake')),
        ('malformed', ('invalid json', 'parse error', 'malformed')),
    ):
        if any(n in text for n in needles): return category
    return 'unspecified'


def summarize(raw):
    """Project native stdout to a closed receipt; never retain model text."""
    types, keys = {}, set()
    items = []
    completed = False
    for line in raw.splitlines():
        try:
            obj = json.loads(line)
        except (ValueError, TypeError):
            continue
        if not isinstance(obj, dict):
            continue
        # Unknown types/keys are omitted rather than allowing arbitrary text out.
        kind = obj.get('type')
        if kind in ('thread.started', 'turn.started', 'turn.completed', 'turn.failed',
                    'item.started', 'item.updated', 'item.completed', 'error'):
            types[kind] = types.get(kind, 0) + 1
        keys.update(k for k in obj if k in ('type', 'thread_id', 'item', 'usage', 'error', 'message'))
        completed |= kind == 'turn.completed'
        item = obj.get('item')
        if isinstance(item, dict) and kind in ('item.started', 'item.updated', 'item.completed'):
            category = item.get('type')
            allowed = ('command_execution', 'file_change', 'mcp_tool_call', 'collab_tool_call',
                       'web_search', 'agent_message', 'reasoning', 'error', 'todo_list', 'collab_agent_tool_call')
            record = {'category': category if category in allowed else 'other', 'event':kind}
            status = item.get('status')
            record['status'] = status if status in ('in_progress','completed','failed','interrupted') else 'unknown'
            tool = item.get('tool')
            known = ('spawn_agent','send_input','wait','close_agent','resume_agent','context_probe')
            if isinstance(tool,str) and tool in known: record['tool'] = tool
            if isinstance(item.get('exit_code'),int): record['exit_success'] = item['exit_code'] == 0
            if item.get('error') is not None: record['error_category'] = error_category(item['error'])
            elif category == 'error': record['error_category'] = error_category(item.get('message'))
            items.append(record)
        if kind in ('turn.failed','error'):
            items.append({'category':'error','event':kind,'error_category':error_category(obj.get('error',obj.get('message')))})
    return {'event_counts': types, 'event_keys': sorted(keys), 'turn_completed': completed, 'items': items}


# These reviewed helpers live outside the model's working directory. Their logs
# contain only closed enums, field presence and correlation hashes.
HELPER = r'''
import hashlib,json,sys,time
from pathlib import Path
mode,log=sys.argv[1:3]
def record(x):
 with Path(log).open('a') as f: f.write(json.dumps(x)+'\n')
def hashed(v): return hashlib.sha256(v.encode()).hexdigest() if isinstance(v,str) else None
if mode=='mcp':
 record({'source':'mcp-lifecycle','event':'started'})
 for line in sys.stdin:
  try: q=json.loads(line)
  except ValueError: continue
  method=q.get('method'); result={}
  if method in ['initialize','tools/list','tools/call','ping']: record({'source':'mcp-lifecycle','event':method})
  if method=='initialize': result={'protocolVersion':'2025-06-18','capabilities':{'tools':{}},'serverInfo':{'name':'public-lab','version':'1'}}
  elif method=='tools/list': result={'tools':[{'name':'context_probe','description':'Read-only public lab context observation; labels are not identity.','annotations':{'readOnlyHint':True,'destructiveHint':False,'idempotentHint':True,'openWorldHint':False},'inputSchema':{'type':'object','properties':{'tag':{'type':'string','enum':['root','child']}},'required':['tag'],'additionalProperties':False}}]}
  elif method=='tools/call':
   p=q.get('params') or {}; args=p.get('arguments') or {}; meta=p.get('_meta') or {}
   tag=args.get('tag')
   if p.get('name')!='context_probe' or tag not in ('root','child'):
    result={'isError':True,'content':[{'type':'text','text':'invalid public lab request'}]}
   else:
    allowed=['openai/session','openai/subject','progressToken']
    record({'source':'mcp','tag':tag,'meta_fields':[k for k in allowed if k in meta],'session_hash':hashed(meta.get('openai/session'))})
    result={'content':[{'type':'text','text':'Public lab context observed. This is not authenticated identity.'}]}
  elif method=='ping': result={}
  else:
   if 'id' in q: print(json.dumps({'jsonrpc':'2.0','id':q['id'],'error':{'code':-32601,'message':'unsupported'}}),flush=True)
   continue
  if 'id' in q: print(json.dumps({'jsonrpc':'2.0','id':q['id'],'result':result}),flush=True)
else:
 q=json.load(sys.stdin)
 event=q.get('hook_event_name'); tool=q.get('tool_name')
 known_tools=['shell','shell_command','exec_command','apply_patch','mcp__lab__context_probe','spawn_agent','wait_agent','functions.exec_command','functions.shell_command','functions.apply_patch','functions.spawn_agent','functions.send_input','functions.wait','functions.close_agent','functions.resume_agent','functions.mcp__lab__context_probe','mcp__lab.context_probe']
 record({'source':'hook','event':event if event in ['PreToolUse','PostToolUse','SubagentStart','SubagentStop','SessionStart','Stop'] else 'other','tool':tool if tool in known_tools else 'other','fields':[k for k in ['session_id','agent_id','turn_id','tool_use_id'] if k in q],'session_hash':hashed(q.get('session_id')),'agent_hash':hashed(q.get('agent_id'))})
 if event=='PreToolUse':
  if mode=='deny':
   print(json.dumps({'hookSpecificOutput':{'hookEventName':'PreToolUse','permissionDecision':'deny','permissionDecisionReason':'Public lab denial'}}))
  elif mode=='malformed': print('{broken')
  elif mode=='timeout': time.sleep(15)
  elif mode=='error': sys.exit(1)
'''


def execution_command(binary, work, mode, persist_session=False):
    cmd = [binary,'-a','never','exec','--json','--skip-git-repo-check','--sandbox','workspace-write','-C',str(work)]
    if not persist_session: cmd.append('--ephemeral')
    if mode != 'untrusted': cmd.append('--dangerously-bypass-hook-trust')
    if mode == 'disabled': cmd.extend(['--disable','hooks'])
    return cmd


def run_case(binary, auth, mode, persist_session=False):
    with tempfile.TemporaryDirectory(prefix='dacli-native-actions-') as tmp:
        base = Path(tmp); home = base/'home'; work = base/'work'
        home.mkdir(); work.mkdir()
        # Reference only: no credential bytes pass through this process.
        (home/'auth.json').symlink_to(auth)
        helper = base/'helper.py'; helper.write_text(HELPER)
        log = base/'safe.jsonl'
        import shlex
        command = ' '.join(shlex.quote(x) for x in (sys.executable, str(helper), mode, str(log)))
        hooks = {'hooks': {event: [{'hooks': [{'type':'command','command':command,'timeout':2}]}]
                           for event in ('SessionStart','PreToolUse','PostToolUse','SubagentStart','SubagentStop','Stop')}}
        (home/'hooks.json').write_text(json.dumps(hooks))
        # JSON strings are TOML-compatible for these generated POSIX paths.
        (home/'config.toml').write_text('[mcp_servers.lab]\ncommand = '+json.dumps(sys.executable)+'\nargs = '+json.dumps([str(helper),'mcp',str(log)])+'\n')
        env = {k:os.environ[k] for k in ('PATH','TMPDIR','SystemRoot') if k in os.environ}
        env.update(HOME=str(base), CODEX_HOME=str(home))
        try:
            features = subprocess.run([binary,'features','list'],cwd=work,env=env,
                stdin=subprocess.DEVNULL,capture_output=True,text=True,timeout=8)
            advertised = feature_declarations(features.stdout) if features.returncode == 0 else {}
        except subprocess.TimeoutExpired:
            advertised = {}
        cmd = execution_command(binary, work, mode, persist_session)
        if mode == 'observe':
            prompt = ('This is a public isolated measurement lab. Do not inspect credentials, configuration, history, or any paths outside this working directory. '
                      'Use your shell tool to write exactly ROOT to root-shell.txt. Use apply_patch to create root-patch.txt containing ROOT. '
                      'Call lab context_probe with tag root. Explicitly delegate to one native child using the host-provided delegation tool if exposed (its name may differ from spawn_agent); ask that child to use shell to write CHILD to child-shell.txt, '
                      'apply_patch to create child-patch.txt containing CHILD, and lab context_probe with tag child. Wait for it. '
                      'Do not simulate unavailable tools or use substitutes. Do not launch another CLI. Finish briefly.')
        else:
            prompt = ('This is a public isolated measurement lab. Do not inspect credentials, configuration, history, or paths outside this working directory. '
                      'Attempt exactly one shell command: printf LAB > marker.txt . If denied or failed, do not retry or use another tool. Finish briefly.')
        cmd.append(prompt)
        p = subprocess.Popen(cmd,cwd=work,env=env,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
        killed=False
        try:
            out,_ = p.communicate(timeout=90)
        except subprocess.TimeoutExpired:
            killed=True
            try: os.killpg(p.pid,signal.SIGKILL)
            except ProcessLookupError: pass
            try:
                out,_=p.communicate(timeout=3)
            except subprocess.TimeoutExpired as exc:
                # A detached descendant may retain a pipe. Do not wait forever.
                out=exc.output or b''
                p.stdout.close(); p.stderr.close()
                p.kill(); p.wait(timeout=3)
        finally:
            # Reap surviving hook/server descendants even after a normal CLI exit.
            try: os.killpg(p.pid,signal.SIGKILL)
            except ProcessLookupError: pass
        receipt = summarize(out.decode(errors='replace'))
        receipt.update(case=mode,exit_code=p.returncode,runner_killed_at_bound=killed,advertised_features=advertised,session_persistence_requested=persist_session)
        receipt['observations'] = [json.loads(x) for x in log.read_text().splitlines()] if log.exists() else []
        receipt['markers']={}
        for name,expected in [('marker.txt',b'LAB'),('root-shell.txt',b'ROOT'),('root-patch.txt',b'ROOT\n'),('child-shell.txt',b'CHILD'),('child-patch.txt',b'CHILD\n')]:
            path=work/name
            # Avoid reading symlink targets or large/unexpected content.
            exists=path.is_file() and not path.is_symlink()
            exact=False
            if exists and path.stat().st_size <= 64:
                exact=path.read_bytes()==expected
            receipt['markers'][name]={'present':exists,'expected_bytes_match':exact,
                'expected_sha256':hashlib.sha256(expected).hexdigest()}
        return receipt


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--codex',required=True); p.add_argument('--expected-version',required=True)
    p.add_argument('--auth-file',required=True,help='Operator-provided existing auth.json reference; never printed or read by runner')
    p.add_argument('--case',choices=CASES,required=True)
    p.add_argument('--persist-session',action='store_true',help='Omit --ephemeral in the disposable home; never read generated rollouts')
    a=p.parse_args(); binary=str(Path(a.codex).resolve()); auth=Path(a.auth_file).resolve()
    version=subprocess.check_output([binary,'--version'],stderr=subprocess.DEVNULL,text=True,timeout=8).strip()
    if version!=a.expected_version: p.error('host version mismatch')
    if not auth.is_file(): p.error('operator auth reference unavailable')
    result=run_case(binary,auth,a.case,a.persist_session)
    print(json.dumps({'schema':'codex-native-actions/v1','host_version':version,'client_login_reference_supplied':True,
      'dacli_principal_authenticated':False,'beta_certified':False,'measurement':result},indent=2))

if __name__=='__main__': main()
