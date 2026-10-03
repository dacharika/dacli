package sessions

import (
	"encoding/hex"
	"strings"
	"time"
	"unicode"
)

// DispatchCapabilityAdmission observes the explicit manual provisioning channel.
type DispatchCapabilityAdmission interface {
	AdmitDispatchCapability(workspace, project, harness, digest string) error
}

func (a *Authority) dispatchCapability(s *state, d DispatchReservation) error {
	p, ok := s.SwarmPolicies[d.ProjectID]
	if !ok || p.Stopped || p.Revision != d.PolicyRevision || p.Harness != d.Harness {
		return ErrDenied
	}
	observer, ok := a.admission.(DispatchCapabilityAdmission)
	if !ok {
		return ErrDenied
	}
	return observer.AdmitDispatchCapability(s.Binding.Workspace, d.ProjectID, d.Harness, p.CapabilityDigest)
}

// DispatchIntent is cooperative execution correlation, never host authentication.
// A retained reservation remains protected until the expected worker authenticates.
type DispatchIntent struct {
	SchemaVersion                                                                       int
	Sequence                                                                            uint64
	ID, OperationKey, DispatchID, ControllerSessionID, BridgeSessionID, WorkerSessionID string
	PromptSpecDigest, CapabilityDigest, State, HostAgentID                              string
	Reservation                                                                         DispatchReservation
	RequestedAt, ObservedAt                                                             time.Time
	Lease                                                                               *Lease
}
type DispatchIntentReceipt struct {
	Digest string
	Intent DispatchIntent
}
type RequestDispatchIntent struct {
	OperationKey, DispatchID, BridgeSessionID, WorkerSessionID, PromptSpecDigest string
	ExpectedFence                                                                uint64
}

// DispatchIntentResult grants a host action only on the first durable request.
// Historical receipts intentionally never grant another launch.
type DispatchIntentResult struct {
	Intent               DispatchIntent
	HostActionAuthorized bool
}
type CorrelateDispatchIntent struct {
	OperationKey, DispatchID, IntentID, HostAgentID, State string
	ExpectedFence, ExpectedSequence                        uint64
}
type AcknowledgeDispatchIntent struct {
	OperationKey, DispatchID, IntentID, AttemptID, HostAgentID string
	ExpectedFence, ExpectedSequence                            uint64
}

