package rolebundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/team"
)

type legacyReleaseContent struct {
	SchemaVersion int         `json:"schema_version"`
	Version       string      `json:"version"`
	Role          team.Role   `json:"role"`
	References    []Reference `json:"references"`
}

func fixtureContent() ReleaseContent {
	return ReleaseContent{SchemaVersion: SchemaVersion, Version: "1.2.3", Role: team.Role{Name: "implementer", Summary: "build", Skills: []string{"go"}, Scope: []string{"internal/**"}, OutOfScope: []string{"secrets/**"}, Shortcuts: []string{"test"}, Grant: "rw", EscalateTo: []string{"reviewer"}, WIP: 2, Kind: "implementer", Runtime: "codex", Model: "model-x", Profile: team.ModelProfile{ID: "model-x", CostTier: 2, MaxTaskPoints: 5, ContextLimit: 80000, CapabilityTags: []string{"code"}}, FallbackTo: []string{"senior"}, MaxPoints: 7, Prompt: "inspect, change, verify"}, References: []Reference{{Kind: "skill", ID: "go", Version: "v2", Digest: "sha256:" + strings.Repeat("a", 64)}, {Kind: "policy", ID: "native", Version: "v1", Digest: "sha256:" + strings.Repeat("b", 64)}}}
}

func TestReleaseSignsNormalizedRoleAndRejectsTampering(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	release, err := Sign(fixtureContent(), "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Verify(map[string]ed25519.PublicKey{"publisher-a": pub}); err != nil {
		t.Fatal(err)
	}
	changed := release
	changed.Content.Role.Prompt += "\nnew instruction"
	if err := changed.Verify(map[string]ed25519.PublicKey{"publisher-a": pub}); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("tampered prompt accepted: %v", err)
	}
	changed = release
	changed.ContentDigest = "sha256:" + strings.Repeat("0", 64)
	if err := changed.Verify(map[string]ed25519.PublicKey{"publisher-a": pub}); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("tampered digest accepted: %v", err)
	}
	if err := release.Verify(map[string]ed25519.PublicKey{"other": pub}); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("unknown publisher accepted: %v", err)
	}
}

func TestEveryExportedRoleBehaviorLeafChangesDigest(t *testing.T) {
	base := fixtureContent()
	baseDigest, err := ContentDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range roleLeafPaths(reflect.TypeOf(team.Role{}), "") {
		t.Run(path, func(t *testing.T) {
			content := fixtureContent()
			v := reflect.ValueOf(&content.Role).Elem()
			for _, part := range strings.Split(path, ".") {
				v = v.FieldByName(part)
			}
			if !mutateLeaf(v) {
				t.Fatalf("add a digest mutation case for %s", path)
			}
			digest, err := ContentDigest(content)
			if err != nil {
				t.Fatal(err)
			}
			if digest == baseDigest {
				t.Fatalf("behavior field %s is omitted from release digest", path)
			}
		})
	}
}

func roleLeafPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		path := field.Name
		if prefix != "" {
			path = prefix + "." + path
		}
		if field.Type.Kind() == reflect.Struct {
			out = append(out, roleLeafPaths(field.Type, path)...)
		} else {
			out = append(out, path)
		}
	}
	return out
}

func mutateLeaf(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-changed")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(v.Float() + 1)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return false
		}
		v.Set(reflect.Append(v, reflect.ValueOf("changed").Convert(v.Type().Elem())))
	default:
		return false
	}
	return true
}

func TestSemverAndReferenceValidation(t *testing.T) {
	for _, version := range []string{"0.0.0", "1.2.3", "v1.2.3", "1.2.3-rc.1+build.2"} {
		if !validVersion(version) {
			t.Errorf("valid semver rejected: %s", version)
		}
	}
	for _, version := range []string{"", "01.2.3", "1.2", "1.2.3-", "1.2.3+", "1.2.3-01", "1.2.3-alpha.01"} {
		if validVersion(version) {
			t.Errorf("invalid semver accepted: %s", version)
		}
	}
	for _, version := range []string{"v1", "v2", "1.2.3", "policy-rev-7"} {
		if !validReferenceVersion(version) {
			t.Errorf("exact existing policy/skill revision rejected: %s", version)
		}
	}
	for _, version := range []string{"latest", "*", ">=1.0.0", "^1.0.0", "~1.0.0", "1.x", "1.2.*"} {
		if validReferenceVersion(version) {
			t.Errorf("floating reference selector accepted: %s", version)
		}
	}
	content := fixtureContent()
	content.References[0].Version = ""
	if _, err := Sign(content, "publisher-a", make(ed25519.PrivateKey, ed25519.PrivateKeySize)); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("unversioned ref accepted: %v", err)
	}
}

