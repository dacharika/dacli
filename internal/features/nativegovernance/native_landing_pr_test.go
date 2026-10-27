package nativegovernance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func TestNativeLandingPRTransportIsStrictAndBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake process fixture")
	}
	dir := t.TempDir()
	reply := filepath.Join(dir, "reply")
	capture := filepath.Join(dir, "args")
	stderr := filepath.Join(dir, "stderr")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DACLI_LANDING_ARGS\"\ncat \"$DACLI_LANDING_REPLY\"\ncat \"$DACLI_LANDING_STDERR\" >&2\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DACLI_LANDING_REPLY", reply)
	t.Setenv("DACLI_LANDING_ARGS", capture)
	t.Setenv("DACLI_LANDING_STDERR", stderr)
	valid := `{"number":123,"url":"https://github.com/owner/repo/pull/123","state":"MERGED","headRefOid":"head","baseRefName":"main","mergeCommit":{"oid":"merge"}}`
	for _, tc := range []struct {
		name, body, stderr string
		valid              bool
	}{{"valid", valid, "", true}, {"unknown-field", strings.TrimSuffix(valid, "}") + `,"extra":true}`, "", false}, {"trailing", valid + ` {}`, "", false}, {"bounded-stdout", strings.Replace(valid, `"oid":"merge"`, `"oid":"merge","messageBody":"`+strings.Repeat("x", 1<<20)+`"`, 1), "", false}, {"bounded-stderr", valid, strings.Repeat("x", (1<<20)+1), false}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(stderr, []byte(tc.stderr), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(reply, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := observeNativeLandingPR(context.Background(), "owner/repo", 123)
			if tc.valid {
				if err != nil || got.Number != 123 || got.MergeCommitOID != "merge" || got.HeadRefOID != "head" {
					t.Fatalf("typed observation mismatch: %+v %v", got, err)
				}
			} else if !errors.Is(err, sessions.ErrDenied) {
				t.Fatalf("invalid process output was accepted: %+v %v", got, err)
			}
		})
	}
	args, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := "pr\nview\n123\n--repo\nowner/repo\n--json\nnumber,url,state,headRefOid,baseRefName,mergeCommit\n"
	if string(args) != want {
		t.Fatalf("unexpected command arguments: %q", args)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeNativeLandingPR(ctx, "owner/repo", 123); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("canceled observation accepted: %v", err)
	}
}
