package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestNativeSwarmCanonicalCompleteDependencyGraph(t *testing.T) {
	w, _ := readFixture(t)
	for _, p := range []string{"p", "other"} {
		if _, e := store.CreateProject(w, "root", p, p, "goal", ""); e != nil {
			t.Fatal(e)
		}
	}
	dep, e := store.CreateTask(w, "root", "other", "Cross project prerequisite", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	task, e := store.CreateTask(w, "root", "p", "Implement", store.TaskOpts{Priority: "must", Claims: []string{"src"}, DependsOn: []string{dep.ID}})
	if e != nil {
		t.Fatal(e)
	}
	admission, e := NewLeaseAdmission(w)
	if e != nil {
		t.Fatal(e)
	}
	before, e := admission.ObserveSwarm(w.ID, "p")
	if e != nil {
		t.Fatal(e)
	}
	if len(before.Candidates) != 1 || before.Candidates[0].TaskID != task.ID || before.Candidates[0].Reason != "dependencies_unsatisfied" {
		t.Fatalf("incomplete graph: %+v", before)
	}
	if e = store.MoveTask(w, dep, model.StatusDone); e != nil {
		t.Fatal(e)
	}
	after, e := admission.ObserveSwarm(w.ID, "p")
	if e != nil || len(after.Candidates) != 1 || after.Candidates[0].Reason != "ready" || after.Digest == before.Digest {
		t.Fatalf("cross-project completion not observed: %+v %v", after, e)
	}
	if e = store.MoveTask(w, dep, model.StatusOpen); e != nil {
		t.Fatal(e)
	}
	again, e := admission.ObserveSwarm(w.ID, "p")
	if e != nil || again.Candidates[0].Reason != "dependencies_unsatisfied" {
		t.Fatal("dependency reopening admitted work", e)
	}
}
func TestNativeSwarmManualCapabilityFreshPrivateSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capability.json")
	source := manualSwarmCapabilityFile{path: path}
	profile := ManualSwarmCapability{SchemaVersion: 1, WorkspaceID: "workspace", ProjectID: "project", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(30 * time.Minute)}
	save := func() string {
		data, e := json.Marshal(profile)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
		digest := sha256.Sum256(data)
		return "sha256:" + hex.EncodeToString(digest[:])
	}
	digest := save()
	if e := source.AdmitSwarmCapability("workspace", "project", "codex", digest); e != nil {
		t.Fatal(e)
	}
	for _, change := range []string{"wrong_workspace", "wrong_harness", "withdrawn", "stale", "future", "unattended", "public", "changed"} {
		t.Run(change, func(t *testing.T) {
			orig := profile
			digest := save()
			switch change {
			case "wrong_workspace":
				profile.WorkspaceID = "other"
				save()
			case "wrong_harness":
				profile.Harness = "other"
				save()
			case "withdrawn":
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
			case "stale":
				profile.ObservedAt = time.Now().Add(-2 * time.Hour)
				profile.ExpiresAt = time.Now().Add(time.Minute)
				digest = save()
			case "future":
				profile.ObservedAt = time.Now().Add(time.Hour)
				profile.ExpiresAt = time.Now().Add(2 * time.Hour)
				digest = save()
			case "unattended":
				profile.Source = "unattended-public-api"
				digest = save()
			case "public":
				if e := os.Chmod(path, 0644); e != nil {
					t.Fatal(e)
				}
			case "changed":
				profile.ExpiresAt = profile.ExpiresAt.Add(time.Minute)
				save()
			}
			if e := source.AdmitSwarmCapability("workspace", "project", "codex", digest); e == nil {
				t.Fatal("fresh capability accepted changed or unsupported source")
			}
			profile = orig
			if e := os.Chmod(path, 0600); e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
		})
	}
}
