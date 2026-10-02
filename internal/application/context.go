// Package application owns transport-independent workspace operations.
package application

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/contracts"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// RequestContext is a validated read binding. Its private fields prevent callers
// from constructing an unchecked binding. Neither constructor authenticates a
// credential: adapters must supply a principal they have already resolved.
type RequestContext struct {
	workspace workspace.Workspace
	principal string
	session   string
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
		return bind(w, principal, session)
	}
	return RequestContext{}, clikit.Refusedf("missing read session authority")
}

// NewCooperativeContext adapts the existing CLI token resolver. This is not a
// governance host session and grants no authenticated-host assurance.
func NewCooperativeContext(w *workspace.Workspace, id *agentid.Identity) (RequestContext, error) {
	if id == nil || strings.TrimSpace(id.ID) == "" || (id.Grant != model.GrantRW && id.Grant != model.GrantRO) {
		return RequestContext{}, clikit.Refusedf("missing read principal")
	}
	return bind(w, id.ID, "cooperative:"+id.ID)
}

func bind(w *workspace.Workspace, principal, session string) (RequestContext, error) {
	if w == nil || w.ID == "" || !filepath.IsAbs(w.Root) {
		return RequestContext{}, clikit.Refusedf("missing workspace read authority")
	}
	return RequestContext{workspace: *w, principal: principal, session: session}, nil
}

func (r RequestContext) validate() error {
	if r.workspace.ID == "" || !filepath.IsAbs(r.workspace.Root) || r.principal == "" || r.session == "" {
		return clikit.Refusedf("missing workspace read authority")
	}
	return nil
}
