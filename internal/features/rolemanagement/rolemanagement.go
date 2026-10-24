package rolemanagement

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
)

// Commands exposes explicit local role release management. The app layer
// registers these handlers and excludes them from legacy MCP execution.
func Commands() []clikit.Command {
	return []clikit.Command{
		{Path: "role registry import", Brief: "Cache one explicit signed role release and lifecycle file", Mutates: true, JSON: true, Usage: "dacli role registry import --config <private-owner> --project <slug> --release <file> --lifecycle <file> [--json]", Run: func(c *clikit.Ctx, a []string) error { return run(c, a, "import") }},
		{Path: "role registry verify", Brief: "Verify one exact cached signed role release", JSON: true, Usage: "dacli role registry verify --config <private-owner> --project <slug> --role <id> --version <semver> --digest <sha256:...> [--json]", Run: func(c *clikit.Ctx, a []string) error { return run(c, a, "verify") }},
		{Path: "role registry pin", Brief: "Pin one verified exact role release when no pin exists", Mutates: true, JSON: true, Usage: "dacli role registry pin --config <private-owner> --project <slug> --role <id> --version <semver> --digest <sha256:...> --expected-generation <n> --reason <text> [--json]", Run: func(c *clikit.Ctx, a []string) error { return run(c, a, "pin") }},
		{Path: "role registry diff", Brief: "Compare one exact release with current selection and canonical role", JSON: true, Usage: "dacli role registry diff --config <private-owner> --project <slug> --role <id> --version <semver> --digest <sha256:...> [--json]", Run: func(c *clikit.Ctx, a []string) error { return run(c, a, "diff") }},
		{Path: "role registry upgrade", Brief: "Explicitly select a newer verified exact cached role release", Mutates: true, JSON: true, Usage: "dacli role registry upgrade --config <private-owner> --project <slug> --role <id> --version <semver> --digest <sha256:...> --expected-generation <n> --expected-version <semver> --expected-digest <sha256:...> --reason <text> [--json]", Run: func(c *clikit.Ctx, a []string) error { return run(c, a, "upgrade") }},
		{Path: "role registry rollback", Brief: "Explicitly restore a real prior verified role selection", Mutates: true, JSON: true, Usage: "dacli role registry rollback --config <private-owner> --project <slug> --role <id> --version <semver> --digest <sha256:...> --expected-generation <n> --expected-version <semver> --expected-digest <sha256:...> --reason <text> [--json]", Run: func(c *clikit.Ctx, a []string) error { return run(c, a, "rollback") }},
	}
}

func AuthenticatesExplicitly(path string) bool {
	switch path {
	case "role registry import", "role registry verify", "role registry pin", "role registry diff", "role registry upgrade", "role registry rollback":
		return true
	}
	return false
}

func parseFlags(args []string, allowed ...string) (*clikit.Flags, error) {
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return nil, err
	}
	if err := f.Reject(allowed...); err != nil {
		return nil, err
	}
	if len(f.Pos) != 0 {
		return nil, clikit.Usagef("unexpected positional arguments")
	}
	for _, name := range allowed {
		if len(f.All(name)) > 1 {
			return nil, clikit.Usagef("flag --%s must appear once", name)
		}
	}
	return f, nil
}

func denied() error {
	return clikit.Refusedf("explicit private owner role registry authority unavailable or refused")
}

func run(ctx *clikit.Ctx, args []string, action string) error {
	allowed := []string{"config", "project", "role", "version", "digest", "release", "lifecycle", "expected-generation", "expected-version", "expected-digest", "reason", "json"}
	f, err := parseFlags(args, allowed...)
	if err != nil {
		return err
	}
	for _, name := range []string{"config", "project"} {
		if f.Get(name) == "" {
			return clikit.Usagef("--%s is required", name)
		}
	}
	generation, err := validateAction(f, action)
	if err != nil {
		return err
	}
	owner, err := application.OpenNativeRoleRegistryOwner(f.Get("config"), f.Get("project"))
	if err != nil {
		return denied()
	}
	now := time.Now().UTC()
	switch action {
	case "import":
		var release rolebundle.Release
		var lifecycle rolebundle.LifecycleStatus
		releasePath, releasePathErr := resolveImportPath(ctx.Cwd, f.Get("release"))
		lifecyclePath, lifecyclePathErr := resolveImportPath(ctx.Cwd, f.Get("lifecycle"))
		if releasePathErr != nil || lifecyclePathErr != nil || store.ReadRoleRegistryImport(releasePath, &release) != nil || store.ReadRoleRegistryImport(lifecyclePath, &lifecycle) != nil {
			return denied()
		}
		receipt, err := owner.Import(release, lifecycle, now)
		if err != nil {
			return denied()
		}
		return emit(ctx, action, receipt)
	case "verify", "diff":
		if action == "diff" {
			diff, err := owner.Diff(f.Get("role"), f.Get("version"), f.Get("digest"), now)
			if err != nil {
				return denied()
			}
			return emit(ctx, action, diff)
		}
		view, err := owner.Verify(f.Get("role"), f.Get("version"), f.Get("digest"), now)
		if err != nil {
			return denied()
		}
		return emit(ctx, action, view)
	case "pin", "upgrade", "rollback":
		expectedVersion, expectedDigest := f.Get("expected-version"), f.Get("expected-digest")
		pins, err := owner.Select(f.Get("role"), f.Get("version"), f.Get("digest"), generation, expectedVersion, expectedDigest, f.Get("reason"), action, now)
		if err != nil {
			return denied()
		}
		return emit(ctx, action, pins)
	default:
		return clikit.Usagef("unknown role registry operation")
	}
}

