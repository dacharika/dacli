// Package contracts defines governance/v1 metadata, not authenticated host integration.
package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const Version = "governance/v1"

type Record struct {
	Schema    string `json:"schema"`
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
}

type Human struct {
	Record
	Credential string `json:"credential"` // Reference to a verified credential, never its secret.
}
type Installation struct {
	Record
	Human string `json:"human"`
}
type Authority struct {
	Record
	Installation string `json:"installation"`
	Epoch        uint64 `json:"epoch"`
}
type HostCapability struct {
	Record
	Installation string   `json:"installation"`
	Host         string   `json:"host"`
	Assurance    string   `json:"assurance"`
	Actions      []string `json:"actions"`
}
type Session struct {
	Record
	Kind       string `json:"kind"`   // host-session or agent-session
	Parent     string `json:"parent"` // installation or host-session respectively
	Human      string `json:"human"`
	Capability string `json:"capability"`
	Delegation string `json:"delegation"` // Explicit delegation reference; not a credential.
}
type Attempt struct {
	Record
	Session  string `json:"session"`
	Task     string `json:"task"`
	Revision string `json:"revision"`
}
type Lease struct {
	Record
	Authority string    `json:"authority"`
	Epoch     uint64    `json:"epoch"`
	Attempt   string    `json:"attempt"`
	Resource  string    `json:"resource"`
	Fence     uint64    `json:"fence"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Evidence struct {
	Record
	Attempt    string `json:"attempt"`
	Session    string `json:"session"`
	Revision   string `json:"revision"`
	Digest     string `json:"digest"`
	Origin     string `json:"origin"` // observation or authenticated-host
	Capability string `json:"capability"`
}
type Decision struct {
	Record
	Authority string    `json:"authority"`
	Epoch     uint64    `json:"epoch"`
	Human     string    `json:"human"`
	Session   string    `json:"session"`
	Attempt   string    `json:"attempt"`
	Lease     string    `json:"lease"`
	Fence     uint64    `json:"fence"`
	Revision  string    `json:"revision"`
	Action    string    `json:"action"` // completion, acceptance, merge, deployment
	Assurance string    `json:"assurance"`
	Evidence  []string  `json:"evidence"`
	Previous  string    `json:"previous,omitempty"`
	DecidedAt time.Time `json:"decided_at"`
}

// Bundle is a complete validation snapshot for one authority and workspace.
// Validate does not verify credentials, signatures, artifact bytes or live leases.
type Bundle struct {
	Schema        string           `json:"schema"`
	Workspace     string           `json:"workspace"`
	Humans        []Human          `json:"humans"`
	Installations []Installation   `json:"installations"`
	Authorities   []Authority      `json:"authorities"`
	Capabilities  []HostCapability `json:"capabilities"`
	Sessions      []Session        `json:"sessions"`
	Attempts      []Attempt        `json:"attempts"`
	Leases        []Lease          `json:"leases"`
	Evidence      []Evidence       `json:"evidence"`
	Decisions     []Decision       `json:"decisions"`
}

// Decode rejects unknown fields, trailing documents and incompatible versions.
func Decode(data []byte) (*Bundle, error) {
	var b Bundle
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing document")
		}
		return nil, fmt.Errorf("trailing document: %w", err)
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &b, nil
}

func required(values ...string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}
func rank(a string) int {
	switch a {
	case "observed":
		return 1
	case "authenticated":
		return 2
	case "human-approved":
		return 3
	}
	return 0
}
func floor(action string) int {
	switch action {
	case "completion":
		return 1
	case "acceptance", "merge":
		return 2
	case "deployment":
		return 3
	}
	return 0
}
func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// Validate resolves every reference, including historical lease fences. A caller
// must separately compare the authority epoch and fence with durable live state.
func (b Bundle) Validate() error {
	if b.Schema != Version {
		return fmt.Errorf("incompatible schema %q", b.Schema)
	}
	if !required(b.Workspace) {
		return fmt.Errorf("missing workspace")
	}
	ids := map[string]bool{}
	check := func(r Record) error {
		if r.Schema != Version {
			return fmt.Errorf("%s: incompatible schema", r.ID)
		}
		if !required(r.ID, r.Workspace) || r.Workspace != b.Workspace {
			return fmt.Errorf("%s: invalid workspace or identity", r.ID)
		}
		if ids[r.ID] {
			return fmt.Errorf("duplicate identity %s", r.ID)
		}
		ids[r.ID] = true
		return nil
	}
	humans := map[string]Human{}
	for _, h := range b.Humans {
		if err := check(h.Record); err != nil {
			return err
		}
		if !required(h.Credential) {
			return fmt.Errorf("%s: missing credential", h.ID)
		}
		humans[h.ID] = h
	}
	installations := map[string]Installation{}
	for _, i := range b.Installations {
		if err := check(i.Record); err != nil {
			return err
		}
		if _, ok := humans[i.Human]; !ok {
			return fmt.Errorf("%s: invalid human", i.ID)
		}
		installations[i.ID] = i
	}
	if len(b.Authorities) != 1 {
		return fmt.Errorf("workspace requires one authority")
	}
	authority := b.Authorities[0]
	if err := check(authority.Record); err != nil {
		return err
	}
	if _, ok := installations[authority.Installation]; !ok || authority.Epoch == 0 {
		return fmt.Errorf("invalid authority installation or epoch")
	}
	caps := map[string]HostCapability{}
	for _, c := range b.Capabilities {
		if err := check(c.Record); err != nil {
			return err
		}
		if _, ok := installations[c.Installation]; !ok || !required(c.Host) || rank(c.Assurance) == 0 || len(c.Actions) == 0 {
			return fmt.Errorf("%s: invalid host capability", c.ID)
		}
		for _, a := range c.Actions {
			if floor(a) == 0 {
				return fmt.Errorf("%s: invalid capability action", c.ID)
			}
		}
		caps[c.ID] = c
	}
	sessions := map[string]Session{}
	for _, s := range b.Sessions {
		if err := check(s.Record); err != nil {
			return err
		}
		sessions[s.ID] = s
	}
	for _, s := range b.Sessions {
		c, ok := caps[s.Capability]
		if !ok || !required(s.Delegation) {
			return fmt.Errorf("%s: invalid capability or delegation", s.ID)
		}
		if _, ok := humans[s.Human]; !ok {
			return fmt.Errorf("%s: invalid session human", s.ID)
		}
		switch s.Kind {
		case "host-session":
			i, ok := installations[s.Parent]
			if !ok || c.Installation != i.ID || i.Human != s.Human {
				return fmt.Errorf("%s: invalid host parent", s.ID)
			}
		case "agent-session":
			p, ok := sessions[s.Parent]
			if !ok || p.Kind != "host-session" || p.Human != s.Human || p.Capability != s.Capability {
				return fmt.Errorf("%s: invalid agent parent", s.ID)
			}
		default:
			return fmt.Errorf("%s: invalid session kind", s.ID)
		}
	}
	attempts := map[string]Attempt{}
	for _, a := range b.Attempts {
		if err := check(a.Record); err != nil {
			return err
		}
		s, ok := sessions[a.Session]
		if !ok || s.Kind != "agent-session" || !required(a.Task, a.Revision) {
			return fmt.Errorf("%s: invalid attempt binding", a.ID)
		}
		attempts[a.ID] = a
	}
	leases := map[string]Lease{}
	for _, l := range b.Leases {
		if err := check(l.Record); err != nil {
			return err
		}
		if _, ok := attempts[l.Attempt]; !ok || l.Authority != authority.ID || l.Epoch != authority.Epoch || l.Fence == 0 || !required(l.Resource) || l.IssuedAt.IsZero() || !l.ExpiresAt.After(l.IssuedAt) {
			return fmt.Errorf("%s: invalid lease binding", l.ID)
		}
		for _, p := range leases {
			if p.Resource == l.Resource {
				if p.Fence == l.Fence || (l.IssuedAt.Before(p.ExpiresAt) && p.IssuedAt.Before(l.ExpiresAt)) || (l.IssuedAt.After(p.IssuedAt) && l.Fence < p.Fence) || (p.IssuedAt.After(l.IssuedAt) && p.Fence < l.Fence) {
					return fmt.Errorf("%s: conflicting lease fence", l.ID)
				}
			}
		}
		leases[l.ID] = l
	}
	evidence := map[string]Evidence{}
	for _, e := range b.Evidence {
		if err := check(e.Record); err != nil {
			return err
		}
		a, ok := attempts[e.Attempt]
		s := sessions[a.Session]
		c := caps[e.Capability]
		if !ok || e.Session != a.Session || e.Revision != a.Revision || e.Capability != s.Capability || !strings.HasPrefix(e.Digest, "sha256:") || len(e.Digest) != 71 {
			return fmt.Errorf("%s: invalid provenance", e.ID)
		}
		for _, ch := range strings.TrimPrefix(e.Digest, "sha256:") {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				return fmt.Errorf("%s: invalid provenance digest", e.ID)
			}
		}
		if e.Origin != "observation" && e.Origin != "authenticated-host" || e.Origin == "authenticated-host" && rank(c.Assurance) < 2 {
			return fmt.Errorf("%s: invalid provenance origin", e.ID)
		}
		evidence[e.ID] = e
	}
	decisions := map[string]Decision{}
	for _, d := range b.Decisions {
		if err := check(d.Record); err != nil {
			return err
		}
		decisions[d.ID] = d
	}
	previousAction := map[string]string{"acceptance": "completion", "merge": "acceptance", "deployment": "merge"}
	for _, d := range b.Decisions {
		a, ok := attempts[d.Attempt]
		s := sessions[d.Session]
		c := caps[s.Capability]
		l, leaseOK := leases[d.Lease]
		if !ok || d.Session != a.Session || d.Human != s.Human || d.Revision != a.Revision || d.Authority != authority.ID || d.Epoch != authority.Epoch {
			return fmt.Errorf("%s: invalid lifecycle binding", d.ID)
		}
		if !leaseOK || l.Attempt != a.ID || d.Fence != l.Fence || d.DecidedAt.Before(l.IssuedAt) || !d.DecidedAt.Before(l.ExpiresAt) {
			return fmt.Errorf("%s: invalid decision lease fence", d.ID)
		}
		if floor(d.Action) == 0 || rank(d.Assurance) < floor(d.Action) || rank(d.Assurance) > rank(c.Assurance) || !contains(c.Actions, d.Action) {
			return fmt.Errorf("%s: insufficient action assurance", d.ID)
		}
		if len(d.Evidence) == 0 {
			return fmt.Errorf("%s: missing decision evidence", d.ID)
		}
		for _, id := range d.Evidence {
			e, ok := evidence[id]
			if !ok || e.Attempt != a.ID || e.Revision != d.Revision {
				return fmt.Errorf("%s: invalid decision provenance", d.ID)
			}
		}
		if d.Action == "completion" {
			if d.Previous != "" {
				return fmt.Errorf("%s: completion has predecessor", d.ID)
			}
			continue
		}
		p, ok := decisions[d.Previous]
		if !ok || p.Action != previousAction[d.Action] || p.Attempt != d.Attempt || p.Revision != d.Revision || p.DecidedAt.After(d.DecidedAt) {
			return fmt.Errorf("%s: invalid lifecycle predecessor", d.ID)
		}
	}
	return nil
}
