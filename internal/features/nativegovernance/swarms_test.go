package nativegovernance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestNativeSwarmOwnerProvisioningUpgradedWorkerMCPAndHandoff(t *testing.T) {
	testNativeOwnerProvisioning(t, false)
}
func TestNativeDispatchOwnerProvisioningUpgradedWorkerMCPAndAcknowledgement(t *testing.T) {
	testNativeOwnerProvisioning(t, true)
}
func testNativeOwnerProvisioning(t *testing.T, dispatch bool) {
	testNativeLifecycleOwnerProvisioning(t, dispatch, false)
}
func TestNativeLifecycleOwnerCLIAndSchema9WorkerInteroperability(t *testing.T) {
	testNativeLifecycleOwnerProvisioning(t, true, true)
}
func testNativeLifecycleOwnerProvisioning(t *testing.T, dispatch, lifecycle bool) {
	f := setupVerification(t, "printf verified", true)
	if _, e := f.run(t); e != nil {
		t.Fatal(e)
	} // preserve genuine trusted evidence across upgrade
	if _, e := f.authority.ReleaseLease(f.workerToken, f.worker.SessionID, sessions.ReleaseLeaseRequest{OperationKey: "release-old", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence}); e != nil {
		t.Fatal(e)
	}
	f.task.Doc.Front.Set("claims", "[src]")
	if e := store.SaveTask(f.task); e != nil {
		t.Fatal(e)
	}
	verificationGit(t, f.w.Root, "add", ".dacli")
	verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "declare resource scope")
	private := t.TempDir()
	capability := filepath.Join(private, "capability.json")
	profile := application.ManualSwarmCapability{SchemaVersion: 1, WorkspaceID: f.w.ID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(30 * time.Minute)}
	if dispatch {
		probeFile := filepath.Join(private, "probe.json")
		probeData := dispatchProbeFixture(t)
		if e := os.WriteFile(probeFile, probeData, 0600); e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(probeData)
		profile.SchemaVersion = 2
		profile.Codex = &application.CodexDispatchCapability{SchemaVersion: 1, ProbeFile: probeFile, ProbeSHA256: hex.EncodeToString(sum[:]), NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}
	}
	verificationJSON(t, capability, profile)
	data, _ := os.ReadFile(capability)
	digest := sha256.Sum256(data)
	policy := sessions.SwarmPolicy{ProjectID: "p", Revision: "v1", Harness: "codex", CapabilityDigest: "sha256:" + hex.EncodeToString(digest[:]), Width: 1, WIP: 1, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "implementation", Kind: "implementation", Harness: "codex", Capacity: 1}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{f.task.ID: {Checkout: f.w.Root, ImplementKind: "implementation", ReviewKind: "review"}}}
	policyFile := filepath.Join(private, "policy.json")
	verificationJSON(t, policyFile, policy)
	if out, e := verificationRun(t, f.w, swarmUpgrade, "--config", f.config); e != nil {
		t.Fatalf("upgrade %v %s", e, out)
	}
	if out, e := verificationRun(t, f.w, swarmConfigure, "--config", f.config, "--policy-file", policyFile); e != nil {
		status := exec.Command("git", "-C", f.w.Root, "status", "--porcelain")
		raw, _ := status.CombinedOutput()
		t.Fatalf("configure %v %s git=%s", e, out, raw)
	}
	if dispatch {
		if out, e := verificationRun(t, f.w, dispatchUpgrade, "--config", f.config); e != nil {
			t.Fatalf("upgrade8 %v %s", e, out)
		}
	}
	if lifecycle {
		if out, e := verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return lifecycleOwner(c, a, true) }, "--config", f.config); e != nil {
			t.Fatalf("upgrade9 %v %s", e, out)
		}
		policyFile := filepath.Join(private, "lifecycle-policy.json")
		verificationJSON(t, policyFile, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: "p", Revision: "lifecycle-v1", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "unsupported"})
		if out, e := verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return lifecycleOwner(c, a, false) }, "--config", f.config, "--policy-file", policyFile); e != nil {
			t.Fatalf("configure lifecycle %v %s", e, out)
		}
	}
	controllerProvision := swarmControllerAdd
	bridgeProvision := dispatchBridgeAdd
	if lifecycle {
		controllerProvision = func(c *clikit.Ctx, a []string) error { return swarmScopedActorAdd(c, a, false, true) }
		bridgeProvision = func(c *clikit.Ctx, a []string) error { return swarmScopedActorAdd(c, a, true, true) }
	}
	controllerFile := filepath.Join(private, "controller.json")
	controllerToken := filepath.Join(private, "controller.token")
	if out, e := verificationRun(t, f.w, controllerProvision, "--config", f.config, "--project", "p", "--connection", controllerFile, "--credential", controllerToken); e != nil {
		t.Fatalf("controller %v %s", e, out)
	}
	bridgeFile := filepath.Join(private, "bridge.json")
	if dispatch {
		if out, e := verificationRun(t, f.w, bridgeProvision, "--config", f.config, "--project", "p", "--connection", bridgeFile, "--credential", filepath.Join(private, "bridge.token")); e != nil {
			t.Fatalf("bridge provision %v %s", e, out)
		}
		var bridge native.Connection
		if e := privateJSON(bridgeFile, &bridge); e != nil {
			t.Fatal(e)
		}
		bt, e := native.ReadCredential(bridge.CredentialFile)
		if e != nil {
			t.Fatal(e)
		}
		principal, e := f.authority.Authenticate(bt, bridge.SessionID)
		wantActions := "read,dispatch.correlate"
		if lifecycle {
			wantActions += ",dispatch.observe"
		}
		if e != nil || strings.Join(principal.Scope.Actions, ",") != wantActions {
			t.Fatalf("bridge overprivileged %+v %v", principal, e)
		}
	}
	beforeAuthority, e := os.ReadFile(f.worker.Authority)
	if e != nil {
		t.Fatal(e)
	}
	orphanCredential := filepath.Join(private, "must-not-orphan.token")
	if _, e := verificationRun(t, f.w, controllerProvision, "--config", f.config, "--project", "p", "--connection", controllerFile, "--credential", orphanCredential); clikit.ExitCode(e) != 3 {
		t.Fatalf("occupied output was not refused: %v", e)
	}
	afterAuthority, e := os.ReadFile(f.worker.Authority)
	if e != nil || !bytes.Equal(beforeAuthority, afterAuthority) {
		t.Fatal("occupied controller output minted authority", e)
	}
	if _, e = os.Stat(orphanCredential); !os.IsNotExist(e) {
		t.Fatal("failed controller setup orphaned new credential", e)
	}

	workerServer, e := native.Open(f.connection)
	if e != nil {
		t.Fatal("current worker MCP cannot open owner-upgraded authority", e)
	}
	var output bytes.Buffer
	request := "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"identity\",\"arguments\":{\"schema_version\":1}}}\n"
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture"}}}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	if e = workerServer.Serve(strings.NewReader(init+request), &output); e != nil || !strings.Contains(output.String(), f.worker.SessionID) || strings.Contains(output.String(), "\"isError\":true") {
		t.Fatalf("upgraded MCP identity failed %v %s", e, output.String())
	}
	runOperation := func(kind, connection string, input any) string {
		t.Helper()
		args := []string{"--connection", connection, "--capability-file", capability, "--json"}
		if kind == "plan" {
			args = append(args, "--project", "p")
		} else {
			requestFile := filepath.Join(private, kind+"-request.json")
			verificationJSON(t, requestFile, input)
			args = append(args, "--request", requestFile)
		}
		out, e := verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, kind) }, args...)
		if e != nil {
			t.Fatalf("%s operation %v %s", kind, e, out)
		}
		return out
	}

	controller, e := application.OpenNativeSwarm(controllerFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := controller.Plan("p")
	if e != nil || len(plan.Verdicts) != 1 || !plan.Verdicts[0].Selected {
		t.Fatalf("plan %v %+v", e, plan)
	}
	var cliPlan sessions.SwarmPlan
	if e = json.Unmarshal([]byte(runOperation("plan", controllerFile, nil)), &cliPlan); e != nil || cliPlan.ID != plan.ID {
		t.Fatalf("CLI preview disagrees %v", e)
	}
	var wave sessions.SwarmWave
	if e = json.Unmarshal([]byte(runOperation("reserve", controllerFile, sessions.ReserveWaveRequest{OperationKey: "reserve", Plan: cliPlan, ExpiresAt: time.Now().Add(10 * time.Minute)})), &wave); e != nil {
		t.Fatal(e)
	}

	d := wave.Dispatches[0]
	runOperation("admit", controllerFile, sessions.AdmitDispatchRequest{DispatchID: d.ID, ExpectedFence: d.Reservation.Fence})
	at, e := f.authority.RegisterAttempt(f.workerToken, f.worker.SessionID, sessions.AttemptRequest{RequestID: "fresh-worker", TaskID: f.task.ID, Checkout: f.w.Root})
	if e != nil {
		t.Fatal(e)
	}
	_, e = application.OpenNativeSwarm(f.connection, capability)
	if e != nil {
		t.Fatal(e)
	}
	req := sessions.HandoffDispatchRequest{OperationKey: "handoff", DispatchID: d.ID, AttemptID: at.ID, ExpectedFence: d.Reservation.Fence}
	var lease sessions.Lease
	if !dispatch {
		e = json.Unmarshal([]byte(runOperation("handoff", f.connection, req)), &lease)
	} else {
		var bridge native.Connection
		if e := privateJSON(bridgeFile, &bridge); e != nil {
			t.Fatal(e)
		}
		request := sessions.RequestDispatchIntent{OperationKey: "intent", DispatchID: d.ID, BridgeSessionID: bridge.SessionID, WorkerSessionID: f.worker.SessionID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence}
		var permit sessions.DispatchIntentResult
		if e = json.Unmarshal([]byte(runOperation("dispatch-request", controllerFile, request)), &permit); e != nil || !permit.HostActionAuthorized {
			t.Fatalf("CLI permit %+v %v", permit, e)
		}
		var replay sessions.DispatchIntentResult
		if e = json.Unmarshal([]byte(runOperation("dispatch-request", controllerFile, request)), &replay); e != nil || replay.HostActionAuthorized {
			t.Fatal("CLI replay launched", e)
		}
		var correlated sessions.DispatchIntent
		correlation := sessions.CorrelateDispatchIntent{OperationKey: "correlation", DispatchID: d.ID, IntentID: permit.Intent.ID, HostAgentID: "/root/native_worker", State: "host_correlated", ExpectedFence: d.Reservation.Fence, ExpectedSequence: 1}
		if e = json.Unmarshal([]byte(runOperation("dispatch-correlate", bridgeFile, correlation)), &correlated); e != nil {
			t.Fatal(e)
		}
		var acknowledged sessions.DispatchIntent
		ack := sessions.AcknowledgeDispatchIntent{OperationKey: "ack", DispatchID: d.ID, IntentID: permit.Intent.ID, AttemptID: at.ID, HostAgentID: correlated.HostAgentID, ExpectedFence: d.Reservation.Fence, ExpectedSequence: correlated.Sequence}
		if e = json.Unmarshal([]byte(runOperation("dispatch-acknowledge", f.connection, ack)), &acknowledged); e != nil || acknowledged.Lease == nil {
			t.Fatalf("CLI acknowledgement %+v %v", acknowledged, e)
		}
		lease = *acknowledged.Lease
		var status sessions.DispatchIntent
		if e = json.Unmarshal([]byte(runOperation("dispatch-status", f.connection, struct{ DispatchID string }{d.ID})), &status); e != nil || status.State != "acknowledged" {
			t.Fatal("CLI status", e)
		}
		if lifecycle {
			exerciseLifecycleCLI(t, f, private, controllerFile, bridgeFile, acknowledged)
		}
		if old := os.Getenv("DACLI_SCHEMA7_BINARY"); old != "" {
			cmd := exec.Command(old, "native", "serve", "--connection", f.connection)
			cmd.Stdin = strings.NewReader(init + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"identity","arguments":{"schema_version":1}}}` + "\n")
			raw, e := cmd.CombinedOutput()
			if e == nil || !strings.Contains(string(raw), "refused") {
				t.Fatalf("actual old7 accepted8 %v %s", e, raw)
			}
		}
	}
	if e != nil || lease.SessionID != f.worker.SessionID {
		t.Fatalf("separate worker handoff %v %+v", e, lease)
	}
	if lifecycle {
		before, _ := os.ReadFile(f.worker.Authority)
		if old := os.Getenv("DACLI_SCHEMA8_BINARY"); old != "" {
			cmd := exec.Command(old, "native", "serve", "--connection", f.connection)
			cmd.Stdin = strings.NewReader(init + request)
			raw, e := cmd.CombinedOutput()
			if e == nil || !strings.Contains(string(raw), "refused") {
				t.Fatalf("actual old8 accepted9 %v %s", e, raw)
			}
		}
		after, _ := os.ReadFile(f.worker.Authority)
		if !bytes.Equal(before, after) {
			t.Fatal("old8 writer changed schema9 authority")
		}
	}
	// Old verifier profiles and evidence remain readable after exact schema upgrade.
	if _, e = application.OpenNativeVerificationProfile(f.connection, f.runnerProfile); e != nil {
		t.Fatal("upgrade lost verifier profile", e)
	}
	if actualOld := os.Getenv("DACLI_SCHEMA6_BINARY"); actualOld != "" {
		cmd := exec.Command(actualOld, "native", "serve", "--connection", f.connection)
		cmd.Stdin = strings.NewReader(request)
		raw, e := cmd.CombinedOutput()
		if e == nil || !strings.Contains(string(raw), "refused") {
			t.Fatalf("actual old schema6 binary accepted7 %v %s", e, raw)
		}
	}
}
func TestNativeSwarmOwnerCommandsNeverBorrowAmbientAuthority(t *testing.T) {
	f := setupVerification(t, "printf verified", false)
	private := t.TempDir()
	bad := filepath.Join(private, "owner.json")
	var c ownerConfig
	if e := privateJSON(f.config, &c); e != nil {
		t.Fatal(e)
	}
	c.OwnerCredentialFile = f.worker.CredentialFile
	verificationJSON(t, bad, c)
	gitDirectory := filepath.Join(private, "bin")
	if e := os.Mkdir(gitDirectory, 0700); e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(private, "git-must-not-run")
	if e := os.WriteFile(filepath.Join(gitDirectory, "git"), []byte("#!/bin/sh\nprintf observed > \"$DACLI_SWARM_GIT_MARKER\"\nexit 91\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", gitDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DACLI_SWARM_GIT_MARKER", marker)

	t.Setenv("DACLI_AGENT", "poisoned")
	for _, tc := range []struct {
		handler func(*clikit.Ctx, []string) error
		args    []string
	}{
		{swarmUpgrade, []string{"--config", bad}},
		{swarmConfigure, []string{"--config", bad, "--policy-file", bad}},
		{swarmControllerAdd, []string{"--config", bad, "--project", "p", "--connection", filepath.Join(private, "connection"), "--credential", filepath.Join(private, "credential")}},
	} {
		_, e := verificationRun(t, f.w, tc.handler, tc.args...)
		if clikit.ExitCode(e) != 3 {
			t.Fatalf("owner authority boundary not exercised: %v", e)
		}
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("forged owner caused Git observation before authentication", e)
	}
}

func exerciseLifecycleCLI(t *testing.T, f verificationFixture, private, controllerFile, bridgeFile string, i sessions.DispatchIntent) {
	t.Helper()
	run := func(kind, connection string, input any) string {
		t.Helper()
		path := filepath.Join(private, "lifecycle-"+kind+".json")
		verificationJSON(t, path, input)
		out, e := verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return lifecycleOperation(c, a, kind) }, "--connection", connection, "--request", path, "--json")
		if e != nil {
			t.Fatalf("lifecycle %s: %v %s", kind, e, out)
		}
		return out
	}
	b := sessions.LifecycleBinding{DispatchID: i.DispatchID, IntentID: i.ID, BridgeSessionID: i.BridgeSessionID, WorkerSessionID: i.WorkerSessionID, TaskID: i.Reservation.Reservation.TaskID, Checkout: i.Reservation.Reservation.Checkout, BaseCommit: i.Reservation.BaseCommit, HostAgentID: i.HostAgentID, AttemptID: i.Lease.AttemptID, LeaseID: i.Lease.ID, ReservationFence: i.Reservation.Reservation.Fence, LeaseFence: i.Lease.Fence}
	obs := sessions.ObserveLifecycleRequest{OperationKey: "running-observation", Binding: b, HostSequence: 1, State: "running", Source: "owner-manual-native-host", SourceTime: time.Now().Add(-2 * time.Minute), EvidenceDigest: sessions.OutputDigest([]byte("host heartbeat"))}
	var observation sessions.LifecycleObservation
	if e := json.Unmarshal([]byte(run("observe", bridgeFile, obs)), &observation); e != nil || observation.Sequence != 1 {
		t.Fatalf("CLI observation %+v %v", observation, e)
	}
	var status sessions.LifecycleStatus
	if e := json.Unmarshal([]byte(run("status", bridgeFile, struct{ DispatchID string }{i.DispatchID})), &status); e != nil || status.State != "unknown" || status.Freshness != "stale" {
		t.Fatalf("CLI stale status %+v %v", status, e)
	}
	var cancel sessions.LifecycleCancellationResult
	req := sessions.CancelLifecycleRequest{OperationKey: "cancel", Binding: b}
	if e := json.Unmarshal([]byte(run("cancel", controllerFile, req)), &cancel); e != nil || cancel.HostActionAuthorized || cancel.Cancellation.State != "unsupported" {
		t.Fatalf("CLI unsupported cancellation %+v %v", cancel, e)
	}
	if e := json.Unmarshal([]byte(run("cancel", controllerFile, req)), &cancel); e != nil || cancel.HostActionAuthorized {
		t.Fatalf("CLI cancel replay launched %+v %v", cancel, e)
	}
	// The old worker MCP remains authenticated for reads, but exact cancelled
	// lease mutations and new attempts cannot restore task authority.
	_, e := f.authority.ReleaseLease(f.workerToken, f.worker.SessionID, sessions.ReleaseLeaseRequest{OperationKey: "cancelled-release", AttemptID: i.Lease.AttemptID, LeaseID: i.Lease.ID, ExpectedFence: i.Lease.Fence})
	if e == nil {
		t.Fatal("CLI cancelled writer released retained resources")
	}
	obs.OperationKey = "late-terminal"
	obs.ExpectedSequence = 1
	obs.HostSequence = 2
	obs.State = "completed"
	obs.SourceTime = time.Now()
	run("observe", bridgeFile, obs)
	if e := json.Unmarshal([]byte(run("status", bridgeFile, struct{ DispatchID string }{i.DispatchID})), &status); e != nil || status.State != "completed" || status.Cancellation.State != "unsupported" || !status.ResourcesRetained {
		t.Fatalf("CLI terminal conflated cancel or released resources %+v %v", status, e)
	}
}
