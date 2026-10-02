package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// Issues #715 and #803: a Claude binary can report a version and stream-json
// system/init before its first governed turn exits with "Not logged in".
// Doctor, standalone preflight, spawn, and supervise (the command used by the
// loop for governed turns) must agree that this adapter is unusable before a
// child identity, claim, run, or worktree is created.
func TestUnauthenticatedClaudeIsRefusedBeforeSpawn(t *testing.T) {
	w := newExecWS(t)
	task := mustTask(t, w, "Claude authentication", store.TaskOpts{})
	fake := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'claude 1.0'; exit 0; fi\nprintf '{\"type\":\"system\",\"subtype\":\"init\"}\\n'\nsleep 0.05\necho 'Not logged in · Please run /login' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["claude-code-rw"]
	rt.Name, rt.Binary, rt.Context = "unauthenticated-claude", fake, nil
	rt.BehavioralPreflight = "" // mature runtime persisted before this capability existed
	rt.Args = []string{"--allowedTools", "Edit", "Write", "Read"}
	mustRuntime(t, w, rt)
	loaded, err := store.LoadRuntime(w, rt.Name)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BehavioralPreflight != store.BehavioralPreflightClaudePrintV1 || loaded.BehavioralPreflightProvenance != store.ProvenanceInferred {
		t.Fatalf("legacy Claude strategy = %q/%q", loaded.BehavioralPreflightProvenance, loaded.BehavioralPreflight)
	}

	ctx, doctorOut, _ := newCtx(w.Root)
	if err := cmdRuntimeDoctor(ctx, []string{"--runtime", rt.Name, "--grant", "rw"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doctorOut.String(), "result=probed/incompatible") || !strings.Contains(doctorOut.String(), "authentication") || !strings.Contains(doctorOut.String(), "/login") {
		t.Fatalf("doctor accepted an unauthenticated Claude runtime:\n%s", doctorOut.String())
	}

	ctx, _, _ = newCtx(w.Root)
	if err := cmdPreflight(ctx, []string{"--runtime", rt.Name, "--grant", "rw"}); clikit.ExitCode(err) != 3 {
		t.Fatalf("preflight exit = %d, want refusal (err %v)", clikit.ExitCode(err), err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{"children": w.AgentsDir(), "runs": w.RunsDir(), "worktrees": w.WorktreesDir()}
	baseline := map[string]int{}
	for label, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		baseline[label] = len(entries)
	}
	commands := []struct {
		name string
		run  func(*clikit.Ctx, []string) error
		args []string
	}{
		{name: "spawn", run: cmdSpawn, args: []string{"--task", "001", "--runtime", rt.Name, "--grant", "rw", "--worktree"}},
		{name: "supervise", run: cmdSupervise, args: []string{"--task", "001", "--runtime", rt.Name, "--grant", "rw", "--max-turns", "1"}},
	}
	for _, command := range commands {
		ctx, _, _ = newCtx(w.Root)
		err = command.run(ctx, command.args)
		if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "authentication") || !strings.Contains(err.Error(), "/login") {
			t.Fatalf("%s exit = %d, want authentication refusal (err %v)", command.name, clikit.ExitCode(err), err)
		}
	}
	after, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("unauthenticated Claude spawn changed the task claim")
	}
	for label, dir := range dirs {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if len(entries) != baseline[label] {
			t.Errorf("authentication refusal created %s: %v", label, entries)
		}
	}
}

func TestAuthenticatedClaudeInitIsLaunchCompatible(t *testing.T) {
	oldSettle := claudePostInitSettlingTime
	claudePostInitSettlingTime = 50 * time.Millisecond
	t.Cleanup(func() { claudePostInitSettlingTime = oldSettle })
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '{\"type\":\"system\",\"subtype\":\"init\"}\\n'\nsleep 30 &\nwait\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["claude-code-rw"]
	rt.Binary = fake
	ctx, _, _ := newCtx(dir)
	started := time.Now()
	got := runBehavioralPreflight(ctx, rt, fake, model.GrantRW, "", false)
	if got.State != store.LaunchCompatible {
		t.Fatalf("Claude init classified as %s/%s: %s", got.State, got.Layer, got.Detail)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("Claude readiness waited for model completion: %s", elapsed)
	}
}

func TestClaudePostInitFailureSeparatesAuthenticationFromTransient(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		state            store.LaunchState
		layer            store.LaunchLayer
	}{
		{name: "authentication", diagnostic: "Not logged in · Please run /login", state: store.LaunchIncompatible, layer: store.LaunchAuthentication},
		{name: "quota", diagnostic: "rate limit: quota temporarily unavailable", state: store.LaunchTransient, layer: store.LaunchQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := filepath.Join(t.TempDir(), "claude")
			script := "#!/bin/sh\nprintf '{\"type\":\"system\",\"subtype\":\"init\"}\\n'\nprintf '%s\\n' '" + tc.diagnostic + "' >&2\nexit 1\n"
			if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			rt := presets["claude-code-rw"]
			rt.Binary = fake
			ctx, _, _ := newCtx(t.TempDir())
			got := runBehavioralPreflight(ctx, rt, fake, model.GrantRW, "", false)
			if got.State != tc.state || got.Layer != tc.layer {
				t.Fatalf("post-init %s classified as %s/%s: %s", tc.name, got.State, got.Layer, got.Detail)
			}
		})
	}
}

