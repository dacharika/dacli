//go:build darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArtifactQualificationCleanupAndDeadlines(t *testing.T) {
	tests := []struct {
		name            string
		wantSecurity    bool
		wantTimedOutCLI bool
		wantTimedOutSec bool
	}{
		{name: "cleanup_logout_failure", wantSecurity: true},
		{name: "post_logout_hang", wantTimedOutCLI: true},
		{name: "security_delete_hang", wantSecurity: true, wantTimedOutSec: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := runSyntheticArtifactQualification(t, tc.name)
			t.Cleanup(func() {
				for _, pid := range []int{result.providerPID, result.cliPID, result.securityPID} {
					stopTestOwnedProcess(pid)
				}
			})
			if result.timedOut {
				t.Fatal("qualification harness did not finish within the outer test deadline")
			}
			if result.exitCode == 0 {
				t.Fatal("failure fixture unexpectedly qualified")
			}
			if result.tempRootRemains {
				t.Errorf("qualification temp root remains: %s", result.tempRoot)
			}
			if result.providerAlive {
				t.Errorf("provider process %d remains alive", result.providerPID)
			}
			if result.securityCalled != tc.wantSecurity {
				t.Errorf("security fallback called=%v, want %v", result.securityCalled, tc.wantSecurity)
			}
			if tc.wantTimedOutCLI && result.cliAlive {
				t.Errorf("timed-out CLI process %d remains alive", result.cliPID)
			}
			if tc.wantTimedOutCLI && (!strings.Contains(result.output, "timed out") || !strings.HasSuffix(result.cliCalls, "whoami\n")) {
				t.Errorf("post-logout refusal probe must be bounded and recorded; calls=%q output=%q", result.cliCalls, result.output)
			}
			if tc.wantTimedOutSec && result.securityAlive {
				t.Errorf("timed-out Security process %d remains alive", result.securityPID)
			}
			if tc.wantTimedOutSec {
				if !strings.Contains(result.output, "timed out") || !result.cleanupEvidenceExists {
					t.Errorf("timed-out cleanup must report a sanitized evidence path; output=%q evidence=%v", result.output, result.cleanupEvidenceExists)
				}
				if strings.Contains(result.cleanupEvidence, "http://") || !strings.Contains(result.cleanupEvidence, "credential_cleanup=false") {
					t.Errorf("cleanup evidence contains unexpected sensitive data: %q", result.cleanupEvidence)
				}
			}
		})
	}
}

type syntheticQualificationResult struct {
	exitCode, providerPID, cliPID, securityPID  int
	timedOut, tempRootRemains, providerAlive    bool
	cliAlive, securityAlive, securityCalled     bool
	cleanupEvidenceExists                       bool
	output, cleanupEvidence, tempRoot, cliCalls string
}

