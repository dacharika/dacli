#!/usr/bin/env python3
"""Provision disposable operator fixtures and run independent consumers."""

import argparse
import datetime
import hashlib
import json
import os
import pathlib
import shutil
import signal
import subprocess
import sys
import tempfile
import time


REPO = pathlib.Path(__file__).resolve().parents[2]
FIXTURES = REPO / "contracts/operator/v1/testdata"
OWNER_INSTALLATION = "operator-conformance-fixture"


def run(args, cwd, env, timeout=30):
    result = subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        detail = result.stderr.strip().replace(str(cwd), "<disposable-workspace>")
        raise RuntimeError(f"setup command failed with exit {result.returncode}: {args[1:3]}: {detail[:512]}")
    return result.stdout


def git(args, cwd, env):
    subprocess.run(["git", *args], cwd=cwd, env=env, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=20)


def task_ids(binary, project, cwd, env):
    parsed = json.loads(run([binary, "task", "list", "--project", project, "--json"], cwd, env))
    ids = set()

    def walk(value):
        if isinstance(value, dict):
            for key, child in value.items():
                if key in ("id", "ID") and isinstance(child, str) and child.startswith("t-"):
                    ids.add(child)
                walk(child)
        elif isinstance(value, list):
            for child in value:
                walk(child)

    walk(parsed)
    if not ids:
        raise RuntimeError(f"expected disposable task in {project}, found none")
    return sorted(ids)


def add_reader(binary, owner, private, project, label, cwd, env):
    connection = private / f"{label}.json"
    credential = private / f"{label}.token"
    run([binary, "native", "operator", "add", "--config", str(owner), "--project", project,
         "--connection", str(connection), "--credential", str(credential), "--expires", "10m"], cwd, env)
    if connection.stat().st_mode & 0o777 != 0o600 or credential.stat().st_mode & 0o777 != 0o600:
        raise RuntimeError(f"{label} private reader files are not mode 0600")
    # Owner setup reads only the disposable connection metadata; consumers get
    # only this opaque path and the public workspace/project values.
    selected = json.loads(connection.read_text(encoding="utf-8"))
    return connection, selected


def add_ordinary(binary, owner, private, task_id, label, cwd, env):
    connection = private / f"{label}.json"
    credential = private / f"{label}.token"
    run([binary, "native", "enroll", "--config", str(owner), "--task", task_id,
         "--connection", str(connection), "--credential", str(credential),
         "--checkout", str(cwd), "--expires", "10m"], cwd, env)
    return connection


def stop_process_group(process):
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    try:
        process.wait(timeout=1)
    except subprocess.TimeoutExpired:
        pass
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        raise RuntimeError("consumer process group did not stop after SIGKILL")
    deadline = time.monotonic() + 2
    while True:
        try:
            os.killpg(process.pid, 0)
        except ProcessLookupError:
            return
        if time.monotonic() >= deadline:
            raise RuntimeError("consumer process group remains after SIGKILL; cleanup is unknown")
        time.sleep(0.02)


def fixture_setup(binary, env):
    root = pathlib.Path(tempfile.mkdtemp(prefix="dacli-operator-conformance-", dir="/private/tmp"))
    root.chmod(0o700)
    try:
        return populate_fixture(root, binary, env)
    except Exception:
        shutil.rmtree(root, ignore_errors=True)
        raise


