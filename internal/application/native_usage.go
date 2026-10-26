package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/usage"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeUsageReader must perform a fresh authority read for the requested
// project/task. NativeDashboard satisfies this interface and reauthenticates
// its private enrolled connection on every call.
type NativeUsageReader interface {
	Read(project, task string) (sessions.DashboardSnapshot, error)
	SessionID() string
	WorkspaceID() string
	WorkspaceRoot() string
}

// NativeUsageSample is the metadata delivered by a selected native callback.
// Source and availability are deliberately absent: the producer determines
// those from the callback method, never from fields supplied by the event.
type NativeUsageSample struct {
	SchemaVersion  int          `json:"schema_version"`
	WorkspaceID    string       `json:"workspace_id"`
	ProjectID      string       `json:"project_id"`
	AgentProfileID string       `json:"agent_profile_id,omitempty"`
	SessionID      string       `json:"session_id"`
	TaskID         string       `json:"task_id"`
	AttemptID      string       `json:"attempt_id"`
	Provider       string       `json:"provider"`
	Model          string       `json:"model"`
	StreamID       string       `json:"stream_id"`
	SourceEventID  string       `json:"source_event_id"`
	ObservedAt     time.Time    `json:"observed_at"`
	OutputTokens   *usage.Count `json:"output_tokens,omitempty"`
	InputTokens    *usage.Count `json:"input_tokens,omitempty"`
	Supersedes     []string     `json:"supersedes,omitempty"`
}

// DecodeNativeUsageSample strictly decodes the callback envelope. It rejects
// unknown fields, including caller-supplied source/availability labels and any
// attempt to attach prompts, credentials, or raw output.
func DecodeNativeUsageSample(data []byte) (NativeUsageSample, error) {
	if len(data) == 0 || len(data) > usage.MaxEventBytes {
		return NativeUsageSample{}, usage.ErrInvalid
	}
	var sample NativeUsageSample
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&sample) != nil || decoder.Decode(new(any)) != io.EOF {
		return NativeUsageSample{}, usage.ErrInvalid
	}
	return sample, nil
}

// SessionID exposes the already selected dashboard connection's enrolled
// identity to the usage adapter without trusting an event's session claim.
func (p *NativeDashboard) SessionID() string {
	if p == nil {
		return ""
	}
	return p.connection.SessionID
}

// WorkspaceID exposes the workspace already fixed by the selected dashboard
// profile; a separate constructor argument cannot redirect the reader.
func (p *NativeDashboard) WorkspaceID() string {
	if p == nil || p.workspace == nil {
		return ""
	}
	return p.workspace.ID
}

// WorkspaceRoot exposes the immutable root selected by the dashboard profile.
func (p *NativeDashboard) WorkspaceRoot() string {
	if p == nil || p.workspace == nil {
		return ""
	}
	return p.workspace.Root
}

// NativeAgentProfileResolver returns a profile only when the native host can
// bind it to the independently enrolled session and exact attempt. Implementors
// must derive it from the selected host profile, never from event payload text.
type NativeAgentProfileResolver interface {
	ProfileFor(workspaceID, projectID, sessionID, taskID, attemptID string) (string, bool, error)
}

// NativeOutputUsageProducer is the app boundary for native harness/provider
// callbacks. It intentionally has no CLI, process, transcript, or output reader.
type NativeOutputUsageProducer struct {
	workspace *workspace.Workspace
	reader    NativeUsageReader
	profiles  NativeAgentProfileResolver
	store     usage.Store
}

func NewNativeOutputUsageProducer(w *workspace.Workspace, reader NativeUsageReader, profiles NativeAgentProfileResolver) (*NativeOutputUsageProducer, error) {
	if w == nil || w.ID == "" || !filepath.IsAbs(w.Root) || reader == nil || reader.SessionID() == "" || reader.WorkspaceID() != w.ID || !sameWorkspaceRoot(w.Root, reader.WorkspaceRoot()) {
		return nil, sessions.ErrDenied
	}
	return &NativeOutputUsageProducer{workspace: w, reader: reader, profiles: profiles, store: usage.Store{Root: w.Root}}, nil
}

