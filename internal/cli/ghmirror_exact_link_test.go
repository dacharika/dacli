package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func exactLinkFakeGH(t *testing.T, root string) (state string) {
	t.Helper()
	state = filepath.Join(root, "gh-exact-state")
	bin := filepath.Join(root, "gh-exact-bin")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
S="$GH_EXACT_STATE"
printf '%s\n' "$*" >> "$S/calls"
case "$1 $2" in
  "repo view") echo '{"nameWithOwner":"me/demo","visibility":"PRIVATE","defaultBranchRef":{"name":"main"}}';;
  "issue view") n="$3"; cat "$S/issue_$n.json";;
  *) echo "unexpected gh call: $*" >&2; exit 19;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_EXACT_STATE", state)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	titles := []string{"Map quota settings", "Document retry behavior", "Inspect stale token flow", "Trace database export"}
	for n := 601; n <= 604; n++ {
		payload := fmt.Sprintf(`{"number":%d,"title":%q,"body":"body for issue %d","state":"OPEN"}`, n, titles[n-601], n)
		if err := os.WriteFile(filepath.Join(state, fmt.Sprintf("issue_%d.json", n)), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestExactIssueLinkCLISelectionDoesNotBroadenOrWriteRemote(t *testing.T) {
	dir := t.TempDir()
	ghState := exactLinkFakeGH(t, dir)
	run(t, dir, 0, "init", "--name", "exact-links")
	run(t, dir, 0, "project", "add", "Core", "--slug", "core", "--goal", "bounded exact links")
	titles := []string{"Map quota settings", "Document retry behavior", "Inspect stale token flow", "Trace database export"}
	for i, title := range titles {
		run(t, dir, 0, "task", "add", title, "--project", "core", "--accept", fmt.Sprintf("criterion %d remains local", i+1))
	}
	run(t, dir, 0, "github", "link", "core")
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil || len(tasks) != 4 {
		t.Fatalf("tasks=%d err=%v", len(tasks), err)
	}
	before := make([]string, len(tasks))
	for i, task := range tasks {
		before[i] = fileDigest(t, task.Path)
	}
	args := []string{"github", "issue", "link", "core"}
	for i := 0; i < 3; i++ {
		args = append(args, fmt.Sprintf("%d=%s", 601+i, tasks[i].ID))
	}
	args = append(args, "--dry-run")
	out := run(t, dir, 0, args...)
	if !strings.Contains(out, "exact issue link plan") {
		t.Fatalf("preview did not produce exact plan:\n%s", out)
	}
	fields := strings.Fields(out)
	if len(fields) < 5 || fields[0] != "exact" || fields[1] != "issue" || fields[2] != "link" || fields[3] != "plan" || len(fields[4]) != 64 {
		t.Fatalf("cannot parse immutable plan ID: %s", out)
	}
	planID := fields[4]
	for i, task := range tasks {
		after := fileDigest(t, task.Path)
		if before[i] != after {
			t.Fatalf("preview changed task %s", task.ID)
		}
	}
	// The real command path applies the persisted selection using a private
	// owner profile. This fixture authority has no task holds; production
	// applies re-check task claims protection at each selected write.
	authorityPath := filepath.Join(dir, "native-authority.json")
	authority, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "exact-issue-link-test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := authority.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	ownerCredential := filepath.Join(dir, "owner.token")
	if err := os.WriteFile(ownerCredential, []byte(ownerToken), 0o600); err != nil {
		t.Fatal(err)
	}
	ownerConfig := filepath.Join(dir, "owner.json")
	configRaw, _ := json.Marshal(map[string]any{"schema_version": 1, "workspace": w.Root, "authority": authorityPath, "installation_id": "exact-issue-link-test", "owner_credential_file": ownerCredential})
	if err := os.WriteFile(ownerConfig, configRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, dir, 0, "github", "issue", "link", "--apply", planID, "--owner-config", ownerConfig)
	for i, task := range tasks {
		after, err := store.FindTask(w, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			block, ok := after.Doc.Front.GetBlock("github")
			if !ok || !strings.Contains(block, fmt.Sprintf("issue: %d", 601+i)) || !strings.Contains(block, "repo: me/demo") {
				t.Fatalf("selected task %s not linked: %q", task.ID, block)
			}
			if boxes := after.Acceptance(); len(boxes) != 1 || boxes[0].Done {
				t.Fatalf("selected task acceptance changed: %+v", boxes)
			}
		} else if fileDigest(t, after.Path) != before[i] {
			t.Fatalf("unselected task bytes changed: %s", task.ID)
		}
	}
	linkedBytes := make([]string, 3)
	for i := 0; i < 3; i++ {
		linkedBytes[i] = fileDigest(t, tasks[i].Path)
	}
	run(t, dir, 0, "github", "issue", "link", "--apply", planID, "--owner-config", ownerConfig)
	for i := 0; i < 3; i++ {
		if fileDigest(t, tasks[i].Path) != linkedBytes[i] {
			t.Fatalf("replay changed linked task %s", tasks[i].ID)
		}
	}
	callLog, _ := os.ReadFile(filepath.Join(ghState, "calls"))
	calls := string(callLog)
	for _, want := range []string{"issue view 601 --json number,title,body,state --repo me/demo", "issue view 602 --json number,title,body,state --repo me/demo", "issue view 603 --json number,title,body,state --repo me/demo"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("exact selection omitted remote read %q: %s", want, calls)
		}
	}
	if strings.Contains(calls, "issue view 604") || strings.Contains(calls, "issue list") || strings.Contains(calls, "issue create") || strings.Contains(calls, "issue edit") || strings.Contains(calls, "issue comment") {
		t.Fatalf("selection broadened or performed a GitHub write: %s", calls)
	}
	// The fourth issue and task are both valid. Removing the explicit selection
	// boundary would make this byte-digest assertion fail for that task.
	if got := fileDigest(t, tasks[3].Path); got != before[3] {
		t.Fatalf("valid unselected task changed: %s", tasks[3].ID)
	}
}
