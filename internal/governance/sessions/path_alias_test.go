package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthorityFileAliasesAreRejected(t *testing.T) {
	a, owner := setup(t)
	s, token := enroll(t, a, owner)
	for _, target := range []string{a.path, filepath.Join(t.TempDir(), "missing.json")} {
		alias := filepath.Join(t.TempDir(), "alias.json")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(alias, a.binding); !errors.Is(err, ErrStorage) {
			t.Fatalf("file symlink accepted: %v", err)
		}
	}
	if err := a.Revoke(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err := a.Authenticate(token, s.ID)
	denied(t, err)
}

func TestParentDirectoryAliasSharesAuthorityAndLock(t *testing.T) {
	a, owner := setup(t)
	s, token := enroll(t, a, owner)
	alias := filepath.Join(t.TempDir(), "directory")
	if err := os.Symlink(filepath.Dir(a.path), alias); err != nil {
		t.Fatal(err)
	}
	other, err := Open(filepath.Join(alias, filepath.Base(a.path)), a.binding)
	if err != nil {
		t.Fatal(err)
	}
	if other.path != a.path {
		t.Fatalf("directory aliases chose different authority/lock paths: %q %q", a.path, other.path)
	}
	if _, _, err := other.Enroll(owner, Scope{[]string{"read"}, []string{"task"}}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := a.Revoke(owner, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = other.Authenticate(token, s.ID)
	denied(t, err)
}

func TestTransactionRejectsAliasesIntroducedAfterOpen(t *testing.T) {
	for _, which := range []string{"data", "lock"} {
		t.Run(which, func(t *testing.T) {
			a, owner := setup(t)
			s, token := enroll(t, a, owner)
			target := a.path
			original := a.path + ".original"
			if which == "data" {
				if err := os.Rename(a.path, original); err != nil {
					t.Fatal(err)
				}
			} else {
				target += ".lock"
				if err := os.WriteFile(original, []byte("owner lock"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(original, target); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Authenticate(token, s.ID); !errors.Is(err, ErrStorage) {
				t.Fatalf("transaction accepted %s alias: %v", which, err)
			}
			info, err := os.Lstat(target)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("refusal replaced %s alias", which)
			}
		})
	}
}
