package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	operatorv1 "github.com/mlnomadpy/dacli/contracts/operator/v1"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/operatorread"
	"github.com/mlnomadpy/dacli/internal/store"
)

func operatorReaderFixture(t *testing.T, actions []string) (operatorReadFixture, *NativeOperatorRead) {
	t.Helper()
	f := reviewProfileSetup(t)
	actor, token, err := f.authority.Enroll(f.owner, sessions.Scope{Actions: actions, Resources: []string{"workspace:" + f.workspace.ID, "project:p"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	credentialPath := filepath.Join(dir, "operator.token")
	connectionPath := filepath.Join(dir, "operator.json")
	if err := os.WriteFile(credentialPath, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	connection := nativeconfig.Connection{SchemaVersion: 1, Workspace: f.workspace.Root, WorkspaceID: f.workspace.ID, Authority: f.path, InstallationID: "installation", SessionID: actor.ID, CredentialFile: credentialPath}
	saveReviewPrivate(t, connectionPath, connection)
	reader, err := OpenOperatorRead(connectionPath)
	if err != nil {
		t.Fatal(err)
	}
	return operatorReadFixture{reviewProfileFixture: f, sessionID: actor.ID}, reader
}

type operatorReadFixture struct {
	reviewProfileFixture
	sessionID string
}

func TestOperatorReadRequiresDistinctIdentityAndRechecksRevocation(t *testing.T) {
	worker, reader := operatorReaderFixture(t, []string{"read"})
	req := operatorv1.Request{SchemaVersion: 1, RequestID: "operator-read", Operation: "tasks.list", WorkspaceID: worker.workspace.ID, ProjectID: "p"}
	if _, err := reader.Authorize(context.Background(), req); !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("ordinary read credential gained operator authority: %v", err)
	}

	operator, reader := operatorReaderFixture(t, []string{"read", "operator.read"})
	req.WorkspaceID = operator.workspace.ID
	if _, err := reader.Authorize(context.Background(), req); err != nil {
		t.Fatalf("owner-enrolled operator read denied: %v", err)
	}
	canonicalTask, err := store.CreateTask(operator.workspace, "owner", "p", "No native binding", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	req.Operation = "tasks.get"
	req.TaskID = canonicalTask.ID
	if _, err := reader.Authorize(context.Background(), req); err != nil {
		t.Fatalf("project operator could not read canonical task without native task binding: %v", err)
	}
	if _, err := store.CreateProject(operator.workspace, "owner", "Foreign", "foreign", "foreign project", ""); err != nil {
		t.Fatal(err)
	}
	foreignTask, err := store.CreateTask(operator.workspace, "owner", "foreign", "Foreign task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	foreignRequest := req
	foreignRequest.ProjectID = "foreign"
	foreignRequest.TaskID = foreignTask.ID
	if _, err := reader.Authorize(context.Background(), foreignRequest); !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("project operator read a foreign canonical task: %v", err)
	}
	missingRequest := req
	missingRequest.TaskID = "t-01KZZZZZZZZZZZZZZZZZZZZZZZ"
	if _, err := reader.Authorize(context.Background(), missingRequest); !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("project operator received a projection for a missing task: %v", err)
	}
	if err := operator.authority.Revoke(operator.owner, operator.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Authorize(context.Background(), req); !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("revoked operator identity remained usable: %v", err)
	}
}