// A provider wrapper may exit immediately after init while a just-forked
// helper still owns stderr and emits the actionable diagnostic. Cmd.Wait must
// drain that inherited descriptor before classification; closing caller-owned
// StdoutPipe/StderrPipe handles at leader exit used to lose this line.
func TestClaudePostInitDrainsForkedAuthenticationDiagnostic(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
printf '{"type":"system","subtype":"init"}\n'
(sleep 0.02; echo 'Not logged in · Please run /login' >&2) &
exit 1
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["claude-code-rw"]
	rt.Binary = fake
	ctx, _, _ := newCtx(t.TempDir())
	got := runBehavioralPreflight(ctx, rt, fake, model.GrantRW, "", false)
	if got.State != store.LaunchIncompatible || got.Layer != store.LaunchAuthentication {
		t.Fatalf("forked post-init authentication diagnostic classified as %s/%s: %s", got.State, got.Layer, got.Detail)
	}
}

func TestLegacyClaudeInferenceRejectsUnknownExecutionFlag(t *testing.T) {
	w := newExecWS(t)
	rt := presets["claude-code-rw"]
	rt.Name = "custom-claude"
	rt.BehavioralPreflight = ""
	rt.Args = append(rt.Args, "--dangerously-skip-permissions")
	mustRuntime(t, w, rt)
	loaded, err := store.LoadRuntime(w, rt.Name)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BehavioralPreflight != "" {
		t.Fatalf("unknown custom Claude flag inferred strategy %q", loaded.BehavioralPreflight)
	}
}

// Issue #746: a successful local sandbox probe did not prove that Codex could
// initialize the exact app-server transport used by spawn. This fixture gets
// through the sandbox declaration, then reproduces the observed startup
// refusal on `exec`. The refusal must happen while resolveLaunch is still
// side-effect free: no child, claim, run, or worktree may exist yet.
func TestCodexROBehavioralPreflightRefusesBeforeSpawnRecords(t *testing.T) {
	w := newExecWS(t)
	task := mustTask(t, w, "preflight refusal", store.TaskOpts{})
	fake := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo 'codex-cli 0.147.0'; exit 0; fi
echo 'failed to initialize in-process app-server client: Operation not permitted (os error 1)' >&2
exit 1
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex"]
	rt.Name, rt.Binary = "codex-ro-fixture", fake
	mustRuntime(t, w, rt)
	if err := store.SaveRuntimeROProbe(w, rt, fake, store.RuntimeROVerified, "fixture sandbox verified"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{"children": w.AgentsDir(), "runs": w.RunsDir(), "worktrees": w.WorktreesDir()}
	baseline := map[string]int{}
	for label, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		baseline[label] = len(entries)
	}

	ctx, _, _ := newCtx(w.Root)
	err = cmdSpawn(ctx, []string{"--task", "001", "--runtime", rt.Name, "--grant", "ro", "--worktree", "--allow-user-config"})
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "sandbox") || !strings.Contains(err.Error(), "runtime doctor --runtime "+rt.Name+" --grant ro") {
		t.Fatalf("preflight refusal = exit %d, %v", clikit.ExitCode(err), err)
	}
	after, _ := os.ReadFile(task.Path)
	if string(after) != string(before) {
		t.Fatal("preflight refusal changed the task claim")
	}
	for label, dir := range dirs {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if len(entries) != baseline[label] {
			t.Errorf("preflight refusal created %s: %v", label, entries)
		}
	}
}

