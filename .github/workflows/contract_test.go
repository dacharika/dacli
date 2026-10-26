package workflows_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var requiredGates = []string{
	"test-matrix",
	"lint",
	"clean-checkout",
	"release-snapshot",
	"cross-compile",
}

func TestRequiredTestCheckGatesEveryCIJob(t *testing.T) {
	workflow := readWorkflow(t)
	testJob := jobBlock(t, workflow, "test")

	needs := needsList(t, testJob)
	sort.Strings(needs)
	wantNeeds := append([]string(nil), requiredGates...)
	sort.Strings(wantNeeds)
	if strings.Join(needs, ",") != strings.Join(wantNeeds, ",") {
		t.Fatalf("test.needs = %v, want %v", needs, wantNeeds)
	}

	for _, gate := range requiredGates {
		assertion := `test "${{ needs.` + gate + `.result }}" = "success"`
		if !strings.Contains(testJob, assertion) {
			t.Errorf("test job does not require %s to equal success", gate)
		}
	}

	for _, gate := range requiredGates {
		for _, result := range []string{"failure", "cancelled"} {
			results := successfulResults()
			results[gate] = result
			if requiredCheckSucceeds(testJob, results) {
				t.Errorf("required test check succeeds when %s is %s", gate, result)
			}
		}
	}
}

func TestLintUsesPatchedGo126Toolchain(t *testing.T) {
	workflow := readWorkflow(t)
	lintJob := jobBlock(t, workflow, "lint")

	if !regexp.MustCompile(`(?m)^          go-version: "1\.26\.([1-9][0-9]+)"$`).MatchString(lintJob) {
		t.Fatal("lint/security job must use patched Go 1.26.10 or newer within the 1.26 line")
	}
}

func TestGovulncheckIsPinnedToCompatibleToolchain(t *testing.T) {
	lintJob := jobBlock(t, readWorkflow(t), "lint")
	if strings.Contains(lintJob, "govulncheck@latest") {
		t.Fatal("govulncheck must not float independently of the pinned lint toolchain")
	}
	if !strings.Contains(lintJob, "golang.org/x/vuln/cmd/govulncheck@v1.8.0") {
		t.Fatal("Go 1.26 lint/security job must install reviewed govulncheck v1.8.0")
	}
}

// Validation and docs deployment are opt-in; tagged releases remain explicit.
// Read the event mapping so a new automatic event cannot bypass a blacklist.
func TestValidationWorkflowsAreManualOnly(t *testing.T) {
	for _, name := range []string{"ci.yml", "quality.yml", "dashboard-evidence.yml", "docs.yml"} {
		t.Run(name, func(t *testing.T) {
			workflow := readNamedWorkflow(t, name)
			lines := strings.Split(workflow, "\n")
			inEvents, found, manual := false, false, false
			for _, line := range lines {
				if !inEvents {
					if line == "on:" {
						inEvents, found = true, true
					}
					continue
				}
				if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				if !strings.HasPrefix(line, " ") {
					break
				}
				if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
					match := regexp.MustCompile(`^  ([a-z_]+):\s*(?:#.*)?$`).FindStringSubmatch(line)
					if len(match) != 2 {
						t.Fatalf("validation trigger must use an explicit event mapping: %q", line)
					}
					switch match[1] {
					case "workflow_dispatch":
						manual = true
					case "workflow_call":
						// Reusable APIs may be invoked by an explicitly selected caller.
					default:
						t.Fatalf("validation workflow must be manual-only; found event %q", match[1])
					}
				}
			}
			if !found || !manual {
				t.Fatal("validation workflow must retain an explicit workflow_dispatch mapping")
			}
		})
	}
}