func runSyntheticArtifactQualification(t *testing.T, scenario string) syntheticQualificationResult {
	t.Helper()

	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate qualification harness")
	}
	repoRoot := filepath.Dir(filepath.Dir(source))
	script := filepath.Join(filepath.Dir(source), "qualify-darwin-release-artifact.sh")
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	control := filepath.Join(root, "control")
	home := filepath.Join(root, "home")
	evidence := filepath.Join(root, "evidence")
	for _, dir := range []string{binDir, control, filepath.Join(home, "Library", "Keychains")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	writeExecutable(t, filepath.Join(binDir, "go"), `#!/bin/sh
case "$1:$2" in
  env:GOCACHE) echo "$SYNTHETIC_CONTROL/go-cache" ;;
  env:GOPATH) echo "$SYNTHETIC_CONTROL/go-path" ;;
  env:GOMODCACHE) echo "$SYNTHETIC_CONTROL/go-mod-cache" ;;
  build:*)
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "-o" ]; then shift; provider_out=$1; break; fi
      shift
    done
    printf '%s\n' "$provider_out" > "$SYNTHETIC_CONTROL/provider-path"
    cat > "$provider_out" <<'PROVIDER'
#!/bin/sh
echo $$ > "$SYNTHETIC_CONTROL/provider-pid"
echo http://127.0.0.1:49123
exec sleep 60
PROVIDER
    chmod 700 "$provider_out"
    ;;
  *) exit 2 ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "security"), `#!/bin/sh
printf 'called\n' >> "$SYNTHETIC_CONTROL/security-calls"
echo $$ > "$SYNTHETIC_CONTROL/security-pid"
if [ "$SYNTHETIC_CASE" = security_delete_hang ]; then exec sleep 60; fi
exit 0
`)
	cli := filepath.Join(binDir, "fake-dacli")
	writeExecutable(t, cli, `#!/bin/sh
command_name=$2
printf '%s\n' "$command_name" >> "$SYNTHETIC_CONTROL/cli-calls"
case "$command_name:$SYNTHETIC_CASE" in
  login:*)
    profile="$HOME/Library/Application Support/dacli/device-identity"
    mkdir -p "$profile"
    printf '{}\n' > "$profile/fixture.json"
    printf '{}\n'
    exit 0
    ;;
  whoami:cleanup_logout_failure|whoami:security_delete_hang) exit 5 ;;
  logout:cleanup_logout_failure|logout:security_delete_hang) exit 6 ;;
  whoami:post_logout_hang)
    if [ ! -f "$SYNTHETIC_CONTROL/first-whoami" ]; then
      : > "$SYNTHETIC_CONTROL/first-whoami"
      printf '{}\n'
      exit 0
    fi
    echo $$ > "$SYNTHETIC_CONTROL/hung-cli-pid"
    exec sleep 60
    ;;
  logout:post_logout_hang)
    printf '{"LocalRemoved":true,"RemoteRevoked":true}\n'
    exit 0
    ;;
  whoami:*) printf '{}\n'; exit 3 ;;
  *) exit 2 ;;
esac
`)

	path := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script, cli, hostArtifactArch())
	cmd.Dir = repoRoot
	cmd.WaitDelay = time.Second
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd.Env = []string{
		"PATH=" + path,
		"HOME=" + home,
		"TMPDIR=" + tmpDir,
		"SYNTHETIC_CONTROL=" + control,
		"SYNTHETIC_CASE=" + scenario,
		"DACLI_QUALIFICATION_TIMEOUT_SECONDS=1",
		"DACLI_QUALIFICATION_EVIDENCE_DIR=" + evidence,
	}
	outputBytes, err := cmd.CombinedOutput()
	result := syntheticQualificationResult{output: string(outputBytes), timedOut: ctx.Err() != nil}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run synthetic qualification: %v; output=%s", err, result.output)
	}
	result.providerPID = readPID(filepath.Join(control, "provider-pid"))
	result.cliPID = readPID(filepath.Join(control, "hung-cli-pid"))
	result.securityPID = readPID(filepath.Join(control, "security-pid"))
	result.securityCalled = fileExists(filepath.Join(control, "security-calls"))
	if data, err := os.ReadFile(filepath.Join(control, "cli-calls")); err == nil {
		result.cliCalls = string(data)
	}
	providerPath, _ := os.ReadFile(filepath.Join(control, "provider-path"))
	if len(providerPath) > 0 {
		result.tempRoot = filepath.Dir(strings.TrimSpace(string(providerPath)))
		_, statErr := os.Stat(result.tempRoot)
		result.tempRootRemains = statErr == nil
	}
	result.providerAlive = processAlive(result.providerPID)
	result.cliAlive = processAlive(result.cliPID)
	result.securityAlive = processAlive(result.securityPID)
	evidenceFiles, _ := filepath.Glob(filepath.Join(evidence, "dacli-keychain-cleanup-*"))
	if len(evidenceFiles) > 0 {
		result.cleanupEvidenceExists = true
		evidenceBytes, readErr := os.ReadFile(evidenceFiles[0])
		if readErr != nil {
			t.Fatal(readErr)
		}
		result.cleanupEvidence = string(evidenceBytes)
	}
	return result
}

func hostArtifactArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func readPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func stopTestOwnedProcess(pid int) {
	if !processAlive(pid) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 10; i++ {
		if !processAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