// Issue #763: mature adapters persisted before behavioral_preflight and often
// before usage_format. The exact Codex exec contract must still run the bounded
// handshake, while the adapter name and usage parser remain irrelevant.
func TestLegacyCodexExecWithoutUsageFormatRunsBehavioralPreflight(t *testing.T) {
	w := newExecWS(t)
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "codex")
	marker := filepath.Join(fakeDir, "handshake-ran")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then exit 0; fi\ntouch \"" + marker + "\"\nprintf '%s\\n' '{\"type\":\"turn.started\"}'\nsleep 30\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex-rw"]
	rt.Name, rt.Binary = "mature-custom-name", fake
	rt.UsageFormat = ""
	rt.BehavioralPreflight = ""
	mustRuntime(t, w, rt)
	loaded, err := store.LoadRuntime(w, rt.Name)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BehavioralPreflight != store.BehavioralPreflightCodexExecJSONV2 || loaded.BehavioralPreflightProvenance != store.ProvenanceInferred {
		t.Fatalf("legacy strategy = %q/%q", loaded.BehavioralPreflightProvenance, loaded.BehavioralPreflight)
	}
	ctx, out, _ := newCtx(w.Root)
	if err := cmdPreflight(ctx, []string{"--runtime", rt.Name, "--grant", "rw", "--allow-user-config"}); err != nil {
		t.Fatalf("legacy preflight: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "strategy=inferred/codex-exec-json-v2") || !strings.Contains(out.String(), "result=probed/compatible") {
		t.Fatalf("preflight omitted strategy/probe provenance:\n%s", out.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("legacy adapter bypassed handshake: %v", err)
	}
}

func TestBootstrapCodexRuntimeWithCombinedArgsRunsBehavioralPreflight(t *testing.T) {
	w := newExecWS(t)
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "codex")
	marker := filepath.Join(fakeDir, "handshake-ran")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then exit 0; fi\ntouch \""+marker+"\"\nprintf '%s\\n' '{\"type\":\"turn.started\"}'\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := store.Runtime{
		Name: "mature-bootstrap", Binary: fake, Mode: "stdin",
		Args: []string{"--ask-for-approval", "never", "exec", "--ignore-user-config", "--disable", "plugins", "--disable", "plugin_sharing", "--disable", "remote_plugin", "--sandbox", "workspace-write", "--add-dir", filepath.Join(w.Root, ".git"), "--add-dir", filepath.Join(w.Root, ".dacli"), "--ephemeral", "--color", "never", "--json"},
	}
	mustRuntime(t, w, rt)
	loaded, err := store.LoadRuntime(w, rt.Name)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BehavioralPreflight != store.BehavioralPreflightCodexExecJSONV2 || loaded.BehavioralPreflightProvenance != store.ProvenanceInferred {
		t.Fatalf("bootstrap strategy = %q/%q", loaded.BehavioralPreflightProvenance, loaded.BehavioralPreflight)
	}
	ctx, out, _ := newCtx(w.Root)
	if err := cmdPreflight(ctx, []string{"--runtime", rt.Name, "--grant", "rw", "--allow-user-config"}); err != nil {
		t.Fatalf("bootstrap preflight: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("bootstrap adapter bypassed handshake: %v", err)
	}
}

func TestLegacyCodexInferenceRejectsUnknownExecutionFlag(t *testing.T) {
	w := newExecWS(t)
	fake := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex-rw"]
	rt.Name, rt.Binary = "custom-codex", fake
	rt.BehavioralPreflight = ""
	rt.Args = append(rt.Args, "--dangerously-bypass-approvals-and-sandbox")
	mustRuntime(t, w, rt)
	loaded, err := store.LoadRuntime(w, rt.Name)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BehavioralPreflight != "" {
		t.Fatalf("unknown custom flag inferred strategy %q", loaded.BehavioralPreflight)
	}
}