func validateAction(f *clikit.Flags, action string) (uint64, error) {
	var generation uint64
	switch action {
	case "import":
		if f.Get("release") == "" || f.Get("lifecycle") == "" || f.Get("role") != "" || f.Get("version") != "" || f.Get("digest") != "" || f.Get("expected-generation") != "" || f.Get("expected-version") != "" || f.Get("expected-digest") != "" || f.Get("reason") != "" {
			return 0, clikit.Usagef("import requires --release and --lifecycle only")
		}
	case "verify", "diff":
		if err := requireTarget(f); err != nil {
			return 0, err
		}
		if f.Get("release") != "" || f.Get("lifecycle") != "" || f.Get("expected-generation") != "" || f.Get("expected-version") != "" || f.Get("expected-digest") != "" || f.Get("reason") != "" {
			return 0, clikit.Usagef("%s accepts only exact target fields", action)
		}
	case "pin", "upgrade", "rollback":
		if err := requireTarget(f); err != nil {
			return 0, err
		}
		parsedGeneration, err := strconv.ParseUint(f.Get("expected-generation"), 10, 64)
		if err != nil || f.Get("expected-generation") == "" {
			return 0, clikit.Usagef("--expected-generation must be a non-negative integer")
		}
		generation = parsedGeneration
		if strings.TrimSpace(f.Get("reason")) == "" || len(f.Get("reason")) > 2048 {
			return 0, clikit.Usagef("--reason is required and must be at most 2048 bytes")
		}
		expectedVersion, expectedDigest := f.Get("expected-version"), f.Get("expected-digest")
		if action == "pin" {
			if expectedVersion != "" || expectedDigest != "" || f.Get("release") != "" || f.Get("lifecycle") != "" {
				return 0, clikit.Usagef("pin accepts only --expected-generation and requires no current pin")
			}
		} else if expectedVersion == "" || expectedDigest == "" || f.Get("release") != "" || f.Get("lifecycle") != "" {
			return 0, clikit.Usagef("%s requires expected current version and digest", action)
		}
		if action == "upgrade" && !store.RoleVersionGreater(f.Get("version"), expectedVersion) {
			return 0, clikit.Usagef("upgrade target must have a greater SemVer than the expected current version")
		}
	default:
		return 0, clikit.Usagef("unknown role registry operation")
	}
	return generation, nil
}

func resolveImportPath(cwd, path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(cwd) {
		var err error
		cwd, err = filepath.Abs(cwd)
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(cwd, path), nil
}

func requireTarget(f *clikit.Flags) error {
	for _, name := range []string{"role", "version", "digest"} {
		if f.Get(name) == "" {
			return clikit.Usagef("--%s is required", name)
		}
	}
	return nil
}

func emit(ctx *clikit.Ctx, action string, value any) error {
	ctx.Result = value
	if ctx.JSON {
		enc := json.NewEncoder(ctx.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(value)
	}
	switch v := value.(type) {
	case application.NativeRoleRegistryView:
		fmt.Fprintf(ctx.Stdout, "Role registry %s: %s %s (%s)\n", action, v.Target.Release.Content.Role.Name, v.Target.Release.Content.Version, v.Target.Release.ContentDigest)
		fmt.Fprintf(ctx.Stdout, "Publisher: %s; lifecycle generation: %d; project generation: %d; canonical role matches: %t; authority schema: %d; pin operations supported: %t\n", v.Target.Release.PublisherKeyID, v.Target.Lifecycle.Generation, v.ProjectGeneration, v.CanonicalRoleMatches, v.AuthorityVersion, v.SelectionSupported)
		fmt.Fprintln(ctx.Stdout, v.SelectionNote)
		if v.CurrentPin != nil {
			fmt.Fprintf(ctx.Stdout, "Current selection: %s %s (%s)\n", v.CurrentPin.RoleID, v.CurrentPin.Version, v.CurrentPin.Digest)
		} else {
			fmt.Fprintln(ctx.Stdout, "Current selection: none")
		}
	case application.NativeRoleRegistryDiff:
		fmt.Fprintf(ctx.Stdout, "Role registry diff for %s: %s %s (%s)\n", v.Target.Release.Content.Role.Name, v.Target.Release.Content.Version, v.Target.Release.ContentDigest, v.Target.Release.PublisherKeyID)
		fmt.Fprintf(ctx.Stdout, "Current lifecycle: %s; canonical role matches target: %t\n", v.CurrentLifecycleObservation, v.CanonicalRoleMatches)
		if v.CurrentPin == nil {
			fmt.Fprintln(ctx.Stdout, "Current selection: none")
		} else if v.CurrentReleaseAvailable {
			fmt.Fprintf(ctx.Stdout, "Current selection: %s %s (%s)\n", v.CurrentPin.RoleID, v.CurrentPin.Version, v.CurrentPin.Digest)
		}
		for _, change := range v.Changes {
			fmt.Fprintf(ctx.Stdout, "%s: %s -> %s\n", change.Field, change.Current, change.Target)
		}
		if !v.CanonicalRoleMatches {
			fmt.Fprintln(ctx.Stdout, "Target differs from the canonical team.Role; pin operations will refuse it.")
		}
	case store.ProjectRolePins:
		fmt.Fprintf(ctx.Stdout, "Role registry %s recorded at project generation %d.\n", action, v.Generation)
	case application.NativeRoleRegistryImportReceipt:
		fmt.Fprintf(ctx.Stdout, "Imported signed %s %s (%s), lifecycle %s generation %d; project generation %d; selected: %t.\n", v.RoleID, v.Version, v.ContentDigest, v.LifecycleState, v.LifecycleGeneration, v.ProjectGeneration, v.Selected)
		fmt.Fprintln(ctx.Stdout, v.ActivationNote)
	default:
		fmt.Fprintln(ctx.Stdout, "Role registry operation complete.")
	}
	return nil
}
