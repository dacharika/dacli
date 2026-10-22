// Package application owns transport-independent workspace operations.
package application

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/contracts"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// RequestContext is a validated read binding. Its private fields prevent callers
// from constructing an unchecked binding. It contains no bearer credential.
type RequestContext struct {
	workspace workspace.Workspace
	principal string
	session   string
	resource  string
}

// NewNativeContext authenticates a native session credential for this read request.
// It proves session identity only, not human, host, or subagent identity. Adapters
// must call it afresh for each request; contexts are not revocation-aware caches.
func NewNativeContext(w *workspace.Workspace, authority *sessions.Authority, credential, session string) (RequestContext, error) {
	return NewNativeReadContext(w, authority, credential, session, "workspace:"+workspaceID(w))
}

// NewNativeReadContext authenticates a fresh native read against one exact
// workspace, project, or task resource. Callers must choose the narrowest
// resource that contains the requested output; this is not a cached session.
func NewNativeReadContext(w *workspace.Workspace, authority *sessions.Authority, credential, session, resource string) (RequestContext, error) {
	if w == nil || w.ID == "" || !filepath.IsAbs(w.Root) {
		return RequestContext{}, clikit.Refusedf("native read authority denied")
	}
	if resource != "workspace:"+w.ID {
		kind, value, ok := strings.Cut(resource, ":")
		if !ok || value == "" || (kind != "project" && kind != "task") || !workspace.SafeSegment(value) {
			return RequestContext{}, clikit.Refusedf("native read authority denied")
		}
	}
	s, err := authority.Authorize(credential, session, w.ID, "read", resource)
	if err != nil {
		// Authority errors and caller inputs never enter transport diagnostics.
		return RequestContext{}, clikit.Refusedf("native read authority denied")
	}
	return bind(w, s.ID, s.ID, resource)
}

func workspaceID(w *workspace.Workspace) string {
	if w == nil {
		return ""
	}
	return w.ID
}

// NewRequestContext validates a governance snapshot and binds the resolved human
// principal to an unrevoked session in the selected workspace.
func NewRequestContext(w *workspace.Workspace, principal, session string, b contracts.Bundle, now time.Time) (RequestContext, error) {
	if w == nil || b.Workspace != w.ID || principal == "" || session == "" || now.IsZero() {
		return RequestContext{}, clikit.Refusedf("missing or wrong-workspace read authority")
	}
	if err := b.Validate(); err != nil {
		return RequestContext{}, clikit.Refusedf("invalid read authority: %v", err)
	}
	for _, s := range b.Sessions {
		if s.ID != session || s.Human != principal {
			continue
		}
		for _, r := range b.Revocations {
			if (r.Session == session || r.Session == s.Parent) && !now.Before(r.RevokedAt) {
				return RequestContext{}, clikit.Refusedf("revoked read session")
			}
		}
		return bind(w, principal, session, "workspace:"+w.ID)
	}
	return RequestContext{}, clikit.Refusedf("missing read session authority")
}

// NewCooperativeContext adapts the existing CLI token resolver. This is not a
// governance host session and grants no authenticated-host assurance.
func NewCooperativeContext(w *workspace.Workspace, id *agentid.Identity) (RequestContext, error) {
	if id == nil || strings.TrimSpace(id.ID) == "" || (id.Grant != model.GrantRW && id.Grant != model.GrantRO) {
		return RequestContext{}, clikit.Refusedf("missing read principal")
	}
	return bind(w, id.ID, "cooperative:"+id.ID, "workspace:"+workspaceID(w))
}

func bind(w *workspace.Workspace, principal, session, resource string) (RequestContext, error) {
	if w == nil || w.ID == "" || !filepath.IsAbs(w.Root) {
		return RequestContext{}, clikit.Refusedf("missing workspace read authority")
	}
	return RequestContext{workspace: *w, principal: principal, session: session, resource: resource}, nil
}

func (r RequestContext) validate() error {
	if r.workspace.ID == "" || !filepath.IsAbs(r.workspace.Root) || r.principal == "" || r.session == "" || r.resource == "" {
		return clikit.Refusedf("missing workspace read authority")
	}
	return nil
}
