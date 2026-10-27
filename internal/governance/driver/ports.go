package driver

import (
	"context"

	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// Receipt is an adapter's typed observation of one existing operation. It is
// never sufficient by itself to advance the pump; Run rereads durable state.
type Receipt struct {
	Kind, OperationKey, State string
	Host                      *executor.HostResult
	Intent                    *sessions.DispatchIntent
	Lifecycle                 *sessions.LifecycleStatus
	Verification              *sessions.VerificationLookup
	Review                    *sessions.Review
}

// EffectBudget is synchronously consumed immediately before a write-capable
// producer or host boundary. Reads and lookups consume work, not effects.
type EffectBudget interface {
	ReserveEffect() error
}

// Ports are trusted in-process adapters. They do not accept caller-supplied
// completion booleans, and each effect authenticates its own principal.
type Ports interface {
	Read(context.Context, Request) (sessions.ControllerDriverView, error)
	Step(context.Context, sessions.ControllerStepRequest) (sessions.ControllerStatus, sessions.ControllerHostDispatchPermit, error)
	Stop(context.Context, sessions.ControllerStepRequest) (sessions.ControllerStatus, error)
	Reconcile(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error)
	Dispatch(context.Context, sessions.ControllerHostDispatchPermit, sessions.ControllerAction, EffectBudget) ([]Receipt, error)
	Observe(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error)
	Verify(context.Context, sessions.ControllerAction, sessions.VerificationRequest, EffectBudget) ([]Receipt, error)
	Review(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error)
	Cancel(context.Context, sessions.ControllerAction, EffectBudget) ([]Receipt, error)
}
