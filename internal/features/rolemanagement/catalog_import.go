package rolemanagement

import (
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
)

// RunCatalogImport is the explicit private-owner path for a hosted exact-role
// export. It only caches verified signed data and never enrolls trust or selects.
func RunCatalogImport(ctx *clikit.Ctx, owner *application.NativeRoleRegistryOwner, path string, now time.Time) error {
	if ctx == nil || owner == nil || path == "" {
		return denied()
	}
	resolved, err := resolveImportPath(ctx.Cwd, path)
	if err != nil {
		return denied()
	}
	export, err := application.ReadNativeRoleRegistryCatalogExport(resolved)
	if err != nil {
		return denied()
	}
	receipt, err := owner.ImportCatalogExport(export, now)
	if err != nil {
		return denied()
	}
	return emit(ctx, "import", receipt)
}