func TestLifecycleSignatureAndFreshness(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	status, err := SignStatus(LifecycleStatus{SchemaVersion: SchemaVersion, RoleID: "implementer", Version: "1.2.3", ContentDigest: "sha256:" + strings.Repeat("c", 64), Generation: 4, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "publisher-a"}, private)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"publisher-a": pub}
	if err := status.Verify(keys, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := status.Verify(keys, status.ExpiresAt); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("expired lifecycle accepted: %v", err)
	}
	status.Generation++
	if err := status.Verify(keys, now); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("tampered lifecycle accepted: %v", err)
	}
}

func contractFor(role team.Role) RoleContract {
	return RoleContract{
		Purpose: role.Summary, Instructions: role.Prompt,
		CapabilityScope: CapabilityScope{Grant: role.Grant, Scope: role.Scope, OutOfScope: role.OutOfScope, Shortcuts: role.Shortcuts},
		Runtime:         RuntimePreference{Runtime: role.Runtime, Model: role.ModelID(), Profile: role.Profile},
		Host:            HostRequirements{Capabilities: []string{"native-swarm-admission", "native-policy-observation"}, Sandbox: "cooperative", Network: "unrestricted", Filesystem: "role-scope", Process: "unrestricted"},
		Budget:          BudgetRequirement{Mode: "native-policy"}, Gates: []string{"swarm", "budget", "preflight", "model-routing"},
		Escalation: role.EscalateTo, Skills: role.Skills,
		MCP: []CapabilityNeed{{ID: "mcp-search", Required: false}}, Tools: []CapabilityNeed{{ID: "read-file", Required: false}},
		Scenarios: []Scenario{
			{ID: "allowed", RequestedActions: []string{"dispatch"}, AllowedActions: []string{"dispatch"}, Gates: currentScenarioGates(), BudgetState: "valid", Escalation: "none", ExpectedOutcome: "allowed"},
			{ID: "forbidden", RequestedActions: []string{"write-secrets"}, AllowedActions: []string{"dispatch"}, ForbiddenActions: []string{"write-secrets"}, BudgetState: "valid", Escalation: "none", ExpectedOutcome: "refused"},
			{ID: "exhausted", RequestedActions: []string{"dispatch"}, AllowedActions: []string{"dispatch"}, BudgetState: "exhausted", Escalation: "none", ExpectedOutcome: "refused"},
			{ID: "missing-gate", RequestedActions: []string{"dispatch"}, AllowedActions: []string{"dispatch"}, Gates: []ScenarioGate{{ID: "preflight", Observed: "missing"}}, BudgetState: "valid", Escalation: "none", ExpectedOutcome: "refused"},
			{ID: "omitted-required-gate", RequestedActions: []string{"dispatch"}, AllowedActions: []string{"dispatch"}, Gates: []ScenarioGate{{ID: "budget", Observed: "current"}, {ID: "model-routing", Observed: "current"}, {ID: "swarm", Observed: "current"}}, BudgetState: "valid", Escalation: "none", ExpectedOutcome: "refused"},
			{ID: "escalate", RequestedActions: []string{"dispatch"}, AllowedActions: []string{"dispatch"}, Gates: currentScenarioGates(), BudgetState: "valid", Escalation: "required", ExpectedOutcome: "escalated"},
		},
	}
}

func currentScenarioGates() []ScenarioGate {
	return []ScenarioGate{{ID: "budget", Observed: "current"}, {ID: "model-routing", Observed: "current"}, {ID: "preflight", Observed: "current"}, {ID: "swarm", Observed: "current"}}
}

