package cli

import (
	"bytes"
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
  "repo view") visibility="${GH_EXACT_VISIBILITY:-PRIVATE}"; echo "{\"nameWithOwner\":\"me/demo\",\"visibility\":\"$visibility\",\"defaultBranchRef\":{\"name\":\"main\"}}";;
  "issue view") n="$3"; if [ "${GH_EXACT_MUTATE_ISSUE:-}" = "$n" ]; then cp "$GH_EXACT_MUTATE_PROJECT" "$GH_EXACT_PROJECT_PATH"; fi; cat "$S/issue_$n.json";;
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

func TestExactIssueLinkCLIRefusesRepositoryRelinkDuringIssueRefresh(t *testing.T) {
	dir, state, w, tasks, planID, ownerConfig := prepareExactIssueLinkCLI(t, "PRIVATE", false)
	projectPath := w.ProjectPath("core")
	mutated := prepareMutatedProjectFile(t, w, func(project *store.Project) {
		project.Doc.Front.Set("github_repo", "me/other")
	})
	t.Setenv("GH_EXACT_MUTATE_ISSUE", "603")
	t.Setenv("GH_EXACT_MUTATE_PROJECT", mutated)
	t.Setenv("GH_EXACT_PROJECT_PATH", projectPath)
	before := make([]string, len(tasks))
	for i, task := range tasks {
		before[i] = fileDigest(t, task.Path)
	}
	out := run(t, dir, 3, "github", "issue", "link", "--apply", planID, "--owner-config", ownerConfig)
	if !strings.Contains(out, "changed") && !strings.Contains(out, "stale") {
		t.Fatalf("apply did not report late repository binding drift: %s", out)
	}
	for i, task := range tasks {
		if got := fileDigest(t, task.Path); got != before[i] {
			t.Fatalf("selected task %s changed after late repository relink", task.ID)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(state, "calls"))
	if strings.Contains(string(calls), "issue edit") || strings.Contains(string(calls), "issue create") {
		t.Fatalf("repository drift path issued remote mutation: %s", calls)
	}
}

func TestExactIssueLinkCLIRefusesPublicConsentRemovalDuringIssueRefresh(t *testing.T) {
	dir, state, w, tasks, planID, ownerConfig := prepareExactIssueLinkCLI(t, "PUBLIC", true)
	mutated := prepareMutatedProjectFile(t, w, func(project *store.Project) {
		project.Doc.Front.Delete("github_public_confirmed")
	})
	t.Setenv("GH_EXACT_MUTATE_ISSUE", "603")
	t.Setenv("GH_EXACT_MUTATE_PROJECT", mutated)
	t.Setenv("GH_EXACT_PROJECT_PATH", w.ProjectPath("core"))
	before := make([]string, len(tasks))
	for i, task := range tasks {
		before[i] = fileDigest(t, task.Path)
	}
	out := run(t, dir, 3, "github", "issue", "link", "--apply", planID, "--owner-config", ownerConfig)
	if !strings.Contains(out, "changed") && !strings.Contains(out, "consent") && !strings.Contains(out, "stale") {
		t.Fatalf("apply did not report public consent withdrawal: %s", out)
	}
	for i, task := range tasks {
		if got := fileDigest(t, task.Path); got != before[i] {
			t.Fatalf("selected task %s changed after public consent withdrawal", task.ID)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(state, "calls"))
	if strings.Contains(string(calls), "issue edit") || strings.Contains(string(calls), "issue create") {
		t.Fatalf("consent withdrawal path issued remote mutation: %s", calls)
	}
}

func TestExactIssueLinkOwnerApplyIgnoresLegacyAmbientIdentity(t *testing.T) {
	for _, ambient := range []string{"absent", "read-only", "invalid", "empty"} {
		t.Run(ambient, func(t *testing.T) {
			dir, _, w, _, planID, ownerConfig := prepareExactIssueLinkCLI(t, "PRIVATE", false)
			switch ambient {
			case "absent":
				prior, hadPrior := os.LookupEnv("DACLI_AGENT")
				if err := os.Unsetenv("DACLI_AGENT"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if hadPrior {
						_ = os.Setenv("DACLI_AGENT", prior)
					} else {
						_ = os.Unsetenv("DACLI_AGENT")
					}
				})
			case "read-only":
				setExactLinkLegacyAgent(t, w, "fixture-exact-link-ro", "ro")
				t.Setenv("DACLI_AGENT", "fixture-exact-link-ro")
			case "invalid":
				t.Setenv("DACLI_AGENT", "unrecognized-exact-link-token")
			case "empty":
				t.Setenv("DACLI_AGENT", "")
			}
			var out bytes.Buffer
			ctx := &Ctx{Cwd: dir, Stdout: &out, Stderr: &out}
			cmd, args := dispatch([]string{"github", "issue", "link", "--apply", planID, "--owner-config", ownerConfig})
			if cmd == nil || cmd.Path != "github issue link" {
				t.Fatalf("dispatch command = %#v", cmd)
			}
			if err := invoke(ctx, cmd, args); err != nil {
				t.Fatalf("valid explicit owner apply with %s DACLI_AGENT failed: %v", ambient, err)
			}
		})
	}
}

func TestExactIssueLinkInvalidOwnerCannotBorrowLegacyRWIdentity(t *testing.T) {
	for _, ownerConfigKind := range []string{"missing", "invalid"} {
		t.Run(ownerConfigKind, func(t *testing.T) {
			for _, ambient := range []string{"rw", "root"} {
				t.Run(ambient, func(t *testing.T) {
					dir, _, w, tasks, planID, ownerConfig := prepareExactIssueLinkCLI(t, "PRIVATE", false)
					if ambient == "rw" {
						setExactLinkLegacyAgent(t, w, "fixture-exact-link-rw", "rw")
						t.Setenv("DACLI_AGENT", "fixture-exact-link-rw")
					} else {
						prior, hadPrior := os.LookupEnv("DACLI_AGENT")
						if err := os.Unsetenv("DACLI_AGENT"); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if hadPrior {
								_ = os.Setenv("DACLI_AGENT", prior)
							} else {
								_ = os.Unsetenv("DACLI_AGENT")
							}
						})
					}
					selectedBefore := fileDigest(t, tasks[0].Path)
					if ownerConfigKind == "invalid" {
						if err := os.WriteFile(ownerConfig, []byte("not a valid owner profile"), 0o600); err != nil {
							t.Fatal(err)
						}
					} else {
						ownerConfig = filepath.Join(dir, "missing-owner.json")
					}
					var out bytes.Buffer
					ctx := &Ctx{Cwd: dir, Stdout: &out, Stderr: &out}
					cmd, args := dispatch([]string{"github", "issue", "link", "--apply", planID, "--owner-config", ownerConfig})
					err := invoke(ctx, cmd, args)
					if got := exitCode(err); got != 3 {
						t.Fatalf("invalid explicit owner status=%d err=%v; ambient %s identity must not authorize it", got, err, ambient)
					}
					if got := fileDigest(t, tasks[0].Path); got != selectedBefore {
						t.Fatalf("task changed despite invalid explicit owner: %s", tasks[0].ID)
					}
					if _, err := os.Stat(filepath.Join(w.Root, ".dacli", "recovery", "issue-links", planID+".json")); !os.IsNotExist(err) {
						t.Fatalf("invalid owner created recovery journal: stat err=%v", err)
					}
				})
			}
		})
	}
}

