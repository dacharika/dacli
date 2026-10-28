package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/driver"
	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type nilContextDriverBackend struct{}

func (*nilContextDriverBackend) CapabilitiesContext(context.Context) (executor.Capabilities, error) {
	panic("typed nil backend must not be called")
}
func (*nilContextDriverBackend) RegisterContext(context.Context, executor.Registration) error {
	panic("typed nil backend must not be called")
}
func (*nilContextDriverBackend) VerifyBindingContext(context.Context, executor.Binding) error {
	panic("typed nil backend must not be called")
}
func (*nilContextDriverBackend) DispatchContext(context.Context, executor.Dispatch) (executor.HostResult, error) {
	panic("typed nil backend must not be called")
}
func (*nilContextDriverBackend) LookupContext(context.Context, string, executor.Binding) (executor.HostResult, error) {
	panic("typed nil backend must not be called")
}
func (*nilContextDriverBackend) ReadObservationContext(context.Context, string, executor.Binding, uint64) (executor.Observation, error) {
	panic("typed nil backend must not be called")
}
func (*nilContextDriverBackend) CancelContext(context.Context, executor.CancellationRequest) (string, error) {
	panic("typed nil backend must not be called")
}
func (*nilContextDriverBackend) LookupCancellationContext(context.Context, executor.CancellationRequest) (string, error) {
	panic("typed nil backend must not be called")
}

func TestNativeControllerDriverRefusesMissingHostBeforeStep(t *testing.T) {
	selection := testPinnedDriverSelection(t)
	ports := &nativeControllerDriverPorts{selection: selection}
	err := ports.requireDispatchCapability(context.Background())
	if !errors.Is(err, executor.ErrUnsupportedContextBackend) {
		t.Fatalf("step with no cooperative host returned %v, want unsupported before authority call", err)
	}
}

func TestNativeControllerDriverTreatsTypedNilHostAsMissing(t *testing.T) {
	var nilBackend *nilContextDriverBackend
	selection := testPinnedDriverSelection(t)
	selection.backend = nilBackend
	ports := &nativeControllerDriverPorts{selection: selection}
	err := ports.requireDispatchCapability(context.Background())
	if !errors.Is(err, executor.ErrUnsupportedContextBackend) {
		t.Fatalf("step with typed nil host returned %v, want unsupported", err)
	}
}

