package providerv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadinessFixtureAndSchemaAreValid(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "readiness.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(fixture, 16*1024); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	schema, err := os.ReadFile("readiness.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(schema, &document); err != nil {
		t.Fatalf("schema JSON: %v", err)
	}
	if document["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected schema version: %v", document["$schema"])
	}
}

func TestDecodeConfigRejectsUnknownFieldsTrailingDataAndRelativePaths(t *testing.T) {
	for _, raw := range []string{
		`{"schema_version":1,"token":"do-not-accept"}`,
		`{"schema_version":1} {}`,
		`{"schema_version":1,"codex_path":"relative/codex"}`,
		`{"schema_version":2}`,
	} {
		if _, err := DecodeConfig([]byte(raw), 4096); err == nil {
			t.Errorf("accepted invalid config %s", raw)
		}
	}
	if _, err := DecodeConfig([]byte(`{"schema_version":1}`), 0); err == nil {
		t.Fatal("accepted non-positive input limit")
	}
	if _, err := DecodeConfig([]byte(`{"schema_version":1,"antigravity_path":"/tmp/agy"}`), 4096); err != nil {
		t.Fatalf("valid explicit selection: %v", err)
	}
}

func TestDecodeReportRejectsUnknownFieldsAndProviderDuplicates(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "readiness.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport([]byte(strings.Replace(string(fixture), `"schema_version": 1`, `"schema_version": 1, "token": "secret"`, 1)), 16*1024); err == nil {
		t.Fatal("accepted unknown field")
	}
	duplicate := strings.Replace(string(fixture), `"provider": "antigravity"`, `"provider": "claude-code"`, 1)
	if _, err := DecodeReport([]byte(duplicate), 16*1024); err == nil {
		t.Fatal("accepted duplicate/missing provider")
	}
	if _, err := DecodeReport(append(fixture, []byte(` {}`)...), 16*1024); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}
