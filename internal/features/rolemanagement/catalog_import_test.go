package rolemanagement

import (
	"strings"
	"testing"
)

func TestCatalogExportImportFlagIsExclusiveToCatalogImport(t *testing.T) {
	valid, err := parseFlags([]string{"--config", "owner.json", "--project", "core", "--catalog-export", "export.json"}, "config", "project", "catalog-export", "release", "lifecycle", "role", "version", "digest", "expected-generation", "expected-version", "expected-digest", "reason", "json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateAction(valid, "import"); err != nil {
		t.Fatalf("exclusive catalog export import rejected: %v", err)
	}
	for _, pair := range [][]string{{"--release", "release.json"}, {"--lifecycle", "status.json"}, {"--role", "builder"}} {
		args := []string{"--config", "owner.json", "--project", "core", "--catalog-export", "export.json"}
		args = append(args, pair...)
		flags, err := parseFlags(args, "config", "project", "catalog-export", "release", "lifecycle", "role", "version", "digest", "expected-generation", "expected-version", "expected-digest", "reason", "json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := validateAction(flags, "import"); err == nil || !strings.Contains(err.Error(), "--catalog-export is exclusive") {
			t.Fatalf("catalog export coexisting with %s was not a usage refusal: %v", pair[0], err)
		}
	}
	if _, err := validateAction(valid, "verify"); err == nil || !strings.Contains(err.Error(), "does not accept --catalog-export") {
		t.Fatalf("non-import operation accepted catalog export: %v", err)
	}
}
