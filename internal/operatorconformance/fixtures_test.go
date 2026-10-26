package operatorconformance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type golden struct {
	Case     string          `json:"case"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}

func TestIndependentGoConsumerDecodesGoldenFrames(t *testing.T) {
	files, err := filepath.Glob("../../contracts/operator/v1/testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no public golden fixtures found")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var f golden
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatal(err)
			}
			if f.Case == "" || len(f.Request) == 0 || len(f.Response) == 0 {
				t.Fatal("fixture envelope must provide case, request and response")
			}
			var req request
			if err := json.Unmarshal(f.Request, &req); err != nil {
				t.Fatal(err)
			}
			var rawResponse struct {
				SchemaVersion int    `json:"schema_version"`
				RequestID     string `json:"request_id"`
				OK            bool   `json:"ok"`
				Error         *struct {
					Code string `json:"code"`
				} `json:"error,omitempty"`
			}
			if err := json.Unmarshal(f.Response, &rawResponse); err != nil {
				t.Fatal(err)
			}
			if rawResponse.RequestID != req.RequestID {
				t.Fatalf("fixture response id %q != request id %q", rawResponse.RequestID, req.RequestID)
			}
			if !rawResponse.OK && (rawResponse.Error == nil || rawResponse.Error.Code == "") {
				t.Fatal("failure fixture is missing a stable error code")
			}
			if rawResponse.OK && rawResponse.SchemaVersion != protocolVersion {
				t.Fatalf("success fixture version %d", rawResponse.SchemaVersion)
			}
			if _, err := decodeResponse(f.Response, req.RequestID, req.WorkspaceID, req.ProjectID, req.TaskID); err != nil {
				t.Fatalf("independent response decoder: %v", err)
			}
			if f.Case == "stale-source-remains-stale" {
				var compact bytes.Buffer
				if err := json.Compact(&compact, f.Response); err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(compact.Bytes(), []byte(`"freshness":"stale"`)) {
					t.Fatal("stale fixture lost its explicit stale marker")
				}
			}
		})
	}
}

func TestConsumerRejectsMismatchedResponseIdentity(t *testing.T) {
	raw := []byte(`{"schema_version":1,"request_id":"other","ok":false,"error":{"code":"denied","message":"denied","retryable":false}}`)
	if _, err := decodeResponse(raw, "requested", "ws", "p", ""); err == nil || !strings.Contains(err.Error(), "request_id") {
		t.Fatalf("mismatched response id accepted: %v", err)
	}
}

func TestGoldenResponseScopeIsBoundToItsRequest(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/operator/v1/testdata/tasks-list-success.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture golden
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	mutated := bytes.Replace(fixture.Response, []byte(`"workspace_id": "ws-operator-01"`), []byte(`"workspace_id": "ws-foreign-02"`), 1)
	var req request
	if err := json.Unmarshal(fixture.Request, &req); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeResponse(mutated, req.RequestID, req.WorkspaceID, req.ProjectID, req.TaskID); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("response scope forged relative to request accepted: %v", err)
	}
}

func TestConsumerRejectsMismatchedVersionAndScope(t *testing.T) {
	wrongVersion := []byte(`{"schema_version":2,"request_id":"r","ok":false,"error":{"code":"denied","message":"denied","retryable":false}}`)
	if _, err := decodeResponse(wrongVersion, "r", "ws", "p", ""); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("wrong response version accepted: %v", err)
	}
	wrongScope := []byte(`{"schema_version":1,"request_id":"r","ok":true,"scope":{"workspace_id":"elsewhere","project_id":"p"},"observed_at":"2026-10-25T16:09:02Z","source":{"producer":"p","schema":"s","observed_at":"2026-10-25T16:09:02Z"},"data":{}}`)
	if _, err := decodeResponse(wrongScope, "r", "ws", "p", ""); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("wrong response scope accepted: %v", err)
	}
}

func TestConsumerDoesNotTreatMissingCapabilityAsSuccess(t *testing.T) {
	frame := response{SchemaVersion: 1, RequestID: "cap", OK: true, Scope: scope{WorkspaceID: "ws", ProjectID: "p"}, Data: json.RawMessage(`{}`), Source: source{Producer: "producer", Schema: "schema"}}
	if err := denied(frame, "denied"); err == nil {
		t.Fatal("missing-capability success bypassed the expected denial gate")
	}
}
