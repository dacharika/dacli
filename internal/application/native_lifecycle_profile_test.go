package application

import (
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeLifecycleProfilePinsIndependentIdentityAndFreshAuthority(t *testing.T) {
	f := nativeSetup(t, "printf checked")
	dir := t.TempDir()
	cp := filepath.Join(dir, "connection.json")
	cred := filepath.Join(dir, "worker.token")
	if e := os.WriteFile(cred, []byte(f.workerToken), 0600); e != nil {
		t.Fatal(e)
	}
	c := nativeconfig.Connection{SchemaVersion: 1, Workspace: f.workspace.Root, WorkspaceID: f.workspace.ID, Authority: f.path, InstallationID: "installation", SessionID: f.worker.ID, CredentialFile: cred}
	saveReviewPrivate(t, cp, c)
	p, e := OpenNativeLifecycle(cp)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = p.authenticate(); e != nil {
		t.Fatal(e)
	}
	changed := c
	changed.SessionID = f.runner.ID
	saveReviewPrivate(t, cp, changed)
	if _, _, e = p.authenticate(); !errors.Is(e, sessions.ErrDenied) {
		t.Fatalf("lifecycle profile identity substitution accepted: %v", e)
	}
	saveReviewPrivate(t, cp, c)
	if e = f.authority.Revoke(f.owner, f.worker.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = p.authenticate(); !errors.Is(e, sessions.ErrDenied) {
		t.Fatalf("lifecycle profile reused revoked authentication: %v", e)
	}
}