func TestReleaseV1CanonicalBytesAndSignatureDomainRemainCompatible(t *testing.T) {
	content := fixtureContent()
	refs, err := canonicalReferences(content.References)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(legacyReleaseContent{SchemaVersion: content.SchemaVersion, Version: content.Version, Role: content.Role, References: refs})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalContent(content)
	if err != nil || string(canonical) != string(legacy) {
		t.Fatalf("schema-1 canonical bytes changed: got %s, want %s, err=%v", canonical, legacy, err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	release, err := Sign(content, "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(public, []byte(signatureDomain+"publisher-a\x00"+release.ContentDigest), release.Signature) {
		t.Fatal("schema-1 signature domain changed")
	}
}

func TestReleaseV2SignsCompleteConstraintContractAndUsesDistinctDomain(t *testing.T) {
	content := fixtureContent()
	content.SchemaVersion = ReleaseContractSchemaVersion
	contract := contractFor(content.Role)
	content.Contract = &contract
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	release, err := Sign(content, "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Verify(map[string]ed25519.PublicKey{"publisher-a": public}); err != nil {
		t.Fatal("v2 release did not verify:", err)
	}
	if !ed25519.Verify(public, []byte(contractSignatureDomain+"publisher-a\x00"+release.ContentDigest), release.Signature) {
		t.Fatal("schema-2 release did not use its distinct signature domain")
	}
	base, err := ContentDigest(content)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*ReleaseContent){
		"purpose":      func(c *ReleaseContent) { c.Role.Summary += " changed"; c.Contract.Purpose = c.Role.Summary },
		"instructions": func(c *ReleaseContent) { c.Role.Prompt += " changed"; c.Contract.Instructions = c.Role.Prompt },
		"capability-scope": func(c *ReleaseContent) {
			c.Role.Scope = append(c.Role.Scope, "pkg/**")
			c.Contract.CapabilityScope.Scope = append(c.Contract.CapabilityScope.Scope, "pkg/**")
		},
		"runtime-model": func(c *ReleaseContent) {
			c.Role.Profile.ID = "other-model"
			c.Contract.Runtime.Model = "other-model"
			c.Contract.Runtime.Profile.ID = "other-model"
		},
		"required-capabilities": func(c *ReleaseContent) { c.Contract.Host.Capabilities = c.Contract.Host.Capabilities[:1] },
		"sandbox":               func(c *ReleaseContent) { c.Contract.Host.Sandbox = "required" },
		"network":               func(c *ReleaseContent) { c.Contract.Host.Network = "disabled" },
		"filesystem":            func(c *ReleaseContent) { c.Contract.Host.Filesystem = "contained" },
		"process":               func(c *ReleaseContent) { c.Contract.Host.Process = "contained" },
		"budget":                func(c *ReleaseContent) { c.Contract.Budget.Mode = "none" },
		"gates":                 func(c *ReleaseContent) { c.Contract.Gates = c.Contract.Gates[:len(c.Contract.Gates)-1] },
		"escalation": func(c *ReleaseContent) {
			c.Role.EscalateTo = append(c.Role.EscalateTo, "human")
			c.Contract.Escalation = append(c.Contract.Escalation, "human")
		},
		"skills": func(c *ReleaseContent) {
			c.Role.Skills = append(c.Role.Skills, "lint")
			c.Contract.Skills = append(c.Contract.Skills, "lint")
		},
		"mcp":       func(c *ReleaseContent) { c.Contract.MCP = append(c.Contract.MCP, CapabilityNeed{ID: "other-mcp"}) },
		"tools":     func(c *ReleaseContent) { c.Contract.Tools = append(c.Contract.Tools, CapabilityNeed{ID: "write-file"}) },
		"scenarios": func(c *ReleaseContent) { c.Contract.Scenarios[0].ExpectedOutcome = "refused" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := content
			copy := *content.Contract
			copy.Host.Capabilities = append([]string(nil), copy.Host.Capabilities...)
			copy.Gates = append([]string(nil), copy.Gates...)
			copy.Escalation = append([]string(nil), copy.Escalation...)
			copy.Skills = append([]string(nil), copy.Skills...)
			copy.MCP = append([]CapabilityNeed(nil), copy.MCP...)
			copy.Tools = append([]CapabilityNeed(nil), copy.Tools...)
			copy.Scenarios = append([]Scenario(nil), copy.Scenarios...)
			copy.Scenarios[0] = content.Contract.Scenarios[0]
			changed.Contract = &copy
			mutate(&changed)
			digest, err := ContentDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if digest == base {
				t.Fatal("contract behavior change was omitted from canonical digest")
			}
		})
	}
	changed := *release.Content.Contract
	changed.Runtime.Runtime = "other-runtime"
	release.Content.Contract = &changed
	if err := release.Verify(map[string]ed25519.PublicKey{"publisher-a": public}); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("conflicting contract/role runtime accepted: %v", err)
	}
}

