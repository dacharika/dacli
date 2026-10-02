// Package policy evaluates historical observations, never bearer authority.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/publication"
)

type Verdict string

const (
	Allow         Verdict = "allow"
	Deny          Verdict = "deny"
	NeedsApproval Verdict = "needs-approval"
)

type ReasonCode string

const (
	InvalidRequest     ReasonCode = "invalid-request"
	UnsupportedAction  ReasonCode = "unsupported-action"
	UnsupportedClosure ReasonCode = "unsupported-closure"
	FieldDenied        ReasonCode = "field-denied"
	DisclosureApproval ReasonCode = "disclosure-approval-required"
	PolicyAllows       ReasonCode = "policy-allows"
)

type Obligation string

const (
	CurrentAuthorityConsumption Obligation = "atomic-current-authority-single-use-consumption"
	ExactProjectedPayload       Obligation = "verify-exact-projected-payload"
	RecordedDisclosure          Obligation = "record-repository-disclosure-authority"
)

type Action string

const (
	IssueCreate Action = "issue-create"
	IssueUpdate Action = "issue-update"
)

// Observation is caller-supplied data. No identifier or authority flag is authenticated.
type Observation struct {
	ActorID                string
	SessionID              string
	AttemptID              string
	WorkspaceID            string
	InstallationID         string
	AuthorityID            string
	Repository             string
	Resource               string // create: repository; update: positive decimal issue number
	Action                 Action
	Fields                 []publication.Field
	ProjectedPayloadSHA256 string // lowercase hex SHA256 of the exact outbound bytes
	PolicyRevision         string
	LeaseID                string
	LeaseGeneration        uint64
	NotBefore              time.Time
	ExpiresAt              time.Time
	ObservedAt             time.Time
	Visibility             string
	IncludeInternal        bool
	RecordedInternal       bool
}
type Reason struct {
	Code   ReasonCode
	Field  publication.Field
	Detail string
}
type Assessment struct {
	Verdict      Verdict
	Assurance    string
	Binding      Observation
	ActionDigest string
	Reasons      []Reason
	Obligations  []Obligation
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var issuePattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// EvaluatePublication neither publishes nor reads or writes authority. Even an
// allow requires later atomic consumption against current durable authority.
func EvaluatePublication(in Observation) Assessment {
	b := in
	b.Fields = append([]publication.Field{}, in.Fields...)
	sort.Slice(b.Fields, func(i, j int) bool { return b.Fields[i] < b.Fields[j] })
	fields := b.Fields[:0]
	for _, f := range b.Fields {
		if len(fields) == 0 || fields[len(fields)-1] != f {
			fields = append(fields, f)
		}
	}
	b.Fields = fields
	b.NotBefore = b.NotBefore.UTC()
	b.ExpiresAt = b.ExpiresAt.UTC()
	b.ObservedAt = b.ObservedAt.UTC()
	a := Assessment{Verdict: Allow, Assurance: "observed/advisory", Binding: b, Reasons: []Reason{}, Obligations: []Obligation{CurrentAuthorityConsumption, ExactProjectedPayload}}
	encoded, encodeErr := json.Marshal(struct {
		Domain  string
		Binding Observation
	}{"dacli-publication-assessment/v1", b})
	if encodeErr != nil {
		a.Verdict = Deny
		a.Reasons = append(a.Reasons, Reason{Code: InvalidRequest, Detail: "binding cannot be canonically encoded"})
		return a
	}
	sum := sha256.Sum256(encoded)
	a.ActionDigest = hex.EncodeToString(sum[:])
	refuse := func(code ReasonCode, field publication.Field, detail string) {
		a.Verdict = Deny
		a.Reasons = append(a.Reasons, Reason{code, field, detail})
	}
	for _, id := range []string{b.ActorID, b.SessionID, b.AttemptID, b.WorkspaceID, b.InstallationID, b.AuthorityID, b.PolicyRevision, b.LeaseID} {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\x00\r\n") {
			refuse(InvalidRequest, "", "invalid binding identifier")
			break
		}
	}
	payload, err := hex.DecodeString(b.ProjectedPayloadSHA256)
	if err != nil || len(payload) != sha256.Size || strings.ToLower(b.ProjectedPayloadSHA256) != b.ProjectedPayloadSHA256 {
		refuse(InvalidRequest, "", "expected lowercase SHA256 projected payload digest")
	}
	if !repositoryPattern.MatchString(b.Repository) || b.LeaseGeneration == 0 || len(b.Fields) == 0 || b.NotBefore.IsZero() || b.ExpiresAt.IsZero() || b.ObservedAt.IsZero() || !b.NotBefore.Before(b.ExpiresAt) || b.ObservedAt.Before(b.NotBefore) || !b.ObservedAt.Before(b.ExpiresAt) {
		refuse(InvalidRequest, "", "invalid repository, lease or validity window")
	}
	switch b.Action {
	case IssueCreate:
		if b.Resource != b.Repository {
			refuse(InvalidRequest, "", "create resource must be exact repository")
		}
	case IssueUpdate:
		if !issuePattern.MatchString(b.Resource) {
			refuse(InvalidRequest, "", "update resource must be positive issue number")
		}
	default:
		refuse(UnsupportedAction, "", "only issue create/update are supported")
	}
	p := publication.New(b.Repository, b.Visibility, b.IncludeInternal, b.RecordedInternal, false)
	// A second policy with recorded authority identifies approval-eligible fields
	// without reproducing publication's internal field list.
	approved := publication.New(b.Repository, b.Visibility, b.IncludeInternal, true, false)
	for _, f := range b.Fields {
		if f == publication.FieldIssueClose {
			refuse(UnsupportedClosure, f, p.Reason(f))
			continue
		}
		if p.Allows(f) {
			continue
		}
		if p.Visibility == publication.VisibilityPublic && b.IncludeInternal && !b.RecordedInternal && approved.Allows(f) {
			if a.Verdict != Deny {
				a.Verdict = NeedsApproval
			}
			a.Reasons = append(a.Reasons, Reason{DisclosureApproval, f, p.Reason(f)})
		} else {
			refuse(FieldDenied, f, p.Reason(f))
		}
	}
	if a.Verdict == NeedsApproval {
		a.Obligations = append(a.Obligations, RecordedDisclosure)
	}
	if len(a.Reasons) == 0 {
		a.Reasons = append(a.Reasons, Reason{Code: PolicyAllows, Detail: "supplied publication policy allows requested fields"})
	}
	return a
}

// ProjectText applies existing content sanitization only. It does not decide
// permission for any field, authenticate authority, or certify payload bytes.
func ProjectText(in Observation, value string) string {
	return publication.New(in.Repository, in.Visibility, in.IncludeInternal, in.RecordedInternal, false).Sanitize(value)
}