func TestRuntimeDoctorJSONExposesInferredStrategyAndProbedResult(t *testing.T) {
	w := newExecWS(t)
	fake := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo codex-test; exit 0; fi\nif [ \"$1\" = \"sandbox\" ]; then exit 1; fi\nprintf '%s\\n' '{\"type\":\"turn.started\"}'\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex-rw"]
	rt.Name, rt.Binary, rt.UsageFormat, rt.BehavioralPreflight = "legacy-doctor", fake, "", ""
	mustRuntime(t, w, rt)
	ctx, out, _ := newCtx(w.Root)
	ctx.JSON = true
	if err := cmdRuntimeDoctor(ctx, []string{"--runtime", rt.Name, "--grant", "rw"}); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Strategy           string                       `json:"behavioral_preflight"`
		StrategyProvenance store.CapabilityProvenance   `json:"behavioral_preflight_provenance"`
		Launch             store.RuntimeLaunchPreflight `json:"launch"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Strategy != store.BehavioralPreflightCodexExecJSONV2 || rows[0].StrategyProvenance != store.ProvenanceInferred || rows[0].Launch.Provenance != store.ProvenanceProbed || rows[0].Launch.State != store.LaunchCompatible {
		t.Fatalf("doctor provenance = %#v", rows)
	}
	if strings.Contains(out.String(), "Return no content") || strings.Contains(out.String(), "HOME=") {
		t.Fatal("doctor JSON leaked prompt or environment values")
	}
}

func TestRuntimeNameAndUsageFormatDoNotEnableCodexBehavioralPreflight(t *testing.T) {
	w := newExecWS(t)
	bin := fakeBinary(t)
	mustRuntime(t, w, store.Runtime{Name: "codex-rw", Binary: bin, Mode: "stdin", UsageFormat: "codex-jsonl"})
	loaded, err := store.LoadRuntime(w, "codex-rw")
	if err != nil {
		t.Fatal(err)
	}
	if hasBehavioralPreflight(loaded) {
		t.Fatal("runtime name or usage parser enabled provider-specific execution")
	}
}

func TestLaunchPreflightCacheRejectsExpiredCompatibleResult(t *testing.T) {
	w := newExecWS(t)
	bin := fakeBinary(t)
	rt := store.Runtime{Name: "cache", Binary: bin, Mode: "stdin"}
	result := store.RuntimeLaunchPreflight{State: store.LaunchCompatible, Provenance: store.ProvenanceProbed, CommandTimestamp: time.Now().Add(-10 * time.Minute)}
	if err := store.SaveRuntimeLaunchPreflight(w, rt, bin, model.GrantRO, "", false, result); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.LoadFreshRuntimeLaunchPreflight(w, rt, bin, model.GrantRO, "", false, time.Now(), 5*time.Minute); ok {
		t.Fatal("expired compatible preflight authorized launch")
	}
}

func TestLaunchPreflightCacheIncludesBehavioralStrategyVersion(t *testing.T) {
	w := newExecWS(t)
	bin := fakeBinary(t)
	rt := store.Runtime{Name: "cache-strategy", Binary: bin, Mode: "stdin", BehavioralPreflight: store.BehavioralPreflightCodexExecJSONV1}
	result := store.RuntimeLaunchPreflight{State: store.LaunchCompatible, Provenance: store.ProvenanceProbed, CommandTimestamp: time.Now()}
	if err := store.SaveRuntimeLaunchPreflight(w, rt, bin, model.GrantRW, "", false, result); err != nil {
		t.Fatal(err)
	}
	rt.BehavioralPreflight = "codex-exec-json-v2"
	if _, ok := store.LoadFreshRuntimeLaunchPreflight(w, rt, bin, model.GrantRW, "", false, time.Now(), 5*time.Minute); ok {
		t.Fatal("evidence from an earlier strategy version authorized launch")
	}
}

func TestCodexBehavioralPreflightTimeoutIsBoundedAndTransient(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex"]
	rt.Binary = fake
	oldTimeout := launchPreflightTimeout
	launchPreflightTimeout = 25 * time.Millisecond
	t.Cleanup(func() { launchPreflightTimeout = oldTimeout })
	ctx, _, _ := newCtx(t.TempDir())
	started := time.Now()
	got := runBehavioralPreflight(ctx, rt, fake, model.GrantRO, "", false)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded preflight took %s", elapsed)
	}
	if got.State != store.LaunchTransient || got.Layer != store.LaunchTransport {
		t.Fatalf("timeout classified as %s/%s", got.State, got.Layer)
	}
}

// Issue #767: the old five-second probe waited for a complete inference. A
// valid lifecycle event is transport evidence; the probe must stop and reap
// the still-running provider tree as soon as that event arrives.
func TestCodexBehavioralPreflightReadinessStopsAndReapsHangingTree(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	pidFile := filepath.Join(dir, "pid")
	script := "#!/bin/sh\necho $$ > \"" + pidFile + "\"\nyes warning | head -c 65536 >&2\nwhile :; do printf 'warning-warning-warning-warning-warning-warning-warning-warning\\n' >&2; done &\nprintf '{\"type\":\"turn.'\nprintf 'started\"}\\n'\nsleep 30 &\nwait\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex-rw"]
	rt.Binary = fake
	oldTimeout := launchPreflightTimeout
	launchPreflightTimeout = time.Second
	t.Cleanup(func() { launchPreflightTimeout = oldTimeout })
	ctx, _, _ := newCtx(dir)
	started := time.Now()
	got := runBehavioralPreflight(ctx, rt, fake, model.GrantRW, "", false)
	if got.State != store.LaunchCompatible {
		t.Fatalf("readiness classified as %s/%s: %s", got.State, got.Layer, got.Detail)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("readiness waited for model completion: %s", elapsed)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for (procmon.Alive(pid) || procmon.GroupAlive(pid)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if procmon.Alive(pid) || procmon.GroupAlive(pid) {
		t.Fatalf("preflight process group %d survived readiness", pid)
	}
}

func TestCodexBehavioralPreflightAllowsReadinessBeyondLegacyDeadline(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 0.075\nprintf '%s\\n' '{\"type\":\"turn.started\"}'\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex-rw"]
	rt.Binary = fake
	oldTimeout := launchPreflightTimeout
	launchPreflightTimeout = 2 * time.Second
	t.Cleanup(func() { launchPreflightTimeout = oldTimeout })
	ctx, _, _ := newCtx(t.TempDir())
	got := runBehavioralPreflight(ctx, rt, fake, model.GrantRW, "", false)
	if got.State != store.LaunchCompatible {
		t.Fatalf("delayed readiness classified as %s/%s: %s", got.State, got.Layer, got.Detail)
	}
}

func TestCodexBehavioralPreflightMalformedStreamFailsClosed(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' '{not-json}'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex-rw"]
	rt.Binary = fake
	ctx, _, _ := newCtx(t.TempDir())
	got := runBehavioralPreflight(ctx, rt, fake, model.GrantRW, "", false)
	if got.State == store.LaunchCompatible || got.Layer != store.LaunchStartup {
		t.Fatalf("malformed stream classified as %s/%s: %s", got.State, got.Layer, got.Detail)
	}
}

func TestCodexFailureClassificationUsesProviderNeutralLayers(t *testing.T) {
	for name, fixture := range map[string]struct {
		text  string
		state store.LaunchState
		layer store.LaunchLayer
	}{
		"authentication": {"authentication required: not logged in", store.LaunchIncompatible, store.LaunchAuthentication},
		"sandbox":        {"failed to initialize in-process app-server client: Operation not permitted", store.LaunchIncompatible, store.LaunchSandbox},
		"startup":        {"process exited before ready", store.LaunchTransient, store.LaunchStartup},
		"quota":          {"quota exceeded", store.LaunchTransient, store.LaunchQuota},
		"transport":      {"connection reset by peer", store.LaunchTransient, store.LaunchTransport},
	} {
		t.Run(name, func(t *testing.T) {
			got := classifyCodexLaunchFailure(fixture.text)
			if got.State != fixture.state || got.Layer != fixture.layer {
				t.Fatalf("classified as %s/%s, want %s/%s", got.State, got.Layer, fixture.state, fixture.layer)
			}
		})
	}
}

func TestRuntimeDoctorJSONSeparatesPresenceAndLaunchProvenance(t *testing.T) {
	w := newExecWS(t)
	bin := fakeBinary(t)
	mustRuntime(t, w, store.Runtime{Name: "generic", Binary: bin, Mode: "stdin"})
	ctx, out, _ := newCtx(w.Root)
	ctx.JSON = true
	if err := cmdRuntimeDoctor(ctx, []string{"--runtime", "generic", "--grant", "rw"}); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Presence string `json:"presence"`
		Grant    string `json:"grant"`
		Launch   struct {
			State      string    `json:"state"`
			Provenance string    `json:"provenance"`
			Timestamp  time.Time `json:"command_timestamp"`
		} `json:"launch"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("doctor did not emit JSON: %v\n%s", err, out.String())
	}
	if len(rows) != 1 || rows[0].Presence != "present" || rows[0].Grant != "rw" || rows[0].Launch.State != "unsupported" || rows[0].Launch.Provenance != "declared" || rows[0].Launch.Timestamp.IsZero() {
		t.Fatalf("doctor row = %#v", rows)
	}
	if strings.Contains(out.String(), "Return no content") {
		t.Fatal("doctor JSON leaked the behavioral handshake prompt")
	}
}

