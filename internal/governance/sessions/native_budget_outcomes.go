package sessions

import (
	"sort"
	"time"
)

// SettleBudget charges the original conservative estimate only after observing an
// exact persisted producer outcome. It never asserts token measurement, success
// of later phases, or permission to release native protection.
func (a *Authority) SettleBudget(token, session string, r BudgetSettleRequest) (BudgetAllocation, error) {
	var out BudgetAllocation
	e := a.transaction(false, true, func(s *state) error {
		alloc, ok := s.BudgetAllocations[r.DispatchID]
		if !ok {
			return ErrDenied
		}
		if _, e := budgetAuth(s, token, session, alloc.ProjectID, "budget.settle"); e != nil {
			return e
		}
		if !validID(r.OperationKey) || !validID(r.ProducerID) || !budgetPhaseName(r.Phase) || r.ExpectedFence != alloc.ReservationFence {
			return ErrDenied
		}
		digest := identity("budget.settle", session, r)
		if old, ok := s.BudgetOperations[r.OperationKey]; ok {
			if old.Digest != digest {
				return ErrDenied
			}
			out = old.Allocation
			return nil
		}
		index := -1
		for i, phase := range alloc.Phases {
			if phase.Phase == r.Phase {
				index = i
			}
		}
		if index < 0 {
			return ErrDenied
		}
		phase := alloc.Phases[index]
		if phase.State == "charged" {
			if phase.ProducerID != r.ProducerID {
				return ErrDenied
			}
			out = alloc
			s.BudgetOperations[r.OperationKey] = BudgetReceipt{Digest: digest, Allocation: alloc}
			return nil
		}
		outcome, e := budgetProducerOutcome(s, alloc, r)
		if e != nil {
			return e
		}
		phase.ProducerID = r.ProducerID
		phase.Outcome = outcome
		// Timeout, unsupported cancellation, and producer pending records retain the
		// liability forever until an actual authenticated terminal outcome is known.
		phase.State = "unknown-retained"
		if outcome == "completed" || outcome == "failed" {
			phase.State = "charged"
			phase.SettledAt = time.Now().UTC()
		}
		alloc.Phases[index] = phase
		if _, e := budgetAuth(s, token, session, alloc.ProjectID, "budget.settle"); e != nil {
			return e
		}
		s.BudgetAllocations[alloc.ID] = alloc
		s.BudgetOperations[r.OperationKey] = BudgetReceipt{Digest: digest, Allocation: alloc}
		out = alloc
		return nil
	})
	if e != nil {
		return BudgetAllocation{}, e
	}
	return out, nil
}
func budgetProducerOutcome(s *state, alloc BudgetAllocation, r BudgetSettleRequest) (string, error) {
	d, ok := s.Dispatches[alloc.DispatchID]
	if !ok || d.Reservation.Fence != alloc.ReservationFence || d.ProjectID != alloc.ProjectID || d.Reservation.TaskID != alloc.TaskID {
		return "", ErrDenied
	}
	intent, hasIntent := s.DispatchIntents[d.ID]
	if !hasIntent {
		return "", ErrDenied
	}
	switch r.Phase {
	case "implementation":
		if cancellation, ok := s.LifecycleCancellations[d.ID]; ok && cancellation.OperationKey == r.ProducerID && cancellation.State == "acknowledged" && cancellation.Binding.IntentID == intent.ID {
			return "acknowledged-unknown", nil
		}
		for _, observation := range s.LifecycleObservations[d.ID] {
			if identity("budget.lifecycle-producer", "", observation) != r.ProducerID {
				continue
			}
			if observation.Binding.IntentID != intent.ID || observation.Binding.DispatchID != d.ID || observation.Binding.WorkerSessionID != intent.WorkerSessionID || observation.Binding.ReservationFence != alloc.ReservationFence {
				return "", ErrDenied
			}
			switch observation.State {
			case "completed":
				return "completed", nil
			case "failed":
				return "failed", nil
			case "cancelled":
				return "unknown", nil
			default:
				return "unknown", nil
			}
		}
	case "verification":
		op, ok := s.Operations[r.ProducerID]
		if !ok || op.CheckpointVerification == nil || op.CheckpointVerification.ProjectID != alloc.ProjectID || op.CheckpointVerification.TaskID != alloc.TaskID || intent.Lease == nil || op.CheckpointVerification.Request.AttemptID != intent.Lease.AttemptID || op.CheckpointVerification.Request.LeaseID != intent.Lease.ID || op.CheckpointVerification.Request.ExpectedFence != intent.Lease.Fence {
			return "", ErrDenied
		}
		if op.Pending || op.Evidence == nil {
			return "unknown", nil
		}
		if op.Evidence.ExitCode == 0 {
			return "completed", nil
		}
		return "failed", nil
	case "review", "re-review":
		review, ok := s.Reviews[r.ProducerID]
		if !ok || intent.Lease == nil || review.Request.AttemptID != intent.Lease.AttemptID || review.Request.LeaseID != intent.Lease.ID || review.Request.ExpectedFence != intent.Lease.Fence || (r.Phase == "re-review" && review.Revision.Sequence < 2) || (r.Phase == "review" && review.Revision.Sequence != 1) {
			return "", ErrDenied
		}
		// A historical authenticated proposal proves work was produced, not current
		// eligibility or canonical acceptance. These predicates remain separate.
		if review.Request.Outcome == "approve" {
			return "completed", nil
		}
		return "failed", nil
	case "correction":
		if intent.Lease == nil {
			return "", ErrDenied
		}
		for _, revision := range s.Revisions[intent.Lease.AttemptID] {
			if revision.ID == r.ProducerID && revision.Sequence > 1 && revision.LeaseID == intent.Lease.ID && revision.Fence == intent.Lease.Fence {
				return "completed", nil
			}
		}
	case "integration":
		p := s.BudgetPolicyHistory[alloc.ProjectID][alloc.PolicyRevision]
		if ref := p.Tasks[alloc.TaskID].IntegrationOperationID; ref != "" && ref == r.ProducerID {
			return "unknown", nil
		}
		return "", ErrDenied
	case "recovery":
		// core592/594 producer outcomes are absent. Owner recovery records prove
		// protection decisions, never completed model work or measured usage.
		return "", ErrDenied
	}
	return "", ErrDenied
}

