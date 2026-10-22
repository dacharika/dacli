package store

import (
	"encoding/hex"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

type NativeRetentionReference struct{ TaskID, Digest, Kind string }
type NativeRetentionHold struct{ TaskID, Reason string }
type NativeRetentionAuthority struct {
	Complete   bool
	Digest     string
	References []NativeRetentionReference
	Holds      []NativeRetentionHold
}
type NativeRetentionItem struct {
	Path      string   `json:"path"`
	TaskID    string   `json:"task_id"`
	Identity  string   `json:"identity"`
	Digest    string   `json:"digest"`
	Size      int64    `json:"size"`
	Removable bool     `json:"removable"`
	Reasons   []string `json:"reasons"`
}
type NativeRetentionPlan struct {
	Schema          string                `json:"schema"`
	AuthorityDigest string                `json:"authority_digest"`
	DryRun          bool                  `json:"dry_run"`
	Items           []NativeRetentionItem `json:"items"`
}

// PlanNativeRetention narrows the existing run-artifact classifier with fresh
// native evidence references and retained protections. It never applies cleanup.
func PlanNativeRetention(w *workspace.Workspace, project string, native NativeRetentionAuthority) (NativeRetentionPlan, error) {
	out := NativeRetentionPlan{Schema: "native-artifact-retention/v1", AuthorityDigest: native.Digest, DryRun: true, Items: []NativeRetentionItem{}}
	if !native.Complete || len(native.Digest) != 64 {
		return out, Refusedf("complete fresh native retention observation required")
	}
	graph, e := ReadCheckpointCanonical(w)
	if e != nil {
		return out, e
	}
	if graph.Projects[project] == nil {
		return out, Refusedf("retention project unavailable")
	}
	tasks := map[string]*Task{}
	for _, t := range graph.Tasks {
		tasks[t.ID] = t
	}
	_, artifacts, e := observeCleanupRuns(w, project, tasks)
	if e != nil {
		return out, Refusedf("retention run evidence incomplete")
	}
	for _, a := range artifacts {
		t := tasks[a.Task]
		if t != nil && t.Project != project {
			continue
		}
		item := NativeRetentionItem{Path: a.Path, TaskID: a.Task, Identity: a.Identity, Digest: a.Digest, Size: a.Size, Removable: a.Pruneable, Reasons: []string{a.Reason}}
		for _, hold := range native.Holds {
			if hold.TaskID == "" || hold.TaskID == a.Task {
				item.Removable = false
				item.Reasons = append(item.Reasons, hold.Reason)
			}
		}
		for _, ref := range native.References {
			digest := strings.TrimPrefix(ref.Digest, "sha256:")
			_, decodeErr := hex.DecodeString(digest)
			if digest == strings.TrimPrefix(a.Digest, "sha256:") || ((ref.TaskID == "" || ref.TaskID == a.Task) && (len(digest) != 64 || decodeErr != nil)) {
				item.Removable = false
				item.Reasons = append(item.Reasons, "retained native evidence reference: "+ref.Kind)
			}
		}
		out.Items = append(out.Items, item)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Path < out.Items[j].Path })
	return out, nil
}
