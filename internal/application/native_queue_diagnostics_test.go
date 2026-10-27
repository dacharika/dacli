package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/operatorread"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestQueueDiagnosticsPrivateSelectionAndCredentialRevalidated(t *testing.T) {
	f, reader := operatorReaderFixture(t, []string{"operator.read"})
	snapshot, err := reader.ReadQueueDiagnostics(context.Background(), "p", "", 50)
	if err != nil {
		t.Fatalf("operator queue diagnostics refused its exact project: %v", err)
	}
	if snapshot.Schema != "native-queue-diagnostics/v1" || !snapshot.DiagnosticOnly || snapshot.Project != "p" {
		t.Fatalf("unexpected queue diagnostic response metadata: %+v", snapshot)
	}

	// Changing the selected immutable profile after OpenOperatorRead must fail
	// before any authority or canonical graph is exposed.
	other, token, err := f.authority.Enroll(f.owner, sessions.Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + f.workspace.ID, "project:p"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(t.TempDir(), "other.token")
	if err := os.WriteFile(credential, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := reader.dashboard.connection
	changed.SessionID = other.ID
	changed.CredentialFile = credential
	saveReviewPrivate(t, reader.dashboard.path, changed)
	if _, err = reader.ReadQueueDiagnostics(context.Background(), "p", "", 50); !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("substituted but otherwise valid private connection remained usable: %v", err)
	}
}

