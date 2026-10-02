package knowledge

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/prompts"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const promptTestDeclaration = "<!-- dacli-prompt schema: " + prompts.Schema + " base: " + prompts.Version + " -->\n"

func newWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	if v, ok := os.LookupEnv(agentid.EnvVar); ok {
		t.Setenv(agentid.EnvVar, v)
		_ = os.Unsetenv(agentid.EnvVar)
	}
	w, err := workspace.Init(t.TempDir(), "knowledge-test")
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func newCtx(cwd string) (*clikit.Ctx, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &clikit.Ctx{Stdout: &out, Stderr: &errb, Cwd: cwd}, &out, &errb
}

func becomeChild(t *testing.T, w *workspace.Workspace, role string, grant model.Grant) string {
	t.Helper()
	id, token, err := agentid.Spawn(w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW}, role, grant)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(agentid.EnvVar, token)
	return id
}

func mustProject(t *testing.T, w *workspace.Workspace) {
	t.Helper()
	if _, err := store.CreateProject(w, agentid.RootID, "Core", "core", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestNoteAddUsageAndRejects(t *testing.T) {
	w := newWS(t)
	ctx, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdNoteAdd(ctx, []string{"decision"})); code != 2 {
		t.Error("note add with fewer than 2 positionals must be a usage error")
	}
	ctx2, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdNoteAdd(ctx2, []string{"decision", "a title", "--projct", "core"})); code != 2 {
		t.Error("a typo'd flag must be a usage error, not silently dropped")
	}
	ctx3, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdNoteAdd(ctx3, []string{"bogus", "a title", "--project", "core"})); code != 2 {
		t.Error("an unknown note kind must be a usage error")
	}
	ctx4, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdNoteAdd(ctx4, []string{"decision", "a title"})); code != 2 {
		t.Error("a missing --project must be a usage error")
	}
}