def populate_fixture(root, binary, env):
    workspace = root / "workspace"
    workspace.mkdir(mode=0o700)
    private = root / "private"
    private.mkdir(mode=0o700)
    git(["init", "-b", "main"], workspace, env)
    (workspace / ".gitignore").write_text(".dacli/\n", encoding="utf-8")
    (workspace / "README.md").write_text("Disposable operator conformance source.\n", encoding="utf-8")
    run([binary, "init", "--name", "operator-conformance"], workspace, env)
    run([binary, "project", "add", "Conformance demo", "--slug", "demo", "--goal", "Qualify disposable local reads"], workspace, env)
    run([binary, "project", "add", "Foreign demo", "--slug", "foreign", "--goal", "Prove project isolation"], workspace, env)
    for title in ("Track cursor page one", "Record a separate fixture action", "Inspect the final synthetic marker"):
        run([binary, "task", "add", title, "--project", "demo", "--accept", "Synthetic fixture"], workspace, env)
    run([binary, "task", "add", "Keep the foreign task isolated", "--project", "foreign", "--accept", "Synthetic fixture"], workspace, env)
    git(["add", ".gitignore", "README.md"], workspace, env)
    git(["-c", "user.name=Conformance Fixture", "-c", "user.email=conformance@example.invalid",
         "commit", "-m", "Create disposable conformance source"], workspace, env)

    authority = private / "authority.json"
    owner = private / "owner.json"
    owner.write_text(json.dumps({"schema_version": 1, "workspace": str(workspace),
                                 "authority": str(authority), "installation_id": OWNER_INSTALLATION,
                                 "owner_credential_file": str(private / "owner.token")}) + "\n", encoding="utf-8")
    owner.chmod(0o600)
    run([binary, "native", "bootstrap", "--config", str(owner)], workspace, env)
    for family in ("swarm", "dispatch", "lifecycle", "recovery"):
        run([binary, "native", family, "upgrade", "--config", str(owner)], workspace, env)

    demo_tasks = task_ids(binary, "demo", workspace, env)
    task_id = demo_tasks[0]
    for task, dependency in zip(demo_tasks[1:], demo_tasks[:-1]):
        run([binary, "task", "depend", task, "--add", dependency], workspace, env)
    go_connection, go_selection = add_reader(binary, owner, private, "demo", "reader-go", workspace, env)
    py_connection, py_selection = add_reader(binary, owner, private, "demo", "reader-python", workspace, env)
    if go_selection["workspace_id"] != py_selection["workspace_id"]:
        raise RuntimeError("fixture readers selected different workspaces")
    workspace_id = go_selection["workspace_id"]
    go_ordinary = add_ordinary(binary, owner, private, task_id, "ordinary-go", workspace, env)
    py_ordinary = add_ordinary(binary, owner, private, task_id, "ordinary-python", workspace, env)
    return {
        "root": root, "workspace": workspace, "private": private, "owner": owner,
        "cleanup_safe": True,
        "workspace_id": workspace_id, "project": "demo", "task_id": task_id,
        "demo_tasks": demo_tasks,
        "go_connection": go_connection, "go_session": go_selection["session_id"],
        "python_connection": py_connection, "python_session": py_selection["session_id"],
        "go_ordinary": go_ordinary, "python_ordinary": py_ordinary,
    }