func TestExactIssueLinkOwnerApplyIsLocalOnlyToGenericMCP(t *testing.T) {
	dir, _, w, tasks, planID, ownerConfig := prepareExactIssueLinkCLI(t, "PRIVATE", false)
	project, err := store.LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(w.Root, ".dacli", "plans", "issue-links", planID+".json")
	authorityPath := filepath.Join(dir, "native-authority.json")
	before := []string{fileDigest(t, planPath), fileDigest(t, authorityPath), fileDigest(t, project.Path)}
	for _, task := range tasks {
		before = append(before, fileDigest(t, task.Path))
	}
	callLog := filepath.Join(filepath.Dir(authorityPath), "gh-exact-state", "calls")
	callBefore := fileDigest(t, callLog)
	journalPath := filepath.Join(w.Root, ".dacli", "recovery", "issue-links", planID+".json")
	_, message, code := executor(dir)([]string{"github", "issue", "link", "--apply", planID, "--owner-config", ownerConfig}, false)
	if code != 3 || !strings.Contains(message, "local-only") {
		t.Fatalf("generic MCP apply status=%d message=%q, want local-only policy refusal", code, message)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("generic MCP refusal created recovery journal: stat err=%v", err)
	}
	after := []string{fileDigest(t, planPath), fileDigest(t, authorityPath), fileDigest(t, project.Path)}
	for _, task := range tasks {
		after = append(after, fileDigest(t, task.Path))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("generic MCP refusal changed protected state at index %d", i)
		}
	}
	if got := fileDigest(t, callLog); got != callBefore {
		t.Fatal("generic MCP refusal reached the GitHub transport")
	}
	if out, msg, code := executor(dir)([]string{"github", "issue", "link", "--help"}, false); code != 0 || !strings.Contains(out+msg, "owner-config") {
		t.Fatalf("generic MCP help status=%d out=%q msg=%q", code, out, msg)
	}
}

