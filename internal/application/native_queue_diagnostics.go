package application

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/operatorread"
	"github.com/mlnomadpy/dacli/internal/pathclaims"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const queueDiagnosticsDeadline = 15 * time.Second

// OpenPinnedQueueDiagnosticsRead opens the operator reader only when the
// selected private connection matches the already authenticated MCP pin.
func OpenPinnedQueueDiagnosticsRead(path string, expected nativeconfig.Connection) (*NativeOperatorRead, error) {
	reader, err := OpenOperatorRead(path)
	if err != nil || reader.dashboard.connection != expected {
		return nil, operatorread.ErrDenied
	}
	return reader, nil
}

// ReadQueueDiagnostics returns a bounded, read-only projection for one exact
// project. The canonical callback is deliberately application-owned: callers
// cannot substitute an authority or task source.
func (p *NativeOperatorRead) ReadQueueDiagnostics(ctx context.Context, project, task string, limit int) (sessions.QueueDiagnosticsSnapshot, error) {
	return p.readQueueDiagnostics(ctx, project, task, limit, func(readCtx context.Context) (sessions.QueueCanonicalSnapshot, error) {
		if p == nil || p.workspace == nil {
			return sessions.QueueCanonicalSnapshot{}, operatorread.ErrDenied
		}
		return readQueueCanonicalSnapshot(readCtx, p.workspace, project, task)
	})
}

// readQueueDiagnostics keeps the source callback private to this application
// package. Its parameter exists for deterministic boundary tests; production
// always supplies readQueueCanonicalSnapshot above.
func (p *NativeOperatorRead) readQueueDiagnostics(ctx context.Context, project, task string, limit int, readCanonical sessions.QueueCanonicalReader) (sessions.QueueDiagnosticsSnapshot, error) {
	if p == nil || p.dashboard == nil || p.workspace == nil || project == "" || limit < 1 || limit > 100 || readCanonical == nil {
		return sessions.QueueDiagnosticsSnapshot{}, operatorread.ErrDenied
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, queueDiagnosticsDeadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return sessions.QueueDiagnosticsSnapshot{}, operatorread.ErrStale
	}

	connection, token, err := p.queueDiagnosticsSelection()
	if err != nil {
		return sessions.QueueDiagnosticsSnapshot{}, operatorread.ErrDenied
	}
	authority, err := sessions.OpenQueueDiagnosticsAuthority(connection.Authority, sessions.Binding{Workspace: connection.WorkspaceID, Installation: connection.InstallationID})
	if err != nil {
		return sessions.QueueDiagnosticsSnapshot{}, queueDiagnosticsError(err)
	}
	snapshot, err := authority.ReadQueueDiagnostics(ctx, token, connection.SessionID, project, task, limit, readCanonical)
	if err != nil {
		return sessions.QueueDiagnosticsSnapshot{}, queueDiagnosticsError(err)
	}
	if err := ctx.Err(); err != nil {
		return sessions.QueueDiagnosticsSnapshot{}, operatorread.ErrStale
	}
	_, finalToken, err := p.queueDiagnosticsSelection()
	if err != nil || subtle.ConstantTimeCompare([]byte(token), []byte(finalToken)) != 1 {
		return sessions.QueueDiagnosticsSnapshot{}, operatorread.ErrDenied
	}
	return snapshot, nil
}

