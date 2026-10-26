package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/usage"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type usageReaderFixture struct {
	snapshot sessions.DashboardSnapshot
	root     string
}

func (f usageReaderFixture) Read(project, task string) (sessions.DashboardSnapshot, error) {
	if f.snapshot.Project != project {
		return sessions.DashboardSnapshot{}, sessions.ErrDenied
	}
	return f.snapshot, nil
}

func (f usageReaderFixture) SessionID() string     { return "session-1" }
func (f usageReaderFixture) WorkspaceID() string   { return "workspace-1" }
func (f usageReaderFixture) WorkspaceRoot() string { return f.root }

type usageProfileFixture string

func (f usageProfileFixture) ProfileFor(workspaceID, projectID, sessionID, taskID, attemptID string) (string, bool, error) {
	return string(f), true, nil
}

func usageAttempt(id, actor string, at time.Time) sessions.DashboardRecord {
	return sessions.DashboardRecord{Kind: "attempt", ID: id, TaskID: "task-1", ActorID: actor, State: "registered", Freshness: "historical", ObservedAt: at}
}

func usageSession(id string) sessions.DashboardRecord {
	return sessions.DashboardRecord{Kind: "session", ID: id, TaskID: "task-1", State: "enrolled", Freshness: "historical"}
}

func measuredUsage(eventID string, sequence uint64, basis string, value int64) usage.Observation {
	return usage.Observation{
		SchemaVersion: 1, WorkspaceID: "workspace-1", ProjectID: "project-1", SessionID: "session-1", TaskID: "task-1", AttemptID: "attempt-2",
		Provider: "native-harness", Model: "model-a", StreamID: "turn-1", SourceEventID: eventID, ObservedAt: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC),
		Source: "native-harness", Availability: "measured", OutputTokens: &usage.Count{Value: value, Unit: "tokens", Basis: basis, Sequence: sequence},
	}
}

func TestNativeUsageAppendReconcilesReplaySupersessionAndOutOfOrder(t *testing.T) {
	store := usage.Store{Root: t.TempDir()}
	first := measuredUsage("event-2", 2, "delta", 25)
	if err := store.Append(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(first); err != nil {
		t.Fatalf("identical replay should be idempotent: %v", err)
	}
	sequenceConflict := measuredUsage("event-2-duplicate", 2, "delta", 2)
	if err := store.Append(sequenceConflict); !errors.Is(err, usage.ErrConflict) {
		t.Fatalf("ambiguous repeated sequence error = %v, want ErrConflict", err)
	}
	late := measuredUsage("event-1", 1, "delta", 10)
	if err := store.Append(late); err != nil {
		t.Fatal(err)
	}
	_, totals, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputTokens != 35 || !totals.OutputAvailable {
		t.Fatalf("out-of-order deltas = %+v, %v; want 35 available", totals, err)
	}
	final := measuredUsage("event-final", 3, "cumulative", 42)
	final.Supersedes = []string{"event-1", "event-2"}
	if err := store.Append(final); err != nil {
		t.Fatal(err)
	}
	_, totals, err = store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputTokens != 42 || totals.ObservationCount != 3 {
		t.Fatalf("superseding cumulative final = %+v, %v; want 42 from 3 immutable facts", totals, err)
	}
	correction := measuredUsage("event-correction", 4, "delta", 43)
	correction.Supersedes = []string{"event-final"}
	if err := store.Append(correction); err != nil {
		t.Fatal(err)
	}
	_, totals, err = store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputTokens != 43 {
		t.Fatalf("superseding delta correction = %+v, %v; want replacement value 43", totals, err)
	}
	conflict := final
	conflict.OutputTokens = &usage.Count{Value: 43, Unit: "tokens", Basis: "cumulative", Sequence: 3}
	if err := store.Append(conflict); !errors.Is(err, usage.ErrConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrConflict", err)
	}
}

func TestNativeUsageRejectsWrongUnitAndKeepsMissingUsageUnavailable(t *testing.T) {
	store := usage.Store{Root: t.TempDir()}
	wrongUnit := measuredUsage("bad-unit", 1, "delta", 5)
	wrongUnit.OutputTokens.Unit = "characters"
	if err := store.Append(wrongUnit); !errors.Is(err, usage.ErrInvalid) {
		t.Fatalf("wrong unit error = %v, want ErrInvalid", err)
	}
	missing := measuredUsage("silent", 0, "", 0)
	missing.Availability = "unavailable"
	missing.UnavailableCause = "provider-usage-unsupported"
	missing.OutputTokens = nil
	if err := store.Append(missing); err != nil {
		t.Fatalf("store explicit unavailable event: %v", err)
	}
	rows, totals, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputTokens != 0 || totals.OutputAvailable || totals.UnavailableObservations != 1 || len(rows) != 1 {
		t.Fatalf("missing usage became a count: rows=%+v totals=%+v err=%v", rows, totals, err)
	}
}

func TestNativeUsageSeparatesAgentDeclaredCounts(t *testing.T) {
	store := usage.Store{Root: t.TempDir()}
	declared := measuredUsage("declared", 1, "delta", 90)
	declared.Source, declared.Availability = "agent-declared", "declared"
	measured := measuredUsage("measured", 1, "delta", 12)
	if err := store.Append(declared); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(measured); err != nil {
		t.Fatal(err)
	}
	_, totals, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputTokens != 12 || totals.DeclaredOutputTokens != 90 {
		t.Fatalf("declared count mixed with measured: %+v, %v", totals, err)
	}
}

func TestNativeUsageProducerRejectsRolloverForgedProfileAndForeignProject(t *testing.T) {
	now := time.Now().UTC()
	base := sessions.DashboardSnapshot{Schema: "native-dashboard/v1", State: "ready", Project: "project-1", Records: []sessions.DashboardRecord{
		usageAttempt("attempt-1", "session-1", now.Add(-time.Minute)), usageAttempt("attempt-2", "session-1", now), usageSession("session-1"),
	}}
	w := &workspace.Workspace{ID: "workspace-1", Root: t.TempDir()}
	reader := usageReaderFixture{snapshot: base, root: w.Root}
	producer, err := NewNativeOutputUsageProducer(w, reader, usageProfileFixture("agent-good"))
	if err != nil {
		t.Fatal(err)
	}
	wrongWorkspace := &workspace.Workspace{ID: "workspace-2", Root: t.TempDir()}
	if _, err = NewNativeOutputUsageProducer(wrongWorkspace, reader, nil); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("foreign reader workspace error = %v, want denied", err)
	}
	clonedWorkspace := &workspace.Workspace{ID: "workspace-1", Root: t.TempDir()}
	if _, err = NewNativeOutputUsageProducer(clonedWorkspace, reader, nil); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("same-ID foreign root error = %v, want denied", err)
	}
	stale := measuredUsage("stale", 1, "delta", 1)
	stale.AttemptID = "attempt-1"
	if err = producer.Record(stale); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("stale attempt error = %v, want denied", err)
	}
	forged := measuredUsage("forged-profile", 1, "delta", 1)
	forged.AgentProfileID = "agent-other"
	if err = producer.Record(forged); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("forged profile error = %v, want denied", err)
	}
	foreign := measuredUsage("foreign-project", 1, "delta", 1)
	foreign.ProjectID = "project-2"
	if err = producer.Record(foreign); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("foreign project error = %v, want denied", err)
	}
	foreignSession := measuredUsage("foreign-session", 1, "delta", 1)
	foreignSession.SessionID = "session-2"
	if err = producer.Record(foreignSession); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("foreign session error = %v, want denied", err)
	}
	ambiguous := base
	ambiguous.Records = append(append([]sessions.DashboardRecord(nil), base.Records...), usageAttempt("attempt-3", "session-1", now))
	ambiguousProducer, err := NewNativeOutputUsageProducer(w, usageReaderFixture{snapshot: ambiguous, root: w.Root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = ambiguousProducer.Record(measuredUsage("ambiguous-attempt", 1, "delta", 1)); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("tied latest attempts error = %v, want denied", err)
	}
	good := measuredUsage("good", 1, "delta", 7)
	if err = producer.Record(good); err != nil {
		t.Fatalf("valid observation: %v", err)
	}
	rows, totals, err := producer.Read("project-1", "task-1", "attempt-2")
	if err != nil || len(rows) != 1 || rows[0].AgentProfileID != "agent-good" || totals.MeasuredOutputTokens != 7 {
		t.Fatalf("bound read = rows %+v totals %+v err %v", rows, totals, err)
	}
	if _, _, err = producer.Read("project-2", "task-1", "attempt-2"); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("cross-project read error = %v, want denied", err)
	}
}