func TestContextIssuesRefuseUndeclaredGlobalSkillAndOverrideRecordsSource(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".agents", "skills", "bad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex"]
	env := map[string]string{"HOME": home, "CODEX_HOME": filepath.Join(home, ".codex")}
	issues, _ := contextIssues(rt, team.Role{}, false, false, t.TempDir(), env)
	if len(issues) == 0 || !issues[0].refuse {
		t.Fatalf("strict context accepted undeclared fixture: %#v", issues)
	}
	issues, sources := contextIssues(rt, team.Role{}, false, true, t.TempDir(), env)
	for _, issue := range issues {
		if issue.refuse {
			t.Fatalf("override still refused: %#v", issues)
		}
	}
	record := contextInvocation(team.Role{Skills: []string{"declared"}}, true, true, sources)
	if !strings.Contains(record, "declared_role_skills: declared") || !strings.Contains(record, filepath.Join(home, ".agents", "skills")) || strings.Contains(record, "invalid\n") {
		t.Fatalf("invocation provenance incomplete or leaked contents:\n%s", record)
	}
}

func TestEveryShippedPresetDeclaresCompleteContextContract(t *testing.T) {
	for name, rt := range presets {
		if err := store.ValidateContextContract(rt); err != nil {
			t.Errorf("preset %s: %v", name, err)
		}
	}
}

// mustRoleWithPrompt writes a role file with a real markdown body — unlike
// store.CreateRole, which only ever writes the (short) Summary as the body
// and so can never produce a Prompt distinct from it (roleBody treats a body
// that equals the summary as empty). Mirrors the on-disk shape of a real role
// file like .dacli/roles/fixer.md: an H1 section titled after the role.
func mustRoleWithPrompt(t *testing.T, w *workspace.Workspace, r team.Role, prompt string) {
	t.Helper()
	d := &mdstore.Doc{}
	d.Front.Set("id", "role-"+r.Name)
	d.Front.Set("kind", "role")
	d.Front.Set("name", r.Name)
	if r.Runtime != "" {
		d.Front.Set("runtime", r.Runtime)
	}
	if r.Grant != "" {
		d.Front.Set("grant", r.Grant)
	}
	d.Sections = []mdstore.Section{{Level: 1, Title: r.Name, Content: prompt + "\n"}}
	if err := mdstore.WriteFile(w.RolePath(r.Name), d); err != nil {
		t.Fatal(err)
	}
}

