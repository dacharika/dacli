package planning

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// The command must reject a loaded foreign connection before a tombstone,
// queue or device credential operation. An unusable provider makes reaching
// device setup observable without requesting access to the OS credential store.
func TestNativeLocalBindingCommandsRefuseForeignConnection(t *testing.T) {
	commands := []struct {
		name string
		run  func(*clikit.Ctx, []string) error
	}{
		{"sync", cmdProjectSync},
		{"disconnect", cmdProjectDisconnect},
	}
	for _, command := range commands {
		for _, mismatch := range []string{"workspace", "project"} {
			t.Run(command.name+"/"+mismatch, func(t *testing.T) {
				w, ctx := taskAddEnv(t)
				for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", "https://example.invalid/owner/repo.git"}} {
					if _, err := gitx.Run(w.Root, args...); err != nil {
						t.Fatal(err)
					}
				}
				connection := nativeLocalBindingConnection(w)
				if mismatch == "workspace" {
					connection.Binding.Route.WorkspaceID = "foreign-workspace"
				} else {
					connection.LocalProject = "foreign-project"
				}
				root := filepath.Join(w.Root, workspace.Dir, "cloudsync", "p")
				if err := cloudsync.SaveProjectConnection(root, connection); err != nil {
					t.Fatal(err)
				}
				// Preserve a queued item and prior transition even when they cannot
				// be decoded: the refusal must precede reading either of them.
				for _, name := range []string{"transport/outbox/pending.json", "history/prior.json"} {
					path := filepath.Join(root, filepath.FromSlash(name))
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("durable sentinel\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := nativeLocalBindingFiles(t, root)
				err := command.run(ctx, []string{"p"})
				if clikit.ExitCode(err) != 3 || err == nil || !strings.Contains(err.Error(), "project connection belongs to another local workspace or project") {
					t.Errorf("binding refusal = %v (exit %d), want exact local binding policy refusal", err, clikit.ExitCode(err))
				}
				if after := nativeLocalBindingFiles(t, root); !reflect.DeepEqual(before, after) {
					t.Errorf("refused %s changed connection, history or durable queue: before=%v after=%v", command.name, before, after)
				}
			})
		}
	}
}

func TestNativeLocalBindingAcceptsExactWorkspaceAndProject(t *testing.T) {
	w, _ := taskAddEnv(t)
	if err := cloudLocalBinding(w, "p", nativeLocalBindingConnection(w)); err != nil {
		t.Fatalf("exact local binding refused: %v", err)
	}
}

func nativeLocalBindingConnection(w *workspace.Workspace) cloudsync.ProjectConnection {
	return cloudsync.ProjectConnection{
		SchemaVersion: 1,
		Provider:      "invalid-provider-before-device-access",
		LocalProject:  "p",
		Binding: cloudsync.ProjectBindingResponse{
			Route:         cloudsync.SyncRoute{WorkspaceID: w.ID, ProjectID: "remote-project", TenantID: "tenant", DeviceID: "device"},
			RepositoryURL: "https://example.invalid/owner/repo.git",
		},
		Connected:   true,
		Generation:  7,
		ConnectedAt: time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC),
	}
}

func nativeLocalBindingFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path == filepath.Join(root, "project.lock") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = string(raw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