func TestCIIsLinuxOnlyAndManuallyTriggered(t *testing.T) {
	workflow := readWorkflow(t)
	testJob := jobBlock(t, workflow, "test-matrix")

	if regexp.MustCompile(`(?m)^\s*runs-on:\s*macos`).MatchString(workflow) {
		t.Fatal("routine ci must not run a native macOS job")
	}
	if strings.Contains(testJob, "strategy:") || strings.Contains(testJob, "matrix.") {
		t.Fatal("single-platform routine ci must not retain a one-entry matrix")
	}
	if !strings.Contains(testJob, "runs-on: ubuntu-latest") {
		t.Fatal("routine test job must run on ubuntu-latest")
	}
	if !strings.Contains(workflow, "  workflow_dispatch:\n") {
		t.Fatal("CI must retain explicit workflow_dispatch validation")
	}
	if !strings.Contains(workflow, "group: ci-${{ github.event.pull_request.number || github.ref }}") ||
		!strings.Contains(workflow, "cancel-in-progress: true") {
		t.Fatal("CI must retain its existing explicit-run concurrency contract")
	}
}

func TestRoutineCIEnforcesDashboardCoverage(t *testing.T) {
	testJob := jobBlock(t, readWorkflow(t), "test-matrix")
	if !strings.Contains(testJob, "run: npm run test:coverage") {
		t.Fatal("routine CI must run the dashboard coverage command, not only unmeasured unit tests")
	}
}

func TestFuzzCampaignsRunOnlyInQualityWorkflow(t *testing.T) {
	routine := readWorkflow(t)
	quality := readNamedWorkflow(t, "quality.yml")

	for _, target := range fuzzTargets {
		if strings.Contains(routine, target) {
			t.Errorf("routine ci must not run fuzz target %q", target)
		}
		if !strings.Contains(quality, target) {
			t.Errorf("quality workflow is missing fuzz target %q", target)
		}
	}
	if !strings.Contains(quality, "  workflow_dispatch:\n") {
		t.Fatal("quality workflow must retain explicitly requested fuzz campaigns")
	}
}

func TestDashboardEvidenceCaptureIsManualAndLinuxOnly(t *testing.T) {
	workflow := readNamedWorkflow(t, "dashboard-evidence.yml")

	if !strings.Contains(workflow, "  workflow_dispatch:\n") {
		t.Fatal("dashboard evidence capture must remain manually dispatched")
	}
	if strings.Contains(workflow, "  pull_request:\n") || strings.Contains(workflow, "  push:\n") || strings.Contains(workflow, "  schedule:\n") {
		t.Fatal("dashboard evidence capture must not charge routine CI or scheduled runs")
	}
	if !strings.Contains(workflow, "runs-on: ubuntu-latest") {
		t.Fatal("dashboard evidence capture must use the Linux runner")
	}
	for _, size := range []string{"--window-size=1280,2600", "--window-size=390,844"} {
		if !strings.Contains(workflow, size) {
			t.Errorf("dashboard evidence capture is missing %q", size)
		}
	}
}

func TestCrossCompileUsesOneJobForAllReleaseTargets(t *testing.T) {
	workflow := readWorkflow(t)
	crossCompile := jobBlock(t, workflow, "cross-compile")

	if strings.Contains(crossCompile, "matrix:") || strings.Contains(crossCompile, "matrix.") {
		t.Fatal("cross-compile must not use a matrix")
	}
	for _, target := range crossCompileTargets {
		if !strings.Contains(crossCompile, target) {
			t.Errorf("cross-compile is missing target %q", target)
		}
	}
}

var fuzzTargets = []string{
	"FuzzParseFlags",
	"FuzzValueSpellingsAgree",
	"FuzzFrontMatterRoundTrip",
	"FuzzParseNeverPanics",
	"FuzzSafeSegmentNeverEscapes",
	"FuzzSafeRelPathNeverEscapes",
}

var crossCompileTargets = []string{
	"windows/amd64",
	"windows/arm64",
	"darwin/amd64",
	"darwin/arm64",
	"linux/amd64",
	"linux/arm64",
}

