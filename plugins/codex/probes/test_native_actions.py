"""Synthetic parser/helper checks; never native attribution evidence or login."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import native_actions as actions


class NativeActionTests(unittest.TestCase):
    def test_output_projection_never_retains_model_text(self):
        raw='\n'.join([json.dumps({'type':'turn.completed','message':'PRIVATE','usage':{'secret':'PRIVATE'}}),
                       json.dumps({'type':'PRIVATE','PRIVATE':'PRIVATE'}),'not-json','[]'])
        receipt=actions.summarize(raw)
        self.assertTrue(receipt['turn_completed'])
        self.assertEqual(receipt['event_counts'],{'turn.completed':1})
        self.assertNotIn('PRIVATE',json.dumps(receipt))

    def test_version_mismatch_never_reaches_auth_or_execution(self):
        result=subprocess.run([sys.executable,str(Path(actions.__file__)),
          '--codex',sys.executable,'--expected-version','not-the-installed-version',
          '--auth-file','/nonexistent/public-fixture-auth','--case','observe'],
          text=True,capture_output=True,timeout=3)
        self.assertEqual(result.returncode,2)
        self.assertIn('host version mismatch',result.stderr)
        self.assertEqual(result.stdout,'')

    def test_item_projection_and_coarse_failure_category(self):
        raw=json.dumps({'type':'item.completed','item':{'type':'mcp_tool_call',
          'status':'failed','tool':'context_probe','arguments':{'secret':'PRIVATE'},
          'error':{'message':'transport connection failed PRIVATE'},'result':'PRIVATE'}})
        record=actions.summarize(raw)['items'][0]
        self.assertEqual(record['category'],'mcp_tool_call')
        self.assertEqual(record['status'],'failed')
        self.assertEqual(record['error_category'],'transport')
        self.assertNotIn('PRIVATE',json.dumps(record))

    def test_namespaced_hook_tool_is_preserved_without_arguments(self):
        with tempfile.TemporaryDirectory() as tmp:
            log=Path(tmp)/'safe'
            result=subprocess.run([sys.executable,'-c',actions.HELPER,'observe',str(log)],
              input='{"hook_event_name":"PreToolUse","tool_name":"functions.spawn_agent","tool_input":"PRIVATE"}',
              text=True,capture_output=True,timeout=3)
            self.assertEqual(result.returncode,0)
            self.assertEqual(json.loads(log.read_text())['tool'],'functions.spawn_agent')
            self.assertNotIn('PRIVATE',log.read_text())

    def test_advertised_features_are_closed_declarations(self):
        self.assertEqual(actions.feature_declarations('multi_agent stable true\nhooks stable false\nPRIVATE stable true'),
                         {'multi_agent':True,'hooks':False})

    def test_public_tool_read_only_annotations(self):
        with tempfile.TemporaryDirectory() as tmp:
            r=subprocess.run([sys.executable,'-c',actions.HELPER,'mcp',str(Path(tmp)/'safe')],
                input='{"id":1,"method":"tools/list"}\n',text=True,capture_output=True,timeout=3)
            self.assertEqual(json.loads(r.stdout)['result']['tools'][0]['annotations'],
                {'readOnlyHint':True,'destructiveHint':False,'idempotentHint':True,'openWorldHint':False})

    def test_session_persistence_is_explicit_and_preserves_sandbox(self):
        default=actions.execution_command('/public/codex','/public/work','observe')
        persistent=actions.execution_command('/public/codex','/public/work','observe',True)
        self.assertIn('--ephemeral',default)
        self.assertNotIn('--ephemeral',persistent)
        self.assertEqual([x for x in default if x!='--ephemeral'],persistent)
        self.assertIn('workspace-write',persistent)
        self.assertIn('never',persistent)

    def test_ephemeral_item_diagnostic_is_enum_only(self):
        raw=json.dumps({'type':'item.completed','item':{'type':'error',
            'message':'ephemeral tasks cannot create inspectable background tasks PRIVATE'}})
        receipt=actions.summarize(raw)
        self.assertEqual(receipt['items'][0]['error_category'],'ephemeral_tasks_unavailable')
        self.assertNotIn('PRIVATE',json.dumps(receipt))

    def test_failed_turn_is_not_completion(self):
        self.assertFalse(actions.summarize('{"type":"turn.failed"}')['turn_completed'])

    def test_mcp_helper_only_records_safe_metadata(self):
        with tempfile.TemporaryDirectory() as tmp:
            log=Path(tmp)/'safe.jsonl'
            request={'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'context_probe',
              'arguments':{'tag':'child','secret':'PRIVATE'},'_meta':{'openai/session':'PRIVATE','authorization':'PRIVATE'}}}
            result=subprocess.run([sys.executable,'-c',actions.HELPER,'mcp',str(log)],input=json.dumps(request)+'\n',text=True,capture_output=True,timeout=3)
            self.assertEqual(result.returncode,0)
            output=log.read_text()+result.stdout
            self.assertNotIn('PRIVATE',output)
            records=[json.loads(line) for line in log.read_text().splitlines()]
            record=next(r for r in records if r['source']=='mcp')
            self.assertEqual(record['tag'],'child')
            self.assertEqual(record['meta_fields'],['openai/session'])
            self.assertEqual(record['session_hash'],actions.digest('PRIVATE'))

    def test_deny_helper_emits_explicit_pretool_denial(self):
        with tempfile.TemporaryDirectory() as tmp:
            result=subprocess.run([sys.executable,'-c',actions.HELPER,'deny',str(Path(tmp)/'safe')],
              input='{"hook_event_name":"PreToolUse","tool_name":"shell","session_id":"PRIVATE"}',text=True,capture_output=True,timeout=3)
            self.assertEqual(result.returncode,0)
            self.assertEqual(json.loads(result.stdout)['hookSpecificOutput']['permissionDecision'],'deny')

    def test_unrecognized_mcp_tag_does_not_become_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            log=Path(tmp)/'safe'
            request={'id':1,'method':'tools/call','params':{'name':'context_probe','arguments':{'tag':'authenticated-admin'}}}
            result=subprocess.run([sys.executable,'-c',actions.HELPER,'mcp',str(log)],input=json.dumps(request)+'\n',text=True,capture_output=True,timeout=3)
            self.assertTrue(json.loads(result.stdout)['result']['isError'])
            self.assertFalse(any(json.loads(line)['source']=='mcp' for line in log.read_text().splitlines()))

if __name__=='__main__': unittest.main()
