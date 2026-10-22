package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestNativeStorageGrowthRequiresOwnerAndDoesNotMutate(t *testing.T) {
	f := controllerSetup(t)
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"", f.token, strings.Repeat("0", 64), hash(f.owner)} {
		out, err := f.a.ObserveStorage(credential)
		if !errors.Is(err, ErrDenied) || !reflect.DeepEqual(out, StorageSnapshot{}) {
			t.Fatal("non-owner received storage or growth information", err)
		}
	}
	if _, err := f.a.ObserveStorage(f.owner); err != nil {
		t.Fatal("owner could not observe storage growth", err)
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("growth authorization/observation mutated authority", err)
	}
}

func TestNativeStorageGrowthCountsRetainedHistoriesAndPreservesSnapshot(t *testing.T) {
	f := controllerSetup(t)
	// Deliberately keep unresolved producers and multi-entry histories. A growth
	// observation must report them without pruning or flattening them to one key.
	if err := f.a.transaction(false, true, func(s *state) error {
		s.Operations["private-growth-pending-operation"] = operation{Pending: true, Digest: "private-operation-digest", CheckpointVerification: &CheckpointVerificationBinding{TaskID: "task-a"}}
		s.Operations["private-growth-old-operation"] = operation{Digest: "private-old-result"}
		s.ControllerOperations["private-growth-controller-operation"] = controllerReceipt{}
		s.ControllerHistory["private-growth-controller"] = []ControllerStatus{{Sequence: 1}, {Sequence: 2}, {Sequence: 3}}
		s.Checkpoints["private-growth-checkpoint"] = []CheckpointStatus{{Sequence: 1}, {Sequence: 2}}
		s.BudgetPolicyHistory["private-growth-project"] = map[string]BudgetPolicy{"private-policy-1": {}, "private-policy-2": {}, "private-policy-3": {}}
		s.BudgetAllocations["private-growth-budget"] = BudgetAllocation{TaskID: "task-a", Phases: []BudgetPhase{{State: "unknown-retained"}}}
		s.NativeAcceptanceOperations["private-growth-acceptance"] = NativeAcceptanceRecord{State: "closing", Candidate: NativeAcceptanceCandidate{Input: NativeAcceptanceInput{TaskID: "task-a"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	var original state
	if err = json.Unmarshal(before, &original); err != nil {
		t.Fatal(err)
	}
	out, err := f.a.ObserveStorage(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	growth := out.Growth
	if MaxAuthorityBytes != 32<<20 {
		t.Fatalf("authority size limit = %d, want 32 MiB", MaxAuthorityBytes)
	}
	if growth.AggregateBytes != int64(len(before)) || growth.MaxAllowedBytes != MaxAuthorityBytes {
		t.Fatalf("growth size/limit = %d/%d, want %d/%d", growth.AggregateBytes, growth.MaxAllowedBytes, len(before), MaxAuthorityBytes)
	}
	if !bytes.Equal(out.PrivateBytes, before) || out.Digest != storageDigest(before) || out.AuthorityVersion != original.Version || out.SchemaVersion != 1 {
		t.Fatal("growth summary changed private snapshot identity or schema")
	}
	if len(growth.CollectionCounts) != len(out.Collections) {
		t.Fatal("growth summary omitted schema collections")
	}
	for name, collection := range out.Collections {
		if count, exists := growth.CollectionCounts[name]; !exists || count != collection.Count {
			t.Fatalf("collection count %s = %d, want %d", name, count, collection.Count)
		}
	}
	for name, want := range map[string]int{"Operations": len(original.Operations), "ControllerOperations": len(original.ControllerOperations), "NativeAcceptanceOperations": len(original.NativeAcceptanceOperations)} {
		if count, exists := growth.OperationCounts[name]; !exists || count != want {
			t.Fatalf("operation history count %s = %d, want %d", name, count, want)
		}
	}
	if growth.HistoryEntryCounts["ControllerHistory"] != 3 || growth.HistoryEntryCounts["Checkpoints"] != 2 || growth.HistoryEntryCounts["BudgetPolicyHistory"] != 4 {
		t.Fatal("growth counted history container keys instead of all retained entries", growth.HistoryEntryCounts)
	}
	// Existing snapshot metadata remains available for the owner backup/retention
	// contract; only the new summary excludes IDs and record digests.
	wantIDs := make([]string, 0, len(original.Operations))
	for id := range original.Operations {
		wantIDs = append(wantIDs, id)
	}
	sort.Strings(wantIDs)
	if !reflect.DeepEqual(out.Collections["Operations"].IDs, wantIDs) {
		t.Fatal("growth observation changed existing operation identity inventory")
	}
	wanted := map[string]bool{"unknown native operation outcome": false, "native budget liability retained": false, "native acceptance operation unresolved": false}
	for _, hold := range out.Holds {
		if _, exists := wanted[hold.Reason]; exists {
			wanted[hold.Reason] = true
		}
	}
	for reason, found := range wanted {
		if !found {
			t.Fatalf("growth observation dropped retention hold %s", reason)
		}
	}
	raw, err := json.Marshal(growth)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{f.owner, f.token, hash(f.owner), hash(f.token), f.s.ID, "private-growth-", "private-policy-", "private-operation-digest", "private-old-result", "OwnerHash", "CredentialHash", "OperationKey", "exit 1"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("growth summary exposed a token, hash, identity or raw record")
		}
	}
	var public struct {
		AggregateBytes     int64          `json:"aggregate_bytes"`
		MaxAllowedBytes    int64          `json:"max_allowed_bytes"`
		CollectionCounts   map[string]int `json:"collection_counts"`
		OperationCounts    map[string]int `json:"operation_counts"`
		HistoryEntryCounts map[string]int `json:"history_entry_counts"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&public); err != nil {
		t.Fatal("growth summary departed from fixed count-only schema", err)
	}
	// Every serialized count key is a state schema field, never a stored map key.
	typ := reflect.TypeOf(state{})
	for _, counts := range []map[string]int{public.CollectionCounts, public.OperationCounts, public.HistoryEntryCounts} {
		for name, count := range counts {
			field, exists := typ.FieldByName(name)
			if !exists || field.Type.Kind() != reflect.Map || count < 0 {
				t.Fatal("growth summary contains a non-schema count")
			}
		}
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("growth observation rewrote retained authority history", err)
	}
}

func TestNativeStorageGrowthPreservesVerificationReferences(t *testing.T) {
	f := evidenceSetup(t)
	f.begin(t)
	evidence, err := f.a.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, f.result())
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.a.ObserveStorage(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	want := []StorageReference{{TaskID: f.at.Request.TaskID, Digest: evidence.OutputDigest, Kind: "verification-output"}}
	if !reflect.DeepEqual(out.References, want) || !bytes.Equal(out.PrivateBytes, before) || out.Growth.CollectionCounts["Evidence"] != 1 || out.Growth.HistoryEntryCounts["Revisions"] != 1 {
		t.Fatal("growth observation changed verification reference or revision inventory")
	}
	after, err := os.ReadFile(f.a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("verification growth observation mutated authority", err)
	}
}