func TestNativeUsageProducerDoesNotTrustUnresolvedProfileClaim(t *testing.T) {
	now := time.Now().UTC()
	snapshot := sessions.DashboardSnapshot{State: "ready", Project: "project-1", Records: []sessions.DashboardRecord{usageAttempt("attempt-2", "session-1", now), usageSession("session-1")}}
	w := &workspace.Workspace{ID: "workspace-1", Root: t.TempDir()}
	producer, err := NewNativeOutputUsageProducer(w, usageReaderFixture{snapshot: snapshot, root: w.Root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	event := measuredUsage("profile-claim", 1, "delta", 1)
	event.AgentProfileID = "agent-forged"
	if err = producer.Record(event); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("unresolved profile claim error = %v, want denied", err)
	}
}

func TestNativeUsageStoreIsBoundedAndRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	store := usage.Store{Root: root}
	if err := store.Append(measuredUsage("event", 1, "delta", 1)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".dacli", "build", "output-usage", "v1", "project-1", "task-1", "attempt-2")
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("event files = %v, %v", entries, err)
	}
	info, err := entries[0].Info()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("event mode = %v, %v; want 0600", info.Mode(), err)
	}
	data, err := os.ReadFile(filepath.Join(path, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || containsForbiddenContent(data) {
		t.Fatalf("persisted observation contains unexpected raw content: %s", data)
	}
	withUnknown := append(append(append([]byte(nil), data[:len(data)-1]...), []byte(`,"raw_output":"fixture"`)...), '}')
	if err := os.WriteFile(filepath.Join(path, entries[0].Name()), withUnknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Read("workspace-1", "project-1", "task-1", "attempt-2"); !errors.Is(err, usage.ErrUnavailable) {
		t.Fatalf("unknown raw-output field error = %v, want unavailable", err)
	}
}

func containsForbiddenContent(data []byte) bool {
	for _, forbidden := range []string{"prompt", "source_code", "credential", "raw_output"} {
		if strings.Contains(string(data), forbidden) {
			return true
		}
	}
	return false
}