// TestPreflightIssuesReportsEveryMismatchInOnePass is dacli 272's core claim:
// a role whose grant, binary-allowlist path, AND prompt-named tools all
// disagree with its runtime must surface all three mismatches, not just the
// first one a naive early-return would have hit.
func TestPreflightIssuesReportsEveryMismatchInOnePass(t *testing.T) {
	rt := store.Runtime{Name: "cc", Args: []string{"--allowedTools", "Read,Grep,Bash(/opt/dacli:*)"}}
	role := team.Role{Name: "junior", Prompt: "Use `WebFetch` to check the changelog before summarizing."}

	issues := preflightIssues(rt, role, true, model.GrantRW, false, "/repo/dacli")

	byClass := map[string]preflightIssue{}
	for _, iss := range issues {
		byClass[iss.class] = iss
	}
	if len(issues) != 3 {
		t.Fatalf("expected exactly 3 issues (one per class), got %d: %#v", len(issues), issues)
	}
	if iss, ok := byClass["grant-write"]; !ok {
		t.Errorf("missing grant-write issue: %#v", issues)
	} else if !iss.refuse {
		t.Errorf("grant-write without --cooperative must refuse (dacli 250)")
	}
	if iss, ok := byClass["binary-allowlist"]; !ok {
		t.Errorf("missing binary-allowlist issue: %#v", issues)
	} else if iss.refuse {
		t.Errorf("binary-allowlist must warn, not refuse (dacli 267)")
	}
	if iss, ok := byClass["prompt-tools"]; !ok {
		t.Errorf("missing prompt-tools issue: %#v", issues)
	} else if iss.refuse {
		t.Errorf("prompt-tools must warn, not refuse")
	} else if !strings.Contains(iss.message, "WebFetch") {
		t.Errorf("prompt-tools message must name the tool, got %q", iss.message)
	}
}

// TestPreflightIssuesCooperativeDowngradesGrantWriteToWarn matches sandboxFor's
// existing convention: --cooperative accepts the grant-write mismatch out
// loud rather than refusing it.
func TestPreflightIssuesCooperativeDowngradesGrantWriteToWarn(t *testing.T) {
	rt := store.Runtime{Name: "cc", SandboxRO: []string{"--allowedTools", "Read"}}
	issues := preflightIssues(rt, team.Role{}, false, model.GrantRW, true, "")
	if len(issues) != 1 || issues[0].class != "grant-write" || issues[0].refuse {
		t.Fatalf("expected one non-refusing grant-write issue under --cooperative, got %#v", issues)
	}
}

// TestPreflightIssuesNoMismatches proves a clean role/runtime/grant combo
// reports nothing — a preflight that always finds something to say is noise.
func TestPreflightIssuesNoMismatches(t *testing.T) {
	rt := store.Runtime{Name: "generic-exec", Args: []string{"-p"}}
	role := team.Role{Name: "junior", Prompt: "Write the failing test first, then read the code."}
	if issues := preflightIssues(rt, role, true, model.GrantRW, false, ""); len(issues) != 0 {
		t.Errorf("expected no issues, got %#v", issues)
	}
}

// TestCmdPreflightReportsAllAndRefuses drives the standalone command
// (dacli 272's "a single command") end to end: it must print every class's
// mismatch and exit 3 because one of them (grant-write) refuses.
func TestCmdPreflightReportsAllAndRefuses(t *testing.T) {
	w := newExecWS(t)
	// The allowlist lives in invoke_args (not just the ro sandbox) so it is
	// the EFFECTIVE args an rw child actually runs with — grant-write and
	// prompt-tools both read against it, exactly as exeAllowlistWarning does.
	mustRuntime(t, w, store.Runtime{Name: "cc", Binary: "sh", Args: []string{"--allowedTools", "Read,Grep"}})
	mustRoleWithPrompt(t, w, team.Role{Name: "junior", Runtime: "cc", Grant: "rw"},
		"Use `WebFetch` to check the changelog.")

	ctx, out, _ := newCtx(w.Root)
	err := cmdPreflight(ctx, []string{"--role", "junior"})
	if code := clikit.ExitCode(err); code != 3 {
		t.Fatalf("exit %d, want 3 (err %v)", code, err)
	}
	got := out.String()
	if !strings.Contains(got, "grant-write") {
		t.Errorf("report missing grant-write issue:\n%s", got)
	}
	if !strings.Contains(got, "prompt-tools") {
		t.Errorf("report missing prompt-tools issue:\n%s", got)
	}
}

