package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

// These tests use a synthetic capability fixture to exercise public producer
// APIs across real process loss; the fixture is not an actual Codex swarm receipt.

const nativeHostProducerCrashEnv = "DACLI_TEST_NATIVE_HOST_PRODUCER_CRASH"

type nativeHostProducerCrashRequest struct {
	Authority, WorkspaceID, WorkerToken, RunnerToken, RunnerID, Marker string
	Kind, ConnectionPath, ProfilePath, CapabilityPath                  string
	Review                                                             NativeReviewRequest
	Lifecycle                                                          sessions.ObserveLifecycleRequest
	Dispatch                                                           sessions.AcknowledgeDispatchIntent
	Check                                                              sessions.CheckDescriptor
	GrantID                                                            string
	Request                                                            NativeVerificationRequest
}

// This helper exits only after the public application producer returned its
// durable receipt, modeling loss before the supervising caller can observe it.
func TestNativeHostProducerCrashHelper(t *testing.T) {
	path := os.Getenv(nativeHostProducerCrashEnv)
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r nativeHostProducerCrashRequest
	if json.Unmarshal(raw, &r) != nil {
		t.Fatal("invalid host producer crash request")
	}
	if r.Kind == "review" {
		profile, err := OpenNativeReviewProfile(r.ConnectionPath, r.ProfilePath)
		if err != nil {
			t.Fatal(err)
		}
		access, err := profile.Authenticate()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = access.Propose(r.Review); err != nil {
			t.Fatal(err)
		}
		os.Exit(92)
	}
	if r.Kind == "dispatch" {
		access, err := OpenNativeSwarm(r.ConnectionPath, r.CapabilityPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = access.AcknowledgeDispatch(r.Dispatch); err != nil {
			t.Fatal(err)
		}
		os.Exit(93)
	}
	if r.Kind == "lifecycle" {
		access, err := OpenNativeLifecycle(r.ConnectionPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = access.Observe(r.Lifecycle); err != nil {
			t.Fatal(err)
		}
		os.Exit(94)
	}
	a, err := sessions.Open(r.Authority, sessions.Binding{Workspace: r.WorkspaceID, Installation: "installation"})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewNativeVerificationRunner(a, r.RunnerToken, r.RunnerID, []NativeCheck{{Descriptor: r.Check, GrantID: r.GrantID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(context.Background(), r.WorkerToken, r.Request); err != nil {
		t.Fatal(err)
	}
	os.Exit(91)
}

type nativeHostProducerFixture struct {
	worker                                         *NativeSwarmAccess
	bridge                                         *NativeLifecycleAccess
	workerConnection, bridgeConnection, capability string
	ack                                            sessions.AcknowledgeDispatchIntent
	observe                                        sessions.ObserveLifecycleRequest
	intent                                         sessions.DispatchIntent
	taskID                                         string
}

func nativeHostProducerSetup(t *testing.T, seedAcknowledgement bool) nativeHostProducerFixture {
	f := nativeSetup(t, "printf checked")
	for _, upgrade := range []struct {
		name string
		run  func(string) error
	}{{"swarms", f.authority.UpgradeSwarms}, {"dispatch", f.authority.UpgradeDispatchIntents}, {"lifecycle", f.authority.UpgradeLifecycle}} {
		if err := upgrade.run(f.owner); err != nil {
			t.Fatalf("upgrade %s: %v", upgrade.name, err)
		}
	}
	var err error
	probePath := filepath.Join(t.TempDir(), "dispatch-probe.json")
	probe := map[string]any{}
	probeRaw := []byte(`{"schema_version":1,"probe":"dacli-codex-native-dispatch/v1","observed_at":"2000-01-01T00:00:00Z","host_version":"codex-cli 0.159.2","binary_sha256":"50ac633af64851511f9bbc71032cdae7f1ba20b3234c189687d61ba846c354c5","client_protocol_sha256":"f21e6307c8c2c7f2b5a32d434cdca238e3ec19b71a1f40b60f8c305289860436","declared_method_count":170,"app_server_dispatch":{"agent/spawn":{"declared_in_protocol":false,"unknown_method_refusal_observed":true,"status":"unsupported"},"collaboration/spawn_agent":{"declared_in_protocol":false,"unknown_method_refusal_observed":true,"status":"unsupported"}},"thread_config_schema":{"ThreadStartParams":{"config_field_declared":true,"schema_sha256":"dc8bebec4181a5502fc141b0122cc48e066b3b5c77d9fd2a4de875fcd6d66a68"},"ThreadForkParams":{"config_field_declared":true,"schema_sha256":"ea9dd8fb39979f32293a31f98e599509430b7cc80840b2a0afc90a2b953e942e"}},"native_collaboration":{"status":"requires-native-host-attestation"},"private_child_profile_delivery":{"status":"unverified"},"stable_dispatch_key_lookup":{"status":"unverified"},"cancellation":{"method_declared":true,"recognized_method_refusal":true,"running_turn_cancellation":"unverified"},"scope":{"isolated_codex_home":true,"thread_created":false,"model_turn_requested":false,"worker_credentials_read":false,"native_child_dispatched":false},"app_server_exit_code":0}`)
	if err = json.Unmarshal(probeRaw, &probe); err != nil {
		t.Fatal(err)
	}
	probe["observed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	probeRaw, err = json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(probePath, probeRaw, 0600); err != nil {
		t.Fatal(err)
	}
	probeSum := sha256.Sum256(probeRaw)
	capPath := filepath.Join(t.TempDir(), "capability.json")
	capability := ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: f.workspace.ID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(50 * time.Minute).UTC(), Codex: &CodexDispatchCapability{SchemaVersion: 1, ProbeFile: probePath, ProbeSHA256: hex.EncodeToString(probeSum[:]), NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}}
	capBytes, err := json.Marshal(capability)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(capPath, capBytes, 0600); err != nil {
		t.Fatal(err)
	}
	capDigest := sha256.Sum256(capBytes)
	capDigestText := "sha256:" + hex.EncodeToString(capDigest[:])
	checkoutPath := filepath.Join(t.TempDir(), "dispatch-worker")
	if err = os.Mkdir(checkoutPath, 0700); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, checkoutPath, "init", "-b", "main")
	if err = os.WriteFile(filepath.Join(checkoutPath, "dispatch-file"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, checkoutPath, "add", "dispatch-file")
	nativeGit(t, checkoutPath, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "base")
	checkoutPath, err = filepath.EvalSymlinks(checkoutPath)
	if err != nil {
		t.Fatal(err)
	}
	checkoutObservation, err := checkout.Observe(checkoutPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := "dispatch-file"
	task, err := store.CreateTask(f.workspace, "root", "p", "Native dispatch crash fixture", store.TaskOpts{Priority: "must", Claims: []string{claim}})
	if err != nil {
		t.Fatal(err)
	}
	taskID := task.ID
	if err = f.authority.BindTask(f.owner, sessions.TaskBinding{TaskID: taskID, ProjectID: "p", Repository: checkoutObservation.Repository, AdmissionEnabled: true}); err != nil {
		t.Fatal(err)
	}
	workerSession, workerToken, err := f.authority.Enroll(f.owner, sessions.Scope{Actions: []string{"attempt.register", "lease.acquire", "lease.release", "attempt.publish", "evidence.submit", "verification.request"}, Resources: []string{"project:p", "task:" + taskID, "checkout:" + checkoutPath, "path:" + claim}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	workerAttempt, err := f.authority.RegisterAttempt(workerToken, workerSession.ID, sessions.AttemptRequest{RequestID: "native-dispatch-worker", TaskID: taskID, Checkout: checkoutPath})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.authority.ConfigureSwarm(f.owner, sessions.SwarmPolicy{ProjectID: "p", Revision: "crash-policy", Harness: "codex", CapabilityDigest: capDigestText, Width: 2, WIP: 5, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "impl", Kind: "implement", Harness: "codex", Capacity: 5}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 5}}, Placements: map[string]sessions.SwarmPlacement{taskID: {Checkout: checkoutPath, ImplementKind: "implement", ReviewKind: "review"}}}); err != nil {
		t.Fatalf("configure swarm: %v", err)
	}
	if err = f.authority.ConfigureLifecycle(f.owner, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: "p", Revision: "lifecycle-v1", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "owner-manual-native-host"}); err != nil {
		t.Fatalf("configure lifecycle: %v", err)
	}
	admission, err := NewSwarmAdmission(f.workspace, manualSwarmCapabilityFile{path: capPath})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := sessions.OpenSwarm(f.path, sessions.Binding{Workspace: f.workspace.ID, Installation: "installation"}, admission)
	if err != nil {
		t.Fatal(err)
	}
	controller, ct, err := authority.Enroll(f.owner, sessions.Scope{Actions: []string{"swarm.plan", "swarm.reserve", "swarm.dispatch"}, Resources: []string{"project:p", "task:" + taskID, "checkout:" + checkoutPath, "path:" + claim}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("plan swarm: %v", err)
	}
	plan, err := authority.PlanSwarm(ct, controller.ID, "p")
	if err != nil {
		t.Fatal(err)
	}
	selected := false
	for _, verdict := range plan.Verdicts {
		selected = selected || verdict.Selected
	}
	if !selected {
		t.Fatalf("crash fixture task not selected: %+v", plan.Verdicts)
	}
	wave, err := authority.ReserveWave(ct, controller.ID, sessions.ReserveWaveRequest{OperationKey: "crash-wave", Plan: plan, ExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil || len(wave.Dispatches) != 1 {
		t.Fatalf("dispatch reservation: %+v %v", wave, err)
	}
	d := wave.Dispatches[0]
	if d.Reservation.TaskID != taskID {
		t.Fatalf("reserved wrong fixture task: reserved=%s wanted=%s", d.Reservation.TaskID, taskID)
	}
	bridge, bt, err := authority.Enroll(f.owner, sessions.Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: []string{"project:p", "task:" + taskID, "path:" + claim}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("open worker native swarm: %v", err)
	}
	request := sessions.RequestDispatchIntent{OperationKey: "crash-intent", DispatchID: d.ID, BridgeSessionID: bridge.ID, WorkerSessionID: workerSession.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence}
	if _, err = authority.Authorize(workerToken, workerSession.ID, f.workspace.ID, "lease.acquire", "project:p"); err != nil {
		t.Fatalf("worker lease authority: %v", err)
	}
	if _, err = authority.Authorize(bt, bridge.ID, f.workspace.ID, "dispatch.correlate", "project:p"); err != nil {
		t.Fatalf("bridge correlation authority: %v", err)
	}
	if _, err = authority.AdmitDispatch(ct, controller.ID, sessions.AdmitDispatchRequest{DispatchID: d.ID, ExpectedFence: d.Reservation.Fence}); err != nil {
		t.Fatalf("dispatch admission precheck: %v", err)
	}
	intentResult, err := authority.RequestDispatchIntent(ct, controller.ID, request)
	if err != nil {
		t.Fatalf("request dispatch intent: %v", err)
	}
	intent, err := authority.CorrelateDispatchIntent(bt, bridge.ID, sessions.CorrelateDispatchIntent{OperationKey: "crash-correlation", DispatchID: d.ID, IntentID: intentResult.Intent.ID, HostAgentID: "fixture-host-worker", State: "host_correlated", ExpectedFence: d.Reservation.Fence, ExpectedSequence: intentResult.Intent.Sequence})
	if err != nil {
		t.Fatalf("native dispatch acknowledgement setup: %v", err)
	}
	makeConnection := func(session sessions.Session, token, name string) string {
		credential := filepath.Join(t.TempDir(), name+".token")
		if err := os.WriteFile(credential, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), name+".json")
		data, err := json.Marshal(nativeconfig.Connection{SchemaVersion: 1, Workspace: f.workspace.Root, WorkspaceID: f.workspace.ID, Authority: f.path, InstallationID: "installation", SessionID: session.ID, CredentialFile: credential})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	workerConn := makeConnection(workerSession, workerToken, "worker")
	bridgeConn := makeConnection(bridge, bt, "bridge")
	worker, err := OpenNativeSwarm(workerConn, capPath)
	if err != nil {
		t.Fatal(err)
	}
	bridgeAccess, err := OpenNativeLifecycle(bridgeConn)
	if err != nil {
		t.Fatal(err)
	}
	ack := sessions.AcknowledgeDispatchIntent{OperationKey: "crash-ack", DispatchID: d.ID, IntentID: intent.ID, AttemptID: workerAttempt.ID, HostAgentID: intent.HostAgentID, ExpectedFence: d.Reservation.Fence, ExpectedSequence: intent.Sequence}
	current := intent
	if seedAcknowledgement {
		setupAck := ack
		setupAck.OperationKey = "setup-ack"
		current, err = worker.AcknowledgeDispatch(setupAck)
		if err != nil {
			t.Fatal(err)
		}
	}
	binding := sessions.LifecycleBinding{DispatchID: current.DispatchID, IntentID: current.ID, BridgeSessionID: current.BridgeSessionID, WorkerSessionID: current.WorkerSessionID, TaskID: taskID, Checkout: checkoutPath, BaseCommit: current.Reservation.BaseCommit, HostAgentID: current.HostAgentID, AttemptID: workerAttempt.ID, ReservationFence: current.Reservation.Reservation.Fence}
	if current.Lease != nil {
		binding.LeaseID = current.Lease.ID
		binding.LeaseFence = current.Lease.Fence
	}
	obsReq := sessions.ObserveLifecycleRequest{OperationKey: "crash-lifecycle", Binding: binding, ExpectedSequence: 0, HostSequence: 1, State: "running", Source: "owner-manual-native-host", EvidenceDigest: sessions.OutputDigest([]byte("running")), SourceTime: time.Now().UTC()}
	return nativeHostProducerFixture{worker: worker, bridge: bridgeAccess, workerConnection: workerConn, bridgeConnection: bridgeConn, capability: capPath, ack: ack, observe: obsReq, intent: current, taskID: taskID}
}

func TestNativeDispatchAcknowledgementProducerProcessLossThenExactRetry(t *testing.T) {
	f := nativeHostProducerSetup(t, false)
	r := nativeHostProducerCrashRequest{Kind: "dispatch", ConnectionPath: f.workerConnection, CapabilityPath: f.capability, Dispatch: f.ack}
	runNativeHostProducerCrash(t, r, 93)
	beforeReplay, err := os.ReadFile(f.workerConnectionAuthorityPath(t))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.worker.AcknowledgeDispatch(f.ack)
	if err != nil || retry.ID != f.intent.ID || retry.State != "acknowledged" || retry.Sequence != f.intent.Sequence+1 {
		t.Fatalf("acknowledgement replay changed durable dispatch: %+v err=%v", retry, err)
	}
	expectedLeaseFence := f.intent.Reservation.Reservation.Fence + 1
	if retry.Lease == nil || retry.Lease.AttemptID != f.ack.AttemptID || retry.Lease.SessionID != f.intent.WorkerSessionID || retry.Lease.TaskID != f.taskID || retry.Lease.Fence != expectedLeaseFence || retry.Lease.Released {
		t.Fatalf("acknowledgement replay lost or changed lease authority: lease=%+v expected attempt=%s session=%s task=%s fence=%d", retry.Lease, f.ack.AttemptID, f.intent.WorkerSessionID, f.taskID, expectedLeaseFence)
	}
	afterReplay, err := os.ReadFile(f.workerConnectionAuthorityPath(t))
	if err != nil || !bytes.Equal(beforeReplay, afterReplay) {
		t.Fatalf("acknowledgement replay rewrote authority bytes: readErr=%v", err)
	}
}

func TestNativeLifecycleObservationProducerProcessLossThenExactRetry(t *testing.T) {
	f := nativeHostProducerSetup(t, true)
	r := nativeHostProducerCrashRequest{Kind: "lifecycle", ConnectionPath: f.bridgeConnection, Lifecycle: f.observe}
	runNativeHostProducerCrash(t, r, 94)
	beforeReplay, err := os.ReadFile(f.bridgeConnectionAuthorityPath(t))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.bridge.Observe(f.observe)
	if err != nil || retry.Sequence != 1 || retry.State != "running" || retry.EvidenceDigest != f.observe.EvidenceDigest {
		t.Fatalf("lifecycle replay changed observation: %+v %v", retry, err)
	}
	status, err := f.bridge.Status(f.observe.Binding.DispatchID)
	if err != nil || len(status.Observations) != 1 || status.Observations[0].Sequence != retry.Sequence {
		t.Fatalf("observation was duplicated or lost: %+v %v", status, err)
	}
	afterReplay, err := os.ReadFile(f.bridgeConnectionAuthorityPath(t))
	if err != nil || !bytes.Equal(beforeReplay, afterReplay) {
		t.Fatalf("lifecycle replay rewrote authority bytes: readErr=%v", err)
	}
}

func (f nativeHostProducerFixture) workerConnectionAuthorityPath(t *testing.T) string {
	t.Helper()
	return connectionAuthorityPath(t, f.workerConnection)
}

func (f nativeHostProducerFixture) bridgeConnectionAuthorityPath(t *testing.T) string {
	t.Helper()
	return connectionAuthorityPath(t, f.bridgeConnection)
}

func connectionAuthorityPath(t *testing.T, connectionPath string) string {
	t.Helper()
	raw, err := os.ReadFile(connectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var c nativeconfig.Connection
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c.Authority
}

func runNativeHostProducerCrash(t *testing.T, request nativeHostProducerCrashRequest, want int) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "request.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeHostProducerCrashHelper$")
	cmd.Env = append(os.Environ(), nativeHostProducerCrashEnv+"="+path)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != want {
		t.Fatalf("producer did not exit after durable receipt with %d: %v %s", want, err, output)
	}
}

func TestNativeIndependentReviewProducerProcessLossThenExactRetry(t *testing.T) {
	f := reviewProfileSetup(t)
	request := nativeHostProducerCrashRequest{Kind: "review", ConnectionPath: f.connectionPath, ProfilePath: f.profilePath, Review: f.request}
	runNativeHostProducerCrash(t, request, 92)
	access, err := f.selected.Authenticate()
	if err != nil {
		t.Fatal(err)
	}
	review, err := access.Propose(f.request)
	if err != nil || review.ID == "" {
		t.Fatalf("exact replay did not return committed review receipt: %+v %v", review, err)
	}
	replay, err := access.Propose(f.request)
	if err != nil || replay.ID != review.ID {
		t.Fatalf("review retry changed receipt: first=%+v replay=%+v err=%v", review, replay, err)
	}
	var ledger struct{ Reviews map[string]sessions.Review }
	stateBytes, readErr := os.ReadFile(f.path)
	if readErr != nil || json.Unmarshal(stateBytes, &ledger) != nil {
		t.Fatalf("read durable review ledger: %v", readErr)
	}
	count := 0
	for _, record := range ledger.Reviews {
		if record.Request.OperationKey == f.request.OperationKey {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("exact operation produced %d review records, want one", count)
	}
	eligibility, err := access.Evaluate(review.ID)
	if err != nil || !eligibility.Eligible {
		t.Fatalf("committed review is not eligible: %+v %v", eligibility, err)
	}
}

func TestNativeVerificationProducerProcessLossThenExactRetry(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executions")
	f := nativeSetup(t, "printf x >> '"+marker+"'")
	request := nativeHostProducerCrashRequest{Authority: f.path, WorkspaceID: f.authorityBinding(), WorkerToken: f.workerToken, RunnerToken: f.runnerToken, RunnerID: f.runner.ID, Marker: marker, Check: f.check, GrantID: f.grant.ID, Request: f.request}
	runNativeHostProducerCrash(t, request, 91)
	a, err := sessions.Open(f.path, sessions.Binding{Workspace: f.authorityBinding(), Installation: "installation"})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewNativeVerificationRunner(a, f.runnerToken, f.runner.ID, []NativeCheck{{Descriptor: f.check, GrantID: f.grant.ID}})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := restarted.Run(context.Background(), f.workerToken, f.request)
	if err != nil || retry.Provenance != "trusted-local-runner" {
		t.Fatalf("exact retry failed: %+v %v", retry, err)
	}
	if count, readErr := os.ReadFile(marker); readErr != nil || string(count) != "x" {
		t.Fatalf("command executed after replay: %q %v", count, readErr)
	}
	lookup, err := a.LookupVerification(f.workerToken, f.runnerToken, f.runner.ID, sessions.VerificationRequest{OperationKey: f.request.OperationKey, WorkerSession: f.worker.ID, AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, RevisionID: f.request.RevisionID, CheckID: f.request.CheckID, GrantID: f.grant.ID, ExpectedFence: f.request.ExpectedFence}, sessions.CheckDigest(f.check))
	if err != nil || lookup.State != sessions.VerificationCompleted || lookup.Evidence == nil || lookup.Evidence.ID != retry.ID {
		t.Fatalf("unknown outcome was promoted or receipt changed: %+v %v", lookup, err)
	}
}
