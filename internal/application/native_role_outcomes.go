package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// Outcomes returns private owner-authenticated aggregates for this exact
// workspace project. current() reopens and authenticates the private profile
// for every read, matching the other role registry operations.
func (p *NativeRoleRegistryOwner) Outcomes() ([]sessions.NativeRoleOutcome, error) {
	_, _, _, authority, credential, err := p.current()
	if err != nil {
		return nil, sessions.ErrDenied
	}
	out, err := authority.OwnerNativeRoleOutcomes(credential, p.project)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	return out, nil
}