// TestCmdPreflightNoMismatches proves the standalone command exits 0 and
// says so plainly when a role/runtime/grant combo is clean.
func TestCmdPreflightNoMismatches(t *testing.T) {
	w := newExecWS(t)
	mustRuntime(t, w, store.Runtime{Name: "rt", Binary: "sh", Args: []string{"-p"}})

	ctx, out, _ := newCtx(w.Root)
	if err := cmdPreflight(ctx, []string{"--runtime", "rt", "--grant", "rw"}); err != nil {
		t.Fatalf("expected no issues, got %v", err)
	}
	if !strings.Contains(out.String(), "no mismatches") {
		t.Errorf("expected a clean report, got:\n%s", out.String())
	}
}

// TestCmdPreflightUnknownRoleIsNotFound matches `role show`'s existing idiom
// for a name that resolves to nothing.
func TestCmdPreflightUnknownRoleIsNotFound(t *testing.T) {
	w := newExecWS(t)
	ctx, _, _ := newCtx(w.Root)
	err := cmdPreflight(ctx, []string{"--role", "nope"})
	if code := clikit.ExitCode(err); code != 4 {
		t.Fatalf("exit %d, want 4 (err %v)", code, err)
	}
}

// TestSpawnReportsEveryPreflightMismatchBeforeRefusing is the regression this
// task exists to fix: before dacli 272, a grant-write refusal inside
// sandboxFor short-circuited resolveLaunch before the binary-allowlist or
// prompt-tools checks ever ran, so a role with several real mismatches only
// ever heard about the first one. Now every applicable warning must surface
// even though the launch still refuses for the (unchanged) grant-write
// reason — the existing refusal message is untouched, but nothing it would
// have hidden is dropped anymore.
func TestSpawnReportsEveryPreflightMismatchBeforeRefusing(t *testing.T) {
	w := newExecWS(t)
	mustTask(t, w, "some task", store.TaskOpts{})
	// invoke_args (not just the ro sandbox) carries the allowlist, so it is
	// what an rw child actually runs with.
	mustRuntime(t, w, store.Runtime{Name: "rt", Binary: fakeBinary(t), Mode: "arg", Flag: "-p",
		Args: []string{"--allowedTools", "Read,Grep,Glob,LS"}})
	mustRoleWithPrompt(t, w, team.Role{Name: "junior", Runtime: "rt"},
		"Use `WebFetch` to check the changelog before summarizing.")

	ctx, _, errb := newCtx(w.Root)
	err := cmdSpawn(ctx, []string{"--task", "001", "--role", "junior", "--grant", "rw"})
	if code := clikit.ExitCode(err); code != 3 || !strings.Contains(err.Error(), "no write tool") {
		t.Fatalf("expected the existing grant-write refusal, got exit %d err %v", code, err)
	}
	if !strings.Contains(errb.String(), "WebFetch") {
		t.Errorf("expected the prompt-tools warning to surface even though grant-write refused; stderr: %s", errb.String())
	}
}

// A planner's OUTPUT is the size, so refusing it for a missing estimate asks a
// malformed question — and it deadlocked the loop: the review phase files
// unestimated tasks, sizeUnestimated spawns the capped `estimator` to size
// them, this gate refused THAT spawn, and the capped implementer then refused
// the still-unsized task. Every cycle, forever (issue #430: fourteen
// consecutive no-progress cycles, backlog 6 → 8, done stuck at 27).
func TestSeniorityGateLetsAPlannerSizeAnUnsizedTask(t *testing.T) {
	unsized := &store.Task{Seq: 344, Slug: "an-unsized-task", Doc: &mdstore.Doc{}}

	planner := team.Role{Name: "estimator", Kind: "planner", MaxPoints: 2}
	if err := seniorityGate(planner, unsized); err != nil {
		t.Errorf("a capped PLANNER must be allowed onto an unsized task — sizing is its output: %v", err)
	}

	// Every other kind still refuses: a capped role taking work of unknown
	// size is exactly what the cap exists to prevent.
	for _, kind := range []string{"implementer", "reviewer", "researcher", "designer"} {
		r := team.Role{Name: "capped-" + kind, Kind: kind, MaxPoints: 2}
		err := seniorityGate(r, unsized)
		if err == nil {
			t.Errorf("a capped %s must still refuse unsized work", kind)
			continue
		}
		if clikit.ExitCode(err) != 3 {
			t.Errorf("%s: exit %d, want 3 (policy refusal)", kind, clikit.ExitCode(err))
		}
	}
}