func TestSignedScenarioFixturesEvaluateDeterministicallyAndBindDigests(t *testing.T) {
	content := fixtureContent()
	content.SchemaVersion = ReleaseContractSchemaVersion
	contract := contractFor(content.Role)
	content.Contract = &contract
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	release, err := Sign(content, "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct{ id, outcome, action string }{
		{"allowed", "allowed", "dispatch"}, {"forbidden", "refused", "refuse"},
		{"exhausted", "refused", "refuse"}, {"missing-gate", "refused", "refuse"},
		{"omitted-required-gate", "refused", "refuse"}, {"escalate", "escalated", "escalate"},
	} {
		result, err := EvaluateScenario(release, map[string]ed25519.PublicKey{"publisher-a": public}, expected.id)
		if err != nil || !result.MatchesExpected || result.EvaluatedOutcome != expected.outcome || result.EvaluatedAction != expected.action {
			t.Fatalf("scenario %s: %+v %v", expected.id, result, err)
		}
		if result.ReleaseDigest != release.ContentDigest || result.RoleID != content.Role.Name || len(result.PolicyReferences) != 1 || result.PolicyReferences[0].Kind != "policy" || !validDigest(result.ScenarioDigest) {
			t.Fatalf("scenario result lost release/policy/scenario binding: %+v", result)
		}
	}
	unsupported := *release.Content.Contract
	unsupported.Host.Network = "disabled"
	release.Content.Contract = &unsupported
	if _, err := EvaluateScenario(release, map[string]ed25519.PublicKey{"publisher-a": public}, "allowed"); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("unsupported host requirement executed scenario: %v", err)
	}
}

func TestReleaseSignVerifyAndCanonicalizationDoNotMutateNestedScenarioGates(t *testing.T) {
	content := fixtureContent()
	content.SchemaVersion = ReleaseContractSchemaVersion
	contract := contractFor(content.Role)
	contract.Scenarios[0].Gates = []ScenarioGate{{ID: "swarm", Observed: "current"}, {ID: "budget", Observed: "current"}}
	content.Contract = &contract
	before := append([]ScenarioGate(nil), content.Contract.Scenarios[0].Gates...)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	release, err := Sign(content, "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Verify(map[string]ed25519.PublicKey{"publisher-a": pub}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(content.Contract.Scenarios[0].Gates, before) {
		t.Fatalf("Sign or Verify mutated caller gate order: got=%v want=%v", content.Contract.Scenarios[0].Gates, before)
	}
	if _, err := CanonicalContent(content); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(content.Contract.Scenarios[0].Gates, before) {
		t.Fatalf("canonicalization mutated caller gate order: got=%v want=%v", content.Contract.Scenarios[0].Gates, before)
	}
}

