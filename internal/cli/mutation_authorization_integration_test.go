package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func unsetInheritedAgent(t *testing.T) {
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

func initAuthorizationWorkspace(t *testing.T, root string) (*workspace.Workspace, string) {
	t.Helper()
	w, err := workspace.Init(root, "authorization-test")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := agentid.Resolve(w)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := agentid.Spawn(w, owner, "reviewer", model.GrantRO)
	if err != nil {
		t.Fatal(err)
	}
	return w, token
}

func TestInvokeInitRejectsUnresolvedInheritedIdentityBeforeBootstrap(t *testing.T) {
	unsetInheritedAgent(t)
	foreignRoot := t.TempDir()
	_, foreignToken := initAuthorizationWorkspace(t, foreignRoot)
	initCmd, _ := match([]string{"init"})
	if initCmd == nil {
		t.Fatal("init command missing")
	}

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"invalid", "invalid-inherited-secret"},
		{"foreign workspace", foreignToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			t.Setenv(agentid.EnvVar, tc.token)
			var out, errb bytes.Buffer
			ctx := &Ctx{Stdout: &out, Stderr: &errb, Cwd: target}
			err := invoke(ctx, initCmd, nil)
			if err == nil || clikit.ExitCode(err) != 3 {
				t.Fatalf("invoke(init) = %v, want policy refusal 3", err)
			}
			if _, statErr := os.Stat(filepath.Join(target, workspace.Dir)); !os.IsNotExist(statErr) {
				t.Fatalf("refused init created %s: stat err %v", workspace.Dir, statErr)
			}
			if strings.Contains(err.Error(), tc.token) && tc.token != "" {
				t.Fatalf("refusal leaked inherited token: %v", err)
			}
		})
	}
}

func TestInvokeInitStillBootstrapsWhenTokenIsAbsent(t *testing.T) {
	unsetInheritedAgent(t)
	target := t.TempDir()
	initCmd, _ := match([]string{"init"})
	var out, errb bytes.Buffer
	ctx := &Ctx{Stdout: &out, Stderr: &errb, Cwd: target}
	if err := invoke(ctx, initCmd, nil); err != nil {
		t.Fatalf("invoke(init) without DACLI_AGENT = %v\n%s", err, errb.String())
	}
	if _, err := os.Stat(filepath.Join(target, workspace.Dir)); err != nil {
		t.Fatalf("owner bootstrap did not create .dacli: %v", err)
	}
}

func TestInvokeMutationUsesParsedValueFlagsAndPreviewDeclaration(t *testing.T) {
	unsetInheritedAgent(t)
	root := t.TempDir()
	_, roToken := initAuthorizationWorkspace(t, root)
	marker := filepath.Join(root, "effect.marker")
	var output bytes.Buffer
	ctx := &Ctx{Stdout: &output, Stderr: &bytes.Buffer{}, Cwd: root}
	cmd := &Command{
		Path: "marker write", Mutates: true,
		ValueFlags: []string{"config"}, EffectFreeDryRun: true,
		Usage: "dacli marker write [--config path] [--dry-run]",
		Run: func(ctx *Ctx, args []string) error {
			flags, err := clikit.ParseFlags(args, "config")
			if err != nil {
				return err
			}
			if flags.Bool("dry-run") {
				_, err = fmt.Fprintln(ctx.Stdout, "would write marker")
				return err
			}
			return os.WriteFile(marker, []byte("written"), 0o600)
		},
	}
	t.Setenv(agentid.EnvVar, roToken)
	for _, args := range [][]string{
		{"--config", "--dry-run"},
		{"--config", "--", "--dry-run"},
		{"--dry-run=false"},
		{"--dry-run", "--dry-run=false"},
	} {
		if err := invoke(ctx, cmd, args); err == nil || clikit.ExitCode(err) != 3 {
			t.Fatalf("invoke(marker, %q) = %v, want policy refusal 3", args, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("refused invocation %q wrote marker: stat err %v", args, err)
		}
	}

	output.Reset()
	if err := invoke(ctx, cmd, []string{"--dry-run"}); err != nil {
		t.Fatalf("declared effect-free preview = %v", err)
	}
	if got := output.String(); got != "would write marker\n" {
		t.Fatalf("preview output = %q, want explicit preview", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("preview wrote marker: stat err %v", err)
	}
}

func TestInvokeRealRunDryRunRemainsUsableForRO(t *testing.T) {
	unsetInheritedAgent(t)
	root := t.TempDir()
	_, roToken := initAuthorizationWorkspace(t, root)
	marker := filepath.Join(root, "run.marker")
	command := "touch " + marker
	var out, errb bytes.Buffer
	ctx := &Ctx{Stdout: &out, Stderr: &errb, Cwd: root}
	runCmd, _ := match([]string{"run"})
	if runCmd == nil {
		t.Fatal("run command missing")
	}
	t.Setenv(agentid.EnvVar, roToken)
	if err := invoke(ctx, runCmd, []string{"--cmd", command, "--dry-run"}); err != nil {
		t.Fatalf("RO run preview = %v\n%s", err, errb.String())
	}
	if got := out.String(); got != command+"\n" {
		t.Fatalf("run preview output = %q, want command", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("run preview created marker: stat err %v", err)
	}
}

func TestInvokeRWMutationStillReachesHandler(t *testing.T) {
	unsetInheritedAgent(t)
	root := t.TempDir()
	if _, err := workspace.Init(root, "authorization-test"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "effect.marker")
	cmd := &Command{Path: "marker write", Mutates: true, Run: func(*Ctx, []string) error {
		return os.WriteFile(marker, []byte("written"), 0o600)
	}}
	if err := invoke(&Ctx{Cwd: root}, cmd, nil); err != nil {
		t.Fatalf("RW mutation = %v, want handler success", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "written" {
		t.Fatalf("RW marker = %q, err %v", got, err)
	}
}
