//go:build !darwin || !cgo

package credentialstore

import (
	"errors"
	"testing"
)

func TestUnsupportedBuildRefusesCredentialStorage(t *testing.T) {
	store, err := Open()
	if store != nil {
		t.Fatal("unsupported build returned a credential store")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open error = %v, want ErrUnsupported", err)
	}
}