func TestReleaseRetainsNarrowNativeMacOSValidation(t *testing.T) {
	workflow := readNamedWorkflow(t, "release.yml")
	macOSJob := jobBlock(t, workflow, "macos-native")

	if !strings.Contains(macOSJob, "runs-on: macos-latest") {
		t.Fatal("release workflow must retain a native macOS validation job")
	}
	if !strings.Contains(macOSJob, "go test ./internal/procmon/ ./internal/governance/sessions/ ./internal/mcp/native/") {
		t.Fatal("native macOS validation must exercise native authority and process-observation packages")
	}
	if !strings.Contains(jobBlock(t, workflow, "goreleaser"), "needs: [validate-release-ref, macos-native]") {
		t.Fatal("release publication must wait for native macOS validation")
	}
	if !strings.Contains(jobBlock(t, workflow, "goreleaser"), "runs-on: macos-latest") {
		t.Fatal("native release packaging must run on macOS for Security.framework linking")
	}
}

func TestTagReleaseIsManualAndPortableSnapshotCannotPublish(t *testing.T) {
	workflow := readNamedWorkflow(t, "release.yml")
	on := yamlSection(workflow, "on")
	if !strings.Contains(on, "  workflow_dispatch:\n") {
		t.Fatal("release workflow must be manually dispatched")
	}
	for _, forbidden := range []string{"\n  push:", "\n  pull_request:", "\n  schedule:"} {
		if strings.Contains(on, forbidden) {
			t.Fatalf("release workflow must not run automatically; found trigger %q", strings.TrimSpace(forbidden))
		}
	}
	events := regexp.MustCompile(`(?m)^  ([a-z_]+):`).FindAllStringSubmatch(on, -1)
	if len(events) != 1 || events[0][1] != "workflow_dispatch" {
		t.Fatalf("release workflow events = %v, want only workflow_dispatch", events)
	}
	for _, input := range []string{"tag", "expected_commit", "confirm_tag"} {
		block := regexp.MustCompile(`(?m)^      ` + regexp.QuoteMeta(input) + `:\n((?:        [^\n]*\n)+)`).FindStringSubmatch(on)
		if block == nil || !strings.Contains(block[1], "        required: true\n") {
			t.Errorf("manual release must require the %s input", strings.TrimSuffix(input, ":"))
		}
	}

	validation := jobBlock(t, workflow, "validate-release-ref")
	trustedCheckout := strings.Index(validation, "ref: ${{ github.workflow_sha }}")
	validationCall := strings.Index(validation, "scripts/release_ref.py")
	if trustedCheckout < 0 || validationCall < 0 || trustedCheckout > validationCall {
		t.Fatal("release validation must check out the trusted workflow revision before running its resolver")
	}
	if !strings.Contains(validation, "github.workflow_ref") || !strings.Contains(validation, "default_branch") {
		t.Fatal("release validation must require the trusted default-branch workflow")
	}
	if strings.Contains(validation, "ref: ${{ inputs.expected_commit }}") || strings.Contains(validation, "ref: ${{ github.sha }}") {
		t.Fatal("release validation must not check out the unvalidated dispatch SHA or input")
	}
	if !strings.Contains(validation, "EXPECTED_COMMIT: ${{ inputs.expected_commit }}") || !strings.Contains(validation, "CONFIRM_TAG: ${{ inputs.confirm_tag }}") {
		t.Fatal("release validation must bind the explicit commit and typed publication confirmation")
	}
	if !strings.Contains(jobBlock(t, workflow, "validate-release-ref"), "tag_object: ${{ steps.validate.outputs.tag_object }}") {
		t.Fatal("release validation must export the exact tag ref object SHA for move detection")
	}
	for _, name := range []string{"macos-native", "goreleaser"} {
		job := jobBlock(t, workflow, name)
		trusted := strings.Index(job, "ref: ${{ github.workflow_sha }}")
		resolver := strings.Index(job, `python3 "$RUNNER_TEMP/release_ref.py"`)
		target := strings.Index(job, "ref: ${{ needs.validate-release-ref.outputs.commit }}")
		if trusted < 0 || resolver < 0 || target < 0 || trusted > resolver || resolver > target {
			t.Errorf("%s must rerun the trusted resolver before checking out the pinned release commit", name)
		}
		if !strings.Contains(job, `--expected-tag-object "$EXPECTED_TAG_OBJECT"`) {
			t.Errorf("%s must reject replacement of the selected tag ref object", name)
		}
	}

	if !strings.Contains(jobBlock(t, workflow, "goreleaser"), "GORELEASER_CURRENT_TAG: ${{ needs.validate-release-ref.outputs.tag }}") {
		t.Fatal("GoReleaser must publish the exact validated tag, independent of the dispatch ref")
	}
	if !strings.Contains(jobBlock(t, workflow, "goreleaser"), `python3 "$RUNNER_TEMP/release_ref.py"`) {
		t.Fatal("release publication must recheck the tag immediately before GoReleaser")
	}
	if !strings.Contains(workflow, "permissions:\n  contents: read") || !strings.Contains(jobBlock(t, workflow, "goreleaser"), "contents: write") {
		t.Fatal("only the publication job may elevate contents permission to write")
	}
	for _, name := range []string{"validate-release-ref", "macos-native"} {
		if strings.Contains(jobBlock(t, workflow, name), "contents: write") {
			t.Errorf("%s must remain read-only", name)
		}
	}
	if strings.Contains(workflow, "git tag") || strings.Contains(workflow, "gh release create") {
		t.Fatal("manual publication must not create tags or bypass GoReleaser")
	}
	ci := readNamedWorkflow(t, "ci.yml")
	if !strings.Contains(jobBlock(t, ci, "release-snapshot"), "./scripts/verify-release-artifacts.sh --portable-snapshot") {
		t.Fatal("manual Ubuntu validation must select the explicit portable snapshot profile")
	}
	portable := readNamedFile(t, ".goreleaser.portable-snapshot.yaml")
	if !strings.Contains(yamlSection(portable, "release"), "disable: true") {
		t.Fatal("portable snapshot config must disable release publication")
	}
}

