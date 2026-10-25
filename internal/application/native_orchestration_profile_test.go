package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestOrchestrationPrivateProfileFreshIdentityAndRole(t *testing.T) {
	w, _ := readFixture(t)
	private := t.TempDir()
	authority, e := sessions.Open(filepath.Join(private, "authority.json"), sessions.Binding{Workspace: w.ID, Installation: "installation"})
	if e != nil {
		t.Fatal(e)
	}
	owner, e := authority.Bootstrap()
	if e != nil {
		t.Fatal(e)
	}
	// The disposable profile uses the swarm reader, so its owner must upgrade
	// the bootstrapped schema6 authority before opening that profile.
	if e = authority.UpgradeSwarms(owner); e != nil {
		t.Fatal(e)
	}
	actor, token, e := authority.Enroll(owner, sessions.Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: []string{"workspace:" + w.ID, "project:p", "task:task"}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	save := func(path string, value any) []byte {
		t.Helper()
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		return b
	}
	credential := filepath.Join(private, "token")
	if e = os.WriteFile(credential, []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	connection := filepath.Join(private, "connection.json")
	save(connection, nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, InstallationID: "installation", Authority: filepath.Join(private, "authority.json"), SessionID: actor.ID, CredentialFile: credential})
	capability := filepath.Join(private, "capability.json")
	b := save(capability, ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: w.ID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", Codex: &CodexDispatchCapability{SchemaVersion: 1, NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}})
	digest := sha256.Sum256(b)
	c := NativeOrchestrationConfig{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: "p", SessionID: actor.ID, Role: "bridge", CapabilityFile: capability, CapabilitySHA256: hex.EncodeToString(digest[:])}
	profilePath := filepath.Join(private, "orchestration.json")
	save(profilePath, c)
	profile, e := OpenNativeOrchestrationProfile(connection, profilePath)
	if e != nil {
		t.Fatal(e)
	}
	if profile.Role() != "bridge" || profile.Project() != "p" {
		t.Fatal("selected profile lost pinned role/project")
	}
	if _, e = profile.Start("start", "invocation", "checkpoint"); e == nil {
		t.Fatal("bridge started controller")
	}
	c.Role = "controller"
	c.PolicyRevision = "policy"
	c.Routes = map[string]sessions.ControllerTaskRoute{"task": {}}
	save(profilePath, c)
	if _, e = profile.Authenticate(); e == nil {
		t.Fatal("changed profile widened existing connection")
	}
	if _, e = OpenNativeOrchestrationProfile(connection, profilePath); e == nil {
		t.Fatal("owner role label widened enrolled scope")
	}
	c.Role = "bridge"
	c.PolicyRevision = ""
	c.Routes = nil
	save(profilePath, c)
	if _, e = profile.Authenticate(); e != nil {
		t.Fatal("restored original profile failed", e)
	}
	b = append(b, ' ')
	if e = os.WriteFile(capability, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = profile.Authenticate(); e == nil {
		t.Fatal("capability pin not reloaded")
	}
	b = b[:len(b)-1]
	if e = os.WriteFile(capability, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = authority.Revoke(owner, actor.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = profile.Authenticate(); e == nil {
		t.Fatal("revoked bridge retained authority")
	}
}

func TestOrchestrationTaskScopedWorker(t *testing.T) {
	w, _ := readFixture(t)
	for _, project := range []string{"p", "foreign"} {
		if _, e := store.CreateProject(w, "root", project, project, "goal", ""); e != nil {
			t.Fatal(e)
		}
	}
	task, e := store.CreateTask(w, "root", "p", "Worker", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := store.CreateTask(w, "root", "foreign", "Foreign", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	authority, e := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "installation"})
	if e != nil {
		t.Fatal(e)
	}
	owner, e := authority.Bootstrap()
	if e != nil {
		t.Fatal(e)
	}
	if e = authority.UpgradeSwarms(owner); e != nil {
		t.Fatal(e)
	}
	save := func(path string, value any) []byte {
		t.Helper()
		data, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
		return data
	}
	for _, tc := range []struct {
		name, task, project, actor string
		actions                    []string
		want                       bool
	}{
		{"exact task", task.ID, "p", "", []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit", "verification.request"}, true},
		{"foreign project", task.ID, "foreign", "", []string{"read", "lease.acquire"}, false},
		{"foreign task", foreign.ID, "p", "", []string{"read", "lease.acquire"}, false},
		{"missing task", "missing", "p", "", []string{"read", "lease.acquire"}, false},
		{"foreign actor", task.ID, "p", "other", []string{"read", "lease.acquire"}, false},
		{"read only actor", task.ID, "p", "", []string{"read"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, token, e := authority.Enroll(owner, sessions.Scope{Actions: tc.actions, Resources: []string{"workspace:" + w.ID, "task:" + tc.task, "path:src", "checkout:" + w.Root}}, time.Now().Add(time.Hour))
			if e != nil {
				t.Fatal(e)
			}
			dir := t.TempDir()
			credential := filepath.Join(dir, "token")
			if e = os.WriteFile(credential, []byte(token), 0600); e != nil {
				t.Fatal(e)
			}
			connection := filepath.Join(dir, "connection.json")
			save(connection, nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authorityPath, InstallationID: "installation", SessionID: actor.ID, CredentialFile: credential})
			capability := filepath.Join(dir, "capability.json")
			data := save(capability, ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: w.ID, ProjectID: tc.project, Harness: "codex", Source: "owner-manual-native-handoff", Codex: &CodexDispatchCapability{SchemaVersion: 1, NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}})
			digest := sha256.Sum256(data)
			id := actor.ID
			if tc.actor != "" {
				id = tc.actor
			}
			config := filepath.Join(dir, "orchestration.json")
			save(config, NativeOrchestrationConfig{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: tc.project, SessionID: id, Role: "worker", CapabilityFile: capability, CapabilitySHA256: hex.EncodeToString(digest[:])})
			profile, e := OpenNativeOrchestrationProfile(connection, config)
			if !tc.want {
				if e == nil {
					t.Fatal("out-of-scope worker profile admitted")
				}
				return
			}
			if e != nil {
				t.Fatal("task-scoped native enrollment refused", e)
			}
			if _, e = profile.Authenticate(); e != nil {
				t.Fatal(e)
			}
			if _, e = authority.Authorize(token, actor.ID, w.ID, "lease.acquire", "project:p"); e == nil {
				t.Fatal("worker gained project authority")
			}
			if _, e = profile.Acknowledge(sessions.AcknowledgeDispatchIntent{DispatchID: "foreign-dispatch"}); e == nil {
				t.Fatal("unbound dispatch acknowledged")
			}
			if _, e = profile.Start("start", "invocation", "checkpoint"); e == nil {
				t.Fatal("worker became controller")
			}
			if e = authority.Revoke(owner, actor.ID); e != nil {
				t.Fatal(e)
			}
			if _, e = profile.Authenticate(); e == nil {
				t.Fatal("revoked task-scoped worker admitted")
			}
		})
	}
}

func TestOrchestrationWorkerAuthenticationFindsLateEnrolledTask(t *testing.T) {
	w, _ := readFixture(t)
	if _, err := store.CreateProject(w, "root", "Worker project", "p", "goal", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 128; i++ {
		if _, err := store.CreateTask(w, "root", "p", "Unrelated backlog task", store.TaskOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.CreateTask(w, "root", "p", "Late enrolled task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	profile := newOrchestrationWorkerProfile(t, w, "p", task.ID)
	if _, err := profile.Authenticate(); err != nil {
		t.Fatalf("worker with only the late canonical task enrolled was refused: %v", err)
	}
}

func TestAuthorizeEnrolledWorkerTaskChecksOnlyEnrolledCandidates(t *testing.T) {
	tasks := make([]*store.Task, 0, 130)
	for i := 0; i < 128; i++ {
		tasks = append(tasks, &store.Task{ID: fmt.Sprintf("unrelated-%03d", i), Project: "p"})
	}
	tasks = append(tasks, &store.Task{ID: "foreign", Project: "other"})
	tasks = append(tasks, &store.Task{ID: "late", Project: "p"})
	type authorizationCall struct{ action, resource string }
	var calls []authorizationCall
	authorize := func(action, resource string) error {
		calls = append(calls, authorizationCall{action, resource})
		if resource == "task:late" && (action == "read" || action == "lease.acquire") {
			return nil
		}
		return sessions.ErrDenied
	}
	if !authorizeEnrolledWorkerTask("p", tasks, []string{"workspace:w", "task:late"}, authorize) {
		t.Fatal("enrolled late task was not authorized")
	}
	want := []authorizationCall{{"read", "task:late"}, {"lease.acquire", "task:late"}}
	if len(calls) != len(want) {
		t.Fatalf("authorization call count=%d, want exactly %d (late task read and lease only)", len(calls), len(want))
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("authorization calls = %v, want exactly %v", calls, want)
		}
	}
}

func BenchmarkOrchestrationWorkerAuthenticationLargeBacklog(b *testing.B) {
	for _, size := range []int{0, 128, 512} {
		b.Run(fmt.Sprintf("unrelated_tasks_%d", size), func(b *testing.B) {
			w, err := workspace.Init(b.TempDir(), "orchestration-benchmark")
			if err != nil {
				b.Fatal(err)
			}
			if _, err = store.CreateProject(w, "root", "Worker project", "p", "goal", ""); err != nil {
				b.Fatal(err)
			}
			for i := 0; i < size; i++ {
				if _, err = store.CreateTask(w, "root", "p", "Unrelated backlog task", store.TaskOpts{}); err != nil {
					b.Fatal(err)
				}
			}
			task, err := store.CreateTask(w, "root", "p", "Late enrolled task", store.TaskOpts{})
			if err != nil {
				b.Fatal(err)
			}
			profile := newOrchestrationWorkerProfile(b, w, "p", task.ID)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := profile.Authenticate(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func newOrchestrationWorkerProfile(t testing.TB, w *workspace.Workspace, project, task string) *NativeOrchestrationProfile {
	t.Helper()
	private := t.TempDir()
	authorityPath := filepath.Join(private, "authority.json")
	authority, err := sessions.Open(authorityPath, sessions.Binding{Workspace: w.ID, Installation: "installation"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := authority.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if err = authority.UpgradeSwarms(owner); err != nil {
		t.Fatal(err)
	}
	actor, token, err := authority.Enroll(owner, sessions.Scope{Actions: []string{"read", "lease.acquire"}, Resources: []string{"workspace:" + w.ID, "task:" + task}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	save := func(path string, value any) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return data
	}
	credential := filepath.Join(private, "token")
	if err = os.WriteFile(credential, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	connection := filepath.Join(private, "connection.json")
	save(connection, nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authorityPath, InstallationID: "installation", SessionID: actor.ID, CredentialFile: credential})
	capability := filepath.Join(private, "capability.json")
	data := save(capability, ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: w.ID, ProjectID: project, Harness: "codex", Source: "owner-manual-native-handoff", Codex: &CodexDispatchCapability{SchemaVersion: 1, NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}})
	digest := sha256.Sum256(data)
	config := filepath.Join(private, "orchestration.json")
	save(config, NativeOrchestrationConfig{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: project, SessionID: actor.ID, Role: "worker", CapabilityFile: capability, CapabilitySHA256: hex.EncodeToString(digest[:])})
	profile, err := OpenNativeOrchestrationProfile(connection, config)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}