func readQueueCanonicalSnapshot(ctx context.Context, w *workspace.Workspace, project, task string) (sessions.QueueCanonicalSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return sessions.QueueCanonicalSnapshot{}, err
	}
	canonical, err := store.ReadCheckpointCanonicalContext(ctx, w)
	if err != nil {
		return sessions.QueueCanonicalSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return sessions.QueueCanonicalSnapshot{}, err
	}
	if canonical.Projects[project] == nil {
		return sessions.QueueCanonicalSnapshot{}, sessions.ErrDenied
	}
	if task != "" {
		found := false
		for _, candidate := range canonical.Tasks {
			if candidate.ID == task && candidate.Project == project {
				found = true
				break
			}
		}
		if !found {
			return sessions.QueueCanonicalSnapshot{}, sessions.ErrDenied
		}
	}
	if err := ctx.Err(); err != nil {
		return sessions.QueueCanonicalSnapshot{}, err
	}
	frontier := store.ReadyFrontierForProject(canonical.Tasks, project)
	if err := ctx.Err(); err != nil {
		return sessions.QueueCanonicalSnapshot{}, err
	}
	ready, blocked, invalid := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, candidate := range frontier.Ready {
		if err := ctx.Err(); err != nil {
			return sessions.QueueCanonicalSnapshot{}, err
		}
		ready[candidate.ID] = true
	}
	for _, candidate := range frontier.Blocked {
		if err := ctx.Err(); err != nil {
			return sessions.QueueCanonicalSnapshot{}, err
		}
		blocked[candidate.ID] = true
	}
	for _, problem := range frontier.Problems {
		if err := ctx.Err(); err != nil {
			return sessions.QueueCanonicalSnapshot{}, err
		}
		invalid[problem.Task.ID] = true
	}

	out := sessions.QueueCanonicalSnapshot{Tasks: []sessions.QueueCanonicalTask{}}
	ids := make([]string, 0, len(canonical.Digests))
	for id := range canonical.Digests {
		if err := ctx.Err(); err != nil {
			return sessions.QueueCanonicalSnapshot{}, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if err := ctx.Err(); err != nil {
		return sessions.QueueCanonicalSnapshot{}, err
	}
	hash := sha256.New()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return sessions.QueueCanonicalSnapshot{}, err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", id, canonical.Digests[id])
	}
	out.Digest = hex.EncodeToString(hash.Sum(nil))
	for _, candidate := range canonical.Tasks {
		if err := ctx.Err(); err != nil {
			return sessions.QueueCanonicalSnapshot{}, err
		}
		if candidate.Project != project || (task != "" && candidate.ID != task) {
			continue
		}
		reason := ""
		claims := candidate.Claims()
		claimsInvalid := false
		if len(claims) > 0 {
			_, err := pathclaims.ValidateClaims(claims)
			claimsInvalid = err != nil
		}
		switch {
		case invalid[candidate.ID]:
			reason = "dependency_invalid"
		case blocked[candidate.ID]:
			reason = "dependencies_unsatisfied"
		case candidate.Status == model.StatusBlocked:
			reason = "canonical_status_blocked"
		case candidate.Status == model.StatusActive:
			reason = "execution_state_unknown"
		case candidate.Status != model.StatusOpen:
			reason = "canonical_status_blocked"
		case candidate.IsLoopAnchor() || candidate.IsAggregate():
			reason = "nonimplementation_task"
		case candidate.Priority() == "wont":
			reason = "priority_wont"
		case len(claims) == 0:
			reason = "claims_missing"
		case claimsInvalid:
			reason = "claims_invalid"
		case !ready[candidate.ID]:
			reason = "execution_state_unknown"
		}
		resources := make([]sessions.Resource, 0, len(claims))
		for _, claim := range claims {
			if err := ctx.Err(); err != nil {
				return sessions.QueueCanonicalSnapshot{}, err
			}
			resources = append(resources, sessions.Resource{Kind: "path", Path: claim})
		}
		out.Tasks = append(out.Tasks, sessions.QueueCanonicalTask{TaskID: candidate.ID, ProjectID: candidate.Project, Status: string(candidate.Status), Reason: reason, Resources: resources})
	}
	if err := ctx.Err(); err != nil {
		return sessions.QueueCanonicalSnapshot{}, err
	}
	return out, nil
}

func (p *NativeOperatorRead) queueDiagnosticsSelection() (nativeconfig.Connection, string, error) {
	if p == nil || p.dashboard == nil || p.workspace == nil {
		return nativeconfig.Connection{}, "", nativeconfig.ErrDenied
	}
	raw, err := nativeconfig.ReadPrivate(p.dashboard.path, 16*1024)
	if err != nil {
		return nativeconfig.Connection{}, "", nativeconfig.ErrDenied
	}
	var connection nativeconfig.Connection
	if nativeconfig.Decode(raw, &connection) != nil {
		return nativeconfig.Connection{}, "", nativeconfig.ErrDenied
	}
	if connection != p.dashboard.connection {
		return nativeconfig.Connection{}, "", nativeconfig.ErrDenied
	}
	resolved, err := nativeconfig.ResolveWorkspace(connection.Workspace, connection.WorkspaceID)
	if err != nil || resolved.ID != p.workspace.ID || resolved.Root != p.workspace.Root {
		return nativeconfig.Connection{}, "", nativeconfig.ErrDenied
	}
	token, err := nativeconfig.ReadCredential(connection.CredentialFile)
	if err != nil {
		return nativeconfig.Connection{}, "", nativeconfig.ErrDenied
	}
	return connection, token, nil
}

func queueDiagnosticsError(err error) error {
	switch {
	case errors.Is(err, operatorread.ErrDenied), errors.Is(err, nativeconfig.ErrDenied), errors.Is(err, sessions.ErrDenied):
		return operatorread.ErrDenied
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, operatorread.ErrStale):
		return operatorread.ErrStale
	default:
		return operatorread.ErrUnavailable
	}
}