func TestQueueDiagnosticsPinnedConstructorRejectsValidSubstitution(t *testing.T) {
	f, reader := operatorReaderFixture(t, []string{"operator.read"})
	expected := reader.dashboard.connection
	other, token, err := f.authority.Enroll(f.owner, sessions.Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + f.workspace.ID, "project:p"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(t.TempDir(), "other.token")
	if err := os.WriteFile(credential, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := expected
	changed.SessionID = other.ID
	changed.CredentialFile = credential
	saveReviewPrivate(t, reader.dashboard.path, changed)
	if _, err := OpenPinnedQueueDiagnosticsRead(reader.dashboard.path, expected); !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("pinned constructor accepted a valid alternate operator profile: %v", err)
	}
}

func TestQueueDiagnosticsRechecksCredentialAfterSelection(t *testing.T) {
	_, reader := operatorReaderFixture(t, []string{"operator.read"})
	if _, err := reader.ReadQueueDiagnostics(context.Background(), "p", "", 50); err != nil {
		t.Fatalf("operator queue diagnostics refused its exact project: %v", err)
	}
	if err := os.WriteFile(reader.dashboard.connection.CredentialFile, []byte("substituted-credential\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadQueueDiagnostics(context.Background(), "p", "", 50); !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("substituted private credential remained usable: %v", err)
	}
}

func TestQueueDiagnosticsRevalidatesPrivateSelectionAfterCanonicalRead(t *testing.T) {
	f, reader := operatorReaderFixture(t, []string{"operator.read"})
	other, token, err := f.authority.Enroll(f.owner, sessions.Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + f.workspace.ID, "project:p"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(t.TempDir(), "other.token")
	if err := os.WriteFile(credential, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := reader.dashboard.connection
	changed.SessionID = other.ID
	changed.CredentialFile = credential
	_, err = reader.readQueueDiagnostics(context.Background(), "p", "", 50, func(ctx context.Context) (sessions.QueueCanonicalSnapshot, error) {
		saveReviewPrivate(t, reader.dashboard.path, changed)
		return readQueueCanonicalSnapshot(ctx, reader.workspace, "p", "")
	})
	if !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("private selection changed during canonical read without refusal: %v", err)
	}
}

func TestQueueDiagnosticsRevalidatesCredentialAfterCanonicalRead(t *testing.T) {
	f, reader := operatorReaderFixture(t, []string{"operator.read"})
	_, replacement, err := f.authority.Enroll(f.owner, sessions.Scope{Actions: []string{"operator.read"}, Resources: []string{"workspace:" + f.workspace.ID, "project:p"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := reader.dashboard.connection.CredentialFile
	_, err = reader.readQueueDiagnostics(context.Background(), "p", "", 50, func(ctx context.Context) (sessions.QueueCanonicalSnapshot, error) {
		if err := os.WriteFile(credentialPath, []byte(replacement+"\n"), 0600); err != nil {
			return sessions.QueueCanonicalSnapshot{}, err
		}
		return readQueueCanonicalSnapshot(ctx, reader.workspace, "p", "")
	})
	if !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("private credential changed during canonical read without refusal: %v", err)
	}
}

func TestQueueDiagnosticsCanceledContextReturnsNoProjection(t *testing.T) {
	_, reader := operatorReaderFixture(t, []string{"operator.read"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := reader.ReadQueueDiagnostics(ctx, "p", "", 50)
	if !errors.Is(err, operatorread.ErrStale) {
		t.Fatalf("canceled context returned %v", err)
	}
	if snapshot.Schema != "" || snapshot.Project != "" {
		t.Fatalf("canceled context returned partial diagnostic data: %+v", snapshot)
	}
}

func TestQueueDiagnosticsExactTaskScopeRefusesWithoutForeignIdentifier(t *testing.T) {
	f, reader := operatorReaderFixture(t, []string{"operator.read"})
	foreign, err := store.CreateProject(f.workspace, "owner", "Foreign", "foreign", "private", "")
	if err != nil {
		t.Fatal(err)
	}
	foreignTask, err := store.CreateTask(f.workspace, "owner", foreign.Slug, "Foreign secret task", store.TaskOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadQueueDiagnostics(context.Background(), "p", foreignTask.ID, 50); !errors.Is(err, operatorread.ErrDenied) {
		t.Fatalf("exact foreign task did not produce a policy denial: %v", err)
	}
	if strings.Contains(err.Error(), foreignTask.ID) {
		t.Fatal("foreign task identifier escaped in denial")
	}
}

func TestQueueDiagnosticsUsesForeignDependencyTruthWithoutDisclosure(t *testing.T) {
	f, reader := operatorReaderFixture(t, []string{"operator.read"})
	foreign, err := store.CreateProject(f.workspace, "owner", "Foreign", "foreign", "private", "")
	if err != nil {
		t.Fatal(err)
	}
	foreignTask, err := store.CreateTask(f.workspace, "owner", foreign.Slug, "Foreign secret dependency", store.TaskOpts{Claims: []string{"private/secret.go"}})
	if err != nil {
		t.Fatal(err)
	}
	localTask, err := store.CreateTask(f.workspace, "owner", "p", "Local dependent", store.TaskOpts{Claims: []string{"src/local.go"}, DependsOn: []string{foreignTask.ID}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.ReadQueueDiagnostics(context.Background(), "p", localTask.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].TaskID != localTask.ID {
		t.Fatalf("exact local task filter leaked rows: %+v", snapshot.Rows)
	}
	dependencyBlocked := false
	for _, reason := range snapshot.Rows[0].Reasons {
		dependencyBlocked = dependencyBlocked || reason.Code == "dependencies_unsatisfied"
	}
	if !dependencyBlocked {
		t.Fatalf("open foreign dependency did not block local task: %+v", snapshot.Rows[0])
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), foreignTask.ID) || strings.Contains(string(encoded), "Foreign secret dependency") || strings.Contains(string(encoded), "private/secret.go") {
		t.Fatal("cross-project dependency metadata escaped queue projection")
	}
	if err := store.MoveTask(f.workspace, foreignTask, model.StatusDone); err != nil {
		t.Fatal(err)
	}
	snapshot, err = reader.ReadQueueDiagnostics(context.Background(), "p", localTask.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range snapshot.Rows[0].Reasons {
		if reason.Code == "dependencies_unsatisfied" || reason.Code == "dependency_invalid" {
			t.Fatalf("completed foreign dependency still blocked local task: %+v", snapshot.Rows[0])
		}
	}
}
