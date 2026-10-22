package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// ObserveNativePolicy resolves the exact canonical task before the final scoped
// authority read. No owner credential, policy mutation or acceptance is involved.
func ObserveNativePolicy(w *workspace.Workspace, a *sessions.Authority, token, session, project, task, decision string) (sessions.NativePolicyObservation, error) {
	ctx, e := NewNativeContext(w, a, token, session)
	if e != nil {
		return sessions.NativePolicyObservation{}, e
	}
	t, e := GetTask(ctx, task)
	if e != nil || t.ID != task || t.Project != project {
		return sessions.NativePolicyObservation{}, sessions.ErrDenied
	}
	return a.ObserveNativePolicy(token, session, project, task, decision)
}