func TestPortableAndNativeArtifactConfigsStayEquivalent(t *testing.T) {
	native := readNamedFile(t, ".goreleaser.yaml")
	portable := readNamedFile(t, ".goreleaser.portable-snapshot.yaml")

	for _, section := range []string{"archives", "checksum", "sboms"} {
		if got, want := strings.TrimSpace(stripYAMLComments(yamlSection(portable, section))), strings.TrimSpace(stripYAMLComments(yamlSection(native, section))); got != want {
			t.Errorf("portable %s config drifted from native config\nportable:\n%s\nnative:\n%s", section, got, want)
		}
	}
	nativeBuilds := stripYAMLComments(yamlSection(native, "builds"))
	portableBuilds := stripYAMLComments(yamlSection(portable, "builds"))
	nativeBuilds = regexp.MustCompile(`(?m)^    env:\n(?:      .*\n)+`).ReplaceAllString(nativeBuilds, "")
	portableBuilds = regexp.MustCompile(`(?m)^    env:\n(?:      .*\n)+`).ReplaceAllString(portableBuilds, "")
	if nativeBuilds != portableBuilds {
		t.Fatalf("portable build matrix or flags drifted from native config\nportable:\n%s\nnative:\n%s", portableBuilds, nativeBuilds)
	}
	if !strings.Contains(yamlSection(native, "builds"), `CGO_ENABLED={{ if eq .Os "darwin" }}1{{ else }}0{{ end }}`) {
		t.Fatal("native config must enable cgo only for Darwin artifacts")
	}
	if !strings.Contains(yamlSection(portable, "builds"), "CGO_ENABLED=0") {
		t.Fatal("portable snapshot config must explicitly disable cgo for all targets")
	}
}

