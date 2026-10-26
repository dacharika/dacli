package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

type usageProfileStateFixture struct {
	profileID string
	available bool
	err       error
}

func (f usageProfileStateFixture) ProfileFor(workspaceID, projectID, sessionID, taskID, attemptID string) (string, bool, error) {
	return f.profileID, f.available, f.err
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

func nativeSample(o usage.Observation) NativeUsageSample {
	return NativeUsageSample{
		SchemaVersion: o.SchemaVersion, WorkspaceID: o.WorkspaceID, ProjectID: o.ProjectID, AgentProfileID: o.AgentProfileID,
		SessionID: o.SessionID, TaskID: o.TaskID, AttemptID: o.AttemptID, Provider: o.Provider, Model: o.Model,
		StreamID: o.StreamID, SourceEventID: o.SourceEventID, ObservedAt: o.ObservedAt, OutputTokens: o.OutputTokens,
		InputTokens: o.InputTokens, Supersedes: o.Supersedes,
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
	if err != nil || totals.MeasuredOutputTokens != 35 || !totals.MeasuredOutputAvailable {
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
	if err != nil || totals.MeasuredOutputTokens != 0 || totals.MeasuredOutputAvailable || totals.UnavailableObservations != 1 || len(rows) != 1 {
		t.Fatalf("missing usage became a count: rows=%+v totals=%+v err=%v", rows, totals, err)
	}
}

func TestNativeUsageCallbackSampleRejectsSelfAssertedProvenance(t *testing.T) {
	base := `{"schema_version":1,"workspace_id":"workspace-1","project_id":"project-1","session_id":"session-1","task_id":"task-1","attempt_id":"attempt-2","provider":"provider-a","model":"model-a","stream_id":"turn-1","source_event_id":"event-1","observed_at":"2026-10-26T09:00:00Z","source":"provider","availability":"measured","output_tokens":{"value":7,"unit":"tokens","basis":"delta","sequence":1}}`
	if _, err := DecodeNativeUsageSample([]byte(base)); !errors.Is(err, usage.ErrInvalid) {
		t.Fatalf("self-asserted provenance decoded: %v", err)
	}
	valid := strings.Replace(base, `,"source":"provider","availability":"measured"`, "", 1)
	if sample, err := DecodeNativeUsageSample([]byte(valid)); err != nil || sample.SourceEventID != "event-1" || sample.OutputTokens == nil {
		t.Fatalf("valid callback envelope = %+v, %v", sample, err)
	}
	for _, field := range []string{`"credential":"secret"`, `"prompt":"private"`, `"raw_output":"private"`} {
		payload := strings.TrimSuffix(base, "}") + "," + field + "}"
		if _, err := DecodeNativeUsageSample([]byte(payload)); !errors.Is(err, usage.ErrInvalid) {
			t.Errorf("unsafe field %s decoded: %v", field, err)
		}
	}
}

func TestNativeUsageCallbacksSetProvenanceAndKeepUnavailableExplicit(t *testing.T) {
	now := time.Now().UTC()
	snapshot := sessions.DashboardSnapshot{State: "ready", Project: "project-1", Records: []sessions.DashboardRecord{usageAttempt("attempt-2", "session-1", now), usageSession("session-1")}}
	w := &workspace.Workspace{ID: "workspace-1", Root: t.TempDir()}
	producer, err := NewNativeOutputUsageProducer(w, usageReaderFixture{snapshot: snapshot, root: w.Root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	missing := nativeSample(measuredUsage("missing", 0, "", 0))
	missing.OutputTokens = nil
	if err := producer.RecordProviderUnavailable(missing, "usage-callback-unavailable"); err != nil {
		t.Fatal(err)
	}
	rows, totals, err := producer.Read("project-1", "task-1", "attempt-2")
	if err != nil || len(rows) != 1 || rows[0].Source != "provider" || rows[0].Availability != "unavailable" || rows[0].UnavailableCause != "usage-callback-unavailable" || totals.MeasuredOutputAvailable || totals.UnavailableObservations != 1 {
		t.Fatalf("explicit unavailable callback = rows %+v totals %+v err %v", rows, totals, err)
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
	if err != nil || totals.MeasuredOutputTokens != 12 || totals.DeclaredOutputTokens != 90 || !totals.MeasuredOutputAvailable || !totals.DeclaredOutputAvailable {
		t.Fatalf("declared count mixed with measured: %+v, %v", totals, err)
	}
}

func TestNativeUsageSupersessionCannotCrossMeasuredDeclaredLanes(t *testing.T) {
	store := usage.Store{Root: t.TempDir()}
	measured := measuredUsage("measured-fact", 1, "cumulative", 42)
	if err := store.Append(measured); err != nil {
		t.Fatal(err)
	}
	declaredCorrection := measuredUsage("declared-correction", 2, "cumulative", 99)
	declaredCorrection.Source, declaredCorrection.Availability = "agent-declared", "declared"
	declaredCorrection.Supersedes = []string{measured.SourceEventID}
	if err := store.Append(declaredCorrection); !errors.Is(err, usage.ErrConflict) {
		t.Fatalf("cross-lane correction error = %v, want ErrConflict", err)
	}
	_, totals, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputTokens != 42 || totals.DeclaredOutputTokens != 0 {
		t.Fatalf("measured fact after refused correction = %+v, %v; measured 42 must remain active", totals, err)
	}

	corruptStore := usage.Store{Root: t.TempDir()}
	measured = measuredUsage("stored-measured", 1, "cumulative", 42)
	declared := measuredUsage("stored-declared", 1, "cumulative", 99)
	declared.Source, declared.Availability = "agent-declared", "declared"
	if err := corruptStore.Append(measured); err != nil {
		t.Fatal(err)
	}
	if err := corruptStore.Append(declared); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(corruptStore.Root, ".dacli", "build", "output-usage", "v1", "project-1", "task-1", "attempt-2")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("event directory = %v, %v", entries, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var observation usage.Observation
		if err := json.Unmarshal(data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.SourceEventID == declared.SourceEventID {
			observation.Supersedes = []string{measured.SourceEventID}
			changed, marshalErr := json.Marshal(observation)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := corruptStore.Read("workspace-1", "project-1", "task-1", "attempt-2"); !errors.Is(err, usage.ErrUnavailable) {
		t.Fatalf("cross-lane stored supersession error = %v, want ErrUnavailable", err)
	}
}

func TestNativeUsageReconcilesInputOutputAndIndependentStreams(t *testing.T) {
	store := usage.Store{Root: t.TempDir()}
	late := measuredUsage("turn-1-final", 2, "cumulative", 25)
	late.Source = "native-harness"
	late.InputTokens = &usage.Count{Value: 30, Unit: "tokens", Basis: "cumulative", Sequence: 2}
	if err := store.Append(late); err != nil {
		t.Fatal(err)
	}
	duplicateSequence := measuredUsage("turn-1-duplicate", 2, "delta", 4)
	duplicateSequence.Source = "provider"
	if err := store.Append(duplicateSequence); !errors.Is(err, usage.ErrConflict) {
		t.Fatalf("cross-source repeated sequence error = %v, want ErrConflict", err)
	}
	earlier := measuredUsage("turn-1-delta", 1, "delta", 10)
	earlier.Source = "provider"
	earlier.InputTokens = &usage.Count{Value: 7, Unit: "tokens", Basis: "delta", Sequence: 1}
	if err := store.Append(earlier); err != nil {
		t.Fatal(err)
	}
	otherStream := measuredUsage("turn-2", 1, "cumulative", 12)
	otherStream.StreamID = "turn-2"
	if err := store.Append(otherStream); err != nil {
		t.Fatal(err)
	}
	_, totals, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputTokens != 37 || totals.MeasuredInputTokens != 30 || !totals.MeasuredOutputAvailable || !totals.MeasuredInputAvailable {
		t.Fatalf("mixed token streams = %+v, %v; want output 37 and input 30", totals, err)
	}
}

func TestNativeUsageCumulativeTotalsArePerStreamAndAvailabilityIsPerClass(t *testing.T) {
	store := usage.Store{Root: t.TempDir()}
	observations := []usage.Observation{
		measuredUsage("measured-a-1", 1, "delta", 3),
		measuredUsage("measured-a-2", 2, "cumulative", 10),
		measuredUsage("measured-b", 1, "cumulative", 20),
	}
	observations[2].StreamID = "stream-b"
	declaredA := measuredUsage("declared-a", 1, "cumulative", 4)
	declaredA.Source, declaredA.Availability = "agent-declared", "declared"
	declaredB := measuredUsage("declared-b", 1, "cumulative", 6)
	declaredB.Source, declaredB.Availability, declaredB.StreamID = "agent-declared", "declared", "declared-stream-b"
	observations = append(observations, declaredA, declaredB)
	for _, observation := range observations {
		if err := store.Append(observation); err != nil {
			t.Fatal(err)
		}
	}
	_, totals, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputTokens != 30 || totals.DeclaredOutputTokens != 10 || !totals.MeasuredOutputAvailable || !totals.DeclaredOutputAvailable {
		t.Fatalf("per-stream cumulative totals = %+v, %v; want measured 30 and declared 10 with separate availability", totals, err)
	}
	declaredOnlyStore := usage.Store{Root: t.TempDir()}
	if err := declaredOnlyStore.Append(declaredA); err != nil {
		t.Fatal(err)
	}
	_, declaredOnly, err := declaredOnlyStore.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || declaredOnly.MeasuredOutputAvailable || !declaredOnly.DeclaredOutputAvailable {
		t.Fatalf("declared-only availability = %+v, %v; declared must not imply measured availability", declaredOnly, err)
	}
}

func TestNativeUsageAmbiguousSequenceDoesNotExposePartialCount(t *testing.T) {
	root := t.TempDir()
	store := usage.Store{Root: root}
	first := measuredUsage("sequence-one", 1, "delta", 5)
	second := measuredUsage("sequence-two", 2, "delta", 7)
	if err := store.Append(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(second); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".dacli", "build", "output-usage", "v1", "project-1", "task-1", "attempt-2")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("event directory = %v, %v", entries, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var observation usage.Observation
		if err := json.Unmarshal(data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.SourceEventID == "sequence-two" {
			observation.OutputTokens.Sequence = 1 // simulate a corrupted/ambiguous persisted stream
			changed, marshalErr := json.Marshal(observation)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, totals, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || totals.MeasuredOutputAvailable || totals.MeasuredOutputTokens != 0 {
		t.Fatalf("ambiguous sequence total = %+v, %v; must be unavailable, not a partial count", totals, err)
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
	if err = producer.RecordProviderCallback(nativeSample(stale)); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("stale attempt error = %v, want denied", err)
	}
	forged := measuredUsage("forged-profile", 1, "delta", 1)
	forged.AgentProfileID = "agent-other"
	if err = producer.RecordProviderCallback(nativeSample(forged)); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("forged profile error = %v, want denied", err)
	}
	foreign := measuredUsage("foreign-project", 1, "delta", 1)
	foreign.ProjectID = "project-2"
	if err = producer.RecordProviderCallback(nativeSample(foreign)); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("foreign project error = %v, want denied", err)
	}
	foreignSession := measuredUsage("foreign-session", 1, "delta", 1)
	foreignSession.SessionID = "session-2"
	if err = producer.RecordProviderCallback(nativeSample(foreignSession)); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("foreign session error = %v, want denied", err)
	}
	expired := base
	expired.Records = append([]sessions.DashboardRecord(nil), base.Records...)
	for i := range expired.Records {
		if expired.Records[i].Kind == "session" {
			expired.Records[i].Freshness = "stale"
		}
	}
	expiredProducer, err := NewNativeOutputUsageProducer(w, usageReaderFixture{snapshot: expired, root: w.Root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = expiredProducer.RecordProviderCallback(nativeSample(measuredUsage("expired-session", 1, "delta", 1))); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("expired attempt session error = %v, want denied", err)
	}
	if _, _, err = expiredProducer.Read("project-1", "task-1", "attempt-2"); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("expired session read error = %v, want denied", err)
	}
	ambiguous := base
	ambiguous.Records = append(append([]sessions.DashboardRecord(nil), base.Records...), usageAttempt("attempt-3", "session-1", now))
	ambiguousProducer, err := NewNativeOutputUsageProducer(w, usageReaderFixture{snapshot: ambiguous, root: w.Root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = ambiguousProducer.RecordProviderCallback(nativeSample(measuredUsage("ambiguous-attempt", 1, "delta", 1))); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("tied latest attempts error = %v, want denied", err)
	}
	good := measuredUsage("good", 1, "delta", 7)
	if err = producer.RecordProviderCallback(nativeSample(good)); err != nil {
		t.Fatalf("valid observation: %v", err)
	}
	rows, totals, err := producer.Read("project-1", "task-1", "attempt-2")
	if err != nil || len(rows) != 1 || rows[0].AgentProfileID != "agent-good" || rows[0].Source != "provider" || rows[0].Availability != "measured" || totals.MeasuredOutputTokens != 7 {
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
	if err = producer.RecordProviderCallback(nativeSample(event)); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("unresolved profile claim error = %v, want denied", err)
	}
}

func TestNativeUsageReadRevalidatesCurrentProfileAndKeepsDeclaredLane(t *testing.T) {
	now := time.Now().UTC()
	snapshot := sessions.DashboardSnapshot{State: "ready", Project: "project-1", Records: []sessions.DashboardRecord{usageAttempt("attempt-2", "session-1", now), usageSession("session-1")}}
	w := &workspace.Workspace{ID: "workspace-1", Root: t.TempDir()}
	reader := usageReaderFixture{snapshot: snapshot, root: w.Root}
	producerA, err := NewNativeOutputUsageProducer(w, reader, usageProfileStateFixture{profileID: "agent-a", available: true})
	if err != nil {
		t.Fatal(err)
	}
	measured := measuredUsage("profile-a-measured", 1, "cumulative", 42)
	if err := producerA.RecordProviderCallback(nativeSample(measured)); err != nil {
		t.Fatal(err)
	}
	declared := measuredUsage("profile-a-declared", 1, "cumulative", 9)
	if err := producerA.RecordAgentDeclared(nativeSample(declared)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		profile NativeAgentProfileResolver
		cause   string
	}{
		{name: "profile changed", profile: usageProfileStateFixture{profileID: "agent-b", available: true}, cause: "agent-profile-mismatch"},
		{name: "profile unavailable", profile: usageProfileStateFixture{available: false}, cause: "agent-profile-unavailable"},
		{name: "profile resolution failed", profile: usageProfileStateFixture{err: errors.New("host profile lookup failed")}, cause: "agent-profile-resolution-failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			currentProducer, err := NewNativeOutputUsageProducer(w, reader, tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			rows, totals, err := currentProducer.Read("project-1", "task-1", "attempt-2")
			if err != nil || len(rows) != 2 || totals.MeasuredOutputTokens != 0 || totals.MeasuredOutputAvailable || totals.MeasuredUnavailableCause != tc.cause {
				t.Fatalf("stale profile read = rows %+v totals %+v err %v; measured total must be unavailable with cause %q", rows, totals, err, tc.cause)
			}
			if totals.DeclaredOutputTokens != 9 || !totals.DeclaredOutputAvailable {
				t.Fatalf("profile change altered declared lane: totals %+v", totals)
			}
			foundStoredMeasured := false
			for _, row := range rows {
				if row.SourceEventID == "profile-a-measured" && row.AgentProfileID == "agent-a" && row.OutputTokens != nil && row.OutputTokens.Value == 42 {
					foundStoredMeasured = true
				}
			}
			if !foundStoredMeasured {
				t.Fatalf("read should preserve the original immutable profile-A fact: %+v", rows)
			}
		})
	}
	storedRows, storedTotals, err := (usage.Store{Root: w.Root}).Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || len(storedRows) != 2 || storedTotals.MeasuredOutputTokens != 42 || !storedTotals.MeasuredOutputAvailable || storedTotals.DeclaredOutputTokens != 9 {
		t.Fatalf("profile revalidation modified the append-only store: rows %+v totals %+v err %v", storedRows, storedTotals, err)
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

func TestNativeUsageRejectsTraversalIdentifiersAndOversizedStoredEvents(t *testing.T) {
	root := t.TempDir()
	store := usage.Store{Root: root}
	for _, id := range []string{".", ".."} {
		observation := measuredUsage("traversal", 1, "delta", 1)
		observation.ProjectID = id
		if err := store.Append(observation); !errors.Is(err, usage.ErrInvalid) {
			t.Errorf("Append project %q error = %v, want ErrInvalid", id, err)
		}
		if _, _, err := store.Read("workspace-1", id, "task-1", "attempt-2"); !errors.Is(err, usage.ErrInvalid) {
			t.Errorf("Read project %q error = %v, want ErrInvalid", id, err)
		}
	}
	if err := store.Append(measuredUsage("bounded", 1, "delta", 1)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".dacli", "build", "output-usage", "v1", "project-1", "task-1", "attempt-2")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("event directory = %v, %v", entries, err)
	}
	path := filepath.Join(dir, entries[0].Name())
	if err := os.WriteFile(path, make([]byte, usage.MaxEventBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2"); !errors.Is(err, usage.ErrUnavailable) {
		t.Fatalf("oversized stored event error = %v, want ErrUnavailable", err)
	}
}

func TestNativeUsageFullStoreRetainsHistoryAndRejectsNextAppend(t *testing.T) {
	root := t.TempDir()
	store := usage.Store{Root: root}
	dir := filepath.Join(root, ".dacli", "build", "output-usage", "v1", "project-1", "task-1", "attempt-2")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < usage.MaxEvents; i++ {
		row := measuredUsage(fmt.Sprintf("retained-%d", i), uint64(i+1), "delta", 1)
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		identity := strings.Join([]string{row.WorkspaceID, row.ProjectID, row.TaskID, row.AttemptID, row.SourceEventID}, "\x00")
		name := sha256.Sum256([]byte(identity))
		if err := os.WriteFile(filepath.Join(dir, hex.EncodeToString(name[:])+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rows, totals, err := store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || len(rows) != usage.MaxEvents || totals.MeasuredOutputTokens != usage.MaxEvents {
		t.Fatalf("full store read = %d rows, %+v, %v; history must remain readable", len(rows), totals, err)
	}
	if err := store.Append(measuredUsage("beyond-retention", usage.MaxEvents+1, "delta", 1)); !errors.Is(err, usage.ErrLimit) {
		t.Fatalf("full-store append error = %v, want ErrLimit", err)
	}
	rows, _, err = store.Read("workspace-1", "project-1", "task-1", "attempt-2")
	if err != nil || len(rows) != usage.MaxEvents {
		t.Fatalf("full store after rejected append = %d rows, %v; retention must preserve history", len(rows), err)
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
