package operatorconformance

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSelectedLocalTransport(t *testing.T) {
	binary, connection := os.Getenv("DACLI_OPERATOR_BIN"), os.Getenv("DACLI_OPERATOR_CONNECTION")
	if binary == "" || connection == "" {
		t.Skip("set DACLI_OPERATOR_BIN and DACLI_OPERATOR_CONNECTION to qualify the owner-selected local transport")
	}
	workspace, project := os.Getenv("DACLI_OPERATOR_WORKSPACE"), os.Getenv("DACLI_OPERATOR_PROJECT")
	if workspace == "" || project == "" {
		t.Fatal("expected scope must be supplied by the owner, not read from authority files")
	}
	child, err := startChild(binary, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child != nil {
			_ = child.close()
		}
	}()
	limit := 1
	got, err := child.exchange(request{SchemaVersion: 1, RequestID: "go-live-tasks", Operation: "tasks.list", WorkspaceID: workspace, ProjectID: project, Limit: &limit}, workspace, project)
	if err != nil || !got.OK {
		t.Fatalf("live task read = %+v, %v", got, err)
	}
	snapshot, err := child.exchange(request{SchemaVersion: 1, RequestID: "go-live-runtime-capabilities", Operation: "governance.snapshot", WorkspaceID: workspace, ProjectID: project}, workspace, project)
	if err != nil || !snapshot.OK {
		t.Fatalf("live governance snapshot = %+v, %v", snapshot, err)
	}
	assertSchema10UnsupportedRuntime(t, snapshot)
	bad, err := child.exchange(request{SchemaVersion: 999, RequestID: "go-live-version", Operation: "tasks.list", WorkspaceID: workspace, ProjectID: project}, workspace, project)
	if err != nil {
		t.Fatal(err)
	}
	if err := denied(bad, "unsupported_version"); err != nil {
		t.Fatal(err)
	}
	if err := child.close(); err != nil {
		t.Fatalf("close selected transport after EOF: %v", err)
	}
	child = nil
	reconnected, err := startChild(binary, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if reconnected != nil {
			_ = reconnected.close()
		}
	}()
	frame, err := reconnected.exchange(request{SchemaVersion: 1, RequestID: "go-live-reconnect", Operation: "tasks.list", WorkspaceID: workspace, ProjectID: project}, workspace, project)
	if err != nil || !frame.OK {
		t.Fatalf("fresh selected transport reconnect = %+v, %v", frame, err)
	}
	if err := reconnected.close(); err != nil {
		t.Fatalf("close reconnected transport after EOF: %v", err)
	}
	reconnected = nil
	if ordinaryConnection := os.Getenv("DACLI_OPERATOR_MISSING_AUTHORITY_CONNECTION"); ordinaryConnection != "" {
		deniedChild, err := startChild(binary, ordinaryConnection)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = deniedChild.close() }()
		deniedFrame, err := deniedChild.exchange(request{SchemaVersion: 1, RequestID: "go-live-missing-authority", Operation: "tasks.list", WorkspaceID: workspace, ProjectID: project}, workspace, project)
		if err != nil {
			t.Fatal(err)
		}
		if err := denied(deniedFrame, "denied"); err != nil {
			t.Fatalf("ordinary read connection without operator.read authority: %v", err)
		}
	}
}

func assertSchema10UnsupportedRuntime(t *testing.T, got response) {
	t.Helper()
	var data struct {
		AuthorityVersion int               `json:"authority_version"`
		Services         map[string]string `json:"services"`
		Spend            struct {
			State       string `json:"state"`
			Provenance  string `json:"provenance"`
			Enforcement string `json:"enforcement"`
		} `json:"spend"`
	}
	if err := json.Unmarshal(got.Data, &data); err != nil {
		t.Fatalf("decode opaque governance snapshot fields: %v", err)
	}
	if data.AuthorityVersion != 10 {
		t.Fatalf("fixture must exercise disposable schema 10, got schema %d", data.AuthorityVersion)
	}
	for _, service := range []string{"budget", "policy", "controller"} {
		if data.Services[service] != "unsupported" {
			t.Errorf("schema-10 service %q = %q, want explicitly unsupported", service, data.Services[service])
		}
	}
	if data.Spend.State != "unavailable" || data.Spend.Provenance != "unavailable" || data.Spend.Enforcement != "unavailable" {
		t.Errorf("schema-10 spend must remain explicitly unavailable, got %+v", data.Spend)
	}
}
