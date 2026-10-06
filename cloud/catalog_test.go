package cloud_test

import (
	"os"
	"testing"

	"github.com/mlnomadpy/dacli/cloud/internal/envelopeworker"
	"github.com/mlnomadpy/dacli/cloud/internal/migrations"
	"github.com/mlnomadpy/dacli/cloud/internal/tenantrepo"
)

func TestShippedMigrationCatalogIsValid(t *testing.T) {
	catalog, err := migrations.Load(os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 11 {
		t.Fatalf("catalog = %+v", catalog)
	}
	for i, migration := range catalog {
		if migration.Version != i+1 {
			t.Fatalf("migration %d has version %d, want %d", i, migration.Version, i+1)
		}
	}
	if got := catalog[len(catalog)-1].Version; got != tenantrepo.SchemaVersion {
		t.Fatalf("catalog=%d tenantrepo=%d", got, tenantrepo.SchemaVersion)
	}
	if got := catalog[len(catalog)-1].Version; got != envelopeworker.SchemaVersion {
		t.Fatalf("catalog=%d envelopeworker=%d", got, envelopeworker.SchemaVersion)
	}
}
