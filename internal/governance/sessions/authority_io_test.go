package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func authorityBytes(t *testing.T, a *Authority) []byte {
	t.Helper()
	raw, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func authorityEntries(t *testing.T, a *Authority) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(a.path))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestAuthorityReadExactBoundaryAndOversize(t *testing.T) {
	a, _ := setup(t)
	raw := authorityBytes(t, a)
	padded := append(raw, bytes.Repeat([]byte(" "), int(MaxAuthorityBytes)-len(raw))...)
	if err := os.WriteFile(a.path, padded, 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := a.transaction(false, false, func(*state) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("exact-limit authority not decoded: called=%v err=%v", called, err)
	}
	// An extra JSON whitespace byte remains valid JSON, so size is the only
	// reason to refuse it. Truncate avoids allocating a second large payload.
	if err := os.Truncate(a.path, MaxAuthorityBytes+1); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(a.path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteAt([]byte(" "), MaxAuthorityBytes)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("extend authority: %v %v", writeErr, closeErr)
	}
	called = false
	err = a.transaction(false, false, func(*state) error { called = true; return nil })
	if err != ErrStorage || called {
		t.Fatalf("oversize authority reached operation: called=%v err=%v", called, err)
	}
}

func TestAuthorityReadRejectsUnsafeFilesAndPreservesCreateOnce(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "public-mode", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			a, _ := setup(t)
			raw := authorityBytes(t, a)
			if err := os.Remove(a.path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(a.path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(filepath.Dir(a.path), "target.json")
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, a.path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "public-mode":
				if err := os.WriteFile(a.path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(a.path, 0644); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				mkfifo, err := exec.LookPath("mkfifo")
				if err != nil {
					t.Skip("mkfifo unavailable")
				}
				if err := exec.Command(mkfifo, a.path).Run(); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := a.transaction(false, false, func(*state) error { called = true; return nil })
			if err != ErrStorage || called {
				t.Fatalf("unsafe %s read: called=%v err=%v", kind, called, err)
			}
			if token, err := a.Bootstrap(); token != "" || (err != ErrStorage && err != ErrDenied) {
				t.Fatalf("unsafe %s adopted as absent: token=%q err=%v", kind, token, err)
			}
		})
	}
	a, _ := setup(t)
	before := authorityBytes(t, a)
	if token, err := a.Bootstrap(); err != ErrDenied || token != "" {
		t.Fatalf("existing authority overwritten: token=%q err=%v", token, err)
	}
	if !bytes.Equal(before, authorityBytes(t, a)) {
		t.Fatal("create-once refusal changed authority")
	}
	if err := os.Remove(a.path); err != nil {
		t.Fatal(err)
	}
	if err := a.transaction(false, false, func(*state) error { t.Fatal("missing authority reached operation"); return nil }); err != ErrStorage {
		t.Fatalf("missing read not sanitized: %v", err)
	}
	if token, err := a.Bootstrap(); err != nil || token == "" {
		t.Fatalf("actual absence did not permit bootstrap: %v", err)
	}
}

func TestAuthorityWriteRefusalPreservesPermanentRecords(t *testing.T) {
	f, input, policy := nativePolicySetup(t)
	if err := f.a.UpgradeNativePolicy(f.owner); err != nil {
		t.Fatal(err)
	}
	if err := f.a.ConfigureNativePolicy(f.owner, policy); err != nil {
		t.Fatal(err)
	}
	decision, err := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "size-bound-decision", Input: input, ExpiresAt: policy.ExpiresAt}, nativePolicyBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.BeginNativeAcceptanceWithPolicy(f.owner, input, decision.ID, nativePolicyBody); err != nil {
		t.Fatal(err)
	}
	before := authorityBytes(t, f.a)
	var original state
	if err := json.Unmarshal(before, &original); err != nil {
		t.Fatal(err)
	}
	if len(original.Operations) == 0 || len(original.NativePolicyHistory) == 0 || len(original.NativeAcceptanceOperations) == 0 || len(original.NativePolicyConsumptions) == 0 {
		t.Fatal("fixture must contain replay, history, acceptance and consumption records")
	}
	entries := authorityEntries(t, f.a)
	for _, override := range []bool{false, true} {
		called := false
		if override {
			f.a.persistOverride = func([]byte) error { called = true; return nil }
		}
		err := f.a.transaction(false, true, func(s *state) error {
			// Simulate aggregate growth without deleting any permanent records.
			s.Operations["size-bound-growth"] = operation{Digest: strings.Repeat("x", int(MaxAuthorityBytes))}
			return nil
		})
		f.a.persistOverride = nil
		if err != ErrStorage || called {
			t.Fatalf("oversize write reached persistence (override=%v): called=%v err=%v", override, called, err)
		}
		if !bytes.Equal(before, authorityBytes(t, f.a)) || !reflect.DeepEqual(entries, authorityEntries(t, f.a)) {
			t.Fatal("refused write changed original bytes or left temporary artifacts")
		}
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if !reflect.DeepEqual(*s, original) {
			t.Fatal("refused write changed permanent records")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// An exact replay still returns the consumed operation after the refusals.
	if _, err := f.a.BeginNativeAcceptanceWithPolicy(f.owner, input, decision.ID, nativePolicyBody); err != nil {
		t.Fatalf("permanent acceptance replay lost after refusal: %v", err)
	}
}

func TestAuthorityPersistBoundaryAndDefensiveRefusal(t *testing.T) {
	a, _ := setup(t)
	raw := authorityBytes(t, a)
	padded := append(raw, bytes.Repeat([]byte(" "), int(MaxAuthorityBytes)-len(raw))...)
	if err := a.persist(padded); err != nil {
		t.Fatalf("exact boundary persist refused: %v", err)
	}
	entries := authorityEntries(t, a)
	if err := a.persist(append(padded, ' ')); !errors.Is(err, ErrStorage) {
		t.Fatalf("direct oversize persist accepted: %v", err)
	}
	if !bytes.Equal(padded, authorityBytes(t, a)) || !reflect.DeepEqual(entries, authorityEntries(t, a)) {
		t.Fatal("defensive refusal replaced original bytes or left temporary artifacts")
	}
}

func TestAuthorityTransactionWriteExactBoundary(t *testing.T) {
	a, _ := setup(t)
	err := a.transaction(false, true, func(s *state) error {
		s.Operations["boundary"] = operation{}
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		s.Operations["boundary"] = operation{Digest: strings.Repeat("x", int(MaxAuthorityBytes)-len(raw))}
		return nil
	})
	if err != nil {
		t.Fatalf("exact-boundary transaction write refused: %v", err)
	}
	if info, err := os.Stat(a.path); err != nil || info.Size() != MaxAuthorityBytes {
		t.Fatalf("exact-boundary write size: %v %v", info, err)
	}
	if err := a.transaction(false, false, func(*state) error { return nil }); err != nil {
		t.Fatalf("exact-boundary persisted authority unreadable: %v", err)
	}
}
