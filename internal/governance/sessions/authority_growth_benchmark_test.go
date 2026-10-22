package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// authorityGrowthFixture builds disposable schema-19 authority using the real
// explicit owner upgrades, then seeds retained, receipt-bound external
// operation history. No user profile, credential, or authority is read.
func authorityGrowthFixture(tb testing.TB, count int) (*Authority, string, int) {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "authority.json")
	a, err := Open(path, Binding{Workspace: "growth-fixture", Installation: "growth-benchmark"})
	if err != nil {
		tb.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		tb.Fatal(err)
	}
	for _, upgrade := range []func(string) error{
		a.UpgradeSwarms,
		a.UpgradeDispatchIntents,
		a.UpgradeLifecycle,
		a.UpgradeRecovery,
		a.UpgradePreflight,
		a.UpgradeCheckpoints,
		a.UpgradeBudgets,
		a.UpgradeNativeAcceptance,
		a.UpgradeNativePolicy,
		a.UpgradeController,
		a.UpgradeNativeModelRouting,
		a.UpgradeNativeIntegration,
		a.UpgradeNativeExternal,
	} {
		if err := upgrade(owner); err != nil {
			tb.Fatalf("explicit disposable schema upgrade: %v", err)
		}
	}
	if err := a.transaction(false, true, func(s *state) error {
		now := time.Now().UTC()
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("fixture-%06d", i)
			task := "task-" + id
			revision := "revision-" + id
			policy := NativeExternalPolicy{
				SchemaVersion: 1, ProjectID: "fixture-project", TaskID: task,
				Revision: revision, NativePolicyRevision: "native-policy-fixture",
				Repository: "fixture/repository", Checkout: "/fixture/checkout",
				GitHubRepository: "fixture/repository", SourceBranch: "fixture-branch",
				TargetBranch: "main", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40),
				ExpectedTrunk: strings.Repeat("c", 40), Generation: 1, SliceGeneration: 1,
				RequiredChecks: []string{"fixture-check"}, ExpiresAt: now.Add(time.Hour), ConfiguredAt: now,
			}
			input := NativeExternalInput{OperationKey: "operation-" + id, PolicyRevision: revision, Action: "push", Candidate: NativeAcceptanceInput{
				ProjectID: policy.ProjectID, TaskID: policy.TaskID, Repository: policy.Repository,
				Commit: policy.Commit, Tree: policy.Tree,
			}}
			record := NativeExternalRecord{
				OperationKey: input.OperationKey, InputDigest: identity("native-external-input", "", input),
				State: "pending", Input: input, Policy: policy, CreatedAt: now, UpdatedAt: now,
			}
			s.Tasks[task] = TaskBinding{TaskID: task, ProjectID: policy.ProjectID, Repository: "/fixture/repository"}
			s.NativeExternalHistory[task+":"+revision] = policy
			s.NativeExternalPolicies[task] = policy
			s.NativeExternalOperations[input.OperationKey] = sealExternal(record)
		}
		return nil
	}); err != nil {
		tb.Fatalf("seed retained fixture operations: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	if int64(len(raw)) > MaxAuthorityBytes {
		tb.Fatalf("fixture size %d exceeds authority limit %d", len(raw), MaxAuthorityBytes)
	}
	snapshot, err := a.ObserveStorage(owner)
	if err != nil {
		tb.Fatalf("authenticated bounded fixture observation: %v", err)
	}
	if got := snapshot.Collections["NativeExternalOperations"].Count; got != count {
		tb.Fatalf("fixture retained %d operations, want %d", got, count)
	}
	if got := snapshot.Growth.OperationCounts["NativeExternalOperations"]; got != count {
		tb.Fatalf("growth summary counts %d operations, want %d", got, count)
	}
	if got := snapshot.Growth.HistoryEntryCounts["NativeExternalHistory"]; got != count {
		tb.Fatalf("growth summary counts %d history entries, want %d", got, count)
	}
	if snapshot.Growth.AggregateBytes != int64(len(raw)) {
		tb.Fatalf("growth summary reports %d aggregate bytes, file has %d", snapshot.Growth.AggregateBytes, len(raw))
	}
	return a, owner, len(raw)
}

// BenchmarkAuthorityGrowth reports serialized bytes and owner-authenticated
// read, read-only transaction, and no-op durable-write cost at increasing
// retained-history sizes. Setup and history construction are outside timing.
func BenchmarkAuthorityGrowth(b *testing.B) {
	for _, count := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("records_%d", count), func(b *testing.B) {
			a, owner, size := authorityGrowthFixture(b, count)
			reportFixture := func(b *testing.B) {
				b.ReportMetric(float64(size), "authority-bytes")
				b.ReportMetric(float64(count), "retained-operations")
				b.ReportMetric(float64(count), "history-entries")
				b.ReportMetric(float64(MaxAuthorityBytes), "max-authority-bytes")
			}
			b.Run("authenticated-bounded-read", func(b *testing.B) {
				b.SetBytes(int64(size))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := a.ObserveStorage(owner); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				reportFixture(b)
			})
			b.Run("read-only-transaction", func(b *testing.B) {
				b.SetBytes(int64(size))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := a.transaction(false, false, func(*state) error { return nil }); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				reportFixture(b)
			})
			b.Run("no-op-write-persist", func(b *testing.B) {
				b.SetBytes(int64(size))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := a.transaction(false, true, func(*state) error { return nil }); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				reportFixture(b)
			})
		})
	}
}

// TestAuthorityGrowthCooperativeLock proves that the second participating
// transaction cannot enter its callback until the first releases the shared
// authority lock. The benchmark below reports the corresponding contention.
func TestAuthorityGrowthCooperativeLock(t *testing.T) {
	a, _, _ := authorityGrowthFixture(t, 100)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- a.transaction(false, false, func(*state) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- a.transaction(false, false, func(*state) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		close(release)
		t.Fatal("cooperative transaction entered while the first held the lock")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal("first transaction:", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal("second transaction:", err)
	}
	select {
	case <-secondEntered:
	default:
		t.Fatal("second transaction never entered after lock release")
	}
}

func BenchmarkAuthorityGrowthCooperativeLock(b *testing.B) {
	a, _, _ := authorityGrowthFixture(b, 1000)
	b.ResetTimer()
	var waitNanos int64
	for i := 0; i < b.N; i++ {
		entered := make(chan struct{})
		release := make(chan struct{})
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- a.transaction(false, false, func(*state) error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered
		start := time.Now()
		secondDone := make(chan error, 1)
		go func() {
			secondDone <- a.transaction(false, false, func(*state) error { return nil })
		}()
		close(release)
		if err := <-firstDone; err != nil {
			b.Fatal(err)
		}
		if err := <-secondDone; err != nil {
			b.Fatal(err)
		}
		waitNanos += time.Since(start).Nanoseconds()
	}
	b.ReportMetric(float64(waitNanos)/float64(b.N), "lock-contention-ns/op")
}
