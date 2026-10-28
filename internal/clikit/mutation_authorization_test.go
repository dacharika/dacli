package clikit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func unsetAgentToken(t *testing.T) {
	t.Helper()
	old, present := os.LookupEnv(agentid.EnvVar)
	if err := os.Unsetenv(agentid.EnvVar); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(agentid.EnvVar, old)
		} else {
			_ = os.Unsetenv(agentid.EnvVar)
		}
	})
}

func mutationWorkspace(t *testing.T, root string) (*workspace.Workspace, string) {
	t.Helper()
	w, err := workspace.Init(root, "mutation-test")
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := agentid.Resolve(w)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := agentid.Spawn(w, rootID, "reviewer", model.GrantRO)
	if err != nil {
		t.Fatal(err)
	}
	return w, token
}

func TestAuthorizeCommandMutationUnresolvedInheritedIdentityRefuses(t *testing.T) {
	unsetAgentToken(t)
	outside := t.TempDir()
	foreignRoot := t.TempDir()
	_, foreignToken := mutationWorkspace(t, foreignRoot)
	insideRoot := t.TempDir()
	_, _ = mutationWorkspace(t, insideRoot)

	cases := []struct {
		name  string
		cwd   string
		token string
	}{
		{"empty outside workspace", outside, ""},
		{"invalid outside workspace", outside, "invalid-secret-token"},
		{"foreign identity outside workspace", outside, foreignToken},
		{"empty in another workspace", insideRoot, ""},
		{"invalid in another workspace", insideRoot, "invalid-secret-token"},
		{"foreign identity in another workspace", insideRoot, foreignToken},
	}
	cmd := &Command{Path: "marker write", Mutates: true, Run: func(*Ctx, []string) error { return nil }}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(agentid.EnvVar, tc.token)
			err := AuthorizeCommandMutation(&Ctx{Cwd: tc.cwd}, cmd, nil)
			if err == nil || ExitCode(err) != 3 {
				t.Fatalf("AuthorizeCommandMutation() = %v, want exit 3 refusal", err)
			}
			if strings.Contains(err.Error(), tc.token) && tc.token != "" {
				t.Fatalf("refusal leaked inherited token: %v", err)
			}
		})
	}
}

func TestAuthorizeCommandMutationAbsentTokenKeepsBootstrapAndRW(t *testing.T) {
	unsetAgentToken(t)
	cmd := &Command{Path: "init", Mutates: true}
	if err := AuthorizeCommandMutation(&Ctx{Cwd: t.TempDir()}, cmd, nil); err != nil {
		t.Fatalf("absent token in a new directory = %v, want handler fallthrough", err)
	}

	root := t.TempDir()
	if _, err := workspace.Init(root, "mutation-test"); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeCommandMutation(&Ctx{Cwd: root}, cmd, nil); err != nil {
		t.Fatalf("absent token owner = %v, want rw", err)
	}
}

func TestAuthorizeCommandMutationResolvesROAndRWGrants(t *testing.T) {
	unsetAgentToken(t)
	root := t.TempDir()
	_, roToken := mutationWorkspace(t, root)
	cmd := &Command{Path: "marker write", Mutates: true}

	t.Setenv(agentid.EnvVar, roToken)
	if err := AuthorizeCommandMutation(&Ctx{Cwd: root}, cmd, nil); err == nil || ExitCode(err) != 3 {
		t.Fatalf("RO mutation = %v, want exit 3 refusal", err)
	}

	t.Setenv(agentid.EnvVar, "")
	if err := AuthorizeCommandMutation(&Ctx{Cwd: root}, cmd, nil); err == nil || ExitCode(err) != 3 {
		t.Fatalf("present-empty identity = %v, want exit 3 refusal", err)
	}

	if err := os.Unsetenv(agentid.EnvVar); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeCommandMutation(&Ctx{Cwd: root}, cmd, nil); err != nil {
		t.Fatalf("RW owner mutation = %v, want success", err)
	}
}

func TestAuthorizeCommandMutationUsesParsedDeclaredPreviewShapes(t *testing.T) {
	unsetAgentToken(t)
	root := t.TempDir()
	_, roToken := mutationWorkspace(t, root)
	ctx := &Ctx{Cwd: root}
	cmd := &Command{Path: "marker write", Mutates: true, ValueFlags: []string{"config"}, EffectFreeDryRun: true}
	t.Setenv(agentid.EnvVar, roToken)

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"bare preview", []string{"--dry-run"}, 0},
		{"value flag consumes dry-run filename", []string{"--config", "--dry-run"}, 3},
		{"terminator consumes dry-run filename", []string{"--config", "--", "--dry-run"}, 3},
		{"false preview", []string{"--dry-run=false"}, 3},
		{"zero preview", []string{"--dry-run=0"}, 3},
		{"last false preview", []string{"--dry-run", "--dry-run=false"}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AuthorizeCommandMutation(ctx, cmd, tc.args)
			if got := ExitCode(err); got != tc.want {
				t.Fatalf("exit = %d (%v), want %d", got, err, tc.want)
			}
		})
	}

	cmd.EffectFreeDryRun = false
	if err := AuthorizeCommandMutation(ctx, cmd, []string{"--dry-run"}); err == nil || ExitCode(err) != 3 {
		t.Fatalf("undeclared --dry-run = %v, want exit 3 refusal", err)
	}
}

func TestAuthorizeCommandMutationAllowsOnlyDeclaredReadShape(t *testing.T) {
	unsetAgentToken(t)
	root := t.TempDir()
	_, roToken := mutationWorkspace(t, root)
	t.Setenv(agentid.EnvVar, roToken)
	cmd := &Command{
		Path: "project show", Mutates: true,
		ValueFlags:   []string{"project"},
		ReadOnlyWhen: func(flags *Flags) bool { return !flags.Bool("set-landing-policy") },
	}
	if err := AuthorizeCommandMutation(&Ctx{Cwd: root}, cmd, nil); err != nil {
		t.Fatalf("declared RO shape = %v, want success", err)
	}
	if err := AuthorizeCommandMutation(&Ctx{Cwd: root}, cmd, []string{"--set-landing-policy"}); err == nil || ExitCode(err) != 3 {
		t.Fatalf("effecting shape = %v, want exit 3 refusal", err)
	}
}

func TestAuthorizeCommandMutationNonMutatingSkipsIdentityResolution(t *testing.T) {
	unsetAgentToken(t)
	t.Setenv(agentid.EnvVar, "invalid-secret-token")
	cmd := &Command{Path: "proposal", Mutates: false}
	if err := AuthorizeCommandMutation(&Ctx{Cwd: filepath.Join(t.TempDir(), "missing")}, cmd, nil); err != nil {
		t.Fatalf("non-mutating command = %v, want unchanged read/proposal behavior", err)
	}
}
