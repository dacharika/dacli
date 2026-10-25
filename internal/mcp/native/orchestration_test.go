package native

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

func orchestrationFixture(t *testing.T, role string) fixture {
	t.Helper()
	f := setup(t)
	// The default worker fixture bootstraps schema6. Swarm profiles require
	// the explicit owner schema7 transition, even for read/catalog generation.
	if e := f.a.UpgradeSwarms(f.owner); e != nil {
		t.Fatal(e)
	}
	var e error
	f.a, e = sessions.OpenSwarm(f.c.Authority, sessions.Binding{Workspace: f.c.WorkspaceID, Installation: f.c.InstallationID}, nil)
	if e != nil {
		t.Fatal(e)
	}
	actions := map[string][]string{"controller": {"read", "controller.start", "controller.stop", "controller.read", "controller.step", "controller.reconcile", "swarm.plan", "dispatch.cancel"}, "bridge": {"read", "dispatch.correlate", "dispatch.observe"}, "worker": {"read", "lease.acquire"}}[role]
	resources := []string{"project:p", "task:" + f.task.ID, "path:src"}
	if role == "worker" {
		resources = append(resources, "workspace:"+f.c.WorkspaceID)
	}
	actor, token, e := f.a.Enroll(f.owner, sessions.Scope{Actions: actions, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	private := filepath.Dir(f.s.path)
	cred := filepath.Join(private, "orchestration.token")
	if e = os.WriteFile(cred, []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	f.c.SessionID = actor.ID
	f.c.CredentialFile = cred
	connection := filepath.Join(private, "orchestration-connection.json")
	save(t, connection, f.c)
	capability := filepath.Join(private, "capability.json")
	save(t, capability, application.ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: f.c.WorkspaceID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", Codex: &application.CodexDispatchCapability{SchemaVersion: 1, NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}})
	raw, e := os.ReadFile(capability)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw)
	c := application.NativeOrchestrationConfig{SchemaVersion: 1, WorkspaceID: f.c.WorkspaceID, ProjectID: "p", SessionID: actor.ID, Role: role, CapabilityFile: capability, CapabilitySHA256: hex.EncodeToString(sum[:])}
	if role == "controller" {
		c.PolicyRevision = "pinned-policy"
		c.Routes = map[string]sessions.ControllerTaskRoute{f.task.ID: {}}
	}
	profile := filepath.Join(private, "orchestration.json")
	save(t, profile, c)
	f.s, e = OpenWithOrchestration(connection, profile)
	if e != nil {
		t.Fatal(e)
	}
	f.token = token
	return f
}
func TestOrchestrationRoleCatalogAndStrictWireParity(t *testing.T) {
	for _, role := range []string{"controller", "bridge", "worker"} {
		t.Run(role, func(t *testing.T) {
			f := orchestrationFixture(t, role)
			catalog := f.s.catalog()
			if len(catalog) != len(orchestrationReads)+len(orchestrationRoles[role]) {
				t.Fatal("role catalog widened")
			}
			for _, item := range catalog {
				if !f.s.orchestrationToolAllowed(item.Name) {
					t.Fatal("catalog leaked different role")
				}
				response := result(item.Name, nil, ErrDenied).(map[string]any)
				structured, _ := json.Marshal(response["structuredContent"])
				if !validJSON(item.OutputSchema, structured) || string(structured) != response["content"].([]any)[0].(map[string]any)["text"].(string) {
					t.Fatal("structured/text wire mismatch")
				}
			}
			before, e := os.ReadFile(f.c.Authority)
			if e != nil {
				t.Fatal(e)
			}
			for _, name := range []string{"register_attempt", "acquire_lease", "publish_revision", "request_verification", "propose_review", "start_loop", "stop_loop", "observe_dispatch", "acknowledge_dispatch"} {
				if f.s.orchestrationToolAllowed(name) {
					continue
				}
				if _, e = f.s.call(name, json.RawMessage(`{"schema_version":1,"request":{}}`)); e == nil {
					t.Fatal("nonadvertised role tool allowed", name)
				}
			}
			if _, e = f.s.call("get_dispatch_status", json.RawMessage(`{"schema_version":1,"dispatch_id":"unknown","credential":"override"}`)); e == nil {
				t.Fatal("caller credential override accepted")
			}
			after, _ := os.ReadFile(f.c.Authority)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("strict refused requests changed authority")
			}
			if e = f.a.Revoke(f.owner, f.c.SessionID); e != nil {
				t.Fatal(e)
			}
			if _, e = f.s.call("identity", json.RawMessage(`{"schema_version":1}`)); e == nil {
				t.Fatal("orchestration connection did not freshly authenticate")
			}
		})
	}
}

func TestOrchestrationContextReadCatalogAndCalls(t *testing.T) {
	base := map[string]tool{}
	for _, item := range catalog() {
		base[item.Name] = item
	}
	for _, role := range []string{"controller", "bridge", "worker"} {
		t.Run(role, func(t *testing.T) {
			f := orchestrationFixture(t, role)
			other, err := store.CreateProject(mustWorkspace(t, f), "root", "Other", "other", "other goal", "")
			if err != nil {
				t.Fatal(err)
			}
			otherTask, err := store.CreateTask(mustWorkspace(t, f), "root", other.Slug, "Other task", store.TaskOpts{})
			if err != nil {
				t.Fatal(err)
			}

			got := map[string]tool{}
			for _, item := range f.s.catalog() {
				got[item.Name] = item
			}
			for _, name := range []string{"task_brief", "ready_frontier"} {
				item, ok := got[name]
				if !ok {
					t.Fatalf("%s role catalog omitted %s", role, name)
				}
				if !reflect.DeepEqual(item.InputSchema, base[name].InputSchema) || !reflect.DeepEqual(item.OutputSchema, base[name].OutputSchema) {
					t.Fatalf("%s role changed canonical %s schemas", role, name)
				}
			}

			brief := call(t, f, "task_brief", briefArgs{version{1}, f.task.ID, 10000}).(application.NativeTaskBrief)
			if brief.Schema != "native-task-brief/v1" || brief.TaskID != f.task.ID || brief.ObservedAt.IsZero() || brief.TrustNotice == "" || len(brief.SourceRefs) == 0 {
				t.Fatalf("%s role returned incomplete canonical brief: %+v", role, brief)
			}
			if role != "worker" && (len(brief.Omissions) != 1 || !strings.Contains(strings.Join(brief.Omissions, " "), "broader")) {
				t.Fatalf("%s project-scoped role lost the exact-task omission: %v", role, brief.Omissions)
			}
			assertNativeWireParity(t, "task_brief", brief)

			frontier := call(t, f, "ready_frontier", frontierArgs{version{1}, "", f.task.ID, 10}).(application.NativeReadyFrontier)
			if frontier.Schema != "native-ready-frontier/v1" || frontier.Scope != "task" || frontier.Task != f.task.ID || frontier.ObservedAt.IsZero() || !frontier.DiagnosticOnly || len(frontier.Ready) != 1 || frontier.Ready[0].ID != f.task.ID {
				t.Fatalf("%s role returned incomplete exact-task frontier: %+v", role, frontier)
			}
			assertNativeWireParity(t, "ready_frontier", frontier)

			if _, err = f.s.call("ready_frontier", mustJSON(frontierArgs{version{1}, other.Slug, "", 10})); err == nil {
				t.Fatalf("%s role read another project's frontier", role)
			}
			if _, err = f.s.call("ready_frontier", mustJSON(frontierArgs{version{1}, "", otherTask.ID, 10})); err == nil {
				t.Fatalf("%s role read another task's frontier", role)
			}
			if role != "worker" {
				if _, err = f.s.call("task_brief", mustJSON(briefArgs{version{1}, otherTask.ID, 10000})); err == nil {
					t.Fatalf("%s project-scoped role read another project's task brief", role)
				}
			}
		})
	}
}

// Fixture generation is explicit, scoped implementation work, never automatic
// acceptance of a changed catalog. The resulting files remain reviewable.
func TestOrchestrationReviewedWireFixtures(t *testing.T) {
	fixtures := foundationFixtures()
	for _, role := range []string{"controller", "bridge", "worker"} {
		f := orchestrationFixture(t, role)
		name := role + "_catalog.json"
		if role == "worker" {
			name = "orchestration_worker_catalog.json"
		}
		fixtures[name] = map[string]any{"tools": f.s.catalog()}
	}
	for name, current := range fixtures {
		if name != "input_schemas.json" && name != "output_schemas.json" && name != "controller_catalog.json" && name != "bridge_catalog.json" && name != "orchestration_worker_catalog.json" {
			continue
		}
		b, e := json.MarshalIndent(current, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		b = append(b, '\n')
		path := filepath.Join("testdata", name)
		if os.Getenv("DACLI_WRITE_ORCHESTRATION_FIXTURES") == "1" {
			if e = os.WriteFile(path, b, 0644); e != nil {
				t.Fatal(e)
			}
		}
		want, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var a, z any
		if json.Unmarshal(want, &a) != nil || json.Unmarshal(b, &z) != nil || !reflect.DeepEqual(a, z) {
			t.Fatal("orchestration reviewed fixture mismatch", name)
		}
	}
}
