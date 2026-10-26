package application

import (
	"errors"
	"path/filepath"

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

// Record binds an observation to a freshly authenticated authority snapshot.
// Caller-supplied workspace/project/session/task/attempt/profile claims are
// checked against that snapshot before the local append-only store is touched.
func (p *NativeOutputUsageProducer) Record(event usage.Observation) error {
	if p == nil || p.workspace == nil || p.reader == nil || event.WorkspaceID != p.workspace.ID || event.ProjectID == "" || event.TaskID == "" || event.AttemptID == "" || event.SessionID == "" || event.SessionID != p.reader.SessionID() {
		return sessions.ErrDenied
	}
	snapshot, err := p.reader.Read(event.ProjectID, event.TaskID)
	if err != nil || snapshot.Project != event.ProjectID || snapshot.State != "ready" || snapshot.Limited {
		return sessions.ErrDenied
	}
	var attempts []sessions.DashboardRecord
	var actorState *sessions.DashboardRecord
	for i := range snapshot.Records {
		r := &snapshot.Records[i]
		if r.Kind == "attempt" && r.TaskID == event.TaskID {
			attempts = append(attempts, *r)
		}
		if r.Kind == "session" && r.ID == event.SessionID && r.TaskID == event.TaskID {
			actorState = r
		}
	}
	if len(attempts) == 0 || actorState == nil || actorState.State != "enrolled" || actorState.Freshness == "stale" {
		return sessions.ErrDenied
	}
	current, unambiguous := newestAttempt(attempts)
	if !unambiguous || current.ID != event.AttemptID || current.ActorID != event.SessionID {
		return sessions.ErrDenied
	}
	profileID := ""
	if p.profiles != nil {
		var available bool
		profileID, available, err = p.profiles.ProfileFor(event.WorkspaceID, event.ProjectID, event.SessionID, event.TaskID, event.AttemptID)
		if err != nil {
			return sessions.ErrDenied
		}
		if available && profileID == "" || !available && event.AgentProfileID != "" || available && event.AgentProfileID != "" && event.AgentProfileID != profileID {
			return sessions.ErrDenied
		}
		if !available {
			profileID = ""
		}
	} else if event.AgentProfileID != "" {
		return sessions.ErrDenied
	}
	event.AgentProfileID = profileID
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
func (p *NativeOutputUsageProducer) Read(project, task, attempt string) ([]usage.Observation, usage.Totals, error) {
	if p == nil || p.workspace == nil || p.reader == nil {
		return nil, usage.Totals{}, sessions.ErrDenied
	}
	snapshot, err := p.reader.Read(project, task)
	if err != nil || snapshot.Project != project || snapshot.State != "ready" || snapshot.Limited {
		return nil, usage.Totals{}, sessions.ErrDenied
	}
	var attempts []sessions.DashboardRecord
	for i := range snapshot.Records {
		r := &snapshot.Records[i]
		if r.Kind != "attempt" || r.TaskID != task {
			continue
		}
		attempts = append(attempts, *r)
	}
	current, unambiguous := newestAttempt(attempts)
	if !unambiguous || current.ID != attempt || current.ActorID != p.reader.SessionID() {
		return nil, usage.Totals{}, sessions.ErrDenied
	}
	return p.store.Read(p.workspace.ID, project, task, attempt)
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
