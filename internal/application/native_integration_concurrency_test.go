package application

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/store"
)

// This reaches the Git target CAS directly. It proves that two genuine
// fast-forward candidates from one pinned base cannot both replace that ref;
// it does not exercise native owner admission or durable integration receipts.
func TestIntegrationRaceNativeGitTargetCASAllowsOneCandidate(t *testing.T) {
	f, owner, _, base := nativeIntegrationProducerFixture(t)
	baseTree := nativeAcceptanceGitOutput(t, f.w.Root, "rev-parse", base+"^{tree}")
	target := store.NativeIntegrationTarget{
		Repository:     owner.policy.Repository,
		Checkout:       owner.policy.TargetCheckout,
		Ref:            owner.policy.TargetRef,
		ExpectedCommit: base,
		ExpectedTree:   baseTree,
	}

	candidates := make([]store.NativeIntegrationTarget, 2)
	contents := []string{"candidate alpha\n", "candidate beta\n"}
	for i, label := range []string{"alpha", "beta"} {
		checkoutPath := filepath.Join(t.TempDir(), "candidate-"+label)
		nativeGit(t, f.w.Root, "worktree", "add", "--detach", checkoutPath, base)
		t.Cleanup(func() { nativeGit(t, f.w.Root, "worktree", "remove", "--force", checkoutPath) })
		if err := os.WriteFile(filepath.Join(checkoutPath, "src"), []byte(contents[i]), 0600); err != nil {
			t.Fatal(err)
		}
		nativeGit(t, checkoutPath, "add", "src")
		nativeGit(t, checkoutPath, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "integration race "+label)
		commit := nativeAcceptanceGitOutput(t, checkoutPath, "rev-parse", "HEAD^{commit}")
		tree := nativeAcceptanceGitOutput(t, checkoutPath, "rev-parse", "HEAD^{tree}")
		if parent := nativeAcceptanceGitOutput(t, checkoutPath, "rev-parse", "HEAD^"); parent != base {
			t.Fatalf("candidate %s does not descend from the pinned base: got %s want %s", label, parent, base)
		}
		candidates[i] = target
		candidates[i].Commit = commit
		candidates[i].Tree = tree
	}

	// Both operations observe the same clean pinned target before either may
	// enter its CAS. Candidate zero completes first, so candidate one's later
	// refusal cannot be caused by a transient Git ref lock.
	ready := [2]chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	var releaseOnce [2]sync.Once
	openRelease := func(i int) { releaseOnce[i].Do(func() { close(release[i]) }) }
	type result struct {
		candidate int
		observed  store.NativeIntegrationGitObservation
		err       error
	}
	results := [2]chan result{make(chan result, 1), make(chan result, 1)}
	var workers sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer func() {
		openRelease(0)
		openRelease(1)
		workers.Wait()
	}()
	for i, candidate := range candidates {
		workers.Add(1)
		go func(i int, candidate store.NativeIntegrationTarget) {
			defer workers.Done()
			observed, err := store.ApplyNativeIntegrationFastForward(ctx, candidate, func() error {
				ready[i] <- struct{}{}
				select {
				case <-release[i]:
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			})
			results[i] <- result{candidate: i, observed: observed, err: err}
		}(i, candidate)
	}

	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for i := range candidates {
		select {
		case <-ready[i]:
		case <-deadline.C:
			t.Fatalf("candidate %d did not observe the shared pinned target", i)
		}
	}
	openRelease(0)
	var winner result
	select {
	case winner = <-results[0]:
	case <-deadline.C:
		t.Fatal("candidate zero did not finish its target update")
	}
	if winner.err != nil {
		openRelease(1)
		t.Fatalf("candidate zero failed before winning the target CAS: %v", winner.err)
	}
	if winner.observed.Commit != candidates[0].Commit || winner.observed.Tree != candidates[0].Tree || !winner.observed.Clean {
		openRelease(1)
		t.Fatalf("candidate zero returned an unexpected successful target observation: %+v", winner.observed)
	}
	refAfterWinner, err := store.ObserveNativeIntegrationTarget(context.Background(), candidates[0])
	if err != nil || refAfterWinner.Commit != candidates[0].Commit || refAfterWinner.Tree != candidates[0].Tree || !refAfterWinner.Clean || candidates[1].ExpectedCommit != base || refAfterWinner.Commit == candidates[1].ExpectedCommit {
		openRelease(1)
		t.Fatalf("candidate one is not stale against an unlocked target ref: observed=%+v expected=%s err=%v", refAfterWinner, candidates[1].ExpectedCommit, err)
	}

	// Candidate one already finished its clean pinned-base observation, but its
	// update-ref begins only after candidate zero returned and released Git's
	// ref lock. Thus only the stale expected-old value can refuse this update.
	openRelease(1)
	var loser result
	select {
	case loser = <-results[1]:
	case <-deadline.C:
		t.Fatal("candidate one did not finish its stale target attempt")
	}
	if loser.err == nil || !strings.Contains(loser.err.Error(), "native integration ref CAS refused") {
		t.Fatalf("candidate one did not refuse its stale expected-old update from pinned base %s: %v", base, loser.err)
	}
	if loser.observed.Commit != "" || loser.observed.Tree != "" {
		t.Fatalf("losing CAS returned a success observation: %+v; error=%v", loser.observed, loser.err)
	}

	final, err := store.ObserveNativeIntegrationTarget(context.Background(), candidates[0])
	if err != nil {
		t.Fatalf("observe winning target: %v", err)
	}
	if final.Commit != candidates[0].Commit || final.Tree != candidates[0].Tree || !final.Clean {
		t.Fatalf("target does not contain exactly candidate zero: got %+v winner=%+v", final, candidates[0])
	}
	gotBytes, err := os.ReadFile(filepath.Join(owner.policy.TargetCheckout, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBytes) != contents[0] {
		t.Fatalf("target file does not contain candidate zero bytes: got %q winner=%q", gotBytes, contents[0])
	}
}
