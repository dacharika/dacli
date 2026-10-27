//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestCheckpointCanonicalContextPreservesLegacySnapshot(t *testing.T) {
	w, _ := checkpointFixture(t)
	legacy, err := ReadCheckpointCanonical(w)
	if err != nil {
		t.Fatal(err)
	}
	contextual, err := ReadCheckpointCanonicalContext(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy, contextual) {
		t.Fatal("contextual checkpoint read changed the canonical snapshot")
	}
}

func TestCheckpointCanonicalContextHonorsCancellationBeforeRead(t *testing.T) {
	w, _ := checkpointFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := ReadCheckpointCanonicalContext(ctx, w)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context returned %v", err)
	}
	if len(got.Tasks) != 0 || len(got.Projects) != 0 || len(got.Digests) != 0 {
		t.Fatalf("canceled context returned partial graph: %+v", got)
	}
}

func TestCheckpointCanonicalContextHonorsExpiredDeadlineBeforeRead(t *testing.T) {
	w, _ := checkpointFixture(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	got, err := ReadCheckpointCanonicalContext(ctx, w)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired deadline returned %v", err)
	}
	if len(got.Tasks) != 0 || len(got.Projects) != 0 || len(got.Digests) != 0 {
		t.Fatalf("expired deadline returned partial graph: %+v", got)
	}
}

func TestCheckpointCanonicalContextRejectsNilWorkspace(t *testing.T) {
	if _, err := ReadCheckpointCanonicalContext(context.Background(), (*workspace.Workspace)(nil)); err == nil {
		t.Fatal("nil workspace unexpectedly produced a canonical graph")
	}
}
