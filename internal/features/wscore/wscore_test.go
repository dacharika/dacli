package wscore

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func newCtx(cwd string) (*clikit.Ctx, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &clikit.Ctx{Stdout: &out, Stderr: &errb, Cwd: cwd}, &out, &errb
}

func TestCmdInitRejectsUnknownFlags(t *testing.T) {
	ctx, _, _ := newCtx(t.TempDir())
	if code := clikit.ExitCode(cmdInit(ctx, []string{"--bogus", "x"})); code != 2 {
		t.Error("a typo'd flag must be a usage error, not silently dropped")
	}
}

// A typo'd --template must refuse loudly BEFORE creating anything — the
// regression this slice previously had (dacli 143): an unknown flag was
// silently dropped and the workspace was created with no process seeded.
func TestCmdInitRefusesUnknownTemplateBeforeCreatingAnything(t *testing.T) {
	dir := t.TempDir()
	ctx, _, _ := newCtx(dir)
	err := cmdInit(ctx, []string{"--template", "does-not-exist"})
	if code := clikit.ExitCode(err); code != 2 {
		t.Fatalf("unknown template: exit %d, want 2 (err %v)", code, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, workspace.Dir)); statErr == nil {
		t.Error("an unknown --template must refuse before creating the workspace")
	}
}

func TestCmdInitRefusesUnknownRosterBeforeCreatingAnything(t *testing.T) {
	dir := t.TempDir()
	ctx, _, _ := newCtx(dir)
	err := cmdInit(ctx, []string{"--roster", "does-not-exist"})
	if code := clikit.ExitCode(err); code != 2 {
		t.Fatalf("unknown roster: exit %d, want 2 (err %v)", code, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, workspace.Dir)); statErr == nil {
		t.Error("an unknown --roster must refuse before creating the workspace")
	}
}

func TestCmdInitDefaultsNameToCwdBase(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "my-project")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, out, _ := newCtx(sub)
	if err := cmdInit(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `initialized workspace "my-project"`) {
		t.Errorf("workspace name must default to the cwd base name: %q", out)
	}
}

// --template records the workspace default so `project add` with no
// --template inherits it, instead of the flag silently doing nothing.
func TestCmdInitSeedsDefaultTemplate(t *testing.T) {
	dir := t.TempDir()
	ctx, out, _ := newCtx(dir)
	if err := cmdInit(ctx, []string{"--name", "core", "--template", "solo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "default template: solo") {
		t.Errorf("must report the seeded default template: %q", out)
	}
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(w.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "default_template: solo") {
		t.Errorf("config.yml missing default_template:\n%s", raw)
	}
}

// --roster seeds a starting set of role files — the first-run typing saver,
// not a locked-in team.
func TestCmdInitSeedsRoster(t *testing.T) {
	dir := t.TempDir()
	ctx, out, _ := newCtx(dir)
	if err := cmdInit(ctx, []string{"--name", "core", "--roster", "solo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "roster solo: seeded 1 role(s): maker") {
		t.Errorf("must report the seeded roster: %q", out)
	}
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.LoadRole(w, "maker"); !ok {
		t.Error("the roster's role was not actually created")
	}
}

// --json suppresses the human-readable getting-started banner: a machine
// caller wants the workspace facts, not a decorative reading list.
func TestCmdInitJSONSuppressesGettingStarted(t *testing.T) {
	dir := t.TempDir()
	ctx, out, _ := newCtx(dir)
	ctx.JSON = true
	if err := cmdInit(ctx, []string{"--name", "core"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Getting started") {
		t.Errorf("--json must not print the getting-started banner: %q", out)
	}

	dir2 := t.TempDir()
	ctx2, out2, _ := newCtx(dir2)
	if err := cmdInit(ctx2, []string{"--name", "core"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2.String(), "Getting started") {
		t.Errorf("a plain init must print the getting-started banner: %q", out2)
	}
	if strings.Contains(out2.String(), "--preset") || strings.Contains(out2.String(), "dacli spawn") {
		t.Errorf("native onboarding must not advertise removed launchers: %q", out2)
	}
	for _, want := range []string{"dacli native enroll --help", "dacli context <ref> --budget 4000"} {
		if !strings.Contains(out2.String(), want) {
			t.Errorf("native onboarding missing %q: %q", want, out2)
		}
	}
}

func TestCmdInitRefusesADuplicateWorkspace(t *testing.T) {
	dir := t.TempDir()
	ctx, _, _ := newCtx(dir)
	if err := cmdInit(ctx, []string{"--name", "core"}); err != nil {
		t.Fatal(err)
	}
	ctx2, _, _ := newCtx(dir)
	if err := cmdInit(ctx2, []string{"--name", "core"}); err == nil {
		t.Error("re-initializing an existing workspace must fail")
	}
}

func TestCmdWhoami(t *testing.T) {
	dir := t.TempDir()
	initCtx, _, _ := newCtx(dir)
	if err := cmdInit(initCtx, []string{"--name", "core"}); err != nil {
		t.Fatal(err)
	}
	if v, ok := os.LookupEnv(agentid.EnvVar); ok {
		t.Setenv(agentid.EnvVar, v)
		_ = os.Unsetenv(agentid.EnvVar)
	}

	ctx, out, _ := newCtx(dir)
	if err := cmdWhoami(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), agentid.RootID+" (grant: rw, role: root)") {
		t.Errorf("whoami must report the root identity, grant, and role: %q", out)
	}

	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, token, err := agentid.Spawn(w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW}, "reviewer", model.GrantRO)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(agentid.EnvVar, token)
	ctx2, out2, _ := newCtx(dir)
	if err := cmdWhoami(ctx2, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2.String(), id+" (grant: ro, role: reviewer)") {
		t.Errorf("whoami must report a child's role: %q", out2)
	}
}

func TestCmdWhoamiOutsideAWorkspace(t *testing.T) {
	ctx, _, _ := newCtx(t.TempDir())
	if code := clikit.ExitCode(cmdWhoami(ctx, nil)); code != 4 {
		t.Error("whoami outside a workspace must be a not-found")
	}
}

func TestCommandsAreRegistered(t *testing.T) {
	want := map[string]bool{"init": false, "whoami": false}
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