func setExactLinkLegacyAgent(t *testing.T, w *workspace.Workspace, token, grant string) {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	raw := fmt.Sprintf("---\nid: %s\nkind: agent\ngrant: %s\ntoken_hash: sha256:%s\n---\n# Fixture\n", token, grant, hex.EncodeToString(sum[:]))
	path := filepath.Join(w.Root, ".dacli", "agents", token+".md")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func prepareExactIssueLinkCLI(t *testing.T, visibility string, allowPublic bool) (string, string, *workspace.Workspace, []*store.Task, string, string) {
	t.Helper()
	dir := t.TempDir()
	state := exactLinkFakeGH(t, dir)
	t.Setenv("GH_EXACT_VISIBILITY", visibility)
	run(t, dir, 0, "init", "--name", "late-binding")
	run(t, dir, 0, "project", "add", "Core", "--slug", "core", "--goal", "selected issue link")
	titles := []string{"Map quota settings", "Document retry behavior", "Inspect stale token flow"}
	for i, title := range titles {
		run(t, dir, 0, "task", "add", title, "--project", "core", "--accept", fmt.Sprintf("criterion %d remains local", i+1))
	}
	linkArgs := []string{"github", "link", "core"}
	if allowPublic {
		linkArgs = append(linkArgs, "--allow-public")
	}
	run(t, dir, 0, linkArgs...)
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(w, "core", "")
	if err != nil || len(tasks) != 3 {
		t.Fatalf("tasks=%d err=%v", len(tasks), err)
	}
	args := []string{"github", "issue", "link", "core"}
	for i, task := range tasks {
		args = append(args, fmt.Sprintf("%d=%s", 601+i, task.ID))
	}
	args = append(args, "--dry-run")
	out := run(t, dir, 0, args...)
	fields := strings.Fields(out)
	if len(fields) < 5 || len(fields[4]) != 64 {
		t.Fatalf("cannot parse immutable plan ID: %s", out)
	}
	authorityPath := filepath.Join(dir, "native-authority.json")
	authority, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "exact-issue-link-late-test"}, nil)
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
	configRaw, _ := json.Marshal(map[string]any{"schema_version": 1, "workspace": w.Root, "authority": authorityPath, "installation_id": "exact-issue-link-late-test", "owner_credential_file": ownerCredential})
	if err := os.WriteFile(ownerConfig, configRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, state, w, tasks, fields[4], ownerConfig
}

func prepareMutatedProjectFile(t *testing.T, w *workspace.Workspace, mutate func(*store.Project)) string {
	t.Helper()
	project, err := store.LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(project.Path)
	if err != nil {
		t.Fatal(err)
	}
	mutate(project)
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	changed, err := os.ReadFile(project.Path)
	if err != nil {
		t.Fatal(err)
	}
	copy := filepath.Join(t.TempDir(), "mutated-project.md")
	if err := os.WriteFile(copy, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project.Path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	return copy
}
