package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReviewInvalidCredentialCannotRunCheckoutFilter(t *testing.T) {
	a, _, s, _, req := attemptSetup(t)
	r := req.Checkout
	d := t.TempDir()
	marker := filepath.Join(d, "public-marker")
	script := filepath.Join(d, "clean")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf public > '"+marker+"'\ncat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, r, "config", "filter.fixture.clean", script)
	for name, body := range map[string]string{".gitattributes": "public.txt filter=fixture\n", "public.txt": "public fixture\n"} {
		if err := os.WriteFile(filepath.Join(r, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, r, "add", ".")
	gitTest(t, r, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "public filter fixture")
	_ = os.Remove(marker)
	if err := os.WriteFile(filepath.Join(r, "public.txt"), []byte("public fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := a.RegisterAttempt("invalid", s.ID, req)
	denied(t, err)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("invalid credential invoked repository-configured command before refusal")
	}
}

func TestInvalidCredentialDoesNotInvokeGit(t *testing.T) {
	a, _, session, _, req := attemptSetup(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "public-marker")
	wrapper := filepath.Join(dir, "git")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf public > '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := a.RegisterAttempt("invalid", session.ID, req)
	denied(t, err)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unauthenticated request invoked Git", err)
	}
}