func sameWorkspaceRoot(left, right string) bool {
	if !filepath.IsAbs(right) {
		return false
	}
	left, leftErr := filepath.EvalSymlinks(left)
	right, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(left) == filepath.Clean(right)
}

// RecordProviderCallback accepts counts only from the provider callback path.
// It sets provenance itself; the sample has no Source or Availability field.
func (p *NativeOutputUsageProducer) RecordProviderCallback(sample NativeUsageSample) error {
	return p.record(sample, "provider", "measured", "")
}

// RecordHarnessCallback accepts counts only from the native harness callback
// path and labels them as measured harness observations.
func (p *NativeOutputUsageProducer) RecordHarnessCallback(sample NativeUsageSample) error {
	return p.record(sample, "native-harness", "measured", "")
}

// RecordAgentDeclared preserves an agent's own count separately from measured
// provider and harness observations.
func (p *NativeOutputUsageProducer) RecordAgentDeclared(sample NativeUsageSample) error {
	return p.record(sample, "agent-declared", "declared", "")
}

// RecordProviderUnavailable records an explicit unsupported/missing provider
// callback. It cannot turn absent usage into a measured zero.
func (p *NativeOutputUsageProducer) RecordProviderUnavailable(sample NativeUsageSample, cause string) error {
	return p.record(sample, "provider", "unavailable", cause)
}

// RecordHarnessUnavailable records an explicit unsupported/missing harness
// callback. It cannot turn absent usage into a measured zero.
func (p *NativeOutputUsageProducer) RecordHarnessUnavailable(sample NativeUsageSample, cause string) error {
	return p.record(sample, "native-harness", "unavailable", cause)
}

// record binds the callback sample to a freshly authenticated authority
// snapshot before the local append-only store is touched.
func (p *NativeOutputUsageProducer) record(sample NativeUsageSample, source, availability, unavailableCause string) error {
	if p == nil || p.workspace == nil || p.reader == nil || sample.SchemaVersion != 1 || sample.WorkspaceID != p.workspace.ID || sample.ProjectID == "" || sample.TaskID == "" || sample.AttemptID == "" || sample.SessionID == "" || sample.SessionID != p.reader.SessionID() {
		return sessions.ErrDenied
	}
	snapshot, err := p.reader.Read(sample.ProjectID, sample.TaskID)
	if err != nil || snapshot.Project != sample.ProjectID || snapshot.State != "ready" || snapshot.Limited {
		return sessions.ErrDenied
	}
	var attempts []sessions.DashboardRecord
	var actorState *sessions.DashboardRecord
	for i := range snapshot.Records {
		r := &snapshot.Records[i]
		if r.Kind == "attempt" && r.TaskID == sample.TaskID {
			attempts = append(attempts, *r)
		}
		if r.Kind == "session" && r.ID == sample.SessionID && r.TaskID == sample.TaskID {
			actorState = r
		}
	}
	if len(attempts) == 0 || actorState == nil || actorState.State != "enrolled" || actorState.Freshness == "stale" {
		return sessions.ErrDenied
	}
	current, unambiguous := newestAttempt(attempts)
	if !unambiguous || current.ID != sample.AttemptID || current.ActorID != sample.SessionID {
		return sessions.ErrDenied
	}
	profileID := ""
	if p.profiles != nil {
		var available bool
		profileID, available, err = p.profiles.ProfileFor(sample.WorkspaceID, sample.ProjectID, sample.SessionID, sample.TaskID, sample.AttemptID)
		if err != nil {
			return sessions.ErrDenied
		}
		if available && profileID == "" || !available && sample.AgentProfileID != "" || available && sample.AgentProfileID != "" && sample.AgentProfileID != profileID {
			return sessions.ErrDenied
		}
		if !available {
			profileID = ""
		}
	} else if sample.AgentProfileID != "" {
		return sessions.ErrDenied
	}
	event := usage.Observation{
		SchemaVersion: sample.SchemaVersion, WorkspaceID: sample.WorkspaceID, ProjectID: sample.ProjectID, AgentProfileID: profileID,
		SessionID: sample.SessionID, TaskID: sample.TaskID, AttemptID: sample.AttemptID, Provider: sample.Provider, Model: sample.Model,
		StreamID: sample.StreamID, SourceEventID: sample.SourceEventID, ObservedAt: sample.ObservedAt, Source: source,
		Availability: availability, UnavailableCause: unavailableCause, OutputTokens: sample.OutputTokens, InputTokens: sample.InputTokens,
		Supersedes: sample.Supersedes,
	}
	if err = p.store.Append(event); err != nil {
		if errors.Is(err, usage.ErrInvalid) || errors.Is(err, usage.ErrConflict) || errors.Is(err, usage.ErrLimit) {
			return err
		}
		return usage.ErrUnavailable
	}
	return nil
}