func TestV2PreservesMirroredEmptySlicesAcrossSigningAndRoundTrip(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"publisher-a": public}
	cases := []struct {
		name    string
		value   []string
		wantNil bool
	}{
		{name: "nil", value: nil, wantNil: true},
		{name: "empty", value: []string{}, wantNil: false},
		{name: "nonempty", value: []string{"one", "two"}, wantNil: false},
	}
	for _, field := range []string{"skills", "escalation"} {
		for _, tc := range cases {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				content := fixtureContent()
				content.SchemaVersion = ReleaseContractSchemaVersion
				contract := contractFor(content.Role)
				if field == "skills" {
					content.Role.Skills = appendExactStrings(tc.value)
					contract.Skills = appendExactStrings(tc.value)
				} else {
					content.Role.EscalateTo = appendExactStrings(tc.value)
					contract.Escalation = appendExactStrings(tc.value)
				}
				content.Contract = &contract
				before, err := json.Marshal(content)
				if err != nil {
					t.Fatal(err)
				}
				release, err := Sign(content, "publisher-a", private)
				if err != nil {
					t.Fatalf("matching %s slices must remain signable: %v", field, err)
				}
				if err := release.Verify(keys); err != nil {
					t.Fatalf("signed release failed verification: %v", err)
				}
				encoded, err := json.Marshal(release)
				if err != nil {
					t.Fatal(err)
				}
				var roundTrip Release
				if err := json.Unmarshal(encoded, &roundTrip); err != nil {
					t.Fatal(err)
				}
				if err := roundTrip.Verify(keys); err != nil {
					t.Fatalf("round-tripped release failed verification: %v", err)
				}
				var got, contractGot []string
				if field == "skills" {
					got = roundTrip.Content.Role.Skills
					contractGot = roundTrip.Content.Contract.Skills
				} else {
					got = roundTrip.Content.Role.EscalateTo
					contractGot = roundTrip.Content.Contract.Escalation
				}
				if (got == nil) != tc.wantNil || (contractGot == nil) != tc.wantNil || !reflect.DeepEqual(got, tc.value) || !reflect.DeepEqual(contractGot, tc.value) {
					t.Fatalf("round trip changed %s nil/empty/value semantics: role=%#v contract=%#v want=%#v", field, got, contractGot, tc.value)
				}
				after, err := json.Marshal(content)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("Sign mutated caller content: err=%v before=%s after=%s", err, before, after)
				}
			})
		}
	}
}

func TestV2StillRejectsMismatchedMirroredNilAndEmptySlices(t *testing.T) {
	for _, field := range []string{"skills", "escalation"} {
		for _, roleEmpty := range []bool{false, true} {
			name := "contract-empty"
			if roleEmpty {
				name = "role-empty"
			}
			t.Run(field+"/"+name, func(t *testing.T) {
				content := fixtureContent()
				content.SchemaVersion = ReleaseContractSchemaVersion
				content.Role.Skills = nil
				content.Role.EscalateTo = nil
				contract := contractFor(content.Role)
				if field == "skills" {
					if roleEmpty {
						content.Role.Skills = []string{}
					} else {
						contract.Skills = []string{}
					}
				} else if roleEmpty {
					content.Role.EscalateTo = []string{}
				} else {
					contract.Escalation = []string{}
				}
				content.Contract = &contract
				if _, err := Sign(content, "publisher-a", make(ed25519.PrivateKey, ed25519.PrivateKeySize)); !errors.Is(err, ErrInvalidRelease) {
					t.Fatalf("mismatched nil and empty %s slices were accepted: %v", field, err)
				}
			})
		}
	}
}

func appendExactStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func TestV2RejectsUnknownRequiredSemantics(t *testing.T) {
	content := fixtureContent()
	content.SchemaVersion = ReleaseContractSchemaVersion
	contract := contractFor(content.Role)
	contract.Host.Capabilities = append(contract.Host.Capabilities, "unseen-host-authority")
	content.Contract = &contract
	if _, err := Sign(content, "publisher-a", make(ed25519.PrivateKey, ed25519.PrivateKeySize)); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("unknown required host capability accepted: %v", err)
	}
	contract = contractFor(content.Role)
	contract.Gates = append(contract.Gates, "unseen-gate")
	content.Contract = &contract
	if _, err := Sign(content, "publisher-a", make(ed25519.PrivateKey, ed25519.PrivateKeySize)); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("unknown required gate accepted: %v", err)
	}
	contract = contractFor(content.Role)
	contract.Tools = append(contract.Tools, CapabilityNeed{ID: "exec", Required: true})
	content.Contract = &contract
	release, err := Sign(content, "publisher-a", make(ed25519.PrivateKey, ed25519.PrivateKeySize))
	if err != nil || ContractSupported(release.Content.Contract) {
		t.Fatalf("unsupported required tool should be signed but refused at admission: supported=%v err=%v", ContractSupported(release.Content.Contract), err)
	}
}
