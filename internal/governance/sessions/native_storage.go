package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
)

// StorageSnapshot is an owner-only observation, never a writer activation or
// migration permit. PrivateBytes must remain in a private disposable snapshot.
type StorageSnapshot struct {
	SchemaVersion    int                          `json:"schema_version"`
	AuthorityVersion int                          `json:"authority_version"`
	Digest           string                       `json:"digest"`
	Growth           StorageGrowthSummary         `json:"growth"`
	Collections      map[string]StorageCollection `json:"collections"`
	References       []StorageReference           `json:"references"`
	Holds            []StorageHold                `json:"holds"`
	PrivateBytes     []byte                       `json:"-"`
}

// StorageGrowthSummary exposes only bounded aggregate size and fixed-schema
// counts. Map keys are Go state collection names, never stored IDs or records.
// Histories count retained entries, including every nested revision or snapshot.
// These observations do not authorize pruning, expiry, migration or a writer.
type StorageGrowthSummary struct {
	AggregateBytes     int64          `json:"aggregate_bytes"`
	MaxAllowedBytes    int64          `json:"max_allowed_bytes"`
	CollectionCounts   map[string]int `json:"collection_counts"`
	OperationCounts    map[string]int `json:"operation_counts"`
	HistoryEntryCounts map[string]int `json:"history_entry_counts"`
}

type StorageCollection struct {
	Count  int      `json:"count"`
	IDs    []string `json:"ids"`
	Digest string   `json:"digest"`
}
type StorageReference struct {
	TaskID string `json:"task_id"`
	Digest string `json:"digest"`
	Kind   string `json:"kind"`
}
type StorageHold struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

func storageDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// ObserveStorage authenticates and observes the complete aggregate under its
// existing cooperative lock. It neither drops histories nor enables a writer.
func (a *Authority) ObserveStorage(owner string) (StorageSnapshot, error) {
	out := StorageSnapshot{SchemaVersion: 1, Collections: map[string]StorageCollection{}, References: []StorageReference{}, Holds: []StorageHold{}}
	err := a.transaction(false, false, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		// Re-reading for the exact private snapshot uses the same descriptor-safe
		// bound as transaction admission; a path stat is not a read boundary.
		raw, e := nativeconfig.ReadPrivate(a.path, MaxAuthorityBytes)
		if e != nil {
			return ErrStorage
		}
		var current state
		if json.Unmarshal(raw, &current) != nil || !reflect.DeepEqual(current, *s) {
			return ErrStorage
		}
		out.PrivateBytes = append([]byte(nil), raw...)
		out.Digest = storageDigest(raw)
		out.AuthorityVersion = s.Version
		out.Growth = StorageGrowthSummary{AggregateBytes: int64(len(raw)), MaxAllowedBytes: MaxAuthorityBytes, CollectionCounts: map[string]int{}, OperationCounts: map[string]int{}, HistoryEntryCounts: map[string]int{}}
		value := reflect.ValueOf(*s)
		typ := value.Type()
		for n := 0; n < value.NumField(); n++ {
			field := value.Field(n)
			if field.Kind() != reflect.Map {
				continue
			}
			ids := []string{}
			for _, key := range field.MapKeys() {
				ids = append(ids, key.String())
			}
			sort.Strings(ids)
			b, e := json.Marshal(field.Interface())
			if e != nil {
				return ErrStorage
			}
			name := typ.Field(n).Name
			out.Collections[name] = StorageCollection{len(ids), ids, storageDigest(b)}
			out.Growth.CollectionCounts[name] = field.Len()
			if strings.HasSuffix(name, "Operations") {
				out.Growth.OperationCounts[name] = field.Len()
			}
			kind := field.Type().Elem().Kind()
			if strings.HasSuffix(name, "History") || kind == reflect.Slice {
				entries := field.Len()
				if kind == reflect.Map || kind == reflect.Slice {
					entries = 0
					for _, key := range field.MapKeys() {
						entries += field.MapIndex(key).Len()
					}
				}
				out.Growth.HistoryEntryCounts[name] = entries
			}
		}
		for _, ev := range s.Evidence {
			at, e := reviewAttempt(s, ev.Revision.AttemptID)
			if e != nil {
				return ErrStorage
			}
			out.References = append(out.References, StorageReference{at.Request.TaskID, ev.OutputDigest, "verification-output"})
		}
		for _, lease := range s.Leases {
			if !lease.Released {
				out.Holds = append(out.Holds, StorageHold{lease.TaskID, "retained native lease"})
			}
		}
		for _, dispatch := range s.Dispatches {
			if reservationRetained(s, dispatch) {
				out.Holds = append(out.Holds, StorageHold{dispatch.Reservation.TaskID, "retained native reservation"})
			}
		}
		for _, op := range s.Operations {
			if op.Pending {
				task := ""
				if op.CheckpointVerification != nil {
					task = op.CheckpointVerification.TaskID
				}
				out.Holds = append(out.Holds, StorageHold{task, "unknown native operation outcome"})
			}
		}
		for operationKey, op := range s.NativeIntegrationOperations {
			candidate := op.Input.Candidate
			task := candidate.TaskID
			binding, bound := s.Tasks[task]
			taskBound := task != "" && bound && binding.ProjectID == candidate.ProjectID && binding.Repository == candidate.Repository && op.Policy.TaskID == task && op.Policy.ProjectID == candidate.ProjectID && op.Policy.Repository == candidate.Repository
			if !taskBound {
				task = ""
			}
			resolved := taskBound && operationKey == op.OperationKey && op.OperationKey == op.Input.OperationKey && op.InputDigest == identity("native-integration-input", "", op.Input) && op.Input.PolicyRevision == op.Policy.Revision && op.State == "integrated" && ValidNativeIntegrationOutcome(op)
			if !resolved {
				out.Holds = append(out.Holds, StorageHold{task, "native integration outcome unresolved"})
			}
		}
		for operationKey, op := range s.NativeExternalOperations {
			candidate := op.Input.Candidate
			task := candidate.TaskID
			binding, bound := s.Tasks[task]
			taskBound := task != "" && bound && binding.ProjectID == candidate.ProjectID && binding.Repository == candidate.Repository && op.Policy.TaskID == task && op.Policy.ProjectID == candidate.ProjectID && op.Policy.Repository == candidate.Repository
			if !taskBound {
				task = ""
			}
			resolved := taskBound && operationKey == op.OperationKey && op.State == "observed" && validExternalRecord(op) && externalEffectDone(op, op.Outcome)
			if !resolved {
				out.Holds = append(out.Holds, StorageHold{task, "native external outcome unresolved"})
			}
		}
		for _, allocation := range s.BudgetAllocations {
			for _, phase := range allocation.Phases {
				if phase.State != "charged" {
					out.Holds = append(out.Holds, StorageHold{allocation.TaskID, "native budget liability retained"})
					break
				}
			}
		}
		for _, consumption := range s.NativePolicyConsumptions {
			outcome, observed := s.NativePolicyOutcomes[consumption.OperationKey]
			decision, decided := s.NativePolicyDecisions[consumption.DecisionID]
			acceptance, accepted := s.NativeAcceptanceOperations[consumption.OperationKey]
			resolved := observed && decided && accepted && outcome.OperationKey == consumption.OperationKey && decision.Input.OperationKey == consumption.OperationKey && outcome.DecisionID == consumption.DecisionID && outcome.State == "closed" && consumption.CanonicalOutcomeDigest != "" && outcome.ExpectedOutcomeDigest == consumption.CanonicalOutcomeDigest && outcome.ObservedOutcomeDigest == consumption.CanonicalOutcomeDigest && acceptance.State == "closed" && acceptance.OperationKey == consumption.OperationKey && acceptance.CanonicalOutcomeDigest == consumption.CanonicalOutcomeDigest && acceptanceRecordMatchesInput(acceptance, decision.Input)
			if !resolved {
				task := ""
				if decided {
					task = decision.Input.TaskID
				}
				out.Holds = append(out.Holds, StorageHold{task, "native policy consumption outcome unresolved"})
			}
		}
		for _, acceptance := range s.NativeAcceptanceOperations {
			if acceptance.State != "closed" {
				out.Holds = append(out.Holds, StorageHold{acceptance.Candidate.Input.TaskID, "native acceptance operation unresolved"})
			}
		}
		for _, invocation := range s.ControllerInvocations {
			if !invocation.Status.Terminal {
				out.Holds = append(out.Holds, StorageHold{"", "native controller outcome unresolved"})
			}
		}
		for task, terminal := range s.TaskTerminals {
			if terminal.State != "closed" {
				out.Holds = append(out.Holds, StorageHold{task, "canonical acceptance unresolved"})
			}
		}
		sort.Slice(out.References, func(i, j int) bool {
			x, y := out.References[i], out.References[j]
			return x.TaskID+x.Digest+x.Kind < y.TaskID+y.Digest+y.Kind
		})
		sort.Slice(out.Holds, func(i, j int) bool { x, y := out.Holds[i], out.Holds[j]; return x.TaskID+x.Reason < y.TaskID+y.Reason })
		return nil
	})
	if err != nil {
		return StorageSnapshot{}, err
	}
	return out, nil
}
