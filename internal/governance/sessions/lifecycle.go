package sessions

import (
	"reflect"
	"time"
)

// LifecyclePolicy is explicit owner authority, not a claim that a host API works.
type LifecyclePolicy struct {
	SchemaVersion                                    int
	ProjectID, Revision, Source, CancellationChannel string
	Freshness                                        time.Duration
	CancellationEnabled                              bool
}

// LifecycleObservation binds a cooperative host report to immutable dispatch identity.
// SourceTime is supplied by the bridge; ReceivedAt is assigned by DACLI.
type LifecycleObservation struct {
	Sequence, HostSequence                   uint64
	Binding                                  LifecycleBinding
	State, Source, Assurance, EvidenceDigest string
	SourceTime, ReceivedAt                   time.Time
}
type LifecycleBinding struct {
	DispatchID, IntentID, BridgeSessionID, WorkerSessionID, TaskID, Checkout, BaseCommit, HostAgentID, AttemptID, LeaseID string
	ReservationFence, LeaseFence                                                                                          uint64
}

// LifecycleCancellation withdraws future task authority without freeing resources.
// Host acknowledgement and terminal observations remain separate facts.
type LifecycleCancellation struct {
	Sequence                                                     uint64
	Binding                                                      LifecycleBinding
	OperationKey, PolicyRevision, Channel, State, EvidenceDigest string
	RequestedBy                                                  string
	RequestedAt, AcknowledgedAt                                  time.Time
}
type LifecycleReceipt struct {
	Digest       string
	Observation  *LifecycleObservation
	Cancellation *LifecycleCancellation
}
type ObserveLifecycleRequest struct {
	OperationKey                   string
	Binding                        LifecycleBinding
	ExpectedSequence, HostSequence uint64
	State, Source, EvidenceDigest  string
	SourceTime                     time.Time
}
type CancelLifecycleRequest struct {
	OperationKey string
	Binding      LifecycleBinding
}
type AcknowledgeCancellationRequest struct {
	ExpectedSequence      uint64
	OperationKey          string
	Binding               LifecycleBinding
	State, EvidenceDigest string
}
type LifecycleCancellationResult struct {
	Cancellation         LifecycleCancellation
	HostActionAuthorized bool
}
type LifecycleStatus struct {
	Binding                LifecycleBinding
	Observations           []LifecycleObservation
	Cancellation           *LifecycleCancellation
	State, Freshness       string
	ObservedAt, FreshUntil time.Time
	ResourcesRetained      bool
}

func (a *Authority) UpgradeLifecycle(owner string) error {
	old := *a
	old.version = 8
	return old.transaction(false, true, func(s *state) error {
		if s.Version != 8 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.LifecyclePolicyHistory = map[string]map[string]LifecyclePolicy{}
		s.LifecyclePolicies = map[string]LifecyclePolicy{}
		s.LifecycleObservations = map[string][]LifecycleObservation{}
		s.LifecycleCancellations = map[string]LifecycleCancellation{}
		s.LifecycleOperations = map[string]LifecycleReceipt{}
		s.Version = 9
		return nil
	})
}
func (a *Authority) ConfigureLifecycle(owner string, p LifecyclePolicy) error {
	return a.transaction(false, true, func(s *state) error {
		if s.Version < 9 || len(owner) != 64 || hash(owner) != s.OwnerHash || p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.Revision) || p.Source != "owner-manual-native-host" || p.Freshness <= 0 || p.Freshness > time.Hour || (p.CancellationChannel != "unsupported" && p.CancellationChannel != "owner-manual-native-host") {
			return ErrDenied
		}
		if _, ok := s.SwarmPolicies[p.ProjectID]; !ok {
			return ErrDenied
		}
		if old, ok := s.LifecyclePolicyHistory[p.ProjectID][p.Revision]; ok {
			if !reflect.DeepEqual(old, p) {
				return ErrDenied
			}
			return nil // historical retry never resets current policy
		}
		if s.LifecyclePolicyHistory[p.ProjectID] == nil {
			s.LifecyclePolicyHistory[p.ProjectID] = map[string]LifecyclePolicy{}
		}
		s.LifecyclePolicyHistory[p.ProjectID][p.Revision] = p
		s.LifecyclePolicies[p.ProjectID] = p
		return nil
	})
}
func lifecycleBinding(i DispatchIntent) LifecycleBinding {
	d := i.Reservation
	b := LifecycleBinding{DispatchID: i.DispatchID, IntentID: i.ID, BridgeSessionID: i.BridgeSessionID, WorkerSessionID: i.WorkerSessionID, TaskID: d.Reservation.TaskID, Checkout: d.Reservation.Checkout, BaseCommit: d.BaseCommit, HostAgentID: i.HostAgentID, ReservationFence: d.Reservation.Fence}
	if i.Lease != nil {
		b.AttemptID = i.Lease.AttemptID
		b.LeaseID = i.Lease.ID
		b.LeaseFence = i.Lease.Fence
	}
	return b
}
func lifecycleIntent(s *state, b LifecycleBinding) (DispatchIntent, error) {
	i, ok := s.DispatchIntents[b.DispatchID]
	if s.Version < 9 || !ok || !reflect.DeepEqual(lifecycleBinding(i), b) {
		return DispatchIntent{}, ErrDenied
	}
	return i, nil
}