func TestNativeControllerDriverCancellationStopsBeforeHostBoundary(t *testing.T) {
	selection := testPinnedDriverSelection(t)
	ports := &nativeControllerDriverPorts{selection: selection}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ports.Cancel(ctx, sessions.ControllerAction{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel with expired context returned %v, want context cancellation", err)
	}
}

func TestNativeDriverObservationUsesIntentLeaseAndOptionalActionBindings(t *testing.T) {
	request := executor.Dispatch{OperationKey: "original-host-key", DispatchID: "dispatch-a", Repository: "/repo", Checkout: "/repo/work", Binding: executor.Binding{Task: "task-a", Attempt: "attempt-a", Session: "worker-a", Host: "codex"}}
	intent := sessions.DispatchIntent{ID: "intent-a", DispatchID: request.DispatchID, OperationKey: request.OperationKey, State: "acknowledged", BridgeSessionID: "bridge-a", WorkerSessionID: "worker-a", Reservation: sessions.DispatchReservation{ProjectID: "project-a", BaseCommit: "base", Reservation: sessions.Lease{Fence: 17, TaskID: "task-a"}}, Lease: &sessions.Lease{ID: "lease-a", AttemptID: "attempt-a", SessionID: "worker-a", TaskID: "task-a", Repository: request.Repository, Checkout: request.Checkout, Fence: 23}}
	status := sessions.LifecycleStatus{Binding: sessions.LifecycleBinding{DispatchID: intent.DispatchID, IntentID: intent.ID, BridgeSessionID: intent.BridgeSessionID, WorkerSessionID: intent.WorkerSessionID, TaskID: "task-a", Checkout: request.Checkout, BaseCommit: "base", HostAgentID: "host-exec-a", AttemptID: "attempt-a", LeaseID: "lease-a", ReservationFence: 17, LeaseFence: 23}}
	host := executor.HostResult{OperationKey: intent.OperationKey, DispatchID: intent.DispatchID, HostExecutionID: "host-exec-a", Repository: request.Repository, Checkout: request.Checkout, Binding: request.Binding}
	observation := executor.Observation{OperationKey: intent.OperationKey, Binding: request.Binding, Sequence: 2, State: "running", EvidenceDigest: "sha256:test", SourceTime: time.Now()}
	action := sessions.ControllerAction{Kind: "host_dispatch", TaskID: "task-a", DispatchID: intent.DispatchID, IntentID: intent.ID, OperationKey: intent.OperationKey, Fence: 17, ReservationFence: 17}
	if !nativeDriverObservationMatches(action, request, intent, status, host, observation) {
		t.Fatal("genuine host_dispatch observation with empty lease fields was refused")
	}
	action.LeaseID = "substituted-lease"
	if nativeDriverObservationMatches(action, request, intent, status, host, observation) {
		t.Fatal("populated action lease mismatch was ignored")
	}
	action.LeaseID = ""
	action.Fence = 24
	if nativeDriverObservationMatches(action, request, intent, status, host, observation) {
		t.Fatal("populated action lease-fence mismatch was ignored")
	}
	action.Fence = 23
	action.LeaseID = "lease-a"
	if !nativeDriverObservationMatches(action, request, intent, status, host, observation) {
		t.Fatal("populated action lease fence was not compared with the lifecycle lease fence")
	}
	action.Fence = 24
	if nativeDriverObservationMatches(action, request, intent, status, host, observation) {
		t.Fatal("populated action lease-fence mismatch was ignored")
	}
	status.Binding.LeaseFence++
	action.Fence = 0
	if nativeDriverObservationMatches(action, request, intent, status, host, observation) {
		t.Fatal("lifecycle binding that drifted from the authenticated intent lease was accepted")
	}
}

func TestNativeDriverAllowsOnlyTerminalObservationRecoveryAfterStop(t *testing.T) {
	for _, state := range []string{"completed", "failed", "cancelled"} {
		if !terminalHostObservation(state) {
			t.Errorf("terminal state %q was not classified as retained recovery evidence", state)
		}
	}
	for _, state := range []string{"accepted", "running", "unknown", ""} {
		if terminalHostObservation(state) {
			t.Errorf("nonterminal state %q bypassed current effect authorization", state)
		}
	}
}

func TestNativeDriverLifecycleSnapshotIgnoresReadClockAndRejectsDurableDrift(t *testing.T) {
	fixture := nativeHostProducerSetup(t, true)
	if _, err := fixture.bridge.Observe(fixture.observe); err != nil {
		t.Fatalf("record real lifecycle observation: %v", err)
	}
	first, err := fixture.bridge.Status(fixture.intent.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.bridge.Status(fixture.intent.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ObservedAt.Equal(second.ObservedAt) {
		t.Fatal("fixture did not exercise per-read clock metadata")
	}
	if !sameLifecycleDurableSnapshot(first, second) {
		t.Fatal("successive real lifecycle reads with unchanged durable state did not match")
	}

	drifted := second
	drifted.Binding.HostAgentID = "different-host-agent"
	if sameLifecycleDurableSnapshot(first, drifted) {
		t.Fatal("lifecycle binding drift was ignored")
	}
	drifted = second
	drifted.Observations = append([]sessions.LifecycleObservation(nil), second.Observations...)
	drifted.Observations[0].EvidenceDigest = "different-evidence"
	if sameLifecycleDurableSnapshot(first, drifted) {
		t.Fatal("lifecycle observation drift was ignored")
	}
	drifted = second
	drifted.Cancellation = &sessions.LifecycleCancellation{}
	if sameLifecycleDurableSnapshot(first, drifted) {
		t.Fatal("lifecycle cancellation drift was ignored")
	}
	drifted = second
	drifted.ResourcesRetained = !second.ResourcesRetained
	if sameLifecycleDurableSnapshot(first, drifted) {
		t.Fatal("retained-resource drift was ignored")
	}
}

func TestNativeDriverReusesValidatedTerminalLifecycleReceipt(t *testing.T) {
	fixture := nativeHostProducerSetup(t, true)
	observation := fixture.observe
	observation.State = "completed"
	observation.SourceTime = time.Now().UTC()
	observation.EvidenceDigest = sessions.OutputDigest([]byte("durable terminal observation"))
	if _, err := fixture.bridge.Observe(observation); err != nil {
		t.Fatalf("record real terminal lifecycle observation: %v", err)
	}
	lifecycle, err := fixture.bridge.Status(fixture.intent.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	lease := fixture.intent.Lease
	request := executor.Dispatch{
		OperationKey: fixture.intent.OperationKey,
		DispatchID:   fixture.intent.DispatchID,
		Repository:   lease.Repository,
		Checkout:     lease.Checkout,
		Binding: executor.Binding{
			Task: fixture.taskID, Attempt: lease.AttemptID, Session: fixture.intent.WorkerSessionID, Host: "codex",
		},
	}
	action := sessions.ControllerAction{
		Kind: "host_dispatch", TaskID: fixture.taskID, DispatchID: fixture.intent.DispatchID,
		IntentID: fixture.intent.ID, OperationKey: fixture.intent.OperationKey,
		ReservationFence: fixture.intent.Reservation.Reservation.Fence,
	}
	host := executor.HostResult{
		OperationKey: fixture.intent.OperationKey, DispatchID: fixture.intent.DispatchID,
		HostExecutionID: fixture.intent.HostAgentID, Repository: request.Repository, Checkout: request.Checkout, Binding: request.Binding,
	}
	profile := nativeControllerDriverProfile{ProjectID: "p", InvocationID: "driver-fixture", CheckpointID: "driver-checkpoint"}
	receipt, cached, err := cachedTerminalLifecycleReceipt(profile, action, request, fixture.intent, lifecycle, host)
	if err != nil || !cached {
		t.Fatalf("validated terminal lifecycle was not reusable: cached=%t receipt=%+v err=%v", cached, receipt, err)
	}
	wantKey, err := driver.OperationKey(profile.ProjectID, profile.InvocationID, profile.CheckpointID, lifecycle.Observations[len(lifecycle.Observations)-1].HostSequence, "observe", fixture.intent.OperationKey)
	if err != nil || receipt.OperationKey != wantKey || receipt.State != "completed" || receipt.Lifecycle == nil || len(receipt.Lifecycle.Observations) != 1 {
		t.Fatalf("cached terminal receipt changed its original identity: got=%+v want-key=%s err=%v", receipt, wantKey, err)
	}

	drifted := lifecycle
	drifted.Binding.LeaseID = "substituted-lease"
	if _, cached, err = cachedTerminalLifecycleReceipt(profile, action, request, fixture.intent, drifted, host); err == nil || cached {
		t.Fatal("terminal receipt reuse accepted a lifecycle binding mismatch")
	}
	driftedHost := host
	driftedHost.HostExecutionID = "substituted-host"
	if _, cached, err = cachedTerminalLifecycleReceipt(profile, action, request, fixture.intent, lifecycle, driftedHost); err == nil || cached {
		t.Fatal("terminal receipt reuse accepted a host execution mismatch")
	}
}

func TestNativeDriverCancellationRetainsOriginalHostKeyAtLaterStage(t *testing.T) {
	intent := sessions.DispatchIntent{ID: "intent-a", DispatchID: "dispatch-a", OperationKey: "original-host-key", BridgeSessionID: "bridge-a", WorkerSessionID: "worker-a", Reservation: sessions.DispatchReservation{BaseCommit: "base", Reservation: sessions.Lease{Fence: 17, TaskID: "task-a"}}, Lease: &sessions.Lease{ID: "lease-a", AttemptID: "attempt-a", Fence: 23}}
	selection := nativeDriverTaskProfile{WorkerEnrollment: executor.WorkerEnrollment{Binding: executor.Binding{Task: "task-a", Attempt: "attempt-a", Session: "worker-a", Host: "codex"}}, DispatchTemplate: executor.Dispatch{Repository: "/repo", Checkout: "/repo/work", Binding: executor.Binding{Task: "task-a", Session: "worker-a", Host: "codex"}}}
	profile := nativeControllerDriverProfile{ProjectID: "project-a", InvocationID: "invocation-a", CheckpointID: "checkpoint-a"}
	lifecycle := sessions.LifecycleBinding{DispatchID: intent.DispatchID, IntentID: intent.ID, BridgeSessionID: "bridge-a", WorkerSessionID: "worker-a", TaskID: "task-a", Checkout: "/repo/work", BaseCommit: "base", AttemptID: "attempt-a", LeaseID: "lease-a", ReservationFence: 17, LeaseFence: 23}
	// Review and verification action keys are later-stage producer keys. They
	// must never replace the original host dispatch key used for cancellation.
	action := sessions.ControllerAction{Kind: "independent_review", TaskID: "task-a", DispatchID: intent.DispatchID, OperationKey: "review-operation", AttemptID: "attempt-a", LeaseID: "lease-a", Fence: 23, ReservationFence: 17}
	request, err := cancellationForIntent(selection, action, intent, profile, lifecycle)
	if err != nil {
		t.Fatalf("build cancellation from retained intent: %v", err)
	}
	want, err := driver.OperationKey(profile.ProjectID, profile.InvocationID, profile.CheckpointID, 0, "cancel", intent.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	if request.OriginalOperationKey != intent.OperationKey || request.OperationKey != want || request.OriginalOperationKey == action.OperationKey {
		t.Fatalf("cancellation key replaced original dispatch identity: request=%+v want=%q actionKey=%q", request, want, action.OperationKey)
	}
}

func TestNativeDriverCorrelationBridgeUsesCapabilityBearingSwarm(t *testing.T) {
	fixture := nativeHostProducerSetup(t, false)
	selection := &NativeControllerDriver{profile: nativeControllerDriverProfile{CapabilityFile: fixture.capability}}
	ports := &nativeControllerDriverPorts{selection: selection}
	task := nativeDriverTaskAccess{bridge: fixture.bridge}
	bridge, err := ports.dispatchBridge(task)
	if err != nil || bridge == nil {
		t.Fatalf("open the existing bridge through the real capability-bearing swarm constructor: bridge=%v err=%v", bridge, err)
	}
	if _, _, err = bridge.authenticate(); err != nil {
		t.Fatalf("re-authenticate capability-bearing bridge: %v", err)
	}
	replayed, err := bridge.CorrelateDispatch(sessions.CorrelateDispatchIntent{
		OperationKey: "crash-correlation", DispatchID: fixture.intent.DispatchID, IntentID: fixture.intent.ID,
		HostAgentID: fixture.intent.HostAgentID, State: "host_correlated", ExpectedFence: fixture.intent.Reservation.Reservation.Fence,
		ExpectedSequence: fixture.intent.Sequence - 1,
	})
	if err != nil || replayed.State != "host_correlated" || replayed.ID != fixture.intent.ID {
		t.Fatalf("real capability-bearing constructor did not replay the exact correlation: %+v %v", replayed, err)
	}
}

func testPinnedDriverSelection(t *testing.T) *NativeControllerDriver {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.json")
	raw := []byte(`{"schema_version":1}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	selection := &NativeControllerDriver{profilePath: path, profileDigest: sha256.Sum256(raw), pinnedFiles: map[string][32]byte{}, pinnedLimits: map[string]int64{}}
	if err := selection.pinPrivate(path, 4096); err != nil {
		t.Fatal(err)
	}
	return selection
}