// The exemption is for the MISSING estimate only. Once a size exists, a
// planner's cap means what it says.
func TestSeniorityGateStillCapsAPlannerOnAnOversizedTask(t *testing.T) {
	doc := &mdstore.Doc{}
	doc.Front.Set("estimate", "{optimistic: 8, probable: 13, pessimistic: 21}")
	big := &store.Task{Seq: 345, Slug: "a-big-task", Doc: doc}
	if _, ok := big.Estimate(); !ok {
		t.Fatal("fixture has no estimate — this test would measure nothing")
	}

	planner := team.Role{Name: "estimator", Kind: "planner", MaxPoints: 2}
	err := seniorityGate(planner, big)
	if err == nil {
		t.Fatal("a planner over its cap must still be refused once the size is known")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("the refusal must name the cap: %v", err)
	}
}

// Issue #1087: readiness can create context after the strict enumeration.
// Its origin cannot be inferred from its name; the supported path is recorded
// consent, with the same claims and sandbox as the strict launch.
func TestCodexGeneratedContextRequiresRecordedException(t *testing.T) {
	w := newExecWS(t)
	mustTask(t, w, "generated context", store.TaskOpts{})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	fake := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo 'codex-cli 0.159.2'; exit 0; fi
mkdir -p "$CODEX_HOME/skills/.system"
printf 'builtin fixture\n' > "$CODEX_HOME/skills/.system/SKILL.md"
printf '[projects."/fixture"]\ntrust_level = "trusted"\n' > "$CODEX_HOME/config.toml"
printf '%s\n' '{"type":"turn.started"}' '{"type":"item.completed","item":{"type":"agent_message","text":"ready"}}' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := presets["codex-rw"]
	rt.Name, rt.Binary = "generated-fixture", fake
	mustRuntime(t, w, rt)
	ctx, _, _ := newCtx(w.Root)
	if err := cmdPreflight(ctx, []string{"--runtime", rt.Name, "--grant", "rw"}); err != nil {
		t.Fatal(err)
	}
	flags := func(override bool) *clikit.Flags {
		args := []string{"--runtime", rt.Name, "--grant", "rw", "--claim", "internal/features/execution"}
		if override {
			args = append(args, "--allow-user-config")
		}
		f, err := clikit.ParseFlags(args)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if _, err := resolveLaunch(ctx, w, flags(false), "001"); clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("strict refusal: %v", err)
	}
	plan, err := resolveLaunch(ctx, w, flags(true), "001")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ContextOverride || len(plan.ContextSources) < 3 || strings.Join(plan.Claims, ",") != "internal/features/execution" || strings.Join(append(append([]string{}, plan.Runtime.Args...), plan.Sandbox...), " ") != strings.Join(rt.Args, " ") || plan.LaunchContract.AllowUserConfig != true {
		t.Fatalf("exception lost identity: %+v", plan)
	}
	for _, content := range []string{"[mcp_servers.bad]\ncommand = \"bad\"\n", "model = \"unapproved\"\n", "[plugins.bad]\nenabled = true\n"} {
		if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveLaunch(ctx, w, flags(false), "001"); clikit.ExitCode(err) != 3 {
			t.Fatalf("arbitrary configuration allowed: %v", err)
		}
	}
	if err := cmdSpawn(ctx, []string{"--task", "001", "--runtime", rt.Name, "--grant", "rw", "--claim", "internal/features/execution", "--allow-user-config"}); err != nil {
		t.Fatal(err)
	}
	runs, err := os.ReadDir(w.RunsDir())
	if err != nil || len(runs) != 1 {
		t.Fatalf("spawn run records: %v, %v", runs, err)
	}
	invocation, err := os.ReadFile(filepath.Join(w.RunsDir(), runs[0].Name(), "invocation.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"external_context_override: true", "config.toml", "global-skills="} {
		if !strings.Contains(string(invocation), want) {
			t.Fatalf("run did not record %q: %s", want, invocation)
		}
	}
}

func TestContextExceptionCannotDowngradeSandbox(t *testing.T) {
	w := newExecWS(t)
	mustTask(t, w, "sandbox identity", store.TaskOpts{})
	rt := store.Runtime{Name: "unverified", Binary: fakeBinary(t), Mode: "stdin", SandboxRO: []string{"--sandbox", "read-only"}, ROProbe: store.RuntimeROFailed}
	mustRuntime(t, w, rt)
	ctx, _, _ := newCtx(w.Root)
	f, err := clikit.ParseFlags([]string{"--runtime", rt.Name, "--grant", "ro", "--allow-user-config"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLaunch(ctx, w, f, "001"); clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "cannot enforce read-only") {
		t.Fatalf("context exception downgraded sandbox: %v", err)
	}

	if err := cmdPreflight(ctx, []string{"--runtime", rt.Name, "--grant", "ro", "--allow-user-config"}); clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "cannot enforce read-only") {
		t.Fatalf("preflight downgraded sandbox: %v", err)
	}
}
