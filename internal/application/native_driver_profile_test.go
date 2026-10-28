package application

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func TestDecodeNativeControllerDriverProfileRequiresExactNanosecondLimits(t *testing.T) {
	base := map[string]any{
		"schema_version": 1, "workspace_id": "workspace", "installation_id": "installation",
		"project_id": "project", "invocation_id": "invocation", "checkpoint_id": "checkpoint",
		"controller_connection": "/private/controller.json", "capability_file": "/private/capability.json",
		"limits": map[string]any{
			"max_transitions": 1, "max_effects": 1, "max_work": 1, "max_no_progress": 1,
			"max_elapsed": int64(time.Second), "operation_timeout": int64(time.Second), "max_sequence": 1,
		},
		"tasks": []any{},
	}
	encode := func(t *testing.T, value map[string]any) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if _, err := decodeNativeControllerDriverProfile(encode(t, base)); err != nil {
		t.Fatalf("valid profile limits rejected: %v", err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"unknown limit":         func(v map[string]any) { v["limits"].(map[string]any)["max_tokens"] = 0 },
		"null limit":            func(v map[string]any) { v["limits"].(map[string]any)["max_elapsed"] = nil },
		"string duration":       func(v map[string]any) { v["limits"].(map[string]any)["max_elapsed"] = "1s" },
		"fractional duration":   func(v map[string]any) { v["limits"].(map[string]any)["max_elapsed"] = 1.5 },
		"unknown profile field": func(v map[string]any) { v["host_command"] = []string{"sh"} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := map[string]any{}
			for key, value := range base {
				candidate[key] = value
			}
			limits := map[string]any{}
			for key, value := range base["limits"].(map[string]any) {
				limits[key] = value
			}
			candidate["limits"] = limits
			mutate(candidate)
			if _, err := decodeNativeControllerDriverProfile(encode(t, candidate)); !errors.Is(err, sessions.ErrDenied) {
				t.Fatalf("malformed private profile error = %v, want policy denial", err)
			}
		})
	}
}

func TestNativeControllerDriverLimitsEnforceProfileCeilings(t *testing.T) {
	valid := nativeDriverProfileLimits{MaxTransitions: 256, MaxEffects: 256, MaxWork: 4096, MaxNoProgress: 100, MaxElapsed: int64(15 * time.Minute), OperationTimeout: int64(30 * time.Second), MaxSequence: 1}
	if !validNativeDriverLimits(valid) {
		t.Fatal("maximum supported limits were rejected")
	}
	invalid := []nativeDriverProfileLimits{
		{},
		{MaxTransitions: 257, MaxEffects: 1, MaxWork: 1, MaxNoProgress: 1, MaxElapsed: 1, OperationTimeout: 1, MaxSequence: 1},
		{MaxTransitions: 1, MaxEffects: 1, MaxWork: 4097, MaxNoProgress: 1, MaxElapsed: 1, OperationTimeout: 1, MaxSequence: 1},
		{MaxTransitions: 1, MaxEffects: 1, MaxWork: 1, MaxNoProgress: 1, MaxElapsed: int64(15*time.Minute) + 1, OperationTimeout: 1, MaxSequence: 1},
		{MaxTransitions: 1, MaxEffects: 1, MaxWork: 1, MaxNoProgress: 1, MaxElapsed: 1, OperationTimeout: int64(30*time.Second) + 1, MaxSequence: 1},
	}
	for i, candidate := range invalid {
		if validNativeDriverLimits(candidate) {
			t.Fatalf("invalid limit set %d accepted", i)
		}
	}
}

func TestNativeControllerDriverSelectionDigestRefusesValidFileReplacement(t *testing.T) {
	dir := t.TempDir()
	selection := filepath.Join(dir, "selection.json")
	first := []byte(`{"schema_version":1,"project_id":"first"}`)
	if err := os.WriteFile(selection, first, 0o600); err != nil {
		t.Fatal(err)
	}
	p := &NativeControllerDriver{profilePath: selection, profileDigest: sha256Sum(first), pinnedFiles: map[string][32]byte{}, pinnedLimits: map[string]int64{}}
	if err := p.pinPrivate(selection, 4096); err != nil {
		t.Fatalf("pin selected file: %v", err)
	}
	if err := p.pinPrivate(selection, 8192); err != nil {
		t.Fatalf("reuse identical pinned selection: %v", err)
	}
	if err := p.verifyPinnedFiles(); err != nil {
		t.Fatalf("original selection rejected: %v", err)
	}
	alternate := []byte(`{"schema_version":1,"project_id":"second"}`)
	if err := os.WriteFile(selection, alternate, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.verifyPinnedFiles(); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("valid alternate selection error = %v, want policy denial", err)
	}
}

func sha256Sum(data []byte) [32]byte { return sha256.Sum256(data) }
