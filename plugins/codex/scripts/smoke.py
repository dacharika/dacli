#!/usr/bin/env python3
"""Exercise real native governance through an isolated Codex app-server; no model turn."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import queue
import shlex
import signal
import subprocess
import sys
import tempfile
import threading
import time

from package import FILES, build


def command(argv, cwd, env, decode=True):
    p = subprocess.run(argv, cwd=cwd, env=env, capture_output=True, text=True, timeout=30)
    if p.returncode:
        raise RuntimeError('command failed: ' + ' '.join(argv[:3]))
    return json.loads(p.stdout) if decode else None


class HostRPCError(RuntimeError):
    def __init__(self, method, error):
        super().__init__('host refused ' + method)
        # Match the actual native protocol refusal forwarded by Codex. A
        # transport failure or host approval denial is not revocation evidence.
        self.native_refusal = error.get('code') == -32003 and error.get('message') == 'native session authority denied'


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
                    raise HostRPCError(method, result['error'])
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


def smoke(codex, dacli, schema5_dacli=None):
    from datetime import datetime, timedelta, timezone
    import zipfile
    with tempfile.TemporaryDirectory(prefix='dacli-plugin-native-') as tmp:
        base = Path(tmp).resolve(); home = base / 'os-home'; home.mkdir()
        codex_home = base / 'codex-home'; codex_home.mkdir()
        workspace = base / 'workspace'; workspace.mkdir()
        private = base / 'private'; private.mkdir(mode=0o700)
        env = {k: os.environ[k] for k in ('PATH', 'TMPDIR', 'SystemRoot') if k in os.environ}
        env.update(HOME=str(home), CODEX_HOME=str(codex_home), GIT_CONFIG_NOSYSTEM='1')
        version = subprocess.check_output([codex, '--version'], env=env, cwd=workspace, stderr=subprocess.DEVNULL, text=True, timeout=10).strip()
        if version != 'codex-cli 0.159.2':
            raise RuntimeError('this measured harness requires Codex 0.159.2')
        def run(args):
            return command(args, workspace, env, decode=False)
        def git(*args):
            run(['git', '-c', 'user.name=Public Fixture', '-c', 'user.email=fixture@example.invalid', *args])
        git('init', '-b', 'main')
        run([dacli, 'init', '--name', 'public-native-plugin-fixture'])
        run([dacli, 'project', 'add', 'Public fixture', '--slug', 'fixture'])
        run([dacli, 'task', 'add', 'Implement public marker', '--project', 'fixture', '--accept', 'Public marker committed'])
        (workspace / 'src').mkdir(); (workspace / 'src/marker.txt').write_text('initial public fixture\n')
        git('add', '.'); git('commit', '-m', 'Public fixture base')
        owner = private / 'owner.json'
        owner.write_text(json.dumps({'schema_version':1, 'workspace':str(workspace),
            'authority':str(private / 'authority.json'), 'installation_id':'public-plugin-fixture',
            'owner_credential_file':str(private / 'owner.token')})); owner.chmod(0o600)
        producer = schema5_dacli or dacli
        run([producer, 'native', 'bootstrap', '--config', str(owner)])
        profile = private / 'session.json'
        run([producer, 'native', 'enroll', '--config', str(owner), '--task', 'fixture/001',
             '--connection', str(profile), '--credential', str(private / 'session.token'), '--path', 'src'])
        session_id = json.loads(profile.read_text())['session_id']
        migration = {}
        if schema5_dacli:
            authority = private / 'authority.json'
            old_bytes = authority.read_bytes(); old_state = json.loads(old_bytes)
            assert old_state['Version'] == 5, 'producer did not create schema 5'
            profile_bytes = profile.read_bytes()
            owner_bytes = (private / 'owner.token').read_bytes()
            session_bytes = (private / 'session.token').read_bytes()
            def refused(argv, before):
                result = subprocess.run(argv, cwd=workspace, env=env, input='',
                    capture_output=True, text=True, timeout=30)
                assert result.returncode == 3 and not result.stdout, 'expected policy refusal'
                assert authority.read_bytes() == before, 'refusal changed authority'
                for secret in (owner_bytes, session_bytes):
                    assert secret.strip().decode() not in result.stderr, 'credential in diagnostic'
            refused([dacli, 'native', 'serve', '--connection', str(profile)], old_bytes)
            (private / 'owner.token').write_bytes(b'public-forged-owner-fixture')
            refused([dacli, 'native', 'upgrade', '--config', str(owner)], old_bytes)
            (private / 'owner.token').write_bytes(owner_bytes)
            run([dacli, 'native', 'upgrade', '--config', str(owner)])
            upgraded = authority.read_bytes(); new_state = json.loads(upgraded)
            assert new_state['Version'] == 6
            assert all(new_state[key] == value for key, value in old_state.items() if key != 'Version')
            assert profile.read_bytes() == profile_bytes
            assert (private / 'owner.token').read_bytes() == owner_bytes
            assert (private / 'session.token').read_bytes() == session_bytes
            refused([dacli, 'native', 'upgrade', '--config', str(owner)], upgraded)
            refused([schema5_dacli, 'native', 'revoke', '--config', str(owner), '--session', session_id], upgraded)
            migration = {'schema5_producer_binary_sha256':hashlib.sha256(Path(schema5_dacli).read_bytes()).hexdigest(),
                'genuine_schema5_profile_migrated_to_6':True, 'pre_migration_new_server_exit':3,
                'forged_owner_upgrade_exit':3, 'already_target_upgrade_exit':3,
                'old_schema5_writer_exit':3, 'migration_preserved_existing_profile_and_credentials':True}

        archive = build(base / 'package.zip')
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
        selection = home / '.config/dacli/plugin/connection.json'; selection.parent.mkdir(parents=True)
        selection.write_text(json.dumps({'schema_version':1, 'binary':dacli, 'connection_file':str(profile)}))
        selection.chmod(0o600)
        server = Host(codex, workspace, env)
        try:
            status = server.call('mcpServerStatus/list', {})['data']
            assert len(status) == 1 and not status[0]['toolsError'], 'native discovery unavailable'
            names = sorted(status[0]['tools'])
            assert installed['version'] == '0.4.0'
            assert names == sorted(['identity','workspace_status','task_list','task_get','register_attempt',
                                    'acquire_lease','renew_lease','release_lease','publish_revision','submit_evidence'])
            skills = server.call('skills/list', {'cwds':[str(workspace)], 'forceReload':True})
            assert 'governance-workflow' in json.dumps(skills)
            thread = server.call('thread/start', {'cwd':str(workspace), 'ephemeral':True,
                                                 'approvalPolicy':'never', 'sandbox':'read-only'})
            tid = thread['thread']['id']
            def raw_call(tool, args):
                return server.call('mcpServer/tool/call', {'threadId':tid, 'server':status[0]['name'],
                    'tool':tool, 'arguments':dict(schema_version=1, **args)})
            def call(tool, **args):
                response = raw_call(tool, args)
                assert not response.get('isError'), 'native tool refused: ' + tool
                # Actual app-server versions may relay only the textual content.
                result = response.get('structuredContent') or json.loads(response['content'][0]['text'])
                assert result['ok'] and result['assurance'] == 'cooperative-session', 'native result refused: ' + tool
                return result['data']
            identity = call('identity'); assert identity['session_id'] == session_id
            call('workspace_status', project='fixture')
            tasks = call('task_list', project='fixture', status='open')
            task = call('task_get', task='fixture/001')
            task_id = task['task']['id']
            assert any(t['id'] == task_id for t in tasks['tasks'])
            attempt = call('register_attempt', request={'RequestID':'public-attempt', 'TaskID':task_id,
                'Checkout':str(workspace), 'HostLabel':'public-untrusted-label'})
            expiry = (datetime.now(timezone.utc)+timedelta(minutes=10)).isoformat().replace('+00:00','Z')
            lease = call('acquire_lease', request={'OperationKey':'public-acquire', 'AttemptID':attempt['ID'],
                'Resources':[{'Kind':'path','Path':'src'}], 'ExpiresAt':expiry})
            (workspace / 'src/marker.txt').write_text('committed public native fixture\n')
            git('add', 'src/marker.txt'); git('commit', '-m', 'Public native fixture revision')
            revision = call('publish_revision', request={'OperationKey':'public-publish','AttemptID':attempt['ID'],
                'LeaseID':lease['ID'],'ExpectedFence':lease['Fence'],'ExpectedRevision':0})
            for key, ref in (('Commit', 'HEAD'), ('Tree', 'HEAD^{tree}')):
                actual = subprocess.check_output(['git', 'rev-parse', ref], cwd=workspace, env=env, text=True, timeout=10).strip()
                assert revision['Observation'][key] == actual, 'published revision mismatch'
            assert revision['Observation']['Commit'] != attempt['Observation']['Commit']
            evidence = call('submit_evidence', request={'OperationKey':'public-evidence', 'AttemptID':attempt['ID'],
                'LeaseID':lease['ID'],'ExpectedFence':lease['Fence'],'RevisionID':revision['ID'],
                'CheckID':'public-marker-check','ExitCode':0,
                'OutputDigest':'sha256:'+hashlib.sha256(b'public self-report').hexdigest()})
            assert evidence['Provenance'] == 'self-reported' and not evidence['GrantID']
            assert evidence['IssuerSession'] == session_id and evidence['Revision']['ID'] == revision['ID']
            call('release_lease', request={'OperationKey':'public-release', 'AttemptID':attempt['ID'],
                'LeaseID':lease['ID'], 'ExpectedFence':lease['Fence']})
            # Real owner command while the same server/thread remain live.
            run([dacli, 'native', 'revoke', '--config', str(owner), '--session', session_id])
            revoked = False
            try:
                response = raw_call('identity', {})
                revoked = bool(response.get('isError')) and HostRPCError('identity', response).native_refusal
            except HostRPCError as error:
                revoked = error.native_refusal
            assert revoked, 'exact native revocation refusal was not confirmed'
        finally:
            server.close()
        # A separate owner-enrolled worker and runner exercise the optional
        # tools through the actual Codex client. No model turn or production
        # credentials are involved, and the original profile remains retained.
        async_profile = private / 'async-session.json'
        run([dacli, 'native', 'enroll', '--config', str(owner), '--task', 'fixture/001',
             '--connection', str(async_profile), '--credential', str(private / 'async-session.token'),
             '--path', 'src', '--verification'])
        run([dacli, 'task', 'claim', 'fixture/001'])
        started = private / 'public-check-started'
        checks = private / 'checks.json'
        checks.write_text(json.dumps({'schema_version':1,'checks':[
            {'id':'public-complete','command':'printf public-configured-check','deadline':'10s'},
            {'id':'public-cancel','command':'printf started > '+shlex.quote(str(started))+'; sleep 60','deadline':'1m'}]}))
        checks.chmod(0o600)
        runner = private / 'runner.json'
        run([dacli, 'native', 'verifier', 'add', '--config', str(owner), '--task', 'fixture/001',
             '--checks-file', str(checks), '--out', str(runner), '--credential', str(private / 'runner.token')])
        selection.write_text(json.dumps({'schema_version':1,'binary':dacli,
            'connection_file':str(async_profile),'runner_config_file':str(runner)}))
        server = Host(codex, workspace, env)
        try:
            status = server.call('mcpServerStatus/list', {})['data']
            assert len(status) == 1 and not status[0]['toolsError']
            async_names = sorted(status[0]['tools'])
            assert async_names == sorted(names+['request_verification','get_verification_status','cancel_verification'])
            thread = server.call('thread/start', {'cwd':str(workspace),'ephemeral':True,
                                                 'approvalPolicy':'never','sandbox':'read-only'})
            tid = thread['thread']['id']
            # raw_call/call use the newly selected live server/thread above.
            attempt = call('register_attempt', request={'RequestID':'public-async-attempt','TaskID':task_id,
                'Checkout':str(workspace),'HostLabel':'public-native-host'})
            expiry = (datetime.now(timezone.utc)+timedelta(minutes=10)).isoformat().replace('+00:00','Z')
            lease = call('acquire_lease', request={'OperationKey':'public-async-acquire','AttemptID':attempt['ID'],
                'Resources':[{'Kind':'path','Path':'src'}],'ExpiresAt':expiry})
            revision = call('publish_revision', request={'OperationKey':'public-async-publish','AttemptID':attempt['ID'],
                'LeaseID':lease['ID'],'ExpectedFence':lease['Fence'],'ExpectedRevision':0})
            request = {'operation_key':'public-configured-complete','attempt_id':attempt['ID'],
                'lease_id':lease['ID'],'revision_id':revision['ID'],'check_id':'public-complete',
                'expected_fence':lease['Fence']}
            submitted = call('request_verification', request=request)
            assert submitted['operation_key'] == request['operation_key']
            deadline = time.monotonic()+15
            while True:
                completed = call('get_verification_status', request=request)
                if completed['state'] == 'completed':
                    break
                assert time.monotonic() < deadline, 'configured check did not complete'
                assert completed['state'] in ('admitting','reserved')
                time.sleep(0.02)
            trusted = completed['evidence']
            assert trusted['Provenance'] == 'trusted-local-runner' and trusted['ExitCode'] == 0
            assert trusted['Revision']['ID'] == revision['ID']
            replay = call('request_verification', request=request)
            assert replay['state'] == 'completed' and replay['evidence']['ID'] == trusted['ID']
            # Review uses a distinct owner-enrolled root and process. Derive its
            # required descriptor from a separate one-check owner runner profile;
            # the original successful issuer/evidence remain independently current.
            review_checks = private / 'review-checks.json'
            review_checks.write_text(json.dumps({'schema_version':1,'checks':[
                {'id':'public-complete','command':'printf public-configured-check','deadline':'10s'}]}))
            review_checks.chmod(0o600)
            review_runner = private / 'review-runner.json'
            run([dacli,'native','verifier','add','--config',str(owner),'--task','fixture/001',
                 '--checks-file',str(review_checks),'--out',str(review_runner),
                 '--credential',str(private / 'review-runner.token')])
            review_profile = private / 'review-profile.json'
            review_connection = private / 'review-session.json'
            review_policy = private / 'review-policy.json'
            worker_id = json.loads(async_profile.read_text())['session_id']
            run([dacli,'native','reviewer','add','--config',str(owner),'--task','fixture/001',
                 '--worker-session',worker_id,'--runner-config',str(review_runner),
                 '--policy-revision','public-review-policy-v1','--policy-out',str(review_policy),
                 '--review-config',str(review_profile),'--connection',str(review_connection),
                 '--credential',str(private / 'review-session.token')])
            reviewer_id = json.loads(review_connection.read_text())['session_id']
            assert reviewer_id != worker_id and reviewer_id != json.loads(runner.read_text())['runner_session_id']
            reviewer_selection = {'schema_version':1,'binary':dacli,'connection_file':str(review_connection),
                                  'review_config_file':str(review_profile)}
            selection.write_text(json.dumps(reviewer_selection))
            review_server = Host(codex,workspace,env)
            try:
                review_status = review_server.call('mcpServerStatus/list',{})['data']
                assert len(review_status)==1 and not review_status[0]['toolsError']
                review_names = sorted(review_status[0]['tools'])
                assert review_names == sorted(['identity','workspace_status','task_list','task_get',
                                               'propose_review','evaluate_review'])
                review_thread = review_server.call('thread/start',{'cwd':str(workspace),'ephemeral':True,
                    'approvalPolicy':'never','sandbox':'read-only'})['thread']['id']
                def review_raw(tool, args):
                    return review_server.call('mcpServer/tool/call',{'threadId':review_thread,
                        'server':review_status[0]['name'],'tool':tool,'arguments':dict(schema_version=1,**args)})
                def review_call(tool, **args):
                    response=review_raw(tool,args);assert not response.get('isError'),'review tool refused: '+tool
                    result=response.get('structuredContent') or json.loads(response['content'][0]['text'])
                    assert result['ok'],'review result refused: '+tool
                    return result['data']
                principal = review_call('identity')
                assert principal['session_id']==reviewer_id
                assert set(principal['scope']['Actions'])=={'read','review.propose','review.evaluate'}
                before_review_status = review_call('task_get',task='fixture/001')['task']['status']
                review_request={'operation_key':'public-review-approval','attempt_id':attempt['ID'],
                    'lease_id':lease['ID'],'revision_id':revision['ID'],'expected_fence':lease['Fence'],
                    'outcome':'approve','rationale':'Public exact revision passed owner-named check.',
                    'evidence_ids':[trusted['ID']]}
                proposal=review_call('propose_review',request=review_request)
                eligible=review_call('evaluate_review',review_id=proposal['ID'])
                assert eligible['Eligible'] and eligible['Reason']=='eligible'
                assert eligible['Assurance']=='cooperative-credential-separation'
                assert review_call('propose_review',request=review_request)['ID']==proposal['ID']
                forged=review_raw('propose_review',{'request':dict(review_request,grant_id='public-forged-grant')})
                assert forged.get('isError'),'caller grant override was accepted'
                original_policy=review_policy.read_bytes()
                changed=json.loads(original_policy);changed['revision']='public-review-policy-v2'
                review_policy.write_text(json.dumps(changed))
                stale=review_call('evaluate_review',review_id=proposal['ID'])
                assert not stale['Eligible'] and stale['Reason']=='checklist-or-policy-changed'
                assert review_call('propose_review',request=review_request)['ID']==proposal['ID']
                review_policy.write_bytes(original_policy)
                assert review_call('evaluate_review',review_id=proposal['ID'])['Eligible']
                changes=review_call('propose_review',request=dict(review_request,
                    operation_key='public-review-request-changes',outcome='request-changes'))
                changes_result=review_call('evaluate_review',review_id=changes['ID'])
                assert not changes_result['Eligible'] and changes_result['Reason']=='changes-requested'
                assert review_call('task_get',task='fixture/001')['task']['status']==before_review_status
            finally:
                review_server.close()
            selection.write_text(json.dumps({'schema_version':1,'binary':dacli,
                'connection_file':str(async_profile),'runner_config_file':str(runner)}))
            cancelling = dict(request,operation_key='public-configured-cancel',check_id='public-cancel')
            call('request_verification', request=cancelling)
            deadline = time.monotonic()+15
            while not started.exists():
                assert time.monotonic() < deadline, 'configured cancellation check did not start'
                time.sleep(0.02)
            observed = time.monotonic()
            call('identity'); call('task_get', task='fixture/001')
            assert time.monotonic()-observed < 10, 'unrelated calls blocked behind check'
            cancel_result = call('cancel_verification', request=cancelling)
            assert cancel_result['state'] in ('cancellation_requested','reconciliation_required')
            deadline = time.monotonic()+15
            while True:
                cancelled = call('get_verification_status', request=cancelling)
                if cancelled['state'] == 'reconciliation_required':
                    break
                assert time.monotonic() < deadline, 'cancellation did not finish'
                assert cancelled['state'] == 'cancellation_requested'
                time.sleep(0.02)
            assert cancelled['evidence'] is None
            call('release_lease', request={'OperationKey':'public-async-release','AttemptID':attempt['ID'],
                'LeaseID':lease['ID'],'ExpectedFence':lease['Fence']})
        finally:
            server.close()
        # A historical proposal remains replayable after release, but fresh
        # review eligibility must no longer authorize this worker revision.
        selection.write_text(json.dumps(reviewer_selection))
        review_server=Host(codex,workspace,env)
        try:
            review_status=review_server.call('mcpServerStatus/list',{})['data']
            review_thread=review_server.call('thread/start',{'cwd':str(workspace),'ephemeral':True,
                'approvalPolicy':'never','sandbox':'read-only'})['thread']['id']
            released_review=review_call('evaluate_review',review_id=proposal['ID'])
            assert not released_review['Eligible'] and released_review['Reason']=='worker-authority-invalid'
            assert review_call('propose_review',request=review_request)['ID']==proposal['ID']
            run([dacli,'native','revoke','--config',str(owner),'--session',reviewer_id])
            try:
                response=review_raw('identity',{})
                reviewer_revoked=bool(response.get('isError')) and HostRPCError('identity',response).native_refusal
            except HostRPCError as error:
                reviewer_revoked=error.native_refusal
            assert reviewer_revoked,'reviewer revocation was not authenticated'
        finally:
            review_server.close()
        selection.write_text(json.dumps({'schema_version':1,'binary':dacli,
            'connection_file':str(async_profile),'runner_config_file':str(runner)}))
        # Restart observes the durable incomplete reservation, never reruns it.
        server = Host(codex, workspace, env)
        try:
            status = server.call('mcpServerStatus/list', {})['data']
            thread = server.call('thread/start', {'cwd':str(workspace),'ephemeral':True,
                                                 'approvalPolicy':'never','sandbox':'read-only'})
            tid = thread['thread']['id']
            recovered = call('get_verification_status', request=cancelling)
            assert recovered['state'] == 'reconciliation_required' and recovered['evidence'] is None
            recovered_complete = call('get_verification_status', request=request)
            assert recovered_complete['state'] == 'completed' and recovered_complete['evidence']['ID'] == trusted['ID']
        finally:
            server.close()
        selection.write_text(json.dumps({'schema_version':1,'binary':dacli,'connection_file':str(profile)}))
        profile.rename(private / 'retained-session.json')
        missing_profile = subprocess.run(['python3', str(Path(installed['installedPath']) / 'scripts/serve.py')],
            env=env, cwd=workspace, input='', capture_output=True, text=True, timeout=10)
        assert missing_profile.returncode == 3 and not missing_profile.stdout
        selection.write_text('{invalid')
        invalid = subprocess.run(['python3', str(Path(installed['installedPath']) / 'scripts/serve.py')],
            env=env, cwd=workspace, input='', capture_output=True, text=True, timeout=10)
        assert invalid.returncode == 3 and not invalid.stdout
        return {'schema':'dacli-plugin-native-smoke/v1', 'host_version':version,
            'installed_version':installed['version'],
            'observed_at':datetime.now(timezone.utc).isoformat(),
            'dacli_binary_sha256':hashlib.sha256(Path(dacli).read_bytes()).hexdigest(),
            'plugin_source_sha256':{name:hashlib.sha256((Path(__file__).resolve().parents[1] / name).read_bytes()).hexdigest()
                                    for name in FILES if not name.startswith('receipts/')},
            'smoke_script_sha256':hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            'native_host_tool_discovery':True,
            'native_host_skill_discovery':True, 'tool_names':names,
            'configured_async_tool_names':async_names,
            'configured_async_completion_provenance':trusted['Provenance'],
            'configured_async_exact_replay':True,
            'configured_async_unrelated_reads_responsive':True,
            'configured_async_cancel_has_no_evidence':True,
            'configured_async_restart_preserves_unknown_and_completed':True,
            'configured_review_tool_names':review_names,
            'configured_review_separate_credentials':True,
            'configured_review_exact_trusted_evidence_eligible':True,
            'configured_review_exact_replay_is_historical':True,
            'configured_review_fresh_policy_and_released_lease_refuse':True,
            'configured_review_caller_grant_override_refused':True,
            'configured_review_request_changes_ineligible':True,
            'configured_review_preserves_task_status':True,
            'configured_review_live_reviewer_revocation_refused':True,
            'native_owner_bootstrap_and_enrollment':True, 'native_identity_matches_enrollment':True,
            'canonical_task_reads':True, 'external_git_attempt_registered':True,
            'lease_acquired_and_released':True, 'committed_revision_published':True,
            'revision_bound_evidence_provenance':evidence['Provenance'],
            'same_live_host_refuses_after_owner_revocation':True,
            'missing_connection_tools_unavailable':True, 'missing_native_profile_exit':missing_profile.returncode,
            'malformed_selection_exit':invalid.returncode,
            'fixture_only':True,'model_turn_requested':False,'dacli_spawn_used':False,
            'shared_mcp_connection_shares_session_identity':True,'assurance':'cooperative-session', **migration}


ORCHESTRATION_TOOLS = {
    'controller': {'get_dispatch_routing', 'plan_swarm', 'start_loop', 'get_loop_status', 'step_loop', 'reconcile_loop',
                   'stop_loop', 'cancel_dispatch', 'get_dispatch_status', 'get_dispatch_lifecycle'},
    'bridge': {'correlate_dispatch', 'observe_dispatch', 'acknowledge_cancellation',
               'get_dispatch_status', 'get_dispatch_lifecycle'},
    'worker': {'register_attempt', 'acquire_lease', 'renew_lease', 'release_lease',
               'publish_revision', 'submit_evidence', 'acknowledge_dispatch',
               'get_dispatch_status', 'get_dispatch_lifecycle'},
}
ORCHESTRATION_READS = {'identity', 'workspace_status', 'task_list', 'task_get',
                       'get_policy_status', 'get_orchestration_host_status'}


def orchestration_smoke(selection, role):
    """Measure a separately provisioned real stdio profile, without enrollment.

    This read/refusal probe does not imply a successful dispatch or full loop.
    Owner fixtures separately exercise actual lifecycle producers.
    """
    if role not in ORCHESTRATION_TOOLS:
        raise ValueError('unsupported orchestration role')
    script = Path(__file__).with_name('serve.py')
    proc = subprocess.Popen([sys.executable, str(script), str(selection)], stdin=subprocess.PIPE,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    messages = queue.Queue()
    def read_lines():
        for line in proc.stdout:
            messages.put(line)
        messages.put(None)
    threading.Thread(target=read_lines, daemon=True).start()
    def call(ident, method, params):
        proc.stdin.write(json.dumps({'jsonrpc':'2.0', 'id':ident, 'method':method, 'params':params}) + '\n')
        proc.stdin.flush()
        line = messages.get(timeout=30)
        if line is None:
            raise RuntimeError('native orchestration process disconnected')
        response = json.loads(line)
        if 'result' not in response:
            raise RuntimeError('native orchestration transport refused')
        return response['result']
    def tool(ident, name, arguments):
        response = call(ident, 'tools/call', {'name':name, 'arguments':arguments})
        structured = response['structuredContent']
        fallback = json.loads(response['content'][0]['text'])
        if structured != fallback:
            raise RuntimeError('native orchestration structured/text mismatch')
        return structured
    try:
        call(1, 'initialize', {'protocolVersion':'2024-11-05', 'capabilities':{},
                              'clientInfo':{'name':'dacli-orchestration-package', 'version':'1'}})
        proc.stdin.write(json.dumps({'jsonrpc':'2.0', 'method':'notifications/initialized'})+'\n');proc.stdin.flush()
        names = {t['name'] for t in call(2, 'tools/list', {})['tools']}
        if names != ORCHESTRATION_READS | ORCHESTRATION_TOOLS[role]:
            raise RuntimeError('native orchestration role catalog mismatch')
        identity = tool(3, 'identity', {'schema_version':1})
        host = tool(4, 'get_orchestration_host_status', {'schema_version':1})
        refused = tool(5, 'get_dispatch_status', {'schema_version':1, 'dispatch_id':'unknown-package-fixture'})
        if not identity['ok'] or not host['ok'] or host['data']['effect_authorized'] or refused['ok'] or refused['error']['exit_code'] != 3:
            raise RuntimeError('native orchestration role/host/refusal contract mismatch')
    finally:
        proc.stdin.close()
        try:
            code = proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill();proc.wait()
            raise RuntimeError('native orchestration process did not close')
    if code != 0:
        raise RuntimeError('native orchestration process failed')
    return {'role':role, 'tool_names':sorted(names), 'structured_text_parity':True,
            'fresh_identity_observed':True, 'unknown_dispatch_exit':3, 'host_status':host['data'],
            'model_turn_requested':False, 'native_dispatch_exercised':False,
            'assurance':'ordinary-packaging-observation', 'process_exit':code}


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--codex'); p.add_argument('--dacli')
    p.add_argument('--orchestration-selection'); p.add_argument('--orchestration-role', choices=sorted(ORCHESTRATION_TOOLS))
    p.add_argument('--schema5-dacli', help='genuine schema-5 binary for explicit owner migration smoke')
    a = p.parse_args()
    if a.orchestration_selection:
        if not a.orchestration_role:
            p.error('--orchestration-role is required with selection')
        print(json.dumps(orchestration_smoke(Path(a.orchestration_selection).resolve(), a.orchestration_role), indent=2))
        sys.exit(0)
    if not a.codex or not a.dacli:
        p.error('--codex and --dacli are required for installation smoke')
    print(json.dumps(smoke(str(Path(a.codex).resolve()), str(Path(a.dacli).resolve()),
        str(Path(a.schema5_dacli).resolve()) if a.schema5_dacli else None), indent=2))
