package sessions

import (
	"reflect"
	"sort"
	"time"
)

// NativeRoleOutcomeCounts contains cooperative lifecycle observations, not
// provider measurements or a claim about productivity.
type NativeRoleOutcomeCounts struct {
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Unknown   int `json:"unknown"`
}

// NativeRoleOutcome is a closed aggregate for one exact signed role and policy
// set. It deliberately contains no task, dispatch, actor, or evidence IDs.
type NativeRoleOutcome struct {
	RoleID              string                   `json:"role_id"`
	Version             string                   `json:"version"`
	ContentDigest       string                   `json:"content_digest"`
	PolicySetDigest     string                   `json:"policy_set_digest"`
	AdoptionTaskSamples int                      `json:"adoption_task_samples"`
	DispatchSamples     int                      `json:"dispatch_samples"`
	OutcomeCounts       *NativeRoleOutcomeCounts `json:"outcome_dispatch_counts"`
	Suppression         string                   `json:"suppression,omitempty"`
}

const nativeRoleOutcomeMinimumTasks = 5

type nativeRoleOutcomeKey struct {
	roleID, version, contentDigest, policySetDigest string
}

type nativeRoleOutcomeTask struct {
	dispatches map[string]string
}

type nativeRoleOutcomeGroup struct {
	key   nativeRoleOutcomeKey
	tasks map[string]*nativeRoleOutcomeTask
}

