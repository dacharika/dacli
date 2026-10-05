package application

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func TestNativeDashboardProfileActualCommandAndIndependentReview(t *testing.T) {
	f := reviewProfileSetup(t) // Executes the configured command under its independent runner.
	reviewer, e := f.selected.Authenticate()
	if e != nil {
		t.Fatal(e)
	}
	review, e := reviewer.Propose(f.request)
	if e != nil {
		t.Fatal(e)
	}
	reader, token, e := f.authority.Enroll(f.owner, sessions.Scope{Actions: []string{"read"}, Resources: []string{"workspace:" + f.workspace.ID, "task:" + f.attempt.Request.TaskID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	credential := filepath.Join(dir, "reader.token")
	path := filepath.Join(dir, "reader.json")
	if e = os.WriteFile(credential, []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	c := nativeconfig.Connection{SchemaVersion: 2, Workspace: f.workspace.Root, WorkspaceID: f.workspace.ID, Authority: f.path, InstallationID: "installation", SessionID: reader.ID, CredentialFile: credential}
	saveReviewPrivate(t, path, c)
	selected, e := OpenNativeDashboard(path, f.workspace)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = selected.Read("p", ""); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("task reader gained project-wide access", e)
	}
	if _, e = selected.Read("foreign", f.attempt.Request.TaskID); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("task reader gained foreign project access", e)
	}
	before, e := os.ReadFile(f.path)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := selected.Read("p", f.attempt.Request.TaskID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, r := range snapshot.Records {
		if r.ID == review.ID {
			found = r.ReviewerID == f.reviewer.ID && r.AttemptID == f.attempt.ID && r.RevisionID == f.revision.ID && len(r.EvidenceIDs) == 1 && r.EvidenceIDs[0] == f.request.EvidenceIDs[0]
		}
	}
	if !found {
		t.Fatal("actual command and independent review exact links missing")
	}
	after, e := os.ReadFile(f.path)
	if e != nil || string(before) != string(after) {
		t.Fatal("dashboard read changed authority")
	}
	if snapshot.Spend.ProviderTokens != nil || snapshot.Spend.ProviderCost != nil {
		t.Fatal("unavailable provider usage became measured")
	}
	changed := c
	changed.SessionID = "other"
	saveReviewPrivate(t, path, changed)
	if _, e = selected.Read("p", ""); !errors.Is(e, nativeconfig.ErrDenied) {
		t.Fatal("immutable connection substitution accepted", e)
	}
	saveReviewPrivate(t, path, c)
	for _, version := range []int{1, 2} {
		c.SchemaVersion = version
		saveReviewPrivate(t, path, c)
		if _, e = OpenNativeDashboard(path, f.workspace); e != nil {
			t.Fatal("supported connection schema refused", e)
		}
	}
	for _, field := range []string{"session", "installation", "authority", "credential"} {
		bad := c
		switch field {
		case "session":
			bad.SessionID = ""
		case "installation":
			bad.InstallationID = ""
		case "authority":
			bad.Authority = "relative"
		case "credential":
			bad.CredentialFile = "relative"
		}
		saveReviewPrivate(t, path, bad)
		if _, e = OpenNativeDashboard(path, f.workspace); !errors.Is(e, nativeconfig.ErrDenied) {
			t.Fatal("malformed selection accepted", field, e)
		}
	}
	saveReviewPrivate(t, path, c)
	selected, e = OpenNativeDashboard(path, f.workspace)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.authority.Revoke(f.owner, reader.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = selected.Read("p", ""); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("revoked reader retained dashboard access", e)
	}
}
