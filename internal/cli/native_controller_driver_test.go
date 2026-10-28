package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
)

func TestNativeControllerDriveMainRequiresExplicitPrivateSelection(t *testing.T) {
	t.Setenv(agentid.EnvVar, "invalid-legacy-driver-token")
	if out, code := runMainWithJSON(t, "native", "controller", "drive"); code != 2 || !strings.Contains(out, "--profile") {
		t.Fatalf("driver borrowed ambient identity before validating explicit selection: code=%d %s", code, out)
	}
	for _, flag := range []string{"--bad-driver-flag", "--command", "--owner-credential", "--backend"} {
		if out, code := runMainWithJSON(t, "native", "controller", "drive", "--profile", "/missing-private-profile", "--request", "/missing-private-request", flag, "forbidden"); code != 2 || !strings.Contains(out, flag) {
			t.Fatalf("driver did not reject the forbidden flag by name: code=%d %s", code, out)
		}
	}
	private := t.TempDir()
	request := filepath.Join(private, "request.json")
	raw := []byte(`{"schema_version":1,"project_id":"p","invocation_id":"invocation","checkpoint_id":"checkpoint","expected_sequence":1,"mode":"drive"}`)
	if err := os.WriteFile(request, raw, 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(private, "profile.json")
	secret := "private-owner-material-must-not-escape"
	if err := os.WriteFile(profile, []byte(`{"schema_version":1,"owner_credential":"`+secret+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	out, code := runMainWithJSON(t, "native", "controller", "drive", "--profile", profile, "--request", request)
	if code != 3 || !strings.Contains(out, "selection refused") || strings.Contains(out, secret) || strings.Contains(out, private) {
		t.Fatalf("invalid private selection was accepted or disclosed: code=%d %s", code, out)
	}
	after, err := os.ReadFile(profile)
	if err != nil || string(before) != string(after) {
		t.Fatalf("driver rewrote a refused selection: %v", err)
	}
	_, message, exit := executor(private)([]string{"native", "controller", "drive", "--profile", profile, "--request", request}, false)
	if exit != 3 || !strings.Contains(message, "local-only") {
		t.Fatalf("generic CLI MCP escaped the selected local driver boundary: exit=%d %s", exit, message)
	}
}

func TestNativeControllerDriveMainRejectsRequestAuthorityInjection(t *testing.T) {
	private := t.TempDir()
	request := filepath.Join(private, "request.json")
	value := map[string]any{"schema_version": 1, "project_id": "p", "invocation_id": "invocation", "checkpoint_id": "checkpoint", "expected_sequence": 1, "mode": "drive", "owner_credential": "do-not-print-request-secret"}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(request, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, code := runMainWithJSON(t, "native", "controller", "drive", "--profile", filepath.Join(private, "unused.json"), "--request", request)
	if code != 3 || strings.Contains(out, "do-not-print-request-secret") || strings.Contains(out, private) {
		t.Fatalf("request injected authority or disclosed private data: code=%d %s", code, out)
	}
}
