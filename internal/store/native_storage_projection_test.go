package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func storageProjectionFixture(t *testing.T) (*workspace.Workspace, NativeStorageSource, string, string) {
	t.Helper()
	w, e := workspace.Init(t.TempDir(), "storage")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = CreateProject(w, "a-root", "Core", "core", "goal", ""); e != nil {
		t.Fatal(e)
	}
	if _, e = CreateTask(w, "a-root", "core", "Preserve native storage", TaskOpts{Accept: []string{"exact"}}); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "authority.json")
	a, e := sessions.Open(path, sessions.Binding{Workspace: w.ID, Installation: "installation"})
	if e != nil {
		t.Fatal(e)
	}
	owner, e := a.Bootstrap()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = a.Enroll(owner, sessions.Scope{Actions: []string{"read"}, Resources: []string{"workspace:" + w.ID}}, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	for _, up := range []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints, a.UpgradeBudgets, a.UpgradeNativeAcceptance, a.UpgradeNativePolicy, a.UpgradeController} {
		if e = up(owner); e != nil {
			t.Fatal(e)
		}
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	source, e := ReadNativeStorageSource(w, raw)
	if e != nil {
		t.Fatal(e)
	}
	return w, source, path, owner
}
func TestNativeStorageProjectionExactImportAndWriterRefusal(t *testing.T) {
	w, source, path, owner := storageProjectionFixture(t)
	before, _ := os.ReadFile(path)
	destination := filepath.Join(t.TempDir(), "projection")
	out, e := NativeStorageProjection(w, source, destination, "projection-only")
	if e != nil {
		t.Fatal(e)
	}
	if !out.Reconciled || out.Authoritative || out.TaskCount != 1 || out.TaskIDs[0] != source.Tasks[0].ID {
		t.Fatal("storage imported identity/count mismatch")
	}
	for _, name := range []string{"authority.snapshot", "projection.json"} {
		clone, e := sessions.Open(filepath.Join(destination, name), sessions.Binding{Workspace: w.ID, Installation: "installation"})
		if e == nil {
			e = clone.AuthenticateOwner(owner)
		}
		if !errors.Is(e, sessions.ErrStorage) {
			t.Fatalf("projection activated native authority: %s %v", name, e)
		}
	}
	if _, e = NativeStorageProjection(w, source, filepath.Join(t.TempDir(), "writer"), "activate-writer"); e == nil {
		t.Fatal("storage accepted unsupported writer mode")
	}
	if _, e = NativeStorageProjection(w, source, filepath.Join(w.Root, "projection"), "projection-only"); e == nil {
		t.Fatal("storage imported into live workspace")
	}
	changed := source
	changed.Authority = append(append([]byte{}, source.Authority...), byte(' '))
	if _, e = NativeStorageProjection(w, changed, destination, "projection-only"); e == nil {
		t.Fatal("storage resumed against changed source")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("projection changed source authority")
	}
	fresh, e := ReadNativeStorageSource(w, before)
	if e != nil || NativeStorageSourceDigest(fresh) != NativeStorageSourceDigest(source) {
		t.Fatal("projection changed canonical source")
	}
}
func TestNativeStorageProjectionCrashChild(t *testing.T) {
	if os.Getenv("DACLI_STORAGE_CRASH_CHILD") != "1" {
		return
	}
	root, destination, input, stage := os.Getenv("DACLI_STORAGE_SOURCE_ROOT"), os.Getenv("DACLI_STORAGE_DEST"), os.Getenv("DACLI_STORAGE_INPUT"), os.Getenv("DACLI_STORAGE_STAGE")
	w, e := workspace.Find(root)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(input)
	if e != nil {
		t.Fatal(e)
	}
	var source NativeStorageSource
	if json.Unmarshal(raw, &source) != nil {
		t.Fatal("child input")
	}
	_, e = nativeStorageProjection(w, source, destination, "projection-only", func(actual string) error {
		if actual == stage {
			os.Exit(17)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Fatal("crash stage not reached")
}
func TestNativeStorageProjectionActualCrashResumePreservesSource(t *testing.T) {
	for _, stage := range []string{"authority.snapshot", "tasks.snapshot", "projection.json", "manifest.json"} {
		t.Run(stage, func(t *testing.T) {
			w, source, path, _ := storageProjectionFixture(t)
			before, _ := os.ReadFile(path)
			destination := filepath.Join(t.TempDir(), "projection")
			input := filepath.Join(t.TempDir(), "input.json")
			body, _ := json.Marshal(source)
			if e := os.WriteFile(input, body, 0600); e != nil {
				t.Fatal(e)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestNativeStorageProjectionCrashChild$")
			cmd.Env = append(os.Environ(), "DACLI_STORAGE_CRASH_CHILD=1", "DACLI_STORAGE_SOURCE_ROOT="+w.Root, "DACLI_STORAGE_DEST="+destination, "DACLI_STORAGE_INPUT="+input, "DACLI_STORAGE_STAGE="+stage)
			output, e := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 17 {
				t.Fatalf("actual crash17 missing: %v %s", e, output)
			}
			out, e := NativeStorageProjection(w, source, destination, "projection-only")
			if e != nil || !out.Reconciled || out.Authoritative {
				t.Fatalf("crash import did not resume exact non-authoritative projection: %v", e)
			}
			again, e := NativeStorageProjection(w, source, destination, "projection-only")
			if e != nil || again.SourceDigest != out.SourceDigest {
				t.Fatal("resume changed original operation")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("crash import changed source authority")
			}
			fresh, e := ReadNativeStorageSource(w, after)
			if e != nil || NativeStorageSourceDigest(fresh) != NativeStorageSourceDigest(source) {
				t.Fatal("crash import changed canonical source")
			}
		})
	}
}

func TestNativeStorageProjectionRefusesImportedDrift(t *testing.T) {
	for _, drift := range []string{"project", "status", "projection", "mode", "symlink", "oversized"} {
		t.Run(drift, func(t *testing.T) {
			w, source, authorityPath, _ := storageProjectionFixture(t)
			beforeAuthority, e := os.ReadFile(authorityPath)
			if e != nil {
				t.Fatal(e)
			}
			beforeSourceDigest := NativeStorageSourceDigest(source)
			destination := filepath.Join(t.TempDir(), "projection")
			mutated := false
			_, e = nativeStorageProjection(w, source, destination, "projection-only", func(name string) error {
				if name != "projection.json" {
					return nil
				}
				if drift == "mode" {
					err := os.Chmod(filepath.Join(destination, "projection.json"), 0644)
					mutated = err == nil
					return err
				}
				if drift == "symlink" {
					path := filepath.Join(destination, "projection.json")
					backing := filepath.Join(filepath.Dir(destination), "projection.backing")
					body, readErr := os.ReadFile(path)
					if readErr != nil {
						return readErr
					}
					if readErr = os.WriteFile(backing, body, 0600); readErr != nil {
						return readErr
					}
					if readErr = os.Remove(path); readErr != nil {
						return readErr
					}
					readErr = os.Symlink(backing, path)
					mutated = readErr == nil
					return readErr
				}
				if drift == "projection" || drift == "oversized" {
					path := filepath.Join(destination, "projection.json")
					body, readErr := os.ReadFile(path)
					if readErr != nil {
						return readErr
					}
					changed := append([]byte(nil), body...)
					if drift == "projection" {
						changed = bytes.Replace(changed, []byte(`"Authoritative":false`), []byte(`"Authoritative":true `), 1)
						if bytes.Equal(changed, body) || len(changed) != len(body) {
							t.Fatalf("projection mutation did not preserve length: %d -> %d", len(body), len(changed))
						}
					} else {
						changed = append(changed, byte(' '))
						if len(changed) != len(body)+1 {
							t.Fatalf("oversized mutation did not add one byte: %d -> %d", len(body), len(changed))
						}
					}
					readErr = os.WriteFile(path, changed, 0600)
					mutated = readErr == nil
					return readErr
				}
				path := filepath.Join(destination, "tasks.snapshot")
				body, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				var imported []NativeStorageTask
				if readErr = json.Unmarshal(body, &imported); readErr != nil {
					return readErr
				}
				if drift == "project" {
					imported[0].Project = sameLengthReplacement(imported[0].Project)
				} else {
					imported[0].Status = sameLengthReplacement(imported[0].Status)
				}
				changed, marshalErr := json.Marshal(imported)
				if marshalErr != nil {
					return marshalErr
				}
				if len(changed) != len(body) || bytes.Equal(changed, body) {
					t.Fatalf("task metadata mutation did not preserve length: %d -> %d", len(body), len(changed))
				}
				readErr = os.WriteFile(path, changed, 0600)
				mutated = readErr == nil
				return readErr
			})
			if !mutated {
				t.Fatalf("%s drift mutation did not run", drift)
			}
			if e == nil {
				t.Fatalf("reconciled after imported %s drift", drift)
			}
			afterAuthority, readErr := os.ReadFile(authorityPath)
			if readErr != nil || !bytes.Equal(beforeAuthority, afterAuthority) {
				t.Fatal("import drift changed source authority")
			}
			fresh, readErr := ReadNativeStorageSource(w, afterAuthority)
			if readErr != nil || NativeStorageSourceDigest(fresh) != beforeSourceDigest {
				t.Fatal("import drift changed canonical task bytes")
			}
		})
	}
}

func sameLengthReplacement(value string) string {
	if value == "" {
		return "x"
	}
	replacement := strings.Repeat("x", len(value))
	if replacement == value {
		replacement = strings.Repeat("y", len(value))
	}
	return replacement
}