func (a *Authority) UpgradeDispatchIntents(owner string) error {
	old := *a
	old.version = 7
	return old.transaction(false, true, func(s *state) error {
		if s.Version != 7 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.DispatchIntents = map[string]DispatchIntent{}
		s.DispatchIntentOperations = map[string]DispatchIntentReceipt{}
		s.Version = 8
		return nil
	})
}
func dispatchActor(s *state, token, id, project, action string) (record, error) {
	r, e := authenticate(s, token, id, time.Now())
	if e != nil {
		return record{}, e
	}
	if !subset([]string{action}, r.Scope.Actions) || !subset([]string{"project:" + project}, r.Scope.Resources) {
		return record{}, ErrDenied
	}
	return r, nil
}
func dispatchBoundActor(s *state, id, project, action string, d DispatchReservation) bool {
	r, ok := s.Sessions[id]
	if action == "lease.acquire" && workerTaskWithdrawn(s, id, d.Reservation.TaskID) {
		return false
	}
	if !ok || r.Parent != "" || !swarmLineageCurrent(s, id, time.Now()) || !subset([]string{action}, r.Scope.Actions) || (action != "lease.acquire" && !subset([]string{"project:" + project}, r.Scope.Resources)) {
		return false
	}
	for _, res := range d.Reservation.Resources {
		if !subset([]string{res.Kind + ":" + res.Path}, r.Scope.Resources) {
			return false
		}
	}
	return !r.ExpiresAt.Before(d.Reservation.ExpiresAt)
}
func (a *Authority) RequestDispatchIntent(token, controllerID string, req RequestDispatchIntent) (DispatchIntentResult, error) {
	var result DispatchIntentResult
	err := a.transaction(false, true, func(s *state) error {
		if s.Version < 8 {
			return ErrDenied
		}
		d, ok := s.Dispatches[req.DispatchID]
		if !ok {
			return ErrDenied
		}
		if _, e := dispatchActor(s, token, controllerID, d.ProjectID, "swarm.dispatch"); e != nil {
			return e
		}
		if !validID(req.OperationKey) || !validPromptDigest(req.PromptSpecDigest) || req.ExpectedFence != d.Reservation.Fence {
			return ErrDenied
		}
		digest := identity("dispatch.request", controllerID, req)
		if old, ok := s.DispatchIntentOperations[req.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			result.Intent = old.Intent
			return nil
		}
		if _, ok := s.DispatchIntents[d.ID]; ok {
			return ErrDenied
		}
		if req.BridgeSessionID == controllerID || req.WorkerSessionID == controllerID || req.WorkerSessionID == req.BridgeSessionID {
			return ErrDenied
		}
		if !dispatchBoundActor(s, req.BridgeSessionID, d.ProjectID, "dispatch.correlate", d) || !dispatchBoundActor(s, req.WorkerSessionID, d.ProjectID, "lease.acquire", d) {
			return ErrDenied
		}
		if a.dispatchCapability(s, d) != nil {
			return ErrDenied
		}
		fresh, e := a.admitDispatch(s, token, controllerID, AdmitDispatchRequest{DispatchID: d.ID, ExpectedFence: req.ExpectedFence})
		if e != nil {
			return e
		}
		id, e := mint()
		if e != nil {
			return e
		}
		p := s.SwarmPolicies[d.ProjectID]
		intent := DispatchIntent{SchemaVersion: 1, Sequence: 1, ID: id, OperationKey: req.OperationKey, DispatchID: d.ID, ControllerSessionID: controllerID, BridgeSessionID: req.BridgeSessionID, WorkerSessionID: req.WorkerSessionID, PromptSpecDigest: req.PromptSpecDigest, CapabilityDigest: p.CapabilityDigest, State: "dispatch_requested", Reservation: fresh, RequestedAt: time.Now().UTC()}
		if _, e := dispatchActor(s, token, controllerID, d.ProjectID, "swarm.dispatch"); e != nil {
			return e
		}
		if !dispatchBoundActor(s, req.BridgeSessionID, d.ProjectID, "dispatch.correlate", d) || !dispatchBoundActor(s, req.WorkerSessionID, d.ProjectID, "lease.acquire", d) {
			return ErrDenied
		}
		if a.dispatchCapability(s, d) != nil {
			return ErrDenied
		}
		if _, e := dispatchActor(s, token, controllerID, d.ProjectID, "swarm.dispatch"); e != nil {
			return e
		}
		if !time.Now().Before(d.Reservation.ExpiresAt) || !dispatchBoundActor(s, req.BridgeSessionID, d.ProjectID, "dispatch.correlate", d) || !dispatchBoundActor(s, req.WorkerSessionID, d.ProjectID, "lease.acquire", d) {
			return ErrDenied
		}
		s.DispatchIntents[d.ID] = intent
		s.DispatchIntentOperations[req.OperationKey] = DispatchIntentReceipt{Digest: digest, Intent: intent}
		result = DispatchIntentResult{Intent: intent, HostActionAuthorized: true}
		return nil
	})
	if err != nil {
		return DispatchIntentResult{}, err
	}
	return result, nil
}
func (a *Authority) CorrelateDispatchIntent(token, bridge string, req CorrelateDispatchIntent) (DispatchIntent, error) {
	var result DispatchIntent
	err := a.transaction(false, true, func(s *state) error {
		i, ok := s.DispatchIntents[req.DispatchID]
		if s.Version < 8 || !ok || i.ID != req.IntentID || i.BridgeSessionID != bridge {
			return ErrDenied
		}
		if _, e := dispatchActor(s, token, bridge, i.Reservation.ProjectID, "dispatch.correlate"); e != nil {
			return e
		}
		if !validID(req.OperationKey) || req.ExpectedFence != i.Reservation.Reservation.Fence {
			return ErrDenied
		}
		digest := identity("dispatch.correlate", bridge, req)
		if old, ok := s.DispatchIntentOperations[req.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			result = old.Intent
			return nil
		}
		if _, recovered := s.Recoveries["reservation:"+i.DispatchID]; recovered {
			return ErrDenied
		}
		if _, cancelled := s.LifecycleCancellations[i.DispatchID]; cancelled {
			return ErrDenied
		}
		if req.ExpectedSequence != i.Sequence || (i.State != "dispatch_requested" && !((i.State == "dispatch_unknown" || i.State == "manual_handoff_required") && req.State == "host_correlated")) {
			return ErrDenied
		}
		switch req.State {
		case "host_correlated":
			if !validHostAgentID(req.HostAgentID) {
				return ErrDenied
			}
		case "manual_handoff_required", "dispatch_unknown":
			if req.HostAgentID != "" {
				return ErrDenied
			}
		default:
			return ErrDenied
		}
		if !dispatchBoundActor(s, bridge, i.Reservation.ProjectID, "dispatch.correlate", i.Reservation) || !swarmLineageCurrent(s, i.ControllerSessionID, time.Now()) {
			return ErrDenied
		}
		if req.State == "host_correlated" && (a.dispatchCapability(s, i.Reservation) != nil || !time.Now().Before(i.Reservation.Reservation.ExpiresAt)) {
			return ErrDenied
		}
		if _, e := dispatchActor(s, token, bridge, i.Reservation.ProjectID, "dispatch.correlate"); e != nil {
			return e
		}
		if !swarmLineageCurrent(s, i.ControllerSessionID, time.Now()) {
			return ErrDenied
		}
		i.Sequence++
		i.State = req.State
		i.HostAgentID = req.HostAgentID
		i.ObservedAt = time.Now().UTC()
		s.DispatchIntents[req.DispatchID] = i
		s.DispatchIntentOperations[req.OperationKey] = DispatchIntentReceipt{Digest: digest, Intent: i}
		result = i
		return nil
	})
	if err != nil {
		return DispatchIntent{}, err
	}
	return result, nil
}
func (a *Authority) AcknowledgeDispatchIntent(token, worker string, req AcknowledgeDispatchIntent) (DispatchIntent, error) {
	var result DispatchIntent
	err := a.transaction(false, true, func(s *state) error {
		i, ok := s.DispatchIntents[req.DispatchID]
		if s.Version < 8 || !ok || i.ID != req.IntentID || i.WorkerSessionID != worker {
			return ErrDenied
		}
		if _, _, e := leaseAuth(s, token, worker, req.AttemptID, "lease.acquire", time.Now()); e != nil {
			return e
		}
		if !validID(req.OperationKey) || req.ExpectedFence != i.Reservation.Reservation.Fence {
			return ErrDenied
		}
		digest := identity("dispatch.acknowledge", worker, req)
		if old, ok := s.DispatchIntentOperations[req.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			result = old.Intent
			return nil
		}
		if _, recovered := s.Recoveries["reservation:"+i.DispatchID]; recovered {
			return ErrDenied
		}
		if _, cancelled := s.LifecycleCancellations[i.DispatchID]; cancelled {
			return ErrDenied
		}
		if !lifecycleDispatchEligible(s, i.DispatchID) || req.ExpectedSequence != i.Sequence || i.State != "host_correlated" || i.HostAgentID != req.HostAgentID || !dispatchBoundActor(s, i.BridgeSessionID, i.Reservation.ProjectID, "dispatch.correlate", i.Reservation) {
			return ErrDenied
		}
		if a.dispatchCapability(s, i.Reservation) != nil {
			return ErrDenied
		}
		var lease Lease
		if e := a.handoffDispatch(s, token, worker, HandoffDispatchRequest{OperationKey: req.OperationKey, DispatchID: req.DispatchID, AttemptID: req.AttemptID, ExpectedFence: req.ExpectedFence}, &lease); e != nil {
			return e
		}
		if !dispatchBoundActor(s, i.BridgeSessionID, i.Reservation.ProjectID, "dispatch.correlate", i.Reservation) {
			return ErrDenied
		}
		if a.dispatchCapability(s, i.Reservation) != nil {
			return ErrDenied
		}
		if _, _, e := leaseAuth(s, token, worker, req.AttemptID, "lease.acquire", time.Now()); e != nil {
			return e
		}
		if !time.Now().Before(i.Reservation.Reservation.ExpiresAt) || !swarmLineageCurrent(s, i.ControllerSessionID, time.Now()) || !dispatchBoundActor(s, i.BridgeSessionID, i.Reservation.ProjectID, "dispatch.correlate", i.Reservation) {
			return ErrDenied
		}
		i.Sequence++
		i.State = "acknowledged"
		i.Lease = &lease
		i.ObservedAt = time.Now().UTC()
		s.DispatchIntents[req.DispatchID] = i
		s.DispatchIntentOperations[req.OperationKey] = DispatchIntentReceipt{Digest: digest, Intent: i}
		result = i
		return nil
	})
	if err != nil {
		return DispatchIntent{}, err
	}
	return result, nil
}
func (a *Authority) DispatchIntentStatus(token, actor, dispatchID string) (DispatchIntent, error) {
	var result DispatchIntent
	err := a.transaction(false, false, func(s *state) error {
		i, ok := s.DispatchIntents[dispatchID]
		if s.Version < 8 || !ok {
			return ErrDenied
		}
		r, e := authenticate(s, token, actor, time.Now())
		if e != nil {
			return e
		}
		if actor != i.ControllerSessionID && actor != i.BridgeSessionID && actor != i.WorkerSessionID {
			return ErrDenied
		}
		if !subset([]string{"read"}, r.Scope.Actions) || !subset([]string{"task:" + i.Reservation.Reservation.TaskID}, r.Scope.Resources) {
			return ErrDenied
		}
		result = i
		return nil
	})
	if err != nil {
		return DispatchIntent{}, err
	}
	return result, nil
}

func validHostAgentID(id string) bool {
	if len(id) == 0 || len(id) > 1024 {
		return false
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPromptDigest(d string) bool {
	if !strings.HasPrefix(d, "sha256:") || len(d) != 71 || strings.ToLower(d) != d {
		return false
	}
	_, e := hex.DecodeString(d[7:])
	return e == nil
}
