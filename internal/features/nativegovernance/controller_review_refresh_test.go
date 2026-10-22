package nativegovernance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func TestNativeControllerReviewRefreshCommandContract(t *testing.T) {
	found := false
	for _, command := range Commands {
		if command.Path == "native controller refresh-review" {
			found = true
			if !command.Mutates || !command.JSON {
				t.Fatal("owner refresh must declare mutation and JSON support")
			}
		}
	}
	if !found {
		t.Fatal("owner refresh command missing")
	}
	for _, args := range [][]string{nil, {"--config", "owner"}, {"--config", "owner", "--request", "request", "--out", "output"}, {"--config", "owner", "--review-config", "review", "--request", "request", "--out", "output", "--force"}} {
		if e := controllerRefreshReview(&clikit.Ctx{}, args); e == nil || clikit.ExitCode(e) != 2 {
			t.Fatal("missing or unsupported flag was not usage error", args, e)
		}
	}
}

func TestNativeControllerReviewRefreshPrivateBindingAndRefusalPreserveProfile(t *testing.T) {
	for _, variant := range []string{"unknown-invocation", "wrong-reviewer", "wrong-worker", "wrong-authority", "existing-output", "public-request"} {
		t.Run(variant, func(t *testing.T) {
			f := reviewSetupWorker(t)
			dir := t.TempDir()
			if out, err := verificationRun(t, f.w, reviewerAdd, reviewerArgs(f, dir)...); err != nil {
				t.Fatal(out, err)
			}
			profilePath := filepath.Join(dir, "review.json")
			var profile application.NativeReviewerProfile
			if err := privateJSON(profilePath, &profile); err != nil {
				t.Fatal(err)
			}
			request := sessions.ControllerReviewRefreshRequest{OperationKey: "owner-refresh", ProjectID: profile.ProjectID, TaskID: profile.TaskID, InvocationID: "unknown-invocation", ExpectedSequence: 1, ExpectedGrantID: profile.GrantID, ReviewerSessionID: profile.ReviewerSessionID, WorkerSessionID: profile.WorkerSessionID, Repository: profile.Repository, AttemptID: f.request.AttemptID, RevisionID: f.request.RevisionID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence, ExpectedChecklistDigest: sessions.OutputDigest([]byte("changed-checklist"))}
			switch variant {
			case "wrong-reviewer":
				request.ReviewerSessionID = f.worker.SessionID
			case "wrong-worker":
				request.WorkerSessionID = profile.ReviewerSessionID
			case "wrong-authority":
				profile.Authority = filepath.Join(dir, "missing-authority.json")
				verificationJSON(t, profilePath, profile)
			}
			requestPath := filepath.Join(dir, "request.json")
			outputPath := filepath.Join(dir, "fresh-review.json")
			verificationJSON(t, requestPath, request)
			if variant == "public-request" {
				if err := os.Chmod(requestPath, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "existing-output" {
				if err := os.WriteFile(outputPath, []byte("preserve existing"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			beforeAuthority, err := os.ReadFile(profile.Authority)
			if variant != "wrong-authority" && err != nil {
				t.Fatal(err)
			}
			oldProfile, err := os.ReadFile(profilePath)
			if err != nil {
				t.Fatal(err)
			}
			out, err := verificationRun(t, f.w, controllerRefreshReview, "--config", f.config, "--review-config", profilePath, "--request", requestPath, "--out", outputPath, "--json")
			if err == nil || clikit.ExitCode(err) != 3 {
				t.Fatal("unsafe owner refresh was not refused", out, err)
			}
			afterProfile, _ := os.ReadFile(profilePath)
			afterAuthority, _ := os.ReadFile(profile.Authority)
			if !bytes.Equal(oldProfile, afterProfile) || !bytes.Equal(beforeAuthority, afterAuthority) {
				t.Fatal("refusal rewrote private profile or authority")
			}
			if variant == "existing-output" {
				value, _ := os.ReadFile(outputPath)
				if string(value) != "preserve existing" {
					t.Fatal("exclusive output was overwritten")
				}
			} else if _, err = os.Lstat(outputPath); !os.IsNotExist(err) {
				t.Fatal("refused refresh retained unissued profile", err)
			}
		})
	}
}