func TestNoteAddWritesADecision(t *testing.T) {
	w := newWS(t)
	mustProject(t, w)
	ctx, out, _ := newCtx(w.Root)
	err := cmdNoteAdd(ctx, []string{"decision", "chose", "X", "--project", "core", "--rejected", "Y", "--because", "faster"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "note written:") {
		t.Errorf("must report where the note landed: %q", out)
	}
}

// A read-only agent's finding is never written as a note directly — it
// becomes an event, so the owner can promote it later. Any other kind must
// still write straight through as a note.
func TestNoteAddFindingFromReadOnlyAgentBecomesAnEvent(t *testing.T) {
	w := newWS(t)
	mustProject(t, w)
	tk, err := store.CreateTask(w, agentid.RootID, "core", "do it", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	becomeChild(t, w, "auditor", model.GrantRO)

	ctx, out, _ := newCtx(w.Root)
	if err := cmdNoteAdd(ctx, []string{"finding", "found", "a", "bug", "--project", "core", "--about", tk.Slug, "--body", "detail"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "recorded as event") {
		t.Errorf("a ro finding must be recorded as an event, not a note: %q", out)
	}

	events, err := eventlog.List(w, eventlog.Query{Kinds: []model.EventKind{model.EventFinding}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 finding event, got %d", len(events))
	}
	// The ref is resolved to the ULID id at write time, not left as the
	// human-typed slug — a brief filtering on the ULID must still match it.
	if events[0].About != tk.ID {
		t.Errorf("event About = %q, want resolved task id %q", events[0].About, tk.ID)
	}
}

// An rw agent's finding writes straight through as a durable note, same as
// any other kind.
func TestNoteAddFindingFromRWAgentWritesANote(t *testing.T) {
	w := newWS(t)
	mustProject(t, w)
	ctx, out, _ := newCtx(w.Root)
	if err := cmdNoteAdd(ctx, []string{"finding", "found", "a", "bug", "--project", "core", "--severity", "minor"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "note written:") {
		t.Errorf("an rw agent's finding must write a note directly: %q", out)
	}
}

func TestRetroUsage(t *testing.T) {
	w := newWS(t)
	ctx, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdRetro(ctx, nil)); code != 2 {
		t.Error("retro with no ref must be a usage error")
	}
	mustProject(t, w)
	ctx2, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdRetro(ctx2, []string{"core"})); code != 2 {
		t.Error("retro with no well/bad/improve bullets must be a usage error")
	}
}

func TestRetroUnknownRefIsNotFound(t *testing.T) {
	w := newWS(t)
	ctx, _, _ := newCtx(w.Root)
	err := cmdRetro(ctx, []string{"nope", "--well", "x"})
	if clikit.ExitCode(err) != 4 {
		t.Fatalf("retro on an unknown ref: exit %d, want 4 (err %v)", clikit.ExitCode(err), err)
	}
}

// The bullets must appear in went-well / didn't / improve order — that order
// is the technique, not decoration.
func TestRetroOnProjectOrdersSections(t *testing.T) {
	w := newWS(t)
	mustProject(t, w)
	ctx, out, _ := newCtx(w.Root)
	if err := cmdRetro(ctx, []string{"core", "--well", "shipped fast", "--bad", "no tests", "--improve", "write tests first"}); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(strings.TrimPrefix(out.String(), "retro recorded: "))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("retro note not written: %v", err)
	}
	body := string(raw)
	wellIdx := strings.Index(body, "shipped fast")
	badIdx := strings.Index(body, "no tests")
	improveIdx := strings.Index(body, "write tests first")
	if !(wellIdx < badIdx && badIdx < improveIdx) {
		t.Errorf("retro must order went-well, didn't-go-well, improve; got:\n%s", body)
	}
}

func TestRetroOnTaskResolvesProjectAndTaskID(t *testing.T) {
	w := newWS(t)
	mustProject(t, w)
	tk, err := store.CreateTask(w, agentid.RootID, "core", "do it", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, out, _ := newCtx(w.Root)
	if err := cmdRetro(ctx, []string{tk.Slug, "--well", "ok"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "retro recorded:") {
		t.Errorf("retro must report where it landed: %q", out)
	}
}

func TestPromptListMarksOverrides(t *testing.T) {
	w := newWS(t)
	names := prompts.Names()
	if len(names) == 0 {
		t.Skip("no embedded prompts to test against")
	}
	target := names[0]

	ctx, out, _ := newCtx(w.Root)
	if err := cmdPromptList(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), target+"                 embedded") && !strings.Contains(out.String(), target) {
		t.Errorf("prompt list missing %q:\n%s", target, out)
	}
	if strings.Contains(out.String(), "OVERRIDDEN") {
		t.Errorf("nothing is overridden yet, but list reports an override:\n%s", out)
	}

	if err := os.MkdirAll(w.PromptsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.PromptsDir(), target+".md"), []byte(promptTestDeclaration+"custom"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx2, out2, _ := newCtx(w.Root)
	if err := cmdPromptList(ctx2, nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(out2.String(), "\n") {
		if strings.HasPrefix(line, target+" ") && strings.Contains(line, "OVERRIDDEN") {
			found = true
		}
	}
	if !found {
		t.Errorf("prompt list did not mark %q as overridden:\n%s", target, out2)
	}
}

func TestPromptShow(t *testing.T) {
	w := newWS(t)
	names := prompts.Names()
	if len(names) == 0 {
		t.Skip("no embedded prompts to test against")
	}
	target := names[0]

	ctx, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdPromptShow(ctx, nil)); code != 2 {
		t.Error("prompt show with no name must be a usage error")
	}

	ctx2, out2, _ := newCtx(w.Root)
	if err := cmdPromptShow(ctx2, []string{target}); err != nil {
		t.Fatal(err)
	}
	want, _, _ := prompts.Resolve(w.PromptsDir(), target)
	if out2.String() != want {
		t.Errorf("prompt show did not print the resolved content verbatim")
	}

	ctx3, _, _ := newCtx(w.Root)
	if code := clikit.ExitCode(cmdPromptShow(ctx3, []string{"does-not-exist"})); code != 4 {
		t.Error("showing an unknown prompt must be a not-found")
	}

	// An override is flagged on stderr, and its content wins.
	if err := os.MkdirAll(w.PromptsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.PromptsDir(), target+".md"), []byte(promptTestDeclaration+"custom override text"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx4, out4, errb4 := newCtx(w.Root)
	if err := cmdPromptShow(ctx4, []string{target}); err != nil {
		t.Fatal(err)
	}
	if out4.String() != "custom override text" {
		t.Errorf("override content did not win: %q", out4)
	}
	if !strings.Contains(errb4.String(), "workspace override") {
		t.Errorf("must flag the override on stderr: %q", errb4)
	}
}

func TestCommandsAreRegistered(t *testing.T) {
	want := map[string]bool{
		"note add": false, "retro": false, "prompt list": false, "prompt show": false,
	}
	for _, c := range Commands {
		if _, ok := want[c.Path]; !ok {
			t.Errorf("unexpected command path %q", c.Path)
			continue
		}
		want[c.Path] = true
		if c.Run == nil || c.Brief == "" {
			t.Errorf("command %q is missing a Run or Brief", c.Path)
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("command %q is no longer registered", path)
		}
	}
}
