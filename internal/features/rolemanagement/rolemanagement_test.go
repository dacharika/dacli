package rolemanagement

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
)

func roleRegistryCommand(t *testing.T, path string) clikit.Command {
	t.Helper()
	for _, command := range Commands() {
		if command.Path == path {
			return command
		}
	}
	t.Fatalf("command %q was not declared", path)
	return clikit.Command{}
}

func roleRegistryContext() *clikit.Ctx {
	return &clikit.Ctx{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
}

func TestRoleRegistryVerifyHandlerRejectsUnknownDuplicateAndMissingFlags(t *testing.T) {
	command := roleRegistryCommand(t, "role registry verify")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown", args: []string{"--unexpected", "value"}, want: "unknown flag"},
		{name: "duplicate", args: []string{"--config", "one", "--config", "two"}, want: "must appear once"},
		{name: "missing", args: nil, want: "--config is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := command.Run(roleRegistryContext(), tc.args)
			if clikit.ExitCode(err) != 2 || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("handler error = %v (exit %d), want usage error containing %q", err, clikit.ExitCode(err), tc.want)
			}
		})
	}
}

func TestRoleRegistryVerifyHandlerRequiresExplicitPrivateOwner(t *testing.T) {
	command := roleRegistryCommand(t, "role registry verify")
	config := filepath.Join(t.TempDir(), "missing-owner.json")
	err := command.Run(roleRegistryContext(), []string{
		"--config", config,
		"--project", "core",
		"--role", "builder",
		"--version", "1.0.0",
		"--digest", "sha256:" + strings.Repeat("a", 64),
	})
	if clikit.ExitCode(err) != 3 || err == nil || !strings.Contains(err.Error(), "explicit private owner") {
		t.Fatalf("handler error = %v (exit %d), want explicit-owner refusal", err, clikit.ExitCode(err))
	}
}

func TestRoleRegistryHandlersValidateArgumentsBeforeOpeningOwner(t *testing.T) {
	config := filepath.Join(t.TempDir(), "missing-owner.json")
	base := []string{"--config", config, "--project", "core"}
	target := []string{"--role", "builder", "--version", "1.0.0", "--digest", "sha256:" + strings.Repeat("a", 64)}
	selection := append(append([]string{}, target...), "--expected-generation", "0", "--reason", "test")
	withExpected := append(append([]string{}, selection...), "--expected-version", "1.0.0", "--expected-digest", "sha256:"+strings.Repeat("b", 64))
	cases := []struct {
		name  string
		path  string
		extra []string
		want  string
	}{
		{name: "import missing release and lifecycle", path: "role registry import", want: "import requires --release and --lifecycle"},
		{name: "import incompatible role", path: "role registry import", extra: []string{"--release", "release.json", "--lifecycle", "lifecycle.json", "--role", "builder"}, want: "import requires --release and --lifecycle only"},
		{name: "import duplicate", path: "role registry import", extra: []string{"--release", "r", "--lifecycle", "l", "--release", "r2"}, want: "must appear once"},
		{name: "import unknown", path: "role registry import", extra: []string{"--unknown", "x"}, want: "unknown flag"},
		{name: "verify missing target", path: "role registry verify", want: "--role is required"},
		{name: "verify incompatible release", path: "role registry verify", extra: append(append([]string{}, target...), "--release", "release.json"), want: "verify accepts only exact target fields"},
		{name: "verify duplicate", path: "role registry verify", extra: append(append([]string{}, target...), "--role", "other"), want: "must appear once"},
		{name: "verify unknown", path: "role registry verify", extra: []string{"--unknown", "x"}, want: "unknown flag"},
		{name: "pin missing target", path: "role registry pin", extra: []string{"--expected-generation", "0", "--reason", "test"}, want: "--role is required"},
		{name: "pin incompatible expected version", path: "role registry pin", extra: append(append([]string{}, selection...), "--expected-version", "1.0.0"), want: "pin accepts only --expected-generation"},
		{name: "pin duplicate", path: "role registry pin", extra: append(append([]string{}, selection...), "--digest", "sha256:"+strings.Repeat("b", 64)), want: "must appear once"},
		{name: "pin unknown", path: "role registry pin", extra: []string{"--unknown", "x"}, want: "unknown flag"},
		{name: "diff missing target", path: "role registry diff", want: "--role is required"},
		{name: "diff incompatible reason", path: "role registry diff", extra: append(append([]string{}, target...), "--reason", "test"), want: "diff accepts only exact target fields"},
		{name: "diff duplicate", path: "role registry diff", extra: append(append([]string{}, target...), "--version", "2.0.0"), want: "must appear once"},
		{name: "diff unknown", path: "role registry diff", extra: []string{"--unknown", "x"}, want: "unknown flag"},
		{name: "upgrade missing target", path: "role registry upgrade", extra: []string{"--expected-generation", "1", "--expected-version", "1.0.0", "--expected-digest", "sha256:" + strings.Repeat("b", 64), "--reason", "test"}, want: "--role is required"},
		{name: "upgrade incompatible release", path: "role registry upgrade", extra: append(append([]string{}, withExpected...), "--release", "release.json"), want: "upgrade requires expected current version and digest"},
		{name: "upgrade duplicate", path: "role registry upgrade", extra: append(append([]string{}, withExpected...), "--reason", "again"), want: "must appear once"},
		{name: "upgrade unknown", path: "role registry upgrade", extra: []string{"--unknown", "x"}, want: "unknown flag"},
		{name: "rollback missing target", path: "role registry rollback", extra: []string{"--expected-generation", "1", "--expected-version", "1.0.0", "--expected-digest", "sha256:" + strings.Repeat("b", 64), "--reason", "test"}, want: "--role is required"},
		{name: "rollback incompatible lifecycle", path: "role registry rollback", extra: append(append([]string{}, withExpected...), "--lifecycle", "lifecycle.json"), want: "rollback requires expected current version and digest"},
		{name: "rollback duplicate", path: "role registry rollback", extra: append(append([]string{}, withExpected...), "--role", "other"), want: "must appear once"},
		{name: "rollback unknown", path: "role registry rollback", extra: []string{"--unknown", "x"}, want: "unknown flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, base...), tc.extra...)
			err := roleRegistryCommand(t, tc.path).Run(roleRegistryContext(), args)
			if clikit.ExitCode(err) != 2 || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("handler error = %v (exit %d), want usage error containing %q before owner authentication", err, clikit.ExitCode(err), tc.want)
			}
		})
	}
}
