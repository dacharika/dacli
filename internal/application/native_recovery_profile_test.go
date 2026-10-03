package application

import (
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRecoveryProfilePinsExplicitFreshOwnerAuthority(t *testing.T) {
	f := nativeSetup(t, "printf checked")
	dir := t.TempDir()
	config := filepath.Join(dir, "owner.json")
	cred := filepath.Join(dir, "owner.token")
	if e := os.WriteFile(cred, []byte(f.owner), 0600); e != nil {
		t.Fatal(e)
	}
	c := recoveryOwnerConfig{SchemaVersion: 1, Workspace: f.workspace.Root, Authority: f.path, InstallationID: "installation", OwnerCredentialFile: cred}
	saveReviewPrivate(t, config, c)
	p, e := OpenNativeRecovery(config)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = p.authenticate(); e != nil {
		t.Fatal(e)
	}
	changed := c
	changed.InstallationID = "other"
	saveReviewPrivate(t, config, changed)
	if _, _, e = p.authenticate(); !errors.Is(e, sessions.ErrDenied) {
		t.Fatalf("owner profile substitution accepted: %v", e)
	}
	saveReviewPrivate(t, config, c)
	if e = os.WriteFile(cred, []byte(f.workerToken), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = p.authenticate(); !errors.Is(e, sessions.ErrDenied) {
		t.Fatalf("cached owner authority reused worker credential: %v", e)
	}
	if _, e = OpenNativeRecovery(config); !errors.Is(e, sessions.ErrDenied) {
		t.Fatalf("worker credential opened recovery owner profile: %v", e)
	}
}
