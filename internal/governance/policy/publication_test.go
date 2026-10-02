package policy

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/publication"
)

func fixture() Observation {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Observation{ActorID: "actor", SessionID: "session", AttemptID: "attempt", WorkspaceID: "workspace", InstallationID: "installation", AuthorityID: "authority", Repository: "org/repo", Resource: "org/repo", Action: IssueCreate, Fields: []publication.Field{publication.FieldTitle}, ProjectedPayloadSHA256: strings.Repeat("a", 64), PolicyRevision: "revision", LeaseID: "lease", LeaseGeneration: 1, NotBefore: now, ObservedAt: now, ExpiresAt: now.Add(time.Hour), Visibility: "public"}
}
func TestPublicationParity(t *testing.T) {
	fields := []publication.Field{publication.FieldTitle, publication.FieldSummary, publication.FieldAcceptance, publication.FieldIssueRef, publication.FieldFindings, publication.FieldVerdicts, publication.FieldDecisions, publication.FieldTranscripts, publication.FieldJournals, publication.FieldRecovery, publication.FieldLocalPaths, publication.FieldCosts, publication.FieldIdentities, publication.FieldIssueClose, "unknown"}
	for _, visibility := range []string{"public", "private", "unknown", "other-host"} {
		for _, explicit := range []bool{false, true} {
			for _, recorded := range []bool{false, true} {
				for _, field := range fields {
					in := fixture()
					in.Visibility = visibility
					in.IncludeInternal = explicit
					in.RecordedInternal = recorded
					in.Fields = []publication.Field{field}
					p := publication.New(in.Repository, visibility, explicit, recorded, false)
					want := Deny
					if p.Allows(field) {
						want = Allow
					} else if p.Visibility == publication.VisibilityPublic && explicit && !recorded && publication.New(in.Repository, visibility, explicit, true, false).Allows(field) {
						want = NeedsApproval
					}
					got := EvaluatePublication(in)
					if got.Verdict != want || got.Assurance != "observed/advisory" {
						t.Fatalf("visibility=%s explicit=%v recorded=%v field=%s got=%s want=%s", visibility, explicit, recorded, field, got.Verdict, want)
					}
					if len(got.Reasons) == 0 || len(got.Obligations) < 2 {
						t.Fatal("missing reasons/obligations")
					}
				}
			}
		}
	}
}
func TestBindingsChangeDigest(t *testing.T) {
	base := fixture()
	original := EvaluatePublication(base).ActionDigest
	mutations := map[string]func(*Observation){
		"actor": func(b *Observation) { b.ActorID += "2" }, "session": func(b *Observation) { b.SessionID += "2" }, "attempt": func(b *Observation) { b.AttemptID += "2" }, "workspace": func(b *Observation) { b.WorkspaceID += "2" }, "installation": func(b *Observation) { b.InstallationID += "2" }, "authority": func(b *Observation) { b.AuthorityID += "2" }, "repository": func(b *Observation) { b.Repository = "org/other"; b.Resource = b.Repository }, "resource": func(b *Observation) { b.Resource = "3" }, "action": func(b *Observation) { b.Action = IssueUpdate; b.Resource = "2" }, "fields": func(b *Observation) { b.Fields = []publication.Field{publication.FieldSummary} }, "payload": func(b *Observation) { b.ProjectedPayloadSHA256 = strings.Repeat("b", 64) }, "policy": func(b *Observation) { b.PolicyRevision += "2" }, "lease": func(b *Observation) { b.LeaseID += "2" }, "generation": func(b *Observation) { b.LeaseGeneration++ }, "expiry": func(b *Observation) { b.ExpiresAt = b.ExpiresAt.Add(time.Second) }, "start": func(b *Observation) { b.NotBefore = b.NotBefore.Add(-time.Second) }, "observation": func(b *Observation) { b.ObservedAt = b.ObservedAt.Add(time.Second) }, "visibility": func(b *Observation) { b.Visibility = "private" }, "request": func(b *Observation) { b.IncludeInternal = true }, "recorded": func(b *Observation) { b.RecordedInternal = true },
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			b := base
			change(&b)
			if EvaluatePublication(b).ActionDigest == original {
				t.Fatal("binding mutation did not change digest")
			}
		})
	}
}
func TestSortedFieldsAndImmutability(t *testing.T) {
	in := fixture()
	in.Fields = []publication.Field{publication.FieldSummary, publication.FieldTitle, publication.FieldSummary}
	before := in
	before.Fields = append([]publication.Field{}, in.Fields...)
	got := EvaluatePublication(in)
	other := in
	other.Fields = []publication.Field{publication.FieldTitle, publication.FieldSummary}
	if got.ActionDigest != EvaluatePublication(other).ActionDigest || !reflect.DeepEqual(in, before) {
		t.Fatal("canonical set or immutability failed")
	}
	got.Binding.Fields[0] = "changed"
	if !reflect.DeepEqual(in, before) {
		t.Fatal("assessment aliases input")
	}
}
func TestMalformedRefuses(t *testing.T) {
	changes := []func(*Observation){func(b *Observation) { b.ActorID = "" }, func(b *Observation) { b.SessionID = "\n" }, func(b *Observation) { b.Repository = "../repo" }, func(b *Observation) { b.Resource = "wrong" }, func(b *Observation) { b.Action = "issue-close" }, func(b *Observation) { b.Action = IssueUpdate; b.Resource = "0" }, func(b *Observation) { b.ProjectedPayloadSHA256 = "bad" }, func(b *Observation) { b.ProjectedPayloadSHA256 = strings.Repeat("A", 64) }, func(b *Observation) { b.LeaseGeneration = 0 }, func(b *Observation) { b.Fields = nil }, func(b *Observation) { b.ExpiresAt = b.ObservedAt }, func(b *Observation) { b.NotBefore = b.ObservedAt.Add(time.Second) }, func(b *Observation) { b.ObservedAt = time.Time{} }, func(b *Observation) {
		b.Fields = []publication.Field{publication.FieldFindings, "unknown"}
		b.IncludeInternal = true
	}}
	for n, change := range changes {
		b := fixture()
		change(&b)
		if EvaluatePublication(b).Verdict != Deny {
			t.Fatalf("case %d did not refuse", n)
		}
	}
}
func TestProjectionUsesPublicationSanitize(t *testing.T) {
	for _, v := range []string{"public", "private", "unknown"} {
		in := fixture()
		in.Visibility = v
		value := "secret=abc /home/private a-agent"
		if ProjectText(in, value) != publication.New(in.Repository, v, false, false, false).Sanitize(value) {
			t.Fatal("projection differs")
		}
	}
}

func TestUpdateAndApprovalObligations(t *testing.T) {
	in := fixture()
	in.Action = IssueUpdate
	in.Resource = "12"
	if got := EvaluatePublication(in); got.Verdict != Allow || got.Binding.Resource != "12" {
		t.Fatal("valid update denied or resource not bound")
	}
	in.Fields = []publication.Field{publication.FieldFindings}
	in.IncludeInternal = true
	got := EvaluatePublication(in)
	if got.Verdict != NeedsApproval || got.Reasons[0].Code != DisclosureApproval || got.Obligations[len(got.Obligations)-1] != RecordedDisclosure {
		t.Fatal("missing approval requirement")
	}
	in.RecordedInternal = true
	if EvaluatePublication(in).Verdict != Allow {
		t.Fatal("observed recorded disclosure should allow advisory assessment")
	}
	in.Visibility = "unrecognized"
	if EvaluatePublication(in).Verdict != Deny {
		t.Fatal("unknown visibility disclosed internal fields")
	}
	in = fixture()
	in.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if EvaluatePublication(in).Verdict != Deny {
		t.Fatal("uncanonical time allowed")
	}
}
