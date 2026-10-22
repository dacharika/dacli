package native

import (
	"encoding/json"
	"github.com/mlnomadpy/dacli/internal/application"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func foundationFixtures() map[string]any {
	worker := (&Server{}).catalog()
	runner := (&Server{verification: &application.NativeVerificationJobs{}}).catalog()
	reviewer := (&Server{review: &application.NativeReviewProfile{}}).catalog()
	inputs := map[string]any{}
	outputsByName := map[string]any{}
	for name, arg := range arguments {
		inputs[name] = schema(reflect.TypeOf(arg))
		outputsByName[name] = outputSchema(name)
	}
	return map[string]any{"worker_catalog.json": map[string]any{"tools": worker}, "runner_catalog.json": map[string]any{"tools": runner}, "reviewer_catalog.json": map[string]any{"tools": reviewer}, "input_schemas.json": inputs, "output_schemas.json": outputsByName}
}
func TestNativeFoundationSchemasMatchFixedWireFixtures(t *testing.T) {
	// Explicit reviewer-only contract generation precedes every behavioral
	// assertion. Default execution continues to enforce all fixed schemas.
	if os.Getenv("DACLI_WRITE_REVIEWER_CATALOG") == "1" {
		raw, err := json.MarshalIndent(foundationFixtures()["reviewer_catalog.json"], "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "reviewer_catalog.json"), append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		t.Log("reviewer contract generated; behavioral verification not executed")
		return
	}
	if os.Getenv("DACLI_WRITE_NATIVE_SCHEMAS") == "1" {
		for filename, value := range foundationFixtures() {
			raw, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join("testdata", filename), append(raw, '\n'), 0644); err != nil {
				t.Fatal(err)
			}
		}
		t.Log("native schema fixtures generated; behavioral verification not executed")
		return
	}
	for filename, current := range foundationFixtures() {
		raw, e := os.ReadFile(filepath.Join("testdata", filename))
		if e != nil {
			t.Fatal(e)
		}
		var want, got any
		if e = json.Unmarshal(raw, &want); e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(current)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(encoded, &got); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("published native schema changed without reviewed fixture: %s", filename)
		}
	}
}
func TestNativeFoundationCatalogRolesAndRefusalSchemas(t *testing.T) {
	counts := map[string]int{"worker_catalog.json": 13, "runner_catalog.json": 16, "reviewer_catalog.json": 7}
	fixtures := foundationFixtures()
	for filename, want := range counts {
		list := fixtures[filename].(map[string]any)["tools"].([]tool)
		if len(list) != want {
			t.Fatalf("role catalog count changed %s: %d", filename, len(list))
		}
		policyFound := false
		for _, item := range list {
			if item.Name == "get_policy_status" {
				policyFound = true
			}
			switch item.Name {
			case "cli", "shell", "spawn", "enroll", "bootstrap", "grant", "accept", "evaluate_policy", "approve_policy", "consume_policy", "configure_policy":
				t.Fatalf("owner or unrestricted tool exposed: %s", item.Name)
			}
			response := result(item.Name, nil, ErrDenied).(map[string]any)
			structured, _ := json.Marshal(response["structuredContent"])
			fallback := response["content"].([]any)[0].(map[string]any)["text"].(string)
			if string(structured) != fallback || !validJSON(item.OutputSchema, structured) {
				t.Fatalf("role refusal wire parity failed: %s", item.Name)
			}
		}
		if !policyFound {
			t.Fatalf("typed policy observation absent: %s", filename)
		}
	}
}
