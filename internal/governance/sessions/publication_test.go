package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/policy"
	"github.com/mlnomadpy/dacli/internal/publication"
)

type publicationFixture struct {
	a            *Authority
	owner, token string
	worker       Session
	at           Attempt
	lease        Lease
	req          PublicationDecisionRequest
	p            PublicationPolicy
}

func publicationSetup(t *testing.T, delegated bool) publicationFixture {
	t.Helper()
	a, owner := setup(t)
	a.admission = &testAdmission{}
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	scope := Scope{Actions: []string{"attempt.register", "lease.acquire", "lease.release", "attempt.publish", "publication.evaluate"}, Resources: []string{"task:task-a", "checkout:", "repository:fixture/repo"}}
	worker, token, err := a.Enroll(owner, scope, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if delegated {
		worker, token, err = a.Delegate(token, worker.ID, scope, time.Now().Add(30*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
	}
	obs, err := checkout.Observe(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.BindTask(owner, TaskBinding{"task-a", "project", obs.Repository, true}); err != nil {
		t.Fatal(err)
	}
	at, err := a.RegisterAttempt(token, worker.ID, AttemptRequest{RequestID: "attempt", TaskID: "task-a", Checkout: dir})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := a.AcquireLease(token, worker.ID, AcquireLeaseRequest{"lease", at.ID, []Resource{{"checkout", ""}}, time.Now().Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := a.PublishAttemptRevision(token, worker.ID, PublishRevisionRequest{"revision", at.ID, lease.ID, lease.Fence, 0})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.SetPublicationPolicy(owner, SetPublicationPolicyRequest{TaskID: "task-a", Repository: "fixture/repo", Visibility: "public"})
	if err != nil {
		t.Fatal(err)
	}
	req := PublicationDecisionRequest{OperationKey: "decision", AttemptID: at.ID, LeaseID: lease.ID, RevisionID: rev.ID, ExpectedFence: lease.Fence, ExpiresAt: time.Now().Add(time.Minute), Action: PublicationAction{Action: policy.IssueUpdate, Repository: "fixture/repo", Issue: "17", PayloadSHA256: hash("fixture payload"), Fields: []publication.Field{publication.FieldTitle, publication.FieldSummary}}}
	return publicationFixture{a, owner, token, worker, at, lease, req, p}
}
func (f publicationFixture) decision(t *testing.T) PublicationDecision {
	t.Helper()
	d, e := f.a.EvaluatePublication(f.token, f.worker.ID, f.req)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func consumption(d PublicationDecision) ConsumePublicationRequest {
	return ConsumePublicationRequest{OperationKey: "consume", DecisionID: d.ID, Action: d.Request.Action}
}
func TestPublicationDecisionAndHistoricalConsumption(t *testing.T) {
	f := publicationSetup(t, false)
	f.req.Action.Fields = append(f.req.Action.Fields, publication.FieldTitle)
	d := f.decision(t)
	if d.Assessment.Verdict != policy.Allow || d.Assessment.Assurance != "cooperative-session" || d.SessionID != f.worker.ID || d.Assessment.Binding.PolicyRevision != f.p.Digest || len(d.Request.Action.Fields) != 2 {
		t.Fatalf("incorrect bound assessment %+v", d)
	}
	d.Request.Action.Fields[0] = "mutated-return"
	d = f.decision(t)
	if d.Request.Action.Fields[0] == "mutated-return" {
		t.Fatal("caller mutated durable assessment")
	}
	req := consumption(d)
	r, err := f.a.ConsumePublication(f.owner, req)
	if err != nil || !r.Historical || r.DecisionID != d.ID {
		t.Fatalf("consume %+v %v", r, err)
	}
	if err = f.a.Revoke(f.owner, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := f.a.ConsumePublication(f.owner, req)
	if err != nil || !reflect.DeepEqual(retry, r) {
		t.Fatalf("historical lost reply %+v %v", retry, err)
	}
	_, err = f.a.ConsumePublication(f.token, req)
	denied(t, err)
	req.OperationKey = "second"
	_, err = f.a.ConsumePublication(f.owner, req)
	denied(t, err)
	req.OperationKey = "consume"
	req.Action.Issue = "18"
	_, err = f.a.ConsumePublication(f.owner, req)
	denied(t, err)
	_, err = f.a.EvaluatePublication(f.token, f.worker.ID, f.req)
	denied(t, err)
}
func TestPublicationOwnerPolicyAndVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		field   publication.Field
		action  policy.Action
		include bool
		want    policy.Verdict
	}{{"deny", publication.FieldTranscripts, policy.IssueUpdate, false, policy.Deny}, {"approval", publication.FieldTranscripts, policy.IssueUpdate, true, policy.NeedsApproval}, {"create", publication.FieldTitle, policy.IssueCreate, false, policy.Deny}, {"closure", publication.FieldIssueClose, policy.IssueUpdate, false, policy.Deny}} {
		t.Run(tc.name, func(t *testing.T) {
			f := publicationSetup(t, false)
			change := SetPublicationPolicyRequest{TaskID: "task-a", Repository: "fixture/repo", Visibility: "public", IncludeInternal: tc.include, ExpectedGeneration: 1}
			_, err := f.a.SetPublicationPolicy(f.token, change)
			denied(t, err)
			if _, err = f.a.SetPublicationPolicy(f.owner, change); err != nil {
				t.Fatal(err)
			}
			_, err = f.a.SetPublicationPolicy(f.owner, change)
			denied(t, err)
			f.req.Action.Fields = []publication.Field{tc.field}
			f.req.Action.Action = tc.action
			d := f.decision(t)
			if d.Assessment.Verdict != tc.want {
				t.Fatalf("want %s got %s", tc.want, d.Assessment.Verdict)
			}
			_, err = f.a.ConsumePublication(f.owner, consumption(d))
			denied(t, err)
			if tc.want == policy.NeedsApproval {
				change.ExpectedGeneration = 2
				change.RecordedInternal = true
				if _, err = f.a.SetPublicationPolicy(f.owner, change); err != nil {
					t.Fatal(err)
				}
				_, err = f.a.ConsumePublication(f.owner, consumption(d))
				denied(t, err)
				f.req.OperationKey = "fresh-approved"
				fresh := f.decision(t)
				if fresh.Assessment.Verdict != policy.Allow {
					t.Fatal("fresh owner policy not used")
				}
			}
		})
	}
}
func TestPublicationBindingGuard(t *testing.T) {
	f := publicationSetup(t, false)
	d := f.decision(t)
	for _, alter := range []func(*PublicationAction){func(a *PublicationAction) { a.Issue = "18" }, func(a *PublicationAction) { a.Repository = "other/repo" }, func(a *PublicationAction) { a.Action = policy.IssueCreate }, func(a *PublicationAction) { a.PayloadSHA256 = hash("different") }, func(a *PublicationAction) { a.Fields = []publication.Field{publication.FieldTitle} }} {
		req := consumption(d)
		alter(&req.Action)
		_, err := f.a.ConsumePublication(f.owner, req)
		if !errors.Is(err, ErrDenied) {
			t.Fatal("changed action binding accepted", err)
		}
	}
	if _, err := f.a.ConsumePublication(f.owner, consumption(d)); err != nil {
		t.Fatal(err)
	}
}
func TestPublicationOneUseGuard(t *testing.T) {
	f := publicationSetup(t, false)
	d := f.decision(t)
	req := consumption(d)
	if _, err := f.a.ConsumePublication(f.owner, req); err != nil {
		t.Fatal(err)
	}
	req.OperationKey = "another-consumer"
	_, err := f.a.ConsumePublication(f.owner, req)
	if !errors.Is(err, ErrDenied) {
		t.Fatal("second consumption accepted", err)
	}
}
func TestPublicationCurrentAuthorityGuard(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, publicationFixture)
	}{
		{"revoked", func(t *testing.T, f publicationFixture) {
			if e := f.a.Revoke(f.owner, f.worker.ID); e != nil {
				t.Fatal(e)
			}
		}},
		{"ancestor", func(t *testing.T, f publicationFixture) {
			if e := f.a.Revoke(f.owner, f.worker.Parent); e != nil {
				t.Fatal(e)
			}
		}},
		{"released", func(t *testing.T, f publicationFixture) {
			_, e := f.a.ReleaseLease(f.token, f.worker.ID, ReleaseLeaseRequest{"release", f.at.ID, f.lease.ID, f.lease.Fence})
			if e != nil {
				t.Fatal(e)
			}
		}},
		{"policy", func(t *testing.T, f publicationFixture) {
			_, e := f.a.SetPublicationPolicy(f.owner, SetPublicationPolicyRequest{TaskID: "task-a", Repository: "fixture/repo", Visibility: "public", ExpectedGeneration: 1})
			if e != nil {
				t.Fatal(e)
			}
		}},
		{"dirty", func(t *testing.T, f publicationFixture) {
			if e := os.WriteFile(filepath.Join(f.at.Observation.Locator, "dirty"), []byte("fixture"), 0600); e != nil {
				t.Fatal(e)
			}
		}},
		{"revision", func(t *testing.T, f publicationFixture) {
			gitTest(t, f.at.Observation.Locator, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "changed")
		}},
		{"fence", func(t *testing.T, f publicationFixture) {
			if e := f.a.transaction(false, true, func(s *state) error { l := s.Leases[f.lease.ID]; l.Fence++; s.Leases[l.ID] = l; return nil }); e != nil {
				t.Fatal(e)
			}
		}},
		{"lease-expiry", func(t *testing.T, f publicationFixture) {
			if e := f.a.transaction(false, true, func(s *state) error {
				l := s.Leases[f.lease.ID]
				l.ExpiresAt = time.Now()
				s.Leases[l.ID] = l
				return nil
			}); e != nil {
				t.Fatal(e)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := publicationSetup(t, tc.name == "ancestor")
			d := f.decision(t)
			tc.change(t, f)
			_, err := f.a.ConsumePublication(f.owner, consumption(d))
			if !errors.Is(err, ErrDenied) {
				t.Fatal("stale current authority accepted", err)
			}
		})
	}
}
func TestPublicationExpiryAndEvaluationAuthentication(t *testing.T) {
	f := publicationSetup(t, false)
	_, err := f.a.EvaluatePublication("forged", f.worker.ID, f.req)
	denied(t, err)
	f.req.ExpiresAt = time.Now().Add(time.Second)
	d := f.decision(t)
	time.Sleep(time.Until(f.req.ExpiresAt) + time.Millisecond)
	_, err = f.a.ConsumePublication(f.owner, consumption(d))
	denied(t, err)
	f.req.OperationKey = "expired"
	_, err = f.a.EvaluatePublication(f.token, f.worker.ID, f.req)
	denied(t, err)
}
func TestPublicationMigrationPreservesV5(t *testing.T) {
	f := nativeReviewSetup(t)
	_ = f.propose(t)
	var before state
	if err := f.a.transaction(false, false, func(s *state) error { before = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	before.Version = 5
	before.PublicationPolicies = nil
	before.PublicationDecisions = nil
	before.PublicationConsumed = nil
	b, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.a.path, b, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = f.a.Authenticate(f.workerToken, f.worker.ID)
	if !errors.Is(err, ErrStorage) {
		t.Fatal("implicit migration", err)
	}
	denied(t, f.a.UpgradePublication(f.workerToken))
	if err = f.a.UpgradePublication(f.owner); err != nil {
		t.Fatal(err)
	}
	if err = f.a.transaction(false, false, func(s *state) error {
		copy := *s
		copy.Version = 5
		copy.PublicationPolicies = nil
		copy.PublicationDecisions = nil
		copy.PublicationConsumed = nil
		if !reflect.DeepEqual(copy, before) {
			t.Fatal("migration lost history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := *f.a
	old.version = 5
	if err = old.Revoke(f.owner, f.worker.ID); !errors.Is(err, ErrStorage) {
		t.Fatal("old writer accepted", err)
	}
}

type publicationProcessInput struct {
	Path, Owner string
	Binding     Binding
	Request     ConsumePublicationRequest
}

func TestPublicationConsumeChild(t *testing.T) {
	path := os.Getenv("DACLI_PUBLICATION_TEST_INPUT")
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		os.Exit(1)
	}
	var in publicationProcessInput
	if json.Unmarshal(b, &in) != nil {
		os.Exit(1)
	}
	a, err := Open(in.Path, in.Binding, &testAdmission{})
	if err != nil {
		os.Exit(1)
	}
	_, err = a.ConsumePublication(in.Owner, in.Request)
	if errors.Is(err, ErrDenied) {
		os.Exit(3)
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
func publicationProcess(t *testing.T, f publicationFixture, req ConsumePublicationRequest) *exec.Cmd {
	t.Helper()
	b, err := json.Marshal(publicationProcessInput{f.a.path, f.owner, f.a.binding, req})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input.json")
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPublicationConsumeChild$")
	cmd.WaitDelay = 250 * time.Millisecond
	t.Cleanup(func() {
		cancel()
		// Only this helper-created child can be terminated or reaped here.
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "DACLI_PUBLICATION_TEST_INPUT=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "DACLI_PUBLICATION_TEST_INPUT="+path)
	return cmd
}
func TestPublicationSeparateProcessOneUseAndLostReply(t *testing.T) {
	f := publicationSetup(t, false)
	d := f.decision(t)
	req := consumption(d)
	other := req
	other.OperationKey = "concurrent"
	first, second := publicationProcess(t, f, req), publicationProcess(t, f, other)
	if e := first.Start(); e != nil {
		t.Fatal(e)
	}
	if e := second.Start(); e != nil {
		_ = first.Wait()
		t.Fatal(e)
	}
	_ = first.Wait()
	_ = second.Wait()
	codes := []int{first.ProcessState.ExitCode(), second.ProcessState.ExitCode()}
	if !((codes[0] == 0 && codes[1] == 3) || (codes[0] == 3 && codes[1] == 0)) {
		t.Fatalf("expected one transition, codes %v", codes)
	}
	if codes[1] == 0 {
		req = other
	}
	// Child committed and exited without sending a receipt: recover only history.
	a, e := Open(f.a.path, f.a.binding)
	if e != nil {
		t.Fatal(e)
	}
	r, e := a.ConsumePublication(f.owner, req)
	if e != nil || !r.Historical {
		t.Fatalf("lost reply %+v %v", r, e)
	}
	if e = a.transaction(false, false, func(s *state) error {
		if len(s.PublicationConsumed) != 1 {
			t.Fatal("multiple transitions")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	req.OperationKey = "after-restart"
	_, e = a.ConsumePublication(f.owner, req)
	denied(t, e)
}

func TestPublicationFreshAdmissionAndPostObservationExpiry(t *testing.T) {
	for _, mode := range []string{"missing", "blocked", "expired-during-observation"} {
		t.Run(mode, func(t *testing.T) {
			f := publicationSetup(t, false)
			if mode == "expired-during-observation" {
				f.req.ExpiresAt = time.Now().Add(time.Second)
			}
			d := f.decision(t)
			switch mode {
			case "missing":
				f.a.admission = nil
			case "blocked":
				f.a.admission = &testAdmission{err: ErrDenied}
			default:
				f.a.admission = &testAdmission{delayUntil: f.req.ExpiresAt.Add(time.Millisecond)}
			}
			_, err := f.a.ConsumePublication(f.owner, consumption(d))
			if !errors.Is(err, ErrDenied) {
				t.Fatal("stale admission or expired authority accepted", err)
			}
			f.req.OperationKey = "new-evaluation"
			_, err = f.a.EvaluatePublication(f.token, f.worker.ID, f.req)
			denied(t, err)
		})
	}
}
