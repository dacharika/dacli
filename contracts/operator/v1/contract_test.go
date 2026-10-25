package operatorv1

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	Case     string          `json:"case"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Case == "" || len(f.Request) == 0 || len(f.Response) == 0 {
		t.Fatalf("%s is not a complete fixture", name)
	}
	return f
}

func errorCode(t *testing.T, err error) Code {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("got %v, want ValidationError", err)
	}
	return validation.Code
}

func TestGoldenFixtures(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 8 {
		t.Fatalf("got %d fixtures, want eight", len(paths))
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(name, func(t *testing.T) {
			f := loadFixture(t, name)
			req, reqErr := DecodeRequest(f.Request, 64*1024)
			wantDenied := strings.HasSuffix(name, "denied")
			if name == "unsupported-version" {
				if got := errorCode(t, reqErr); got != CodeUnsupportedVersion {
					t.Fatalf("request error = %q", got)
				}
				failure, err := DecodeFailure(f.Response, 1024*1024)
				if err != nil {
					t.Fatal(err)
				}
				if failure.Error.Code != CodeUnsupportedVersion {
					t.Fatalf("error code = %q", failure.Error.Code)
				}
				return
			}
			if reqErr != nil {
				t.Fatalf("decode request: %v", reqErr)
			}
			if wantDenied {
				got, err := DecodeFailure(f.Response, 1024*1024)
				if err != nil {
					t.Fatal(err)
				}
				if got.Error.Code != CodeDenied {
					t.Fatalf("failure code = %q, want denied", got.Error.Code)
				}
				if req.TaskID != "" && bytes.Contains(f.Response, []byte(req.TaskID)) {
					t.Fatal("denied response disclosed task identity")
				}
				if bytes.Contains(f.Response, []byte("ws-foreign-02")) {
					t.Fatal("denied response disclosed foreign workspace")
				}
				return
			}
			got, err := DecodeSuccess(f.Response, 1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			if got.RequestID != req.RequestID {
				t.Fatalf("request id %q != %q", got.RequestID, req.RequestID)
			}
			if got.Source.Producer == "" || got.Source.Schema == "" || got.ObservedAt == "" {
				t.Fatal("success fixture lacks source or observation identity")
			}
			if req.Operation == "attempts.timeline" && !(bytes.Contains(got.Data, []byte(`"freshness"`)) && bytes.Contains(got.Data, []byte(`"stale"`))) {
				t.Fatal("timeline fixture promoted stale evidence")
			}
			var roundTrip bytes.Buffer
			enc := json.NewEncoder(&roundTrip)
			if err := enc.Encode(got); err != nil {
				t.Fatal(err)
			}
			var wantValue, gotValue any
			if err := json.Unmarshal(f.Response, &wantValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(roundTrip.Bytes(), &gotValue); err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(wantValue)
			gotJSON, _ := json.Marshal(gotValue)
			if !bytes.Equal(wantJSON, gotJSON) {
				t.Fatalf("response semantic round-trip changed:\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestRejectUnknownVersionFieldsAndTrailingJSON(t *testing.T) {
	base := loadFixture(t, "tasks-list-success").Request
	for _, tc := range []struct {
		name string
		raw  []byte
		want Code
	}{
		{"future-version", bytes.Replace(base, []byte(`"schema_version": 1`), []byte(`"schema_version": 999`), 1), CodeUnsupportedVersion},
		{"unknown-request-field", bytes.Replace(base, []byte(`"limit": 100`), []byte(`"limit": 100, "surprise": true`), 1), CodeUnknownField},
		{"unknown-nested-filter-field", bytes.Replace(base, []byte(`"limit": 100`), []byte(`"filters": {"unexpected": true}, "limit": 100`), 1), CodeUnknownField},
		{"trailing-document", append(append([]byte(nil), base...), base...), CodeInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeRequest(tc.raw, 64*1024)
			if got := errorCode(t, err); got != tc.want {
				t.Fatalf("error code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRejectUnknownResponseFieldsAndTrailingJSON(t *testing.T) {
	base := loadFixture(t, "tasks-list-success").Response
	cases := []struct {
		name string
		raw  []byte
	}{
		{"unknown-top-level", bytes.Replace(base, []byte(`"ok": true`), []byte(`"ok": true, "surprise": true`), 1)},
		{"unknown-nested-scope", bytes.Replace(base, []byte(`"project_id": "operator"`), []byte(`"project_id": "operator", "surprise": true`), 1)},
		{"unknown-nested-source", bytes.Replace(base, []byte(`"schema": "operator-task-row/v1"`), []byte(`"schema": "operator-task-row/v1", "surprise": true`), 1)},
		{"trailing-document", append(append([]byte(nil), base...), base...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeSuccess(tc.raw, 1024*1024)
			if got := errorCode(t, err); got != CodeUnknownField && tc.name != "trailing-document" {
				t.Fatalf("error code = %q", got)
			}
			if tc.name == "trailing-document" && err == nil {
				t.Fatal("accepted trailing JSON document")
			}
		})
	}
}

func TestRequestLimitsAndRequiredScope(t *testing.T) {
	base := loadFixture(t, "tasks-list-success").Request
	cases := []struct {
		name          string
		replace, with []byte
		want          Code
	}{
		{"zero-limit", []byte(`"limit": 100`), []byte(`"limit": 0`), CodeLimitExceeded},
		{"negative-limit", []byte(`"limit": 100`), []byte(`"limit": -1`), CodeLimitExceeded},
		{"over-limit", []byte(`"limit": 100`), []byte(`"limit": 501`), CodeLimitExceeded},
		{"missing-project", []byte(`, "project_id": "operator"`), nil, CodeInvalidRequest},
		{"timeline-missing-task", []byte(`"operation": "tasks.list"`), []byte(`"operation": "attempts.timeline"`), CodeInvalidRequest},
		{"invalid-project-id", []byte(`"project_id": "operator"`), []byte(`"project_id": "../foreign"`), CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Replace(base, tc.replace, tc.with, 1)
			_, err := DecodeRequest(raw, 64*1024)
			if got := errorCode(t, err); got != tc.want {
				t.Fatalf("error code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedactedFixtureContainsNoSensitiveFields(t *testing.T) {
	f := loadFixture(t, "redacted-governance")
	for _, forbidden := range []string{"credential_hash", "bearer_token", "command", "authority_path", "profile_path", "prompt", "transcript", "source_path", "private_body"} {
		if bytes.Contains(f.Response, []byte(`"`+forbidden+`"`)) {
			t.Errorf("response includes forbidden field %q", forbidden)
		}
	}
	if bytes.Contains(f.Response, []byte("secret-value")) || bytes.Contains(f.Response, []byte("/Users/")) {
		t.Fatal("response includes a secret or absolute local path")
	}
}