def run_go(args, fixture, env, outdir):
    log = outdir / "go-live.log"
    child_env = env.copy()
    child_env.update({
        "DACLI_OPERATOR_BIN": str(pathlib.Path(args.binary).resolve()),
        "DACLI_OPERATOR_CONNECTION": str(fixture["go_connection"]),
        "DACLI_OPERATOR_MISSING_AUTHORITY_CONNECTION": str(fixture["go_ordinary"]),
        "DACLI_OPERATOR_WORKSPACE": fixture["workspace_id"],
        "DACLI_OPERATOR_PROJECT": fixture["project"],
    })
    child_env["GOTOOLCHAIN"] = "go1.26.10"
    child_env["GOCACHE"] = args.go_cache
    with log.open("w", encoding="utf-8") as output:
        process = subprocess.Popen([args.go, "test", "./internal/operatorconformance", "-count=1"], cwd=REPO,
                                   env=child_env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            code = process.wait(timeout=120)
        except subprocess.TimeoutExpired:
            raise TimeoutError(f"Go live consumer timed out; see {log}")
        finally:
            try:
                stop_process_group(process)
            except Exception as error:
                fixture["cleanup_safe"] = False
                raise RuntimeError(f"Go consumer cleanup is unknown; retained disposable fixture {fixture['root']}") from error
    if code:
        raise RuntimeError(f"Go live consumer exited {code}; see {log}")
    return {"log": log.name, "exit_code": code,
            "selection": "reader-go", "missing_operator_authority_selection": "ordinary-go",
            "observations": {"authorized_read": "live-success",
                             "version_mismatch": "live-unsupported_version",
                             "reconnect": "live-success-on-fresh-child",
                             "missing_operator_authority": "live-denied",
                             "runtime_capability_preservation": {
                                 "schema": 10,
                                 "services": {"budget": "unsupported", "policy": "unsupported",
                                              "controller": "unsupported"},
                                 "spend": {"state": "unavailable", "provenance": "unavailable",
                                           "enforcement": "unavailable"}}}}


def run_python(args, fixture, env, outdir):
    fifo = fixture["root"] / "revoke.signal"
    os.mkfifo(fifo, 0o600)
    stage = outdir / "python-stage.json"
    final_receipt = stage
    stdout_path, stderr_path = outdir / "python-live.stdout", outdir / "python-live.stderr"
    child_env = env.copy()
    child_env["PYTHONDONTWRITEBYTECODE"] = "1"
    fixtures_log = outdir / "python-fixtures.log"
    with fixtures_log.open("w", encoding="utf-8") as output:
        fixture_result = subprocess.run([sys.executable, str(REPO / "scripts/operator-conformance/client.py"),
                                         "--self-test"], cwd=REPO, env=child_env,
                                        stdout=output, stderr=subprocess.STDOUT, timeout=30)
    if fixture_result.returncode:
        raise RuntimeError(f"Python fixture consumer exited {fixture_result.returncode}; see {fixtures_log}")
    command = [sys.executable, str(REPO / "scripts/operator-conformance/client.py"),
               "--binary", str(pathlib.Path(args.binary).resolve()),
               "--connection", str(fixture["python_connection"]),
               "--missing-authority-connection", str(fixture["python_ordinary"]),
               "--workspace", fixture["workspace_id"], "--project", fixture["project"],
               "--scenario", "revoke", "--revoke-trigger", str(fifo),
               "--receipt", str(stage), "--transport-identity", "isolated disposable reader-python"]
    with stdout_path.open("w", encoding="utf-8") as stdout, stderr_path.open("w", encoding="utf-8") as stderr:
        process = subprocess.Popen(command, cwd=REPO, env=child_env, stdout=stdout, stderr=stderr,
                                   start_new_session=True)
        try:
            deadline = time.monotonic() + 45
            while time.monotonic() < deadline:
                if stage.exists():
                    current = json.loads(stage.read_text(encoding="utf-8"))
                    if current.get("stage") == "baseline-succeeded":
                        break
                if process.poll() is not None:
                    raise RuntimeError(f"Python consumer exited before revocation gate: {process.returncode}")
                time.sleep(0.05)
            else:
                raise TimeoutError("Python consumer did not reach the owner revocation gate")

            run([args.binary, "native", "revoke", "--config", str(fixture["owner"]),
                 "--session", fixture["python_session"]], fixture["workspace"], env)
            signal_fd = os.open(fifo, os.O_WRONLY | os.O_NONBLOCK)
            try:
                os.write(signal_fd, b"x")
            finally:
                os.close(signal_fd)
            code = process.wait(timeout=45)
        finally:
            try:
                stop_process_group(process)
            except Exception as error:
                fixture["cleanup_safe"] = False
                raise RuntimeError(f"Python consumer cleanup is unknown; retained disposable fixture {fixture['root']}") from error
    if code:
        raise RuntimeError(f"Python live consumer exited {code}; see {stdout_path} and {stderr_path}")
    return {"fixture_log": fixtures_log.name, "fixture_exit_code": fixture_result.returncode,
            "log": stdout_path.name, "stderr_log": stderr_path.name, "exit_code": code,
            "receipt": final_receipt.name, "selection": "reader-python",
            "missing_operator_authority_selection": "ordinary-python",
            "revocation": "owner-revoked disposable session before signaling same-child next request"}


def sha256(path):
    digest = hashlib.sha256()
    with pathlib.Path(path).open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, help="locally built dacli runtime from the qualified source")
    parser.add_argument("--go", default="/Users/tahabsn/go/bin/go")
    parser.add_argument("--go-cache", default="/private/tmp/dacli-next-20261023/go-cache")
    parser.add_argument("--output-dir", required=True, help="directory for logs and final provenance receipt")
    parser.add_argument("--receipt", default="operator-conformance-receipt.json")
    args = parser.parse_args()
    binary = str(pathlib.Path(args.binary).resolve())
    outdir = pathlib.Path(args.output_dir).resolve()
    outdir.mkdir(mode=0o700, parents=True, exist_ok=True)
    outdir.chmod(0o700)
    env = os.environ.copy()
    env.pop("DACLI_AGENT", None)
    env["GOTOOLCHAIN"] = "go1.26.10"
    fixture = fixture_setup(binary, env)
    try:
        go_run = run_go(args, fixture, env, outdir)
        py_run = run_python(args, fixture, env, outdir)
        py_receipt = json.loads((outdir / py_run["receipt"]).read_text(encoding="utf-8"))
        receipt = {
            "schema": "operator-independent-conformance/v1",
            "source": {
                "commit": subprocess.run(["git", "-C", str(REPO), "rev-parse", "HEAD"],
                                          check=True, capture_output=True, text=True).stdout.strip(),
                "files": {str(path.relative_to(REPO)): sha256(path) for path in sorted(
                    [*REPO.glob("internal/operatorconformance/*.go"),
                     *REPO.glob("scripts/operator-conformance/*.py"),
                     REPO / "docs/operator-transport-conformance.md"]
                )},
            },
            "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "runtime": {"python": sys.version, "platform": os.uname().sysname + " " + os.uname().release,
                        "go": subprocess.run([args.go, "version"], check=True, capture_output=True,
                                             text=True).stdout.strip()},
            "transport": {"binary": binary, "sha256": sha256(binary), "fixture_authority": "disposable schema-10"},
            "golden_fixtures": {p.name: sha256(p) for p in sorted(FIXTURES.glob("*.json"))},
            "consumers": {"go": go_run, "python": py_run},
            "observations": {"go": go_run["observations"], "python": py_receipt.get("observations", {})},
            "fixture_scope": {"workspace": "temporary isolated dacli workspace", "project": "demo",
                              "tasks": "three synthetic canonical tasks", "live_user_workspace": False},
        }
        receipt_path = outdir / args.receipt
        receipt_path.write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        receipt_path.chmod(0o600)
        print(json.dumps({"receipt": str(receipt_path), "status": "passed"}, sort_keys=True))
    finally:
        if fixture["cleanup_safe"]:
            shutil.rmtree(fixture["root"])


if __name__ == "__main__":
    main()