// workerTaskWithdrawn includes descendants without revoking unrelated root tasks.
// Withdrawal never expires and does not clear reservations or unreleased leases.
func workerTaskWithdrawn(s *state, session, task string) bool {
	if recoveryTaskWithdrawn(s, session, task) {
		return true
	}
	for _, c := range s.LifecycleCancellations {
		if c.Binding.TaskID != task {
			continue
		}
		id := session
		seen := map[string]bool{}
		for id != "" && !seen[id] {
			if id == c.Binding.WorkerSessionID {
				return true
			}
			seen[id] = true
			r, ok := s.Sessions[id]
			if !ok {
				break
			}
			id = r.Parent
		}
	}
	return false
}
func lifecycleTerminal(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}
func (a *Authority) ObserveLifecycle(token, bridge string, req ObserveLifecycleRequest) (LifecycleObservation, error) {
	var result LifecycleObservation
	err := a.transaction(false, true, func(s *state) error {
		i, ok := s.DispatchIntents[req.Binding.DispatchID]
		if s.Version < 9 || !ok || i.ID != req.Binding.IntentID {
			return ErrDenied
		}
		var e error
		if bridge != i.BridgeSessionID {
			return ErrDenied
		}
		if _, e = dispatchActor(s, token, bridge, i.Reservation.ProjectID, "dispatch.observe"); e != nil {
			return e
		}
		if !validID(req.OperationKey) {
			return ErrDenied
		}
		digest := identity("lifecycle.observe", bridge, req)
		if old, ok := s.LifecycleOperations[req.OperationKey]; ok {
			if old.Digest != digest || old.Observation == nil {
				return ErrDenied
			}
			result = *old.Observation
			return nil
		}
		if _, e = lifecycleIntent(s, req.Binding); e != nil {
			return e
		}
		p, ok := s.LifecyclePolicies[i.Reservation.ProjectID]
		now := time.Now().UTC()
		if !ok || req.Source != p.Source || !validDigest(req.EvidenceDigest) || req.SourceTime.IsZero() || req.SourceTime.After(now.Add(time.Minute)) || i.HostAgentID == "" || (req.State != "running" && req.State != "unknown" && !lifecycleTerminal(req.State)) {
			return ErrDenied
		}
		history := s.LifecycleObservations[i.DispatchID]
		if req.ExpectedSequence != uint64(len(history)) || req.HostSequence == 0 {
			return ErrDenied
		}
		if len(history) > 0 {
			last := history[len(history)-1]
			if req.HostSequence <= last.HostSequence || req.SourceTime.Before(last.SourceTime) || lifecycleTerminal(last.State) {
				return ErrDenied
			}
		}
		result = LifecycleObservation{Sequence: uint64(len(history)) + 1, HostSequence: req.HostSequence, Binding: req.Binding, State: req.State, Source: req.Source, Assurance: "cooperative-owner-provisioned-bridge", EvidenceDigest: req.EvidenceDigest, SourceTime: req.SourceTime.UTC(), ReceivedAt: now}
		// Bridge authentication stays independent of cancelled worker/controller lineage.
		if _, e = dispatchActor(s, token, bridge, i.Reservation.ProjectID, "dispatch.observe"); e != nil {
			return e
		}
		s.LifecycleObservations[i.DispatchID] = append(history, result)
		copy := result
		s.LifecycleOperations[req.OperationKey] = LifecycleReceipt{Digest: digest, Observation: &copy}
		return nil
	})
	if err != nil {
		return LifecycleObservation{}, err
	}
	return result, nil
}
func (a *Authority) CancelLifecycle(token, controller string, req CancelLifecycleRequest) (LifecycleCancellationResult, error) {
	var result LifecycleCancellationResult
	err := a.transaction(false, true, func(s *state) error {
		i, ok := s.DispatchIntents[req.Binding.DispatchID]
		if s.Version < 9 || !ok || i.ID != req.Binding.IntentID {
			return ErrDenied
		}
		var e error
		if i.ControllerSessionID != controller {
			return ErrDenied
		}
		if _, e = dispatchActor(s, token, controller, i.Reservation.ProjectID, "dispatch.cancel"); e != nil {
			return e
		}
		if !validID(req.OperationKey) {
			return ErrDenied
		}
		digest := identity("lifecycle.cancel", controller, req)
		if old, ok := s.LifecycleOperations[req.OperationKey]; ok {
			if old.Digest != digest || old.Cancellation == nil {
				return ErrDenied
			}
			result.Cancellation = *old.Cancellation
			return nil
		}
		if _, e = lifecycleIntent(s, req.Binding); e != nil {
			return e
		}
		if _, ok := s.LifecycleCancellations[i.DispatchID]; ok {
			return ErrDenied
		}
		if _, e = lifecycleIntent(s, req.Binding); e != nil {
			return e
		}
		p, ok := s.LifecyclePolicies[i.Reservation.ProjectID]
		if !ok || !p.CancellationEnabled {
			return ErrDenied
		}
		// Policy authorizes only this immutable assigned worker/task, not session revoke.
		r := s.Sessions[controller]
		for _, res := range i.Reservation.Reservation.Resources {
			if !subset([]string{res.Kind + ":" + res.Path}, r.Scope.Resources) {
				return ErrDenied
			}
		}
		state := "requested"
		permit := true
		if p.CancellationChannel == "unsupported" {
			state = "unsupported"
			permit = false
		} else if i.HostAgentID == "" {
			state = "manual_handoff_required"
			permit = false
		}
		c := LifecycleCancellation{Sequence: 1, Binding: req.Binding, OperationKey: req.OperationKey, PolicyRevision: p.Revision, Channel: p.CancellationChannel, State: state, RequestedBy: controller, RequestedAt: time.Now().UTC()}
		if _, e = dispatchActor(s, token, controller, i.Reservation.ProjectID, "dispatch.cancel"); e != nil {
			return e
		}
		// Persist exact withdrawal under the same authority transaction BEFORE permit.
		s.LifecycleCancellations[i.DispatchID] = c
		copy := c
		s.LifecycleOperations[req.OperationKey] = LifecycleReceipt{Digest: digest, Cancellation: &copy}
		result = LifecycleCancellationResult{Cancellation: c, HostActionAuthorized: permit}
		return nil
	})
	if err != nil {
		return LifecycleCancellationResult{}, err
	}
	return result, nil
}
func (a *Authority) AcknowledgeCancellation(token, bridge string, req AcknowledgeCancellationRequest) (LifecycleCancellation, error) {
	var result LifecycleCancellation
	err := a.transaction(false, true, func(s *state) error {
		i, ok := s.DispatchIntents[req.Binding.DispatchID]
		if s.Version < 9 || !ok || i.ID != req.Binding.IntentID {
			return ErrDenied
		}
		var e error
		if bridge != i.BridgeSessionID {
			return ErrDenied
		}
		if _, e = dispatchActor(s, token, bridge, i.Reservation.ProjectID, "dispatch.observe"); e != nil {
			return e
		}
		if !validID(req.OperationKey) || !validDigest(req.EvidenceDigest) {
			return ErrDenied
		}
		digest := identity("lifecycle.cancel-ack", bridge, req)
		if old, ok := s.LifecycleOperations[req.OperationKey]; ok {
			if old.Digest != digest || old.Cancellation == nil {
				return ErrDenied
			}
			result = *old.Cancellation
			return nil
		}
		if _, e = lifecycleIntent(s, req.Binding); e != nil {
			return e
		}
		c, ok := s.LifecycleCancellations[i.DispatchID]
		if !ok || req.ExpectedSequence != c.Sequence || c.State == "acknowledged" || (req.State != "acknowledged" && req.State != "dispatch_unknown" && req.State != "unsupported") || (req.State == "acknowledged" && (c.Channel != "owner-manual-native-host" || i.HostAgentID == "")) {
			return ErrDenied
		}
		c.Sequence++
		c.State = req.State
		c.EvidenceDigest = req.EvidenceDigest
		c.AcknowledgedAt = time.Now().UTC()
		if _, e = dispatchActor(s, token, bridge, i.Reservation.ProjectID, "dispatch.observe"); e != nil {
			return e
		}
		s.LifecycleCancellations[i.DispatchID] = c
		copy := c
		s.LifecycleOperations[req.OperationKey] = LifecycleReceipt{Digest: digest, Cancellation: &copy}
		result = c
		return nil
	})
	if err != nil {
		return LifecycleCancellation{}, err
	}
	return result, nil
}
func (a *Authority) LifecycleStatus(token, session, dispatch string) (LifecycleStatus, error) {
	var result LifecycleStatus
	err := a.transaction(false, false, func(s *state) error {
		i, ok := s.DispatchIntents[dispatch]
		if s.Version < 9 || !ok {
			return ErrDenied
		}
		r, e := authenticate(s, token, session, time.Now())
		if e != nil {
			return e
		}
		if !subset([]string{"read"}, r.Scope.Actions) || !subset([]string{"task:" + i.Reservation.Reservation.TaskID}, r.Scope.Resources) || (session != i.ControllerSessionID && session != i.BridgeSessionID && session != i.WorkerSessionID) {
			return ErrDenied
		}
		p, ok := s.LifecyclePolicies[i.Reservation.ProjectID]
		if !ok {
			return ErrDenied
		}
		result = LifecycleStatus{Binding: lifecycleBinding(i), Observations: append([]LifecycleObservation{}, s.LifecycleObservations[dispatch]...), State: "unknown", Freshness: "missing", ObservedAt: time.Now().UTC(), ResourcesRetained: reservationRetained(s, s.Dispatches[dispatch])}
		if i.Lease != nil {
			if l, ok := s.Leases[i.Lease.ID]; ok {
				result.ResourcesRetained = !l.Released
			}
		}
		if len(result.Observations) > 0 {
			last := result.Observations[len(result.Observations)-1]
			result.FreshUntil = last.SourceTime.Add(p.Freshness)
			result.State = last.State
			result.Freshness = "fresh"
			if !result.ObservedAt.Before(result.FreshUntil) {
				result.Freshness = "stale"
				if !lifecycleTerminal(last.State) {
					result.State = "unknown"
				}
			}
		}
		if c, ok := s.LifecycleCancellations[dispatch]; ok {
			copy := c
			result.Cancellation = &copy
		}
		return nil
	})
	if err != nil {
		return LifecycleStatus{}, err
	}
	return result, nil
}

func (a *Authority) OwnerLifecyclePolicy(owner, project string) (LifecyclePolicy, error) {
	var p LifecyclePolicy
	e := a.transaction(false, false, func(s *state) error {
		if s.Version < 9 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		var ok bool
		p, ok = s.LifecyclePolicies[project]
		if !ok {
			return ErrDenied
		}
		return nil
	})
	if e != nil {
		return LifecyclePolicy{}, e
	}
	return p, nil
}

// A terminal host execution cannot receive fresh dispatch or worker handoff authority.
// Existing acknowledged lease history stays historical; publication may still need
// its current authority until an explicit cancellation withdraws the task.
func lifecycleDispatchEligible(s *state, dispatch string) bool {
	history := s.LifecycleObservations[dispatch]
	return len(history) == 0 || !lifecycleTerminal(history[len(history)-1].State)
}
