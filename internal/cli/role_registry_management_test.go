package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func invokeRoleRegistry(t *testing.T, dir string, argv ...string) (string, error) {
	t.Helper()
	command, args := dispatch(argv)
	if command == nil || command.Path != strings.Join(argv[:3], " ") {
		t.Fatalf("role registry command is not assembled: %v", argv)
	}
	var out bytes.Buffer
	ctx := &Ctx{Cwd: dir, JSON: true, Stdout: &out, Stderr: &out}
	err := invoke(ctx, command, args)
	return out.String(), err
}

func TestRoleRegistryManagementDispatcherAndMCPAuthorityBoundaries(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "role-management")
	tokenOutput := run(t, dir, 0, "agent", "spawn", "--role", "junior", "--grant", "ro")
	token := strings.Split(strings.TrimSpace(tokenOutput), "\n")[0]
	for _, ambient := range []string{token, "invalid-legacy-token"} {
		t.Run(ambient, func(t *testing.T) {
			t.Setenv("DACLI_AGENT", ambient)
			for _, action := range []string{"import", "verify", "pin", "diff", "upgrade", "rollback"} {
				argv := []string{"role", "registry", action}
				if _, err := invokeRoleRegistry(t, dir, append(argv, "--zz-no-such-flag", "value")...); clikit.ExitCode(err) != 2 || !strings.Contains(err.Error(), "zz-no-such-flag") {
					t.Fatalf("%s borrowed ambient grant instead of validating flags: %v", action, err)
				}
				_, diagnostic, code := executor(dir)(argv, true)
				if code != 3 || !strings.Contains(diagnostic, "local-only") {
					t.Fatalf("legacy MCP exposed %s: exit%d %s", action, code, diagnostic)
				}
				args := append(argv, "--config", filepath.Join(t.TempDir(), "absent-owner.json"), "--project", "core")
				if action == "import" {
					args = append(args, "--release", "release.json", "--lifecycle", "lifecycle.json")
				} else {
					args = append(args, "--role", "builder", "--version", "2.0.0", "--digest", "sha256:"+strings.Repeat("a", 64))
				}
				if action == "pin" || action == "upgrade" || action == "rollback" {
					args = append(args, "--expected-generation", "0", "--reason", "fixture selection")
				}
				if action == "upgrade" || action == "rollback" {
					args = append(args, "--expected-version", "1.0.0", "--expected-digest", "sha256:"+strings.Repeat("b", 64))
				}
				if _, err := invokeRoleRegistry(t, dir, args...); clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "explicit private owner") {
					t.Fatalf("%s accepted ambient authority or lost private-owner refusal: %v", action, err)
				}
			}
		})
	}
	command, _ := dispatch([]string{"native", "role", "registry", "upgrade"})
	if command == nil || command.Path != "native role registry upgrade" {
		t.Fatal("role selection shadowed the distinct authority migration command")
	}
}

func TestRoleRegistryManagementCLIImportsAndReadsWithoutActivation(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "role-management")
	run(t, dir, 0, "project", "add", "Core", "--slug", "core", "--goal", "role fixture")
	run(t, dir, 0, "role", "add", "builder")
	tokenOutput := run(t, dir, 0, "agent", "spawn", "--role", "junior", "--grant", "ro")
	token := strings.Split(strings.TrimSpace(tokenOutput), "\n")[0]
	w, err := workspace.Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := store.ReadCheckpointRoles(w)
	if err != nil || len(roles) != 1 {
		t.Fatalf("canonical fixture roles: %v %v", roles, err)
	}
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	writeJSON := func(path string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	registryDir := filepath.Join(w.Root, workspace.Dir, "role-registry")
	if err := os.MkdirAll(registryDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON(filepath.Join(registryDir, "publishers.json"), map[string][]byte{"fixture-publisher": private.Public().(ed25519.PublicKey)})
	privateDir := t.TempDir()
	privateDir, err = filepath.EvalSymlinks(privateDir)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(privateDir, "owner.json")
	writeJSON(config, map[string]any{"schema_version": 1, "workspace": w.Root, "authority": filepath.Join(privateDir, "authority.json"), "installation_id": "role-cli-fixture", "owner_credential_file": filepath.Join(privateDir, "owner.token")})
	t.Setenv("DACLI_AGENT", token)
	run(t, dir, 0, "native", "bootstrap", "--config", config)
	release, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: "1.0.0", Role: roles[0]}, "fixture-publisher", private)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: "1.0.0", ContentDigest: release.ContentDigest, Generation: 1, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "fixture-publisher"}, private)
	if err != nil {
		t.Fatal(err)
	}
	releasePath, lifecyclePath := filepath.Join(privateDir, "release.json"), filepath.Join(privateDir, "lifecycle.json")
	writeJSON(releasePath, release)
	writeJSON(lifecyclePath, status)
	projectBefore, err := os.ReadFile(w.ProjectPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	roleBefore, err := os.ReadFile(w.RolePath("builder"))
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"--config", config, "--project", "core"}
	text, err := invokeRoleRegistry(t, privateDir, append([]string{"role", "registry", "import"}, append(base, "--release", "release.json", "--lifecycle", "lifecycle.json")...)...)
	var receipt application.NativeRoleRegistryImportReceipt
	if err != nil || json.Unmarshal([]byte(text), &receipt) != nil || receipt.Selected || receipt.RoleID != "builder" || receipt.ContentDigest != release.ContentDigest || receipt.LifecycleState != "active" {
		t.Fatalf("assembled import did not return an exact nonactivating receipt: %v %s", err, text)
	}
	target := append(base, "--role", "builder", "--version", "1.0.0", "--digest", release.ContentDigest)
	for _, action := range []string{"verify", "diff"} {
		text, err := invokeRoleRegistry(t, dir, append([]string{"role", "registry", action}, target...)...)
		var view map[string]any
		if err != nil || json.Unmarshal([]byte(text), &view) != nil || view["canonical_role_matches"] != true || view["selection_supported"] != false {
			t.Fatalf("assembled %s lost exact canonical/schema facts: %v %s", action, err, text)
		}
	}
	human := run(t, dir, 0, append([]string{"role", "registry", "verify"}, target...)...)
	for _, want := range []string{"Role registry verify: builder 1.0.0", "canonical role matches: true", "pin operations supported: false"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human verification omitted %q: %s", want, human)
		}
	}
	if _, err := invokeRoleRegistry(t, dir, append([]string{"role", "registry", "pin"}, append(target, "--expected-generation", "0", "--reason", "requires schema20")...)...); clikit.ExitCode(err) != 3 {
		t.Fatalf("assembled pin silently migrated old authority: %v", err)
	}
	projectAfter, err := os.ReadFile(w.ProjectPath("core"))
	if err != nil || !bytes.Equal(projectBefore, projectAfter) {
		t.Fatalf("nonactivating import/read/refused pin changed project: %v", err)
	}
	roleAfter, err := os.ReadFile(w.RolePath("builder"))
	if err != nil || !bytes.Equal(roleBefore, roleAfter) {
		t.Fatalf("role management rewrote canonical instructions: %v", err)
	}
}
