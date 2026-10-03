package nativegovernance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestNativePreflightOwnerCLIUpgradePreservesHistoryCurrentMCPAndGenuineOldWriters(t *testing.T) {
	f := setupVerification(t, "printf verified", true)
	before, e := os.ReadFile(f.worker.Authority)
	if e != nil {
		t.Fatal(e)
	}
	for _, upgrade := range []func(string) error{f.authority.UpgradeSwarms, f.authority.UpgradeDispatchIntents, f.authority.UpgradeLifecycle, f.authority.UpgradeRecovery} {
		if e = upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	if out, e := verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return preflightOwner(c, a, "upgrade") }, "--config", f.config); e != nil || !strings.Contains(out, "schema11") {
		t.Fatalf("explicit owner CLI11 upgrade %v %s", e, out)
	}
	upgraded, e := os.ReadFile(f.worker.Authority)
	if e != nil {
		t.Fatal(e)
	}
	var old, current map[string]json.RawMessage
	if json.Unmarshal(before, &old) != nil || json.Unmarshal(upgraded, &current) != nil {
		t.Fatal("authority decode")
	}
	for _, key := range []string{"Binding", "OwnerHash", "Sessions", "Tasks", "Attempts", "Leases", "Fence", "Revisions", "Grants", "Evidence", "ReviewGrants", "Reviews", "PublicationPolicies", "PublicationDecisions", "PublicationConsumed", "Operations"} {
		if !bytes.Equal(old[key], current[key]) {
			t.Fatalf("schema11 migration changed existing native history %s", key)
		}
	}
	server, e := native.Open(f.connection)
	if e != nil {
		t.Fatal("native worker cannot open11", e)
	}
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture"}}}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	identity := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"identity","arguments":{"schema_version":1}}}` + "\n"
	var output bytes.Buffer
	if e = server.Serve(strings.NewReader(init+identity), &output); e != nil || !strings.Contains(output.String(), f.worker.SessionID) || strings.Contains(output.String(), `"isError":true`) {
		t.Fatalf("schema11 independent worker MCP identity %v %s", e, output.String())
	}
	expected := map[string]string{"6": "43ca05c6b94779a1642d209ad2c7a94825eeeaeab0e822c571f0c8816ab597fd", "7": "16ad3b501f55f51ebd515e976996cf80838aff948a21806d11c989035389b7f3", "8": "c018f3ef18be8128fd7d3ea9bffb4bfac4b070739587bf0df7286c24e8a18411", "9": "301d5b62c11642854ef2c1bea536f2dd8f94023e9a1806f07c8c5469cfff08a7", "10": "c114218d58ece00d0468f5991bd531114a71faf20bc6eba0e8c5cbc521ff8139"}
	for _, version := range []string{"6", "7", "8", "9", "10"} {
		t.Run("genuine-schema"+version, func(t *testing.T) {
			path := os.Getenv("DACLI_SCHEMA" + version + "_BINARY")
			if path == "" {
				t.Skip("explicit genuine previous binary not selected")
			}
			binary, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			sum := sha256.Sum256(binary)
			if hex.EncodeToString(sum[:]) != expected[version] {
				t.Fatal("selected binary is not measured genuine previous schema")
			}
			cmd := exec.Command(path, "native", "serve", "--connection", f.connection)
			cmd.Stdin = strings.NewReader(init + identity)
			raw, e := cmd.CombinedOutput()
			if e == nil || !strings.Contains(string(raw), "refused") {
				t.Fatalf("genuine schema%s writer accepted11: %v %s", version, e, raw)
			}
			after, e := os.ReadFile(f.worker.Authority)
			if e != nil || !bytes.Equal(after, upgraded) {
				t.Fatal("old writer changed11", e)
			}
		})
	}
}
func TestNativePreflightOwnerScopedCheckoutProvisioningAndPolicyRefusal(t *testing.T) {
	f := setupVerification(t, "printf verified", true)
	private := t.TempDir()
	connection := filepath.Join(private, "worker.json")
	credential := filepath.Join(private, "worker.token")
	out, e := verificationRun(t, f.w, enroll, "--config", f.config, "--task", f.task.ID, "--connection", connection, "--credential", credential, "--path", "src", "--checkout", f.w.Root, "--verification")
	if e != nil {
		t.Fatalf("owner exact checkout enrollment %v %s", e, out)
	}
	var c native.Connection
	if e = privateJSON(connection, &c); e != nil {
		t.Fatal(e)
	}
	token, e := native.ReadCredential(c.CredentialFile)
	if e != nil {
		t.Fatal(e)
	}
	principal, e := f.authority.Authenticate(token, c.SessionID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, resource := range principal.Scope.Resources {
		if resource == "checkout:"+f.w.Root {
			found = true
		}
	}
	if !found {
		t.Fatal("owner checkout scope missing")
	}
	request := filepath.Join(private, "policy.json")
	verificationJSON(t, request, sessions.PreflightPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "invalid"})
	if _, e = verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return preflightOwner(c, a, "configure") }, "--config", f.config, "--request", request, "--json"); clikit.ExitCode(e) != 3 {
		t.Fatalf("unupgraded malformed policy not final refusal: %v", e)
	}
	before, e := os.ReadFile(f.worker.Authority)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = verificationRun(t, f.w, enroll, "--config", f.config, "--task", f.task.ID, "--connection", connection, "--credential", filepath.Join(private, "must-not-orphan"), "--checkout", f.w.Root); clikit.ExitCode(e) != 3 {
		t.Fatalf("occupied profile minted new authority: %v", e)
	}
	after, e := os.ReadFile(f.worker.Authority)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("occupied owner output changed authority", e)
	}
}
func TestNativePreflightCLITransientIsRetryableAndExhaustionFinal(t *testing.T) {
	f := setupVerification(t, "printf verified", true)
	if e := store.SetEstimate(f.w, f.task, "5,8,11"); e != nil {
		t.Fatal(e)
	}
	f.task.Doc.Front.SetList("claims", []string{"src"})
	if e := store.SaveTask(f.task); e != nil {
		t.Fatal(e)
	}
	for _, r := range []team.Role{{Name: "implementation", Kind: "implementer", Runtime: "codex", Model: "model", Grant: "rw", MaxPoints: 13}, {Name: "review", Kind: "reviewer", Runtime: "codex", Model: "model", Grant: "ro", MaxPoints: 13}} {
		if e := store.CreateRole(f.w, "owner", r); e != nil {
			t.Fatal(e)
		}
	}
	verificationGit(t, f.w.Root, "add", ".")
	verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "preflight roster and task")
	private := t.TempDir()
	workerFile := filepath.Join(private, "selected-worker.json")
	if out, e := verificationRun(t, f.w, enroll, "--config", f.config, "--task", f.task.ID, "--connection", workerFile, "--credential", filepath.Join(private, "selected-worker.token"), "--path", "src", "--checkout", f.w.Root); e != nil {
		t.Fatalf("selectedworker %v %s", e, out)
	}
	capability := filepath.Join(private, "capability.json")
	verificationJSON(t, capability, application.ManualSwarmCapability{SchemaVersion: 1, WorkspaceID: f.w.ID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * time.Minute)})
	raw, e := os.ReadFile(capability)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(raw)
	if e = f.authority.UpgradeSwarms(f.owner); e != nil {
		t.Fatal(e)
	}
	swarm := sessions.SwarmPolicy{ProjectID: "p", Revision: "swarm", Harness: "codex", CapabilityDigest: "sha256:" + hex.EncodeToString(digest[:]), Width: 1, WIP: 1, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "implementation", Kind: "implementation", Harness: "codex", Capacity: 1}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{f.task.ID: {Checkout: f.w.Root, ImplementKind: "implementation", ReviewKind: "review"}}}
	if e = f.authority.ConfigureSwarm(f.owner, swarm); e != nil {
		t.Fatal(e)
	}
	for _, upgrade := range []func(string) error{f.authority.UpgradeDispatchIntents, f.authority.UpgradeLifecycle, f.authority.UpgradeRecovery, f.authority.UpgradePreflight} {
		if e = upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	observed, e := checkout.Observe(f.w.Root)
	if e != nil {
		t.Fatal(e)
	}
	contract := filepath.Join(private, "contract.json")
	verificationJSON(t, contract, application.NativePhaseContract{SchemaVersion: 1, WorkspaceID: f.w.ID, ProjectID: "p", TaskID: f.task.ID, Harness: "codex", ObservedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * time.Minute), Models: []string{"model"}, IntegrationRoute: "owner-manual-local", IntegrationCheckout: f.w.Root, IntegrationBase: observed.Commit, IntegrationPolicyDigest: strings.Repeat("a", 64), ExternalStatus: "transient"})
	policy := sessions.PreflightPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "preflight", Harness: "codex", Rationale: "owner accepts bounded cooperative integration observation", AcceptManualAssurance: true, ExpiresAt: time.Now().UTC().Add(30 * time.Minute), TransientRetries: 1, Tasks: map[string]sessions.PreflightTask{f.task.ID: {ImplementationRole: "implementation", ReviewRole: "review", RecoveryRole: "implementation", WorkerConnection: workerFile, ContractProfile: contract, RequireIntegration: true, OwnerAuthorizedIntegration: true}}}
	if e = f.authority.ConfigurePreflight(f.owner, policy); e != nil {
		t.Fatal(e)
	}
	controller := filepath.Join(private, "controller.json")
	if out, e := verificationRun(t, f.w, swarmControllerAdd, "--config", f.config, "--project", "p", "--connection", controller, "--credential", filepath.Join(private, "controller.token")); e != nil {
		t.Fatalf("controller %v %s", e, out)
	}
	request := filepath.Join(private, "request.json")
	check := func(key string) (sessions.PreflightReceipt, error) {
		t.Helper()
		verificationJSON(t, request, struct{ ProjectID, TaskID, OperationKey string }{"p", f.task.ID, key})
		out, e := verificationRun(t, f.w, preflightCheck, "--connection", controller, "--capability-file", capability, "--request", request, "--json")
		var receipt sessions.PreflightReceipt
		if json.Unmarshal([]byte(out), &receipt) != nil {
			t.Fatalf("typed preflight JSON missing: %s %v", out, e)
		}
		return receipt, e
	}
	first, e := check("first")
	if clikit.ExitCode(e) != 1 || first.Report.FailureClass != "transient" || first.TransientAttempts != 1 {
		t.Fatalf("transient CLI preflight incorrectly final policy exit3: exit%d %+v %v", clikit.ExitCode(e), first, e)
	}
	replay, e := check("first")
	if clikit.ExitCode(e) != 1 || !reflect.DeepEqual(first, replay) {
		t.Fatalf("typed transient replay spent another retry: exit%d %+v %v", clikit.ExitCode(e), replay, e)
	}
	last, e := check("second")
	if clikit.ExitCode(e) != 3 || last.Report.FailureClass != "permanent" || last.TransientAttempts != 2 {
		t.Fatalf("exhausted CLI transient not final policy3: exit%d %+v %v", clikit.ExitCode(e), last, e)
	}
}