func TestNativeAndPortableReleaseBuildsIncludeVCSMetadata(t *testing.T) {
	flags := regexp.MustCompile(`(?m)^    flags:\n      - -buildvcs=true$`)
	for _, name := range []string{".goreleaser.yaml", ".goreleaser.portable-snapshot.yaml"} {
		t.Run(name, func(t *testing.T) {
			builds := yamlSection(readNamedFile(t, name), "builds")
			if !flags.MatchString(builds) {
				t.Fatalf("%s builds must explicitly enable Go VCS stamping", name)
			}
		})
	}
}

func TestReleaseInstallsPinnedSyftBeforeGoReleaser(t *testing.T) {
	workflow := readNamedWorkflow(t, "release.yml")

	syft := regexp.MustCompile(`(?m)^      - uses: anchore/sbom-action/download-syft@v0\n        with:\n(?:          #[^\n]*\n)+          syft-version: "v[0-9]+\.[0-9]+\.[0-9]+"$`).FindStringIndex(workflow)
	if syft == nil {
		t.Fatal("release workflow must install a pinned Syft distribution")
	}
	goreleaser := strings.Index(workflow, "      - uses: goreleaser/goreleaser-action@v7\n")
	if goreleaser < 0 {
		t.Fatal("release workflow must run GoReleaser")
	}
	if syft[0] > goreleaser {
		t.Fatal("release workflow must install Syft before GoReleaser")
	}
}

func readWorkflow(t *testing.T) string {
	t.Helper()
	return readNamedWorkflow(t, "ci.yml")
}

func readNamedWorkflow(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", ".github", "workflows", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func readNamedFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func yamlSection(source, name string) string {
	var lines []string
	found := false
	for _, line := range strings.Split(source, "\n") {
		if !found {
			if line == name+":" {
				found = true
				lines = append(lines, line)
			}
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") {
			break
		}
		lines = append(lines, line)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func stripYAMLComments(source string) string {
	lines := strings.Split(source, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func jobBlock(t *testing.T, workflow, name string) string {
	t.Helper()
	start := strings.Index(workflow, "\n  "+name+":\n")
	if start < 0 {
		t.Fatalf("job %q not found", name)
	}
	block := workflow[start+1:]
	if end := regexp.MustCompile(`(?m)^  [a-zA-Z0-9_-]+:\s*$`).FindStringIndex(block[len("  "+name+":\n"):]); end != nil {
		block = block[:len("  "+name+":\n")+end[0]]
	}
	return block
}

func needsList(t *testing.T, job string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^    needs:\s*\[([^]]*)\]\s*$`)
	match := re.FindStringSubmatch(job)
	if match == nil {
		t.Fatal("test.needs must be an explicit list")
	}
	parts := strings.Split(match[1], ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func successfulResults() map[string]string {
	results := make(map[string]string, len(requiredGates))
	for _, gate := range requiredGates {
		results[gate] = "success"
	}
	return results
}

func requiredCheckSucceeds(job string, results map[string]string) bool {
	for gate, result := range results {
		assertion := `test "${{ needs.` + gate + `.result }}" = "success"`
		if strings.Contains(job, assertion) && result != "success" {
			return false
		}
	}
	return true
}

func TestWorkflowsDoNotInvokeRemovedLaunchPackages(t *testing.T) {
	for _, name := range []string{"ci.yml", "release.yml"} {
		source := readNamedWorkflow(t, name)
		for _, removed := range []string{"./internal/features/execution/", "./internal/features/orchestration/", "./internal/providerpolicy/", "./internal/verifyroute/"} {
			if strings.Contains(source, removed) {
				t.Errorf("%s invokes removed launcher package %s", name, removed)
			}
		}
	}
	ci := readNamedWorkflow(t, "ci.yml")
	for _, name := range []string{"TestLeaseCrashPreservesLockAndAuthority", "TestLeaseLostReplyReplayAcrossProcesses"} {
		if !strings.Contains(ci, name) {
			t.Errorf("CI omits repeated native process-boundary test %s", name)
		}
	}
}
