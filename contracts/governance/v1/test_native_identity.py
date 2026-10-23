"""Real native stdio identity and missing-credential boundary proof."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


REPOSITORY = Path(__file__).parents[3]


class NativeIdentityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build_dir = None
        configured = os.environ.get("DACLI_CONFORMANCE_BINARY")
        if configured:
            cls.binary = Path(configured).resolve(strict=True)
        else:
            cls.build_dir = tempfile.TemporaryDirectory(prefix="dacli-conformance-build-")
            cls.binary = Path(cls.build_dir.name) / "dacli"
            subprocess.run(["go", "build", "-o", str(cls.binary), "./cmd/dacli"],
                           cwd=REPOSITORY, check=True, timeout=180)

    @classmethod
    def tearDownClass(cls):
        if cls.build_dir is not None:
            cls.build_dir.cleanup()

    def test_missing_credential_refuses_after_authenticated_identity_control(self):
        with tempfile.TemporaryDirectory(prefix="dacli-native-identity-") as tmp:
            root = Path(tmp)
            root.chmod(0o700)
            workspace = root / "workspace"
            workspace.mkdir(mode=0o700)
            subprocess.run(["git", "init", "-q", str(workspace)], check=True, timeout=10)
            self.run_cli(workspace, "init", "--name", "native-identity-proof")
            self.run_cli(workspace, "project", "add", "Native identity proof", "--slug", "identity-proof")
            self.run_cli(workspace, "task", "add", "Exercise native identity", "--project", "identity-proof",
                         "--accept", "identity returns the enrolled session")
            task_rows = json.loads(self.run_cli(workspace, "task", "list", "--project", "identity-proof", "--json"))
            self.assertEqual(len(task_rows), 1, task_rows)
            task_id = task_rows[0]["id"]

            config = root / "owner.json"
            owner_credential = root / "owner.token"
            authority = root / "authority.json"
            connection = root / "session.json"
            credential = root / "session.token"
            self.write_private(config, {
                "schema_version": 1,
                "workspace": str(workspace),
                "authority": str(authority),
                "installation_id": "native-identity-proof",
                "owner_credential_file": str(owner_credential),
            })
            self.run_cli(workspace, "native", "bootstrap", "--config", str(config))
            self.run_cli(workspace, "native", "enroll", "--config", str(config),
                         "--task", "identity-proof/001", "--connection", str(connection),
                         "--credential", str(credential), "--expires", "1h")

            # This control sends only MCP requests. It never reads or copies token bytes.
            selected = json.loads(connection.read_text())
            control = self.rpc_identity(connection, workspace)
            identity = control["structuredContent"]["data"]
            self.assertEqual(identity["session_id"], selected["session_id"])
            self.assertIn("identity", {tool["name"] for tool in control["tools"]})
            self.assertEqual(control["task"], {"id": task_id, "project": "identity-proof",
                                                 "title": "Exercise native identity", "status": "open"})
            self.assertEqual(set(identity["scope"]["Resources"]),
                             {"workspace:" + selected["workspace_id"], "task:" + task_id})
            authority_before = authority.read_bytes()

            absent_credential = root / "deliberately-absent.token"
            self.assertFalse(absent_credential.exists())
            selected["credential_file"] = str(absent_credential)
            missing_connection = root / "missing-credential-session.json"
            self.write_private(missing_connection, selected)
            refusal = self.run_cli_result(workspace, "native", "serve", "--connection", str(missing_connection),
                                          timeout=5)
            self.assertEqual(refusal.returncode, 3, refusal.stdout + refusal.stderr)
            self.assertFalse(absent_credential.exists(), "server created the absent credential")
            self.assertEqual(authority.read_bytes(), authority_before,
                             "missing credential changed native authority")

    @staticmethod
    def clean_env():
        env = os.environ.copy()
        env.pop("DACLI_AGENT", None)
        return env

    def run_cli(self, cwd, *args):
        result = self.run_cli_result(cwd, *args)
        if result.returncode != 0:
            raise AssertionError(f"dacli {' '.join(args)} exited {result.returncode}: {result.stdout}{result.stderr}")
        return result.stdout

    def run_cli_result(self, cwd, *args, timeout=30):
        return subprocess.run([str(self.binary), *args], cwd=cwd, env=self.clean_env(),
                              capture_output=True, text=True, timeout=timeout)

    @staticmethod
    def write_private(path, value):
        path.write_text(json.dumps(value) + "\n")
        path.chmod(0o600)

    def rpc_identity(self, connection, cwd):
        proc = subprocess.Popen([str(self.binary), "native", "serve", "--connection", str(connection)],
                                cwd=cwd, env=self.clean_env(), stdin=subprocess.PIPE,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        requests = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                "protocolVersion": "2025-06-18", "capabilities": {},
                "clientInfo": {"name": "dacli-native-identity-test", "version": "1"}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
            {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {
                "name": "identity", "arguments": {"schema_version": 1}}},
            {"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {
                "name": "task_get", "arguments": {"schema_version": 1, "task": "identity-proof/001"}}},
        ]
        payload = "".join(json.dumps(request) + "\n" for request in requests)
        try:
            stdout, stderr = proc.communicate(payload, timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.communicate()
            self.fail("authenticated native stdio control exceeded 10 seconds")
        self.assertEqual(proc.returncode, 0, stdout + stderr)
        responses = [json.loads(line) for line in stdout.splitlines() if line]
        by_id = {response.get("id"): response for response in responses}
        self.assertTrue({1, 2, 3, 4} <= by_id.keys(), stdout)
        self.assertNotIn("error", by_id[1], stdout)
        self.assertNotIn("error", by_id[2], stdout)
        self.assertNotIn("error", by_id[3], stdout)
        self.assertNotIn("error", by_id[4], stdout)
        tools = by_id[2]["result"]["tools"]
        result = by_id[3]["result"]
        structured = result["structuredContent"]
        self.assertTrue(structured["ok"], result)
        self.assertEqual(json.loads(result["content"][0]["text"]), structured)
        task_result = by_id[4]["result"]
        task = task_result["structuredContent"]
        self.assertTrue(task["ok"], task_result)
        self.assertEqual(json.loads(task_result["content"][0]["text"]), task)
        return {"tools": tools, "structuredContent": structured, "task": task["data"]["task"]}


if __name__ == "__main__":
    unittest.main()
