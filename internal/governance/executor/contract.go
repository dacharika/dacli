// Package executor defines the versioned boundary between DACLI governance and
// a separately provisioned native host. It deliberately owns no durable state.
package executor

import (
	"errors"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

const ContractVersion = 1

var ErrDenied = errors.New("executor contract refused")
var ErrUncertain = errors.New("executor outcome remains unknown; reconcile the original operation key")

type Support string

const (
	Supported   Support = "supported"
	Unsupported Support = "unsupported"
	Unqualified Support = "unqualified"
)

func UnqualifiedCapabilities(host string) Capabilities {
	unknown := Capability{State: Unqualified, Detail: "not qualified for this host/process channel"}
	return Capabilities{Version: ContractVersion, Host: host, Model: unknown, Dispatch: unknown, Lookup: unknown, PrivateProfileHandoff: unknown, Observe: unknown, Cancel: unknown}
}

// Capability reports evidence without implying an authorization grant.
type Capability struct {
	State  Support `json:"state"`
	Detail string  `json:"detail,omitempty"`
}
type Capabilities struct {
	Version               int        `json:"version"`
	Host                  string     `json:"host"`
	Model                 Capability `json:"model"`
	Dispatch              Capability `json:"dispatch"`
	Lookup                Capability `json:"lookup_original_key"`
	PrivateProfileHandoff Capability `json:"private_profile_handoff"`
	Observe               Capability `json:"observation"`
	Cancel                Capability `json:"cancellation"`
}

// Binding is a credential-free reference to the exact owner enrollment. Labels
// such as Host and Instance are correlation values; the separately trusted
// enrollment is the authority against which they are compared.
type Binding struct {
	Workspace    string    `json:"workspace"`
	Project      string    `json:"project"`
	Task         string    `json:"task"`
	Attempt      string    `json:"attempt"`
	Host         string    `json:"host"`
	Session      string    `json:"session"`
	Instance     string    `json:"instance"`
	CredentialID string    `json:"credential_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	Revoked      bool      `json:"revoked"`
}

// WorkerEnrollment comes from owner-controlled private provisioning config.
// Host and process labels are cooperative claims, not OS process attestations.
type WorkerEnrollment struct {
	Binding        Binding
	Profile        ProfileRef
	AttemptRequest sessions.AttemptRequest
}

// AuthenticatedBinding is minted only after the real session authority
// authenticates the worker and registers its exact attempt.
type AuthenticatedBinding struct {
	binding Binding
	token   string
	profile ProfileRef
}

func (a AuthenticatedBinding) Claim() Binding { return a.binding }

func AuthenticateWorker(authority *sessions.Authority, token string, e WorkerEnrollment, now time.Time) (AuthenticatedBinding, error) {
	if authority == nil || token == "" || e.Binding.Session == "" || e.AttemptRequest.TaskID != e.Binding.Task || e.AttemptRequest.RequestID == "" {
		return AuthenticatedBinding{}, ErrDenied
	}
	session, err := authority.Authenticate(token, e.Binding.Session)
	if err != nil || session.Revoked || !now.Before(session.ExpiresAt) || e.Binding.ExpiresAt.After(session.ExpiresAt) {
		return AuthenticatedBinding{}, ErrDenied
	}
	attempt, err := authority.RegisterAttempt(token, session.ID, e.AttemptRequest)
	if err != nil || attempt.SessionID != session.ID || attempt.Request.TaskID != e.Binding.Task || attempt.ID == "" {
		return AuthenticatedBinding{}, ErrDenied
	}
	e.Binding.Attempt = attempt.ID
	if e.Profile.WorkerSession != session.ID || e.Binding.CredentialID != e.Profile.ID || ValidateProfile(e.Profile, e.Binding, now) != nil || ValidateBinding(e.Binding, e.Binding, now) != nil {
		return AuthenticatedBinding{}, ErrDenied
	}
	return AuthenticatedBinding{binding: e.Binding, token: token, profile: e.Profile}, nil
}

func (b Binding) same(other Binding) bool {
	return b.Workspace == other.Workspace && b.Project == other.Project && b.Task == other.Task && b.Attempt == other.Attempt && b.Host == other.Host && b.Session == other.Session && b.Instance == other.Instance && b.CredentialID == other.CredentialID && b.ExpiresAt.Equal(other.ExpiresAt) && b.Revoked == other.Revoked
}

// ValidateBinding fails closed on any enrollment substitution, expiry or
// revocation. CredentialID is an owner-issued opaque identifier, never a token.
func ValidateBinding(enrolled, observed Binding, now time.Time) error {
	if enrolled.Workspace == "" || enrolled.Project == "" || enrolled.Task == "" || enrolled.Attempt == "" || enrolled.Host == "" || enrolled.Session == "" || enrolled.Instance == "" || enrolled.CredentialID == "" || enrolled.Revoked || !now.Before(enrolled.ExpiresAt) || !enrolled.same(observed) || observed.Revoked {
		return ErrDenied
	}
	return nil
}

type Registration struct {
	Version      int          `json:"version"`
	Binding      Binding      `json:"binding"`
	Capabilities Capabilities `json:"capabilities"`
}

func validateRegistration(enrolled Binding, r Registration, now time.Time) error {
	if r.Version != ContractVersion || r.Capabilities.Version != ContractVersion || r.Capabilities.Host != enrolled.Host {
		return ErrDenied
	}
	if err := ValidateBinding(enrolled, r.Binding, now); err != nil {
		return err
	}
	for _, c := range []Capability{r.Capabilities.Model, r.Capabilities.Dispatch, r.Capabilities.Lookup, r.Capabilities.PrivateProfileHandoff, r.Capabilities.Observe, r.Capabilities.Cancel} {
		if c.State != Supported && c.State != Unsupported && c.State != Unqualified {
			return ErrDenied
		}
	}
	return nil
}

// ValidateRegistration requires a worker binding minted after real session
// authentication; caller-provided identity labels alone cannot register.
func ValidateRegistration(authority *sessions.Authority, worker AuthenticatedBinding, r Registration, now time.Time) error {
	if authority == nil || worker.token == "" {
		return ErrDenied
	}
	current, err := authority.Authenticate(worker.token, worker.binding.Session)
	if err != nil || current.Revoked || !now.Before(current.ExpiresAt) || !worker.binding.same(r.Binding) {
		return ErrDenied
	}
	return validateRegistration(worker.binding, r, now)
}

type ProfileRef struct {
	ID            string    `json:"id"`
	WorkerSession string    `json:"worker_session"`
	ScopeDigest   string    `json:"scope_digest"`
	ExpiresAt     time.Time `json:"expires_at"`
	Revoked       bool      `json:"revoked"`
}

func ValidateProfile(p ProfileRef, b Binding, now time.Time) error {
	if p.ID == "" || p.ScopeDigest == "" || p.WorkerSession != b.Session || p.Revoked || b.Revoked || !now.Before(p.ExpiresAt) || !now.Before(b.ExpiresAt) || p.ExpiresAt.After(b.ExpiresAt) {
		return ErrDenied
	}
	return nil
}

type Dispatch struct {
	Version          int        `json:"version"`
	OperationKey     string     `json:"operation_key"`
	DispatchID       string     `json:"dispatch_id"`
	BridgeSession    string     `json:"bridge_session"`
	ExpectedFence    uint64     `json:"expected_fence"`
	PromptSpecDigest string     `json:"prompt_spec_digest"`
	Binding          Binding    `json:"binding"`
	Profile          ProfileRef `json:"profile"`
	Prompt           string     `json:"prompt"`
}
type HostResult struct {
	Version         int     `json:"version"`
	OperationKey    string  `json:"operation_key"`
	HostExecutionID string  `json:"host_execution_id"`
	State           string  `json:"state"`
	Binding         Binding `json:"binding"`
}
type Observation struct {
	Version        int       `json:"version"`
	OperationKey   string    `json:"operation_key"`
	Binding        Binding   `json:"binding"`
	Sequence       uint64    `json:"sequence"`
	State          string    `json:"state"`
	EvidenceDigest string    `json:"evidence_digest"`
	SourceTime     time.Time `json:"source_time"`
}

// Backend operations are capability-gated. Dispatch must durably bind its
// original operation key before any external call; after uncertainty callers
// use Lookup with that same key and never issue a replacement dispatch.
type Backend interface {
	Register(Registration) error
	Capabilities() Capabilities
	Dispatch(Dispatch) (HostResult, error)
	Lookup(operationKey string, binding Binding) (HostResult, error)
	Observe(Observation) error
	Cancel(operationKey string, binding Binding) (string, error) // acknowledgement: acknowledged|unsupported|unknown
}

// Coordinator makes one host call and, on an uncertain response, looks up the
// same operation key. It never turns a lookup miss into a second launch.
type Coordinator struct {
	Backend                            Backend
	Authority                          *sessions.Authority
	ControllerToken, ControllerSession string
}

func (c Coordinator) DispatchOnce(req Dispatch, enrolled AuthenticatedBinding, now time.Time) (HostResult, error) {
	if c.Backend == nil || c.Authority == nil || req.Version != ContractVersion || req.OperationKey == "" || req.DispatchID == "" || req.BridgeSession == "" || req.ExpectedFence == 0 || req.PromptSpecDigest == "" || ValidateBinding(enrolled.binding, req.Binding, now) != nil || !enrolled.binding.same(req.Binding) || req.Profile != enrolled.profile || ValidateProfile(enrolled.profile, req.Binding, now) != nil {
		return HostResult{}, ErrDenied
	}
	current, authErr := c.Authority.Authenticate(enrolled.token, enrolled.binding.Session)
	if authErr != nil || current.Revoked || !now.Before(current.ExpiresAt) {
		return HostResult{}, ErrDenied
	}
	caps := c.Backend.Capabilities()
	if caps.Version != ContractVersion || caps.Host != enrolled.binding.Host || caps.Dispatch.State != Supported || caps.Lookup.State != Supported || caps.PrivateProfileHandoff.State != Supported {
		return HostResult{}, ErrDenied
	}
	permit, err := c.Authority.RequestDispatchIntent(c.ControllerToken, c.ControllerSession, sessions.RequestDispatchIntent{OperationKey: req.OperationKey, DispatchID: req.DispatchID, ExpectedFence: req.ExpectedFence, BridgeSessionID: req.BridgeSession, WorkerSessionID: req.Binding.Session, PromptSpecDigest: req.PromptSpecDigest})
	if err != nil {
		return HostResult{}, ErrDenied
	}
	if !permit.HostActionAuthorized {
		lookup, lookupErr := c.Backend.Lookup(req.OperationKey, req.Binding)
		if lookupErr == nil && hostResultMatches(lookup, req) {
			return lookup, nil
		}
		return HostResult{}, ErrUncertain
	}
	result, err := c.Backend.Dispatch(req)
	if err == nil && hostResultMatches(result, req) {
		return result, nil
	}
	lookup, lookupErr := c.Backend.Lookup(req.OperationKey, req.Binding)
	if lookupErr == nil && hostResultMatches(lookup, req) {
		return lookup, nil
	}
	return HostResult{}, ErrUncertain
}

func hostResultMatches(result HostResult, req Dispatch) bool {
	return result.Version == ContractVersion && result.OperationKey == req.OperationKey && result.HostExecutionID != "" && result.Binding.same(req.Binding)
}

// ValidateObservation enforces an exact one-step sequence and terminal-state
// monotonicity before the existing authority records a host callback.
func ValidateObservation(previousState string, expected uint64, next Observation) error {
	if next.Version != ContractVersion || next.OperationKey == "" || next.Sequence != expected+1 || next.State == "" || next.EvidenceDigest == "" {
		return ErrDenied
	}
	if previousState == "completed" || previousState == "failed" || previousState == "cancelled" {
		return ErrDenied
	}
	if next.State != "running" && next.State != "unknown" && next.State != "completed" && next.State != "failed" && next.State != "cancelled" {
		return ErrDenied
	}
	return nil
}

// LifecycleAuthority delegates durable observations and cancellation to the
// existing session authority. It is intentionally only an adapter, not a ledger.
type LifecycleAuthority struct {
	Authority                                                      *sessions.Authority
	BridgeToken, BridgeSession, ControllerToken, ControllerSession string
}

func (a LifecycleAuthority) Observe(req sessions.ObserveLifecycleRequest) (sessions.LifecycleObservation, error) {
	if a.Authority == nil {
		return sessions.LifecycleObservation{}, ErrDenied
	}
	return a.Authority.ObserveLifecycle(a.BridgeToken, a.BridgeSession, req)
}
func (a LifecycleAuthority) Cancel(req sessions.CancelLifecycleRequest) (sessions.LifecycleCancellationResult, error) {
	if a.Authority == nil {
		return sessions.LifecycleCancellationResult{}, ErrDenied
	}
	return a.Authority.CancelLifecycle(a.ControllerToken, a.ControllerSession, req)
}
func (a LifecycleAuthority) Status(dispatch string) (sessions.LifecycleStatus, error) {
	if a.Authority == nil {
		return sessions.LifecycleStatus{}, ErrDenied
	}
	return a.Authority.LifecycleStatus(a.BridgeToken, a.BridgeSession, dispatch)
}

// AcceptObservation checks authenticated exact binding and monotonic sequence
// before a host callback reaches the durable sessions lifecycle authority.

func (a LifecycleAuthority) AcceptObservation(enrolled AuthenticatedBinding, b sessions.LifecycleBinding, o Observation, expected uint64, now time.Time) (sessions.LifecycleObservation, error) {
	if a.Authority == nil || ValidateBinding(enrolled.binding, o.Binding, now) != nil || !enrolled.binding.same(o.Binding) {
		return sessions.LifecycleObservation{}, ErrDenied
	}
	current, authErr := a.Authority.Authenticate(enrolled.token, enrolled.binding.Session)
	if authErr != nil || current.Revoked || !now.Before(current.ExpiresAt) || b.TaskID != enrolled.binding.Task || b.AttemptID != enrolled.binding.Attempt || b.WorkerSessionID != enrolled.binding.Session {
		return sessions.LifecycleObservation{}, ErrDenied
	}
	status, err := a.Status(b.DispatchID)
	if err != nil || ValidateObservation(status.State, expected, o) != nil {
		return sessions.LifecycleObservation{}, ErrDenied
	}
	return a.Observe(sessions.ObserveLifecycleRequest{OperationKey: o.OperationKey, Binding: b, ExpectedSequence: expected, HostSequence: o.Sequence, State: o.State, Source: "owner-manual-native-host", EvidenceDigest: o.EvidenceDigest, SourceTime: o.SourceTime})
}
