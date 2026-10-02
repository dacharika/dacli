package contracts

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../contracts/governance/v1/testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func valid(t *testing.T) *Bundle {
	t.Helper()
	b, err := Decode(fixture(t, "valid"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestFixtures(t *testing.T) {
	valid(t)
	for name, want := range map[string]string{"missing-workspace": "missing workspace", "incompatible": "incompatible schema", "cross-workspace": "invalid workspace", "invalid-provenance": "invalid provenance", "invalid-lifecycle": "invalid lifecycle predecessor"} {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(fixture(t, name))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
		})
	}
}
func TestRejectBindings(t *testing.T) {
	tests := []struct {
		name, want string
		mutate     func(*Bundle)
	}{
		{"record-version", "incompatible schema", func(b *Bundle) { b.Sessions[0].Schema = "" }},
		{"record-workspace", "invalid workspace", func(b *Bundle) { b.Attempts[0].Workspace = "" }},
		{"duplicate", "duplicate identity", func(b *Bundle) { b.Attempts[0].ID = b.Sessions[0].ID }},
		{"multiple-authorities", "one authority", func(b *Bundle) { b.Authorities = append(b.Authorities, b.Authorities[0]) }},
		{"credential", "missing credential", func(b *Bundle) { b.Humans[0].Credential = "" }},
		{"delegation", "delegation", func(b *Bundle) { b.Sessions[1].Delegation = "" }},
		{"session-parent", "invalid agent parent", func(b *Bundle) { b.Sessions[1].Parent = "agent-1" }},
		{"installation-human", "invalid human", func(b *Bundle) { b.Installations[0].Human = "missing" }},
		{"host-parent", "invalid host parent", func(b *Bundle) { b.Sessions[0].Parent = "missing" }},
		{"capability", "invalid host capability", func(b *Bundle) { b.Capabilities[0].Installation = "missing" }},
		{"attempt", "invalid attempt binding", func(b *Bundle) { b.Attempts[0].Session = "host-1" }},
		{"epoch", "invalid lease binding", func(b *Bundle) { b.Leases[0].Epoch = 2 }},
		{"overlap", "conflicting lease fence", func(b *Bundle) { l := b.Leases[0]; l.ID = "lease-2"; l.Fence++; b.Leases = append(b.Leases, l) }},
		{"regressing-fence", "conflicting lease fence", func(b *Bundle) {
			l := b.Leases[0]
			l.ID = "lease-2"
			l.Fence--
			l.IssuedAt = l.ExpiresAt
			l.ExpiresAt = l.ExpiresAt.Add(3600000000000)
			b.Leases = append(b.Leases, l)
		}},
		{"digest", "invalid provenance", func(b *Bundle) { b.Evidence[0].Digest = "sha256:" + strings.Repeat("z", 64) }},
		{"origin", "invalid provenance origin", func(b *Bundle) { b.Evidence[0].Origin = "native-authenticated" }},
		{"observed-origin", "invalid provenance origin", func(b *Bundle) { b.Capabilities[0].Assurance = "observed" }},
		{"provenance-session", "invalid provenance", func(b *Bundle) { b.Evidence[0].Session = "host-1" }},
		{"decision-revision", "invalid lifecycle binding", func(b *Bundle) { b.Decisions[0].Revision = "other" }},
		{"decision-human", "invalid lifecycle binding", func(b *Bundle) { b.Decisions[0].Human = "other" }},
		{"decision-fence", "invalid decision lease fence", func(b *Bundle) { b.Decisions[0].Fence = 6 }},
		{"expired", "invalid decision lease fence", func(b *Bundle) { b.Decisions[0].DecidedAt = b.Leases[0].ExpiresAt }},
		{"assurance", "insufficient action assurance", func(b *Bundle) { b.Decisions[3].Assurance = "authenticated" }},
		{"action-capability", "insufficient action assurance", func(b *Bundle) { b.Capabilities[0].Actions = []string{"completion"} }},
		{"missing-evidence", "missing decision evidence", func(b *Bundle) { b.Decisions[0].Evidence = nil }},
		{"unknown-evidence", "invalid decision provenance", func(b *Bundle) { b.Decisions[0].Evidence = []string{"missing"} }},
		{"predecessor", "invalid lifecycle predecessor", func(b *Bundle) { b.Decisions[2].Previous = "decision-0" }},
		{"completion-predecessor", "completion has predecessor", func(b *Bundle) { b.Decisions[0].Previous = "decision-3" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := valid(t)
			tt.mutate(b)
			err := b.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}
func TestDecodeClosedContract(t *testing.T) {
	data := fixture(t, "valid")
	for _, bad := range [][]byte{append(append([]byte{}, data...), data...), []byte(strings.Replace(string(data), `"host": "fixture-host"`, `"host": "fixture-host", "secret": "no"`, 1))} {
		if _, err := Decode(bad); err == nil {
			t.Fatal("accepted trailing document or unknown field")
		}
	}
	b := valid(t)
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(data); err != nil {
		t.Fatal(err)
	}
}
