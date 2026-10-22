//go:build darwin && cgo

package credentialstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func TestKeychainStoreQualification(t *testing.T) {
	store, err := Open()
	if err != nil {
		t.Fatalf("open native Keychain store: %v", err)
	}

	// The digest gives this test an isolated Keychain account without putting
	// credential material in argv, output, workspace state, or a file.
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("generate synthetic account id: %v", err)
	}
	key := sha256.Sum256(nonce[:])
	account := hex.EncodeToString(key[:])
	stored := false
	t.Cleanup(func() {
		if stored {
			if err := store.Delete(account); err != nil {
				t.Errorf("remove synthetic Keychain item: %v", err)
			}
		}
	})

	if _, err := store.Get(account); !errors.Is(err, ErrMissing) {
		t.Fatalf("get before put = %v, want ErrMissing", err)
	}
	value := []byte("dacli-keychain-qualification-v1")
	if err := store.Put(account, value); err != nil {
		t.Fatalf("put synthetic value: %v", err)
	}
	stored = true
	got, err := store.Get(account)
	if err != nil {
		t.Fatalf("get synthetic value: %v", err)
	}
	if string(got) != string(value) {
		t.Fatal("get returned a different synthetic value")
	}
	if err := store.Delete(account); err != nil {
		t.Fatalf("delete synthetic value: %v", err)
	}
	if _, err := store.Get(account); !errors.Is(err, ErrMissing) {
		t.Fatalf("get after delete = %v, want ErrMissing", err)
	}
	if err := store.Delete(account); err != nil {
		t.Fatalf("delete missing synthetic value should be idempotent: %v", err)
	}

	if err := store.Put("invalid-account", value); !errors.Is(err, ErrDenied) {
		t.Fatalf("put with invalid account = %v, want ErrDenied", err)
	}
	if _, err := store.Get("invalid-account"); !errors.Is(err, ErrDenied) {
		t.Fatalf("get with invalid account = %v, want ErrDenied", err)
	}
	if err := store.Delete("invalid-account"); !errors.Is(err, ErrDenied) {
		t.Fatalf("delete with invalid account = %v, want ErrDenied", err)
	}
	if err := store.Put(account, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("put empty value = %v, want ErrDenied", err)
	}
	if err := store.Put(account, make([]byte, MaxCredentialBytes+1)); !errors.Is(err, ErrDenied) {
		t.Fatalf("put oversized value = %v, want ErrDenied", err)
	}
}