// OwnerNativeRoleOutcomes reads owner-authenticated durable native swarm and
// lifecycle authority for one project. It performs no migration or writes.
func (a *Authority) OwnerNativeRoleOutcomes(owner, project string) ([]NativeRoleOutcome, error) {
	if !validID(project) {
		return nil, ErrDenied
	}
	var out []NativeRoleOutcome
	err := a.transaction(false, false, func(s *state) error {
		if s.Version < 9 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		groups := map[nativeRoleOutcomeKey]*nativeRoleOutcomeGroup{}
		dispatchIDs := make([]string, 0, len(s.Dispatches))
		for id := range s.Dispatches {
			dispatchIDs = append(dispatchIDs, id)
		}
		sort.Strings(dispatchIDs)
		for _, id := range dispatchIDs {
			d := s.Dispatches[id]
			if d.ProjectID != project {
				continue
			}
			i, ok := s.DispatchIntents[id]
			if !ok || i.State != "acknowledged" || i.Lease == nil || !nativeRoleOutcomeDispatchBound(id, d, i) || !nativeRoleOutcomeDurableLeaseBound(s, id, d, i) {
				continue
			}
			var provenance *RoleRegistryProvenance
			for index := range d.RoleRegistry {
				candidate := &d.RoleRegistry[index]
				if candidate.RoleID != d.ImplementationRole {
					continue
				}
				if provenance != nil {
					provenance = nil
					break
				}
				provenance = candidate
			}
			if provenance == nil || !validID(provenance.RoleID) || !validID(provenance.Version) || !validDigest(provenance.ContentDigest) {
				continue
			}
			policyDigest, ok := nativeRolePolicySetDigest(provenance.PolicyReferences)
			if !ok {
				continue
			}
			key := nativeRoleOutcomeKey{provenance.RoleID, provenance.Version, provenance.ContentDigest, policyDigest}
			group := groups[key]
			if group == nil {
				group = &nativeRoleOutcomeGroup{key: key, tasks: map[string]*nativeRoleOutcomeTask{}}
				groups[key] = group
			}
			taskID := d.Reservation.TaskID
			task := group.tasks[taskID]
			if task == nil {
				task = &nativeRoleOutcomeTask{dispatches: map[string]string{}}
				group.tasks[taskID] = task
			}
			// Map-key and intent binding checks above make a repeated durable
			// receipt for this dispatch unable to add a second sample.
			task.dispatches[id] = nativeRoleOutcomeState(s, d, i)
		}
		keys := make([]nativeRoleOutcomeKey, 0, len(groups))
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.roleID != b.roleID {
				return a.roleID < b.roleID
			}
			if a.version != b.version {
				return a.version < b.version
			}
			if a.contentDigest != b.contentDigest {
				return a.contentDigest < b.contentDigest
			}
			return a.policySetDigest < b.policySetDigest
		})
		out = make([]NativeRoleOutcome, 0, len(keys))
		for _, key := range keys {
			group := groups[key]
			if len(group.tasks) < nativeRoleOutcomeMinimumTasks {
				continue
			}
			counts := NativeRoleOutcomeCounts{}
			tasksByOutcome := map[string]map[string]bool{"completed": {}, "failed": {}, "unknown": {}}
			dispatchSamples := 0
			taskIDs := make([]string, 0, len(group.tasks))
			for taskID := range group.tasks {
				taskIDs = append(taskIDs, taskID)
			}
			sort.Strings(taskIDs)
			for _, taskID := range taskIDs {
				dispatchIDs := make([]string, 0, len(group.tasks[taskID].dispatches))
				for dispatchID := range group.tasks[taskID].dispatches {
					dispatchIDs = append(dispatchIDs, dispatchID)
				}
				sort.Strings(dispatchIDs)
				for _, dispatchID := range dispatchIDs {
					dispatchSamples++
					state := group.tasks[taskID].dispatches[dispatchID]
					switch state {
					case "completed":
						counts.Completed++
					case "failed":
						counts.Failed++
					default:
						state = "unknown"
						counts.Unknown++
					}
					tasksByOutcome[state][taskID] = true
				}
			}
			row := NativeRoleOutcome{RoleID: key.roleID, Version: key.version, ContentDigest: key.contentDigest, PolicySetDigest: key.policySetDigest, AdoptionTaskSamples: len(group.tasks), DispatchSamples: dispatchSamples}
			if nativeRoleOutcomeSmallCell(counts, tasksByOutcome) {
				row.Suppression = "outcome vector suppressed: positive cell below fixed five-task minimum"
			} else {
				row.OutcomeCounts = &counts
			}
			out = append(out, row)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func nativeRoleOutcomeDispatchBound(id string, d DispatchReservation, i DispatchIntent) bool {
	reservation := i.Reservation
	if id == "" || d.ID != id || i.DispatchID != id || d.ProjectID == "" || !d.Converted || d.WorkerSessionID == "" || d.AttemptID == "" ||
		reservation.Converted || reservation.WorkerSessionID != "" || reservation.AttemptID != "" || i.WorkerSessionID != d.WorkerSessionID || i.Lease == nil {
		return false
	}
	// Dispatch handoff is the only permitted difference between the immutable
	// intent snapshot and the current reservation record.
	requestSnapshot := d
	requestSnapshot.Converted = false
	requestSnapshot.WorkerSessionID = ""
	requestSnapshot.AttemptID = ""
	if !reflect.DeepEqual(reservation, requestSnapshot) {
		return false
	}
	reserved := d.Reservation
	lease := i.Lease
	return lease.ID == id && lease.AttemptID == d.AttemptID && lease.SessionID == d.WorkerSessionID &&
		lease.TaskID == reserved.TaskID && lease.Repository == reserved.Repository && lease.Checkout == reserved.Checkout &&
		reflect.DeepEqual(lease.Resources, reserved.Resources) && lease.Fence != 0
}

func nativeRoleOutcomeDurableLeaseBound(s *state, id string, d DispatchReservation, i DispatchIntent) bool {
	lease, ok := s.Leases[id]
	if !ok || i.Lease == nil {
		return false
	}
	attempt, ok := nativeRoleOutcomeAttempt(s, i.Lease.AttemptID)
	if !ok || attempt.ID != i.Lease.AttemptID || attempt.SessionID != i.WorkerSessionID || attempt.Request.TaskID != d.Reservation.TaskID || attempt.ProjectID != d.ProjectID ||
		attempt.Observation.Repository != d.Reservation.Repository || attempt.Observation.Locator != d.Reservation.Checkout {
		return false
	}
	return lease.ID == i.Lease.ID && lease.AttemptID == i.Lease.AttemptID && lease.SessionID == i.Lease.SessionID &&
		lease.TaskID == i.Lease.TaskID && lease.Repository == i.Lease.Repository && lease.Checkout == i.Lease.Checkout &&
		lease.Fence == i.Lease.Fence && reflect.DeepEqual(lease.Resources, i.Lease.Resources) &&
		lease.TaskID == d.Reservation.TaskID && lease.Repository == d.Reservation.Repository &&
		lease.Checkout == d.Reservation.Checkout && reflect.DeepEqual(lease.Resources, d.Reservation.Resources)
}

func nativeRoleOutcomeAttempt(s *state, id string) (Attempt, bool) {
	for _, attempt := range s.Attempts {
		if attempt.ID == id {
			return attempt, true
		}
	}
	return Attempt{}, false
}

func nativeRolePolicySetDigest(refs []RolePolicyReference) (string, bool) {
	canonical := append([]RolePolicyReference(nil), refs...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Kind != canonical[j].Kind {
			return canonical[i].Kind < canonical[j].Kind
		}
		if canonical[i].Version != canonical[j].Version {
			return canonical[i].Version < canonical[j].Version
		}
		return canonical[i].Digest < canonical[j].Digest
	})
	seen := map[string]bool{}
	for _, ref := range canonical {
		if !validID(ref.Kind) || ref.Version == "" || !validDigest(ref.Digest) || seen[ref.Kind] {
			return "", false
		}
		seen[ref.Kind] = true
	}
	return "sha256:" + identity("dacli-role-outcomes/policy-set/v1", "", canonical), true
}

func nativeRoleOutcomeState(s *state, d DispatchReservation, i DispatchIntent) string {
	if _, cancelled := s.LifecycleCancellations[d.ID]; cancelled {
		return "unknown"
	}
	history := s.LifecycleObservations[d.ID]
	if len(history) == 0 {
		return "unknown"
	}
	want := lifecycleBinding(i)
	now := time.Now().UTC()
	for index, observation := range history {
		if observation.Sequence != uint64(index+1) || observation.Binding != want || observation.Assurance != "cooperative-owner-provisioned-bridge" || !validDigest(observation.EvidenceDigest) || observation.SourceTime.IsZero() || observation.SourceTime.After(now.Add(time.Minute)) || observation.ReceivedAt.IsZero() || observation.ReceivedAt.After(now.Add(time.Minute)) || observation.HostSequence == 0 ||
			(observation.State != "running" && observation.State != "unknown" && observation.State != "completed" && observation.State != "failed" && observation.State != "cancelled") ||
			(index > 0 && (observation.HostSequence <= history[index-1].HostSequence || observation.SourceTime.Before(history[index-1].SourceTime) || observation.ReceivedAt.Before(history[index-1].ReceivedAt))) {
			return "unknown"
		}
		if index > 0 && lifecycleTerminal(history[index-1].State) {
			return "unknown"
		}
	}
	last := history[len(history)-1]
	if last.State != "completed" && last.State != "failed" {
		return "unknown"
	}
	// LifecycleStatus preserves historical terminal observations after freshness
	// expires. Stale nonterminal observations remain unknown above.
	return last.State
}

func nativeRoleOutcomeSmallCell(counts NativeRoleOutcomeCounts, tasks map[string]map[string]bool) bool {
	return (counts.Completed > 0 && len(tasks["completed"]) < nativeRoleOutcomeMinimumTasks) ||
		(counts.Failed > 0 && len(tasks["failed"]) < nativeRoleOutcomeMinimumTasks) ||
		(counts.Unknown > 0 && len(tasks["unknown"]) < nativeRoleOutcomeMinimumTasks)
}
