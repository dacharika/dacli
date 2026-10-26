package nativegovernance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

func TestNativeOperatorAddProvisionsIndependentReadOnlyIdentity(t *testing.T) {
	f := setupVerification(t, "printf verified", false)
	private := t.TempDir()
	connection := filepath.Join(private, "operator.json")
	credential := filepath.Join(private, "operator.token")
	if out, err := verificationRun(t, f.w, operatorAdd, "--config", f.config, "--project", "p", "--connection", connection, "--credential", credential, "--expires", "1h"); err != nil {
		t.Fatalf("operator add: %v %s", err, out)
	}
	var c native.Connection
	if err := privateJSON(connection, &c); err != nil {
		t.Fatal(err)
	}
	token, err := native.ReadCredential(credential)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := f.authority.Authenticate(token, c.SessionID)
	if err != nil || actor.ID == f.worker.SessionID || len(actor.Scope.Actions) != 2 || len(actor.Scope.Resources) != 2 {
		t.Fatalf("operator identity not independently scoped: %v %+v", err, actor)
	}
	for _, path := range []string{connection, credential} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private publication permissions: %v %v", info, err)
		}
	}
	snapshot, err := f.authority.ReadOperatorDashboard(token, c.SessionID, "p", "")
	if err != nil || snapshot.AuthorityVersion != 6 || len(snapshot.Records) == 0 {
		t.Fatalf("operator read or existing schema lost: %v %+v", err, snapshot)
	}
	if _, err := f.authority.RegisterAttempt(token, c.SessionID, sessions.AttemptRequest{RequestID: "operator-cannot-write", TaskID: f.task.ID, Checkout: f.w.Root}); err == nil {
		t.Fatal("read-only operator registered a coding attempt")
	}
	if _, err := f.authority.ReadOperatorDashboard(f.workerToken, f.worker.SessionID, "p", f.task.ID); err == nil {
		t.Fatal("ordinary worker accepted as operator")
	}
	if !AuthenticatesExplicitly("native operator add") {
		t.Fatal("owner provisioner bypassed explicit-owner dispatch")
	}
}

func TestNativeOperatorAddPreservesExistingDestinations(t *testing.T) {
	f := setupVerification(t, "printf verified", false)
	private := t.TempDir()
	connection := filepath.Join(private, "existing.json")
	credential := filepath.Join(private, "new.token")
	if err := os.WriteFile(connection, []byte("keep existing"), 0600); err != nil {
		t.Fatal(err)
	}
	authority := filepath.Join(filepath.Dir(f.config), "authority.json")
	before, err := os.ReadFile(authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verificationRun(t, f.w, operatorAdd, "--config", f.config, "--project", "p", "--connection", connection, "--credential", credential); err == nil {
		t.Fatal("existing connection overwritten")
	}
	if data, err := os.ReadFile(connection); err != nil || string(data) != "keep existing" {
		t.Fatal("existing private connection changed", err)
	}
	if _, err := os.Stat(credential); !os.IsNotExist(err) {
		t.Fatal("failed reservation retained new credential", err)
	}
	after, err := os.ReadFile(authority)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused provisioning changed authority", err)
	}
}
