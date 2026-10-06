package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
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
