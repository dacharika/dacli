package driver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type structPorts struct{}

func (structPorts) Read(context.Context, Request) (sessions.ControllerDriverView, error) {
	panic("unused")
}
func (structPorts) Step(context.Context, sessions.ControllerStepRequest) (sessions.ControllerStatus, sessions.ControllerHostDispatchPermit, error) {
	panic("unused")
}
func (structPorts) Stop(context.Context, sessions.ControllerStepRequest) (sessions.ControllerStatus, error) {
	panic("unused")
}
func (structPorts) Reconcile(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	panic("unused")
}
func (structPorts) Dispatch(context.Context, sessions.ControllerHostDispatchPermit, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	panic("unused")
}
func (structPorts) Observe(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	panic("unused")
}
func (structPorts) Verify(context.Context, sessions.ControllerAction, sessions.VerificationRequest, EffectBudget) ([]Receipt, error) {
	panic("unused")
}
func (structPorts) Review(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	panic("unused")
}
func (structPorts) Cancel(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error) {
	panic("unused")
}

var _ Ports = structPorts{}

func TestOperationKeyPinsTupleAndPurpose(t *testing.T) {
	step, err := OperationKey("project", "invocation", "checkpoint", 4, "step", "")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := OperationKey("project", "invocation", "checkpoint", 4, "step", "")
	if err != nil || replay != step {
		t.Fatalf("step key is not deterministic: %q %v", replay, err)
	}
	if step != "driver-b72344c305060a9977219fd3cab71648b0c2ea86df8156d8fbb9b83554672f5b" {
		t.Fatalf("operation key does not use the fixed v1 field encoding/domain: %q", step)
	}
	for _, changed := range []struct {
		project, invocation, checkpoint string
		sequence                        uint64
		purpose, original               string
	}{
		{"other", "invocation", "checkpoint", 4, "step", ""},
		{"project", "other", "checkpoint", 4, "step", ""},
		{"project", "invocation", "other", 4, "step", ""},
		{"project", "invocation", "checkpoint", 5, "step", ""},
		{"project", "invocation", "checkpoint", 4, "stop", ""},
	} {
		key, err := OperationKey(changed.project, changed.invocation, changed.checkpoint, changed.sequence, changed.purpose, changed.original)
		if err != nil {
			t.Fatalf("valid changed tuple refused: %v", err)
		}
		if key == step {
			t.Fatalf("changed tuple collided with step key: %+v", changed)
		}
	}
	correlate, err := OperationKey("project", "invocation", "checkpoint", 0, "correlate", "host-key-a")
	if err != nil {
		t.Fatal(err)
	}
	otherOriginal, err := OperationKey("project", "invocation", "checkpoint", 0, "correlate", "host-key-b")
	if err != nil || correlate == otherOriginal {
		t.Fatalf("different original host keys did not produce distinct correlations: %q %q %v", correlate, otherOriginal, err)
	}
	observation, err := OperationKey("project", "invocation", "checkpoint", 17, "observe", "host-key-a")
	if err != nil {
		t.Fatal(err)
	}
	differentSourceSequence, err := OperationKey("project", "invocation", "checkpoint", 18, "observe", "host-key-a")
	if err != nil || observation == differentSourceSequence {
		t.Fatalf("host source sequence did not distinguish observation keys: %q %q %v", observation, differentSourceSequence, err)
	}
	for _, invalid := range []struct {
		sequence uint64
		purpose  string
		original string
	}{
		{0, "step", ""}, {1, "step", "host"}, {1, "stop", "host"},
		{1, "correlate", "host"}, {0, "observe", "host"}, {1, "observe", ""},
		{0, "cancel", ""}, {0, "unknown", "host"},
	} {
		if _, err := OperationKey("project", "invocation", "checkpoint", invalid.sequence, invalid.purpose, invalid.original); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid tuple accepted: %+v err=%v", invalid, err)
		}
	}
}

func TestEffectBudgetCombinesRetainedAndUnpersistedRunEffects(t *testing.T) {
	run := &runState{driver: &Driver{limits: Limits{MaxEffects: 4}}, view: sessions.ControllerDriverView{HistoricalOperationCount: 3}, initialHistoryEffects: 3}
	if err := run.ReserveEffect(); err != nil {
		t.Fatalf("first effect at one slot of headroom refused: %v", err)
	}
	if err := run.ReserveEffect(); !errors.Is(err, ErrEffectLimit) {
		t.Fatalf("second effect exceeded combined retained/local ceiling: %v", err)
	}
	run.view.HistoricalOperationCount = 4 // The first local write is now visible in canonical history.
	if err := run.ReserveEffect(); !errors.Is(err, ErrEffectLimit) {
		t.Fatalf("refresh double-counted or reopened exhausted effect capacity: %v", err)
	}
}

