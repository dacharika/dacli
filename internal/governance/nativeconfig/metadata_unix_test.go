//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package nativeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSelectedMetadataReadersRequireBoundedRegularNoFollowFiles(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "runtime")
	if err := os.WriteFile(regular, []byte("executable metadata"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InspectExecutableMetadata(regular, 64); err != nil {
		t.Fatalf("executable metadata rejected: %v", err)
	}
	if _, err := ReadMetadata(regular, 4); !errors.Is(err, ErrDenied) {
		t.Fatalf("oversized metadata read error = %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMetadata(link, 64); !errors.Is(err, ErrDenied) {
		t.Fatalf("symlink metadata read error = %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMetadata(filepath.Join(dir, "fifo"), 64); !errors.Is(err, ErrDenied) {
		t.Fatalf("FIFO metadata read error = %v", err)
	}
	if err := os.Chmod(regular, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InspectExecutableMetadata(regular, 64); !errors.Is(err, ErrDenied) {
		t.Fatalf("non-executable metadata error = %v", err)
	}
}