// BudgetLifecycleProducerID exposes a safe deterministic identity without raw
// authority/private credential records. CLI clients obtain IDs from budget status.
func BudgetLifecycleProducerID(o LifecycleObservation) string {
	return identity("budget.lifecycle-producer", "", o)
}

// BudgetProducer candidates are scoped public identifiers, not raw grants, commands,
// actor credentials or a claim of current producer authority.
type BudgetProducer struct{ Phase, ID, Knowledge string }

func budgetProducers(s *state, alloc BudgetAllocation) []BudgetProducer {
	out := []BudgetProducer{}
	intent, ok := s.DispatchIntents[alloc.DispatchID]
	if !ok {
		return out
	}
	for _, observation := range s.LifecycleObservations[alloc.DispatchID] {
		out = append(out, BudgetProducer{Phase: "implementation", ID: BudgetLifecycleProducerID(observation), Knowledge: "known cooperative observation; provider usage unknown"})
	}
	if cancellation, ok := s.LifecycleCancellations[alloc.DispatchID]; ok {
		out = append(out, BudgetProducer{Phase: "implementation", ID: cancellation.OperationKey, Knowledge: cancellation.State})
	}
	if intent.Lease != nil {
		for id, op := range s.Operations {
			v := op.CheckpointVerification
			if v != nil && v.ProjectID == alloc.ProjectID && v.TaskID == alloc.TaskID && v.Request.AttemptID == intent.Lease.AttemptID && v.Request.LeaseID == intent.Lease.ID && v.Request.ExpectedFence == intent.Lease.Fence {
				knowledge := "pending"
				if op.Evidence != nil {
					knowledge = "known local command outcome; provider usage unknown"
				}
				out = append(out, BudgetProducer{Phase: "verification", ID: id, Knowledge: knowledge})
			}
		}
		for id, review := range s.Reviews {
			if review.Request.AttemptID == intent.Lease.AttemptID && review.Request.LeaseID == intent.Lease.ID && review.Request.ExpectedFence == intent.Lease.Fence {
				phase := "review"
				if review.Revision.Sequence > 1 {
					phase = "re-review"
				}
				out = append(out, BudgetProducer{Phase: phase, ID: id, Knowledge: "historical proposal; current eligibility and acceptance separate"})
			}
		}
		for _, revision := range s.Revisions[intent.Lease.AttemptID] {
			if revision.Sequence > 1 && revision.LeaseID == intent.Lease.ID && revision.Fence == intent.Lease.Fence {
				out = append(out, BudgetProducer{Phase: "correction", ID: revision.ID, Knowledge: "known published revision; model usage unknown"})
			}
		}
	}
	ref := s.BudgetPolicyHistory[alloc.ProjectID][alloc.PolicyRevision].Tasks[alloc.TaskID].IntegrationOperationID
	if ref != "" {
		out = append(out, BudgetProducer{Phase: "integration", ID: ref, Knowledge: "unknown contract reference; core592 authenticated outcome unavailable"})
	}
	actionable := []BudgetProducer{}
	for _, candidate := range out {
		present := false
		for _, phase := range alloc.Phases {
			if phase.Phase == candidate.Phase && (phase.State != "charged" || phase.ProducerID == candidate.ID) {
				present = true
			}
		}
		if present {
			if _, e := budgetProducerOutcome(s, alloc, BudgetSettleRequest{DispatchID: alloc.DispatchID, Phase: candidate.Phase, ProducerID: candidate.ID, ExpectedFence: alloc.ReservationFence}); e == nil {
				actionable = append(actionable, candidate)
			}
		}
	}
	out = actionable
	sort.Slice(out, func(i, j int) bool {
		if out[i].Phase != out[j].Phase {
			return out[i].Phase < out[j].Phase
		}
		return out[i].ID < out[j].ID
	})
	return out
}