// Read returns usage only after the same fresh project/task read proves the
// requested attempt is the current attempt owned by its enrolled session.
// Persisted observations remain unchanged; measured totals are available only
// when their stored agent profile still matches the selected native profile.
func (p *NativeOutputUsageProducer) Read(project, task, attempt string) ([]usage.Observation, usage.Totals, error) {
	if p == nil || p.workspace == nil || p.reader == nil {
		return nil, usage.Totals{}, sessions.ErrDenied
	}
	snapshot, err := p.reader.Read(project, task)
	if err != nil || snapshot.Project != project || snapshot.State != "ready" || snapshot.Limited {
		return nil, usage.Totals{}, sessions.ErrDenied
	}
	var attempts []sessions.DashboardRecord
	var actorState *sessions.DashboardRecord
	for i := range snapshot.Records {
		r := &snapshot.Records[i]
		if r.Kind != "attempt" || r.TaskID != task {
			if r.Kind == "session" && r.ID == p.reader.SessionID() && r.TaskID == task {
				actorState = r
			}
			continue
		}
		attempts = append(attempts, *r)
	}
	if actorState == nil || actorState.State != "enrolled" || actorState.Freshness == "stale" {
		return nil, usage.Totals{}, sessions.ErrDenied
	}
	current, unambiguous := newestAttempt(attempts)
	if !unambiguous || current.ID != attempt || current.ActorID != p.reader.SessionID() {
		return nil, usage.Totals{}, sessions.ErrDenied
	}
	rows, totals, err := p.store.Read(p.workspace.ID, project, task, attempt)
	if err != nil {
		return nil, usage.Totals{}, err
	}
	totals = applyCurrentProfileBinding(totals, rows, p.profiles, p.workspace.ID, project, p.reader.SessionID(), task, attempt)
	return rows, totals, nil
}

func applyCurrentProfileBinding(totals usage.Totals, rows []usage.Observation, profiles NativeAgentProfileResolver, workspaceID, projectID, sessionID, taskID, attemptID string) usage.Totals {
	var profileID string
	cause := ""
	if profiles == nil {
		cause = "agent-profile-resolver-unavailable"
	} else {
		var available bool
		var err error
		profileID, available, err = profiles.ProfileFor(workspaceID, projectID, sessionID, taskID, attemptID)
		switch {
		case err != nil:
			cause = "agent-profile-resolution-failed"
		case !available || profileID == "":
			cause = "agent-profile-unavailable"
		default:
			for _, row := range rows {
				if row.Availability == "measured" && row.AgentProfileID != profileID {
					cause = "agent-profile-mismatch"
					break
				}
			}
		}
	}
	if cause == "" {
		return totals
	}
	totals.MeasuredOutputTokens = 0
	totals.MeasuredInputTokens = 0
	totals.MeasuredOutputAvailable = false
	totals.MeasuredInputAvailable = false
	totals.MeasuredUnavailableCause = cause
	return totals
}

func newestAttempt(attempts []sessions.DashboardRecord) (sessions.DashboardRecord, bool) {
	var current sessions.DashboardRecord
	found, ambiguous := false, false
	for _, candidate := range attempts {
		if !found || candidate.ObservedAt.After(current.ObservedAt) {
			current, found, ambiguous = candidate, true, false
		} else if candidate.ObservedAt.Equal(current.ObservedAt) && candidate.ID != current.ID {
			ambiguous = true
		}
	}
	return current, found && !ambiguous
}