func TestEffectBudgetUsesEntryHistoryAndRetainedRefreshWithoutDoubleCharge(t *testing.T) {
	run := &runState{
		driver:                &Driver{limits: Limits{MaxEffects: 5}},
		view:                  sessions.ControllerDriverView{HistoricalOperationCount: 4},
		initialHistoryEffects: 3,
		effects:               1,
	}
	if !run.canReserveEffects(1) { // H=4, H0=3, L=1; max(H,H0+L+1)=5.
		t.Fatal("one remaining effect was double charged after refresh materialized the local Step")
	}
	if run.canReserveEffects(2) {
		t.Fatal("two effects exceeded max(H,H0+L+n)")
	}
}

func TestTransitionBudgetUsesRetainedSequenceNotActionCensus(t *testing.T) {
	limits := Limits{MaxTransitions: 2}
	run := &runState{
		driver: &Driver{limits: limits},
		view: sessions.ControllerDriverView{
			Status:                sessions.ControllerStatus{Sequence: 2}, // one retained transition
			HistoricalActionCount: 2,                                      // multiple actions from a single wave
		},
		initialHistoryTransitions: 1,
	}
	if !run.canReserveTransitions(1) {
		t.Fatal("action census was incorrectly treated as retained Step/Stop transitions")
	}
	run.transitions = 1
	if run.canReserveTransitions(1) {
		t.Fatal("local and retained transitions exceeded the profile ceiling")
	}
}

func TestNewRejectsUnboundedProfiles(t *testing.T) {
	limits := Limits{MaxTransitions: 1, MaxEffects: 1, MaxWork: 1, MaxNoProgress: 1, MaxElapsed: time.Second, OperationTimeout: time.Second, MaxSequence: 1}
	if _, err := New(nil, limits); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("nil ports accepted: %v", err)
	}
	for _, mutate := range []func(*Limits){
		func(v *Limits) { v.MaxTransitions = 257 },
		func(v *Limits) { v.MaxEffects = 257 },
		func(v *Limits) { v.MaxWork = 4097 },
		func(v *Limits) { v.MaxNoProgress = 101 },
		func(v *Limits) { v.MaxElapsed = 15*time.Minute + time.Nanosecond },
		func(v *Limits) { v.OperationTimeout = 30*time.Second + time.Nanosecond },
		func(v *Limits) { v.MaxSequence = 0 },
	} {
		candidate := limits
		mutate(&candidate)
		if _, err := New(structPorts{}, candidate); !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("unbounded limits accepted: %+v err=%v", candidate, err)
		}
	}
}

func TestUnsupportedContextBackendMapsToUnsupported(t *testing.T) {
	if got := outcomeForError(executor.ErrUnsupportedContextBackend); got != "unsupported" {
		t.Fatalf("unsupported context backend outcome = %q", got)
	}
	if got := reasonForError(executor.ErrUnsupportedContextBackend); got != "unsupported_operation" {
		t.Fatalf("unsupported context backend reason = %q", got)
	}
}

func TestLegacyControllerSchemaHasPreciseUnsupportedReason(t *testing.T) {
	if got := reasonForError(sessions.ErrControllerDriverUnsupported); got != "controller_schema_unsupported" {
		t.Fatalf("legacy controller schema reason = %q", got)
	}
	if got := nextForError(sessions.ErrControllerDriverUnsupported); got != "use an authority with supported controller records; do not upgrade this schema in place" {
		t.Fatalf("legacy controller schema next action = %q", got)
	}
}

func TestExecutorRefusalMapsToNonRetryableRefusal(t *testing.T) {
	if got := outcomeForError(executor.ErrDenied); got != "refused" {
		t.Fatalf("executor refusal outcome = %q", got)
	}
	if got := nextForError(executor.ErrDenied); got != "owner reviews the exact authority refusal; do not retry unchanged" {
		t.Fatalf("executor refusal next action = %q", got)
	}
}
