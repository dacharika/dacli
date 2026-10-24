package nativegovernance

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type verificationFixture struct {
	w                                                                                    *workspace.Workspace
	authority                                                                            *sessions.Authority
	config, connection, runnerProfile, runnerCredential, checksFile, requestFile, marker string
	owner, workerToken                                                                   string
	worker                                                                               native.Connection
	runner                                                                               runnerProfile
	request                                                                              verificationRequest
	task                                                                                 *store.Task
}

func verificationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = store.VerificationEnvironment()
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
}
func verificationJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func verificationRun(t *testing.T, w *workspace.Workspace, handler func(*clikit.Ctx, []string) error, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &out, Stderr: &out, JSON: true}
	e := handler(ctx, args)
	return out.String(), e
}
func setupVerification(t *testing.T, command string, grantRequest bool) verificationFixture {
	t.Helper()
	w, e := workspace.Init(t.TempDir(), "fixture")
	if e != nil {
		t.Fatal(e)
	}
	verificationGit(t, w.Root, "init", "-b", "main")
	if e = os.MkdirAll(filepath.Join(w.Root, "src"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(w.Root, "src", "tracked"), []byte("base"), 0600); e != nil {
		t.Fatal(e)
	}
	w, e = native.ResolveWorkspace(w.Root, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.CreateProject(w, "root", "Fixture", "p", "goal", ""); e != nil {
		t.Fatal(e)
	}
	task, e := store.CreateTask(w, "root", "p", "Verify native revision", store.TaskOpts{})
	if e != nil {
		t.Fatal(e)
	}
	verificationGit(t, w.Root, "add", ".")
	verificationGit(t, w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "base")
	private := t.TempDir()
	f := verificationFixture{w: w, task: task, config: filepath.Join(private, "owner.json"), connection: filepath.Join(private, "worker.json"), runnerProfile: filepath.Join(private, "runner.json"), runnerCredential: filepath.Join(private, "runner.token"), checksFile: filepath.Join(private, "checks.json"), requestFile: filepath.Join(private, "request.json"), marker: filepath.Join(private, "count")}
	ownerFile := filepath.Join(private, "owner.token")
	authorityFile := filepath.Join(private, "authority.json")
	verificationJSON(t, f.config, ownerConfig{1, w.Root, authorityFile, "fixture", ownerFile})
	if out, e := verificationRun(t, w, bootstrap, "--config", f.config); e != nil {
		t.Fatalf("bootstrap %v %s", e, out)
	}
	f.owner, e = native.ReadCredential(ownerFile)
	if e != nil {
		t.Fatal(e)
	}
	workerCredential := filepath.Join(private, "worker.token")
	args := []string{"--config", f.config, "--task", task.ID, "--connection", f.connection, "--credential", workerCredential, "--path", "src"}
	if grantRequest {
		args = append(args, "--verification")
	}
	if out, e := verificationRun(t, w, enroll, args...); e != nil {
		t.Fatalf("enroll %v %s", e, out)
	}
	if e = privateJSON(f.connection, &f.worker); e != nil {
		t.Fatal(e)
	}
	f.workerToken, e = native.ReadCredential(workerCredential)
	if e != nil {
		t.Fatal(e)
	}
	admission, e := application.NewLeaseAdmission(w)
	if e != nil {
		t.Fatal(e)
	}
	f.authority, e = sessions.Open(authorityFile, sessions.Binding{Workspace: w.ID, Installation: "fixture"}, admission)
	if e != nil {
		t.Fatal(e)
	}
	at, e := f.authority.RegisterAttempt(f.workerToken, f.worker.SessionID, sessions.AttemptRequest{RequestID: "attempt", TaskID: task.ID, Checkout: w.Root})
	if e != nil {
		t.Fatalf("register attempt: %v", e)
	}
	lease, e := f.authority.AcquireLease(f.workerToken, f.worker.SessionID, sessions.AcquireLeaseRequest{OperationKey: "lease", AttemptID: at.ID, Resources: []sessions.Resource{{Kind: "path", Path: "src"}}, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if e != nil {
		t.Fatalf("acquire lease: %v", e)
	}
	if e = os.WriteFile(filepath.Join(w.Root, "src", "tracked"), []byte("final"), 0600); e != nil {
		t.Fatal(e)
	}
	verificationGit(t, w.Root, "add", "src/tracked")
	verificationGit(t, w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "final")
	revision, e := f.authority.PublishAttemptRevision(f.workerToken, f.worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "publish", AttemptID: at.ID, LeaseID: lease.ID, ExpectedFence: lease.Fence})
	if e != nil {
		cmd := exec.Command("git", "status", "--porcelain")
		cmd.Dir = w.Root
		b, _ := cmd.CombinedOutput()
		t.Fatalf("publish revision: %v status %s", e, b)
	}
	if command == "" {
		command = "printf ran >> '" + f.marker + "'; printf private-output"
	}
	verificationJSON(t, f.checksFile, checksConfig{SchemaVersion: 1, Checks: []checkDeclaration{{ID: "unit", Command: command, Deadline: "4s"}}})
	if out, e := verificationRun(t, w, verifierAdd, "--config", f.config, "--task", task.ID, "--checks-file", f.checksFile, "--out", f.runnerProfile, "--credential", f.runnerCredential); e != nil {
		t.Fatalf("add %v %s", e, out)
	}
	if e = privateJSON(f.runnerProfile, &f.runner); e != nil {
		t.Fatal(e)
	}
	f.request = verificationRequest{1, "verify", at.ID, lease.ID, revision.ID, "unit", lease.Fence}
	verificationJSON(t, f.requestFile, f.request)
	return f
}
func (f verificationFixture) run(t *testing.T) (string, error) {
	return verificationRun(t, f.w, verify, "--connection", f.connection, "--runner-config", f.runnerProfile, "--request", f.requestFile)
}
func (f verificationFixture) assertNotRun(t *testing.T) {
	t.Helper()
	if _, e := os.Stat(f.marker); !os.IsNotExist(e) {
		t.Fatalf("refused request executed configured command: %v", e)
	}
}

func TestLegacyNativeUpgradeFromSchemaFivePreservesOwnerCredential(t *testing.T) {
	f := setupVerification(t, "", false)
	var cfg ownerConfig
	if err := privateJSON(f.config, &cfg); err != nil {
		t.Fatal(err)
	}
	credentialBefore, err := os.ReadFile(cfg.OwnerCredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	authorityBytes, err := os.ReadFile(cfg.Authority)
	if err != nil {
		t.Fatal(err)
	}
	var authority map[string]json.RawMessage
	if err := json.Unmarshal(authorityBytes, &authority); err != nil {
		t.Fatal(err)
	}
	version, _ := json.Marshal(5)
	authority["Version"] = version
	delete(authority, "PublicationPolicies")
	delete(authority, "PublicationDecisions")
	delete(authority, "PublicationConsumed")
	authorityBytes, err = json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Authority, authorityBytes, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := verificationRun(t, f.w, upgrade, "--config", f.config)
	if err != nil || !strings.Contains(out, "upgraded from schema 5 to 6") {
		t.Fatalf("legacy CLI migration failed: %v %s", err, out)
	}
	credentialAfter, err := os.ReadFile(cfg.OwnerCredentialFile)
	if err != nil || !bytes.Equal(credentialBefore, credentialAfter) || strings.TrimSpace(string(credentialAfter)) != f.owner {
		t.Fatalf("owner credential changed during schema5 migration: %v", err)
	}
	current, err := sessions.OpenCurrent(cfg.Authority, sessions.Binding{Workspace: f.w.ID, Installation: cfg.InstallationID})
	if err != nil || current.AuthenticateOwner(f.owner) != nil {
		t.Fatalf("migrated authority did not retain owner identity: %v", err)
	}
}

func TestNamedVerificationExecutesTrustedCheckAndReplays(t *testing.T) {
	f := setupVerification(t, "", true)
	out, e := f.run(t)
	if e != nil {
		t.Fatal(e, out)
	}
	var result struct {
		Schema   string            `json:"schema"`
		Evidence sessions.Evidence `json:"evidence"`
	}
	if e = json.Unmarshal([]byte(out), &result); e != nil {
		t.Fatal(e, out)
	}
	if result.Schema != "native-verification/v1" || result.Evidence.Provenance != "trusted-local-runner" || result.Evidence.IssuerSession != f.runner.RunnerSessionID || result.Evidence.Revision.ID != f.request.RevisionID || result.Evidence.ExitCode != 0 || result.Evidence.OutputDigest != sessions.OutputDigest([]byte("private-output")) {
		t.Fatalf("wrong trusted evidence: %s", out)
	}
	eligible, e := f.authority.EligibleVerification(f.workerToken, f.worker.SessionID, result.Evidence.ID)
	if e != nil || !eligible {
		t.Fatal("evidence not eligible", e)
	}
	again, e := f.run(t)
	if e != nil || again != out {
		t.Fatal("restart replay did not recover exact evidence", e, again)
	}
	b, e := os.ReadFile(f.marker)
	if e != nil || string(b) != "ran" {
		t.Fatal("command did not run exactly once", e, string(b))
	}
	for _, path := range []string{f.runnerProfile, f.runnerCredential} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("runner file not private", path, e)
		}
	}
	runnerToken, e := native.ReadCredential(f.runnerCredential)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{f.owner, f.workerToken, runnerToken, "private-output", f.runner.Checks[0].Command} {
		if strings.Contains(out, secret) {
			t.Fatal("private executor material in output")
		}
	}
	records, e := os.ReadDir(f.w.RunsDir())
	if (e != nil && !os.IsNotExist(e)) || len(records) != 0 {
		t.Fatal("verification fabricated provider run", records, e)
	}
}
func TestNamedVerificationRefusesBeforeEffects(t *testing.T) {
	for _, kind := range []string{"no-worker-grant", "worker-revoked", "runner-revoked", "grant-revoked", "stale-revision", "dirty-checkout", "stale-fence", "unknown-check", "profile-task", "profile-repository", "profile-command", "profile-installation", "profile-authority-symlink", "worker-authority-symlink", "profile-public", "profile-symlink", "request-override"} {
		t.Run(kind, func(t *testing.T) {
			f := setupVerification(t, "", kind != "no-worker-grant")
			switch kind {
			case "worker-revoked":
				if e := f.authority.Revoke(f.owner, f.worker.SessionID); e != nil {
					t.Fatal(e)
				}
			case "runner-revoked":
				if e := f.authority.Revoke(f.owner, f.runner.RunnerSessionID); e != nil {
					t.Fatal(e)
				}
			case "grant-revoked":
				if e := f.authority.RevokeVerification(f.owner, f.runner.Checks[0].GrantID); e != nil {
					t.Fatal(e)
				}
			case "stale-revision":
				if e := os.WriteFile(filepath.Join(f.w.Root, "src", "tracked"), []byte("later"), 0600); e != nil {
					t.Fatal(e)
				}
				verificationGit(t, f.w.Root, "add", "src/tracked")
				verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "later")
			case "dirty-checkout":
				if e := os.WriteFile(filepath.Join(f.w.Root, "src", "tracked"), []byte("dirty"), 0600); e != nil {
					t.Fatal(e)
				}
			case "stale-fence":
				f.request.ExpectedFence++
				verificationJSON(t, f.requestFile, f.request)
			case "unknown-check":
				f.request.CheckID = "unconfigured"
				verificationJSON(t, f.requestFile, f.request)
			case "profile-task":
				f.runner.TaskID = "another-task"
				verificationJSON(t, f.runnerProfile, f.runner)
			case "profile-repository":
				f.runner.Repository = t.TempDir()
				verificationJSON(t, f.runnerProfile, f.runner)
			case "profile-command":
				f.runner.Checks[0].Command = "printf bypass > '" + f.marker + "'"
				verificationJSON(t, f.runnerProfile, f.runner)
			case "profile-installation":
				f.runner.InstallationID = "another"
				verificationJSON(t, f.runnerProfile, f.runner)
			case "profile-authority-symlink", "worker-authority-symlink":
				link := f.runnerProfile + ".authority-link"
				if e := os.Symlink(f.runner.Authority, link); e != nil {
					t.Fatal(e)
				}
				if kind == "profile-authority-symlink" {
					f.runner.Authority = link
					verificationJSON(t, f.runnerProfile, f.runner)
				} else {
					f.worker.Authority = link
					verificationJSON(t, f.connection, f.worker)
				}
			case "profile-public":
				if e := os.Chmod(f.runnerProfile, 0644); e != nil {
					t.Fatal(e)
				}
			case "profile-symlink":
				target := f.runnerProfile + ".original"
				if e := os.Rename(f.runnerProfile, target); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(target, f.runnerProfile); e != nil {
					t.Fatal(e)
				}
			case "request-override":
				b, _ := json.Marshal(f.request)
				var request map[string]any
				_ = json.Unmarshal(b, &request)
				request["worker_session"] = f.worker.SessionID
				verificationJSON(t, f.requestFile, request)
			}
			out, e := f.run(t)
			if clikit.ExitCode(e) != 3 {
				t.Fatalf("%s not refused: %v %s", kind, e, out)
			}
			f.assertNotRun(t)
		})
	}
}
func TestNamedVerificationUnknownOutcomeCannotRerun(t *testing.T) {
	f := setupVerification(t, "", true)
	check := f.runner.Checks[0]
	deadline, e := time.ParseDuration(check.Deadline)
	if e != nil {
		t.Fatal(e)
	}
	runnerToken, e := native.ReadCredential(f.runnerCredential)
	if e != nil {
		t.Fatal(e)
	}
	req := sessions.VerificationRequest{OperationKey: f.request.OperationKey, WorkerSession: f.worker.SessionID, AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, RevisionID: f.request.RevisionID, CheckID: check.ID, GrantID: check.GrantID, ExpectedFence: f.request.ExpectedFence}
	if _, receipt, e := f.authority.BeginVerification(f.workerToken, runnerToken, f.runner.RunnerSessionID, req, sessions.CheckDigest(sessions.CheckDescriptor{ID: check.ID, Command: check.Command, Deadline: deadline})); e != nil || receipt != nil {
		t.Fatal("failed to reserve interrupted execution", e)
	}
	out, e := f.run(t)
	if clikit.ExitCode(e) != 3 || !strings.Contains(e.Error(), "outcome unknown") {
		t.Fatal("pending outcome not explicit refusal", e, out)
	}
	f.assertNotRun(t)
}
func TestNamedVerificationFailedCheckRecordsFailure(t *testing.T) {
	f := setupVerification(t, "printf private-failure; exit 7", true)
	out, e := f.run(t)
	if e != nil {
		t.Fatal(e, out)
	}
	var result struct {
		Evidence sessions.Evidence `json:"evidence"`
	}
	if e = json.Unmarshal([]byte(out), &result); e != nil {
		t.Fatal(e)
	}
	if result.Evidence.ExitCode != 7 || result.Evidence.Provenance != "trusted-local-runner" {
		t.Fatal("failed check misreported", out)
	}
	eligible, e := f.authority.EligibleVerification(f.workerToken, f.worker.SessionID, result.Evidence.ID)
	if e != nil || eligible {
		t.Fatal("failed evidence eligible", e)
	}
}
func TestNamedVerifierSetupRefusalsPreserveAuthorityAndFiles(t *testing.T) {
	for _, kind := range []string{"forged-owner", "malformed-second-check", "duplicate-check", "unknown-check-field", "oversized-profile", "occupied-profile", "occupied-credential", "unbound-task"} {
		t.Run(kind, func(t *testing.T) {
			f := setupVerification(t, "", true)
			cfg, e := config(f.config)
			if e != nil {
				t.Fatal(e)
			}
			profile, credential := f.runnerProfile+".new", f.runnerCredential+".new"
			switch kind {
			case "forged-owner":
				if e = os.WriteFile(cfg.OwnerCredentialFile, []byte(strings.Repeat("f", 64)), 0600); e != nil {
					t.Fatal(e)
				}
			case "malformed-second-check":
				verificationJSON(t, f.checksFile, checksConfig{SchemaVersion: 1, Checks: []checkDeclaration{{ID: "unit", Command: "true", Deadline: "4s"}, {ID: "second", Command: "true", Deadline: "0s"}}})
			case "duplicate-check":
				verificationJSON(t, f.checksFile, checksConfig{SchemaVersion: 1, Checks: []checkDeclaration{{ID: "unit", Command: "true", Deadline: "4s"}, {ID: "unit", Command: "true", Deadline: "4s"}}})
			case "unknown-check-field":
				verificationJSON(t, f.checksFile, map[string]any{"schema_version": 1, "checks": []any{map[string]any{"id": "unit", "command": "true", "deadline": "4s", "grant_id": "caller"}}})
			case "oversized-profile":
				verificationJSON(t, f.checksFile, checksConfig{SchemaVersion: 1, Checks: []checkDeclaration{{ID: "unit", Command: "#" + strings.Repeat("x", 64400), Deadline: "4s"}}})
			case "occupied-profile":
				if e = os.WriteFile(profile, []byte("existing profile"), 0600); e != nil {
					t.Fatal(e)
				}
			case "occupied-credential":
				if e = os.WriteFile(credential, []byte("existing credential"), 0600); e != nil {
					t.Fatal(e)
				}
			case "unbound-task":
				task, e := store.CreateTask(f.w, "root", "p", "Unbound", store.TaskOpts{})
				if e != nil {
					t.Fatal(e)
				}
				f.task = task
			}
			before, e := os.ReadFile(cfg.Authority)
			if e != nil {
				t.Fatal(e)
			}
			out, e := verificationRun(t, f.w, verifierAdd, "--config", f.config, "--task", f.task.ID, "--checks-file", f.checksFile, "--out", profile, "--credential", credential)
			if clikit.ExitCode(e) != 3 {
				t.Fatal("setup not refused", kind, e, out)
			}
			after, e := os.ReadFile(cfg.Authority)
			if e != nil || !bytes.Equal(before, after) {
				t.Fatal("refused setup changed authority", e)
			}
			for _, path := range []string{profile, credential} {
				b, e := os.ReadFile(path)
				if kind == "occupied-profile" && path == profile {
					if e != nil || string(b) != "existing profile" {
						t.Fatal("existing profile overwritten")
					}
				} else if kind == "occupied-credential" && path == credential {
					if e != nil || string(b) != "existing credential" {
						t.Fatal("existing credential overwritten")
					}
				} else if !os.IsNotExist(e) {
					t.Fatal("refused setup left output", path, e)
				}
			}
		})
	}
}

func TestNamedVerificationRequestRejectsAuthorityOverrides(t *testing.T) {
	f := setupVerification(t, "", true)
	for _, field := range []string{"command", "credential_file", "token", "provenance", "grant_id", "cwd", "worker_session", "issuer_session"} {
		t.Run(field, func(t *testing.T) {
			b, e := json.Marshal(f.request)
			if e != nil {
				t.Fatal(e)
			}
			var request map[string]any
			if e = json.Unmarshal(b, &request); e != nil {
				t.Fatal(e)
			}
			request[field] = "caller-selected"
			verificationJSON(t, f.requestFile, request)
			out, e := f.run(t)
			if clikit.ExitCode(e) != 3 {
				t.Fatal("caller override accepted", field, e, out)
			}
			f.assertNotRun(t)
		})
	}
}

func TestNamedVerifierBudgetAcceptsReadableLargeProfile(t *testing.T) {
	f := setupVerification(t, "#"+strings.Repeat("x", 60000)+"\nprintf bounded-output", true)
	b, e := native.ReadPrivate(f.runnerProfile, 65536)
	if e != nil || len(b) > 65536 {
		t.Fatal("accepted setup created unreadable profile", len(b), e)
	}
	out, e := f.run(t)
	if e != nil {
		t.Fatal("accepted large named check unusable", e, out)
	}
	var result struct {
		Evidence sessions.Evidence `json:"evidence"`
	}
	if e = json.Unmarshal([]byte(out), &result); e != nil || result.Evidence.ExitCode != 0 || result.Evidence.OutputDigest != sessions.OutputDigest([]byte("bounded-output")) {
		t.Fatal("accepted large profile lost exact descriptor", e, out)
	}
}
