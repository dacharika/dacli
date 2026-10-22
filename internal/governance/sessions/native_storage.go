package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"sort"
)

// StorageSnapshot is an owner-only observation, never a writer activation or
// migration permit. PrivateBytes must remain in a private disposable snapshot.
type StorageSnapshot struct {
	SchemaVersion    int                          `json:"schema_version"`
	AuthorityVersion int                          `json:"authority_version"`
	Digest           string                       `json:"digest"`
	Collections      map[string]StorageCollection `json:"collections"`
	References       []StorageReference           `json:"references"`
	Holds            []StorageHold                `json:"holds"`
	PrivateBytes     []byte                       `json:"-"`
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
		file, e := os.Open(a.path)
		if e != nil {
			return ErrStorage
		}
		raw, e := io.ReadAll(io.LimitReader(file, 33554433))
		_ = file.Close()
		if e != nil || len(raw) > 33554432 {
			return ErrStorage
		}
		var current state
		if json.Unmarshal(raw, &current) != nil || !reflect.DeepEqual(current, *s) {
			return ErrStorage
		}
		out.PrivateBytes = append([]byte(nil), raw...)
		out.Digest = storageDigest(raw)
		out.AuthorityVersion = s.Version
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
			out.Collections[typ.Field(n).Name] = StorageCollection{len(ids), ids, storageDigest(b)}
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
