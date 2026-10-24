// Package rolebundle defines immutable, signed releases of dacli's existing
// team.Role model. It does not introduce a second role behavior system.
package rolebundle

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/team"
)

const SchemaVersion = 1
const ReleaseContractSchemaVersion = 2
const signatureDomain = "dacli-role-release/v1\x00"
const contractSignatureDomain = "dacli-role-release/v2\x00"
const statusDomain = "dacli-role-lifecycle/v1\x00"

var (
	ErrInvalidRelease = errors.New("invalid role release")
	ErrInvalidStatus  = errors.New("invalid role lifecycle status")
	semverPattern     = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

// Reference pins a governed object by both stable identity and exact digest.
type Reference struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// ReleaseContent is the immutable signed payload. Encoding the complete
// team.Role directly ensures Prompt and every current exported behavior field
// are committed to the digest; adding a new exported field automatically adds
// it to the JSON representation.
type ReleaseContent struct {
	SchemaVersion int         `json:"schema_version"`
	Version       string      `json:"version"`
	Role          team.Role   `json:"role"`
	References    []Reference `json:"references"`
	// Contract is omitted from schema-1 content so its canonical bytes and
	// publisher signatures remain byte-for-byte compatible. Schema 2 requires it.
	Contract *RoleContract `json:"contract,omitempty"`
}

// RoleContract supplements the existing team.Role execution model with signed
// host requirements, live policy gates, and declarative fixture scenarios.
// Mirrored role fields must match Role exactly; this prevents two sources of
// truth while keeping schema-1 role files and their signatures unchanged.
type RoleContract struct {
	Purpose         string            `json:"purpose"`
	Instructions    string            `json:"instructions"`
	CapabilityScope CapabilityScope   `json:"capability_scope"`
	Runtime         RuntimePreference `json:"runtime"`
	Host            HostRequirements  `json:"host"`
	Budget          BudgetRequirement `json:"budget"`
	Gates           []string          `json:"gates"`
	Escalation      []string          `json:"escalation"`
	Skills          []string          `json:"skills"`
	MCP             []CapabilityNeed  `json:"mcp"`
	Tools           []CapabilityNeed  `json:"tools"`
	Scenarios       []Scenario        `json:"scenarios"`
}

type CapabilityScope struct {
	Grant      string   `json:"grant"`
	Scope      []string `json:"scope"`
	OutOfScope []string `json:"out_of_scope"`
	Shortcuts  []string `json:"shortcuts"`
}

type RuntimePreference struct {
	Runtime string            `json:"runtime"`
	Model   string            `json:"model"`
	Profile team.ModelProfile `json:"profile"`
}

// HostRequirements name cooperative observations supported by current native
// admission. Containment requirements are represented so they can be refused
// before dispatch; they do not claim filesystem, network, or process isolation.
type HostRequirements struct {
	Capabilities []string `json:"capabilities"`
	Sandbox      string   `json:"sandbox"`
	Network      string   `json:"network"`
	Filesystem   string   `json:"filesystem"`
	Process      string   `json:"process"`
}

type BudgetRequirement struct {
	Mode string `json:"mode"`
}

type CapabilityNeed struct {
	ID       string `json:"id"`
	Required bool   `json:"required"`
}

// Scenario is signed declarative fixture data. It contains no command or
// executable field; evaluation is deterministic and never launches a process.
type Scenario struct {
	ID               string         `json:"id"`
	RequestedActions []string       `json:"requested_actions"`
	AllowedActions   []string       `json:"allowed_actions"`
	ForbiddenActions []string       `json:"forbidden_actions"`
	Gates            []ScenarioGate `json:"gates"`
	BudgetState      string         `json:"budget_state"`
	Escalation       string         `json:"escalation"`
	ExpectedOutcome  string         `json:"expected_outcome"`
}

type ScenarioGate struct {
	ID       string `json:"id"`
	Observed string `json:"observed"`
}

// ScenarioResult binds a fixture outcome to the exact signed release, its
// policy references, and the canonical digest of the scenario that was read.
type ScenarioResult struct {
	RoleID           string      `json:"role_id"`
	ReleaseDigest    string      `json:"release_digest"`
	PolicyReferences []Reference `json:"policy_references"`
	ScenarioID       string      `json:"scenario_id"`
	ScenarioDigest   string      `json:"scenario_digest"`
	EvaluatedAction  string      `json:"evaluated_action"`
	ExpectedOutcome  string      `json:"expected_outcome"`
	EvaluatedOutcome string      `json:"evaluated_outcome"`
	MatchesExpected  bool        `json:"matches_expected"`
}

// Release is a content-addressed role release signed by an enrolled publisher.
type Release struct {
	Content        ReleaseContent `json:"content"`
	ContentDigest  string         `json:"content_digest"`
	PublisherKeyID string         `json:"publisher_key_id"`
	Signature      []byte         `json:"signature"`
}

// LifecycleStatus is a publisher-signed, short-lived statement of the current
// state of one exact release. Generation must increase monotonically at store.
type LifecycleStatus struct {
	SchemaVersion        int       `json:"schema_version"`
	RoleID               string    `json:"role_id"`
	Version              string    `json:"version"`
	ContentDigest        string    `json:"content_digest"`
	Generation           uint64    `json:"generation"`
	PreviousStatusDigest string    `json:"previous_status_digest,omitempty"`
	State                string    `json:"state"` // active or revoked
	IssuedAt             time.Time `json:"issued_at"`
	ExpiresAt            time.Time `json:"expires_at"`
	PublisherKeyID       string    `json:"publisher_key_id"`
	Signature            []byte    `json:"signature"`
}

func validVersion(version string) bool { return semverPattern.MatchString(version) }

func validReference(ref Reference) bool {
	return (ref.Kind == "policy" || ref.Kind == "skill") &&
		validID(ref.ID) && validReferenceVersion(ref.Version) && validDigest(ref.Digest)
}

func validReferenceVersion(version string) bool {
	if !validID(version) || strings.EqualFold(version, "latest") || strings.ContainsAny(version, "*^~<>=|,") {
		return false
	}
	for _, part := range strings.Split(version, ".") {
		if strings.EqualFold(part, "x") {
			return false
		}
	}
	return true
}

func validID(v string) bool {
	return v != "" && len(v) <= 256 && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "/\\\x00\r\n")
}

func validDigest(v string) bool {
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(v, "sha256:"))
	return err == nil
}

func canonicalReferences(refs []Reference) ([]Reference, error) {
	out := append([]Reference(nil), refs...)
	for _, ref := range out {
		if !validReference(ref) {
			return nil, fmt.Errorf("%w: invalid governed reference", ErrInvalidRelease)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	for i := 1; i < len(out); i++ {
		if out[i-1].Kind == out[i].Kind && out[i-1].ID == out[i].ID && out[i-1].Version == out[i].Version {
			return nil, fmt.Errorf("%w: duplicate governed reference", ErrInvalidRelease)
		}
	}
	return out, nil
}

func normalizeContent(in ReleaseContent) (ReleaseContent, error) {
	if !validVersion(in.Version) || strings.TrimSpace(in.Role.Name) == "" {
		return ReleaseContent{}, ErrInvalidRelease
	}
	switch in.SchemaVersion {
	case SchemaVersion:
		if in.Contract != nil {
			return ReleaseContent{}, ErrInvalidRelease
		}
	case ReleaseContractSchemaVersion:
		if in.Contract == nil {
			return ReleaseContent{}, ErrInvalidRelease
		}
		contract, err := normalizeContract(*in.Contract, in.Role)
		if err != nil {
			return ReleaseContent{}, err
		}
		in.Contract = &contract
	default:
		return ReleaseContent{}, ErrInvalidRelease
	}
	refs, err := canonicalReferences(in.References)
	if err != nil {
		return ReleaseContent{}, err
	}
	in.References = refs
	return in, nil
}

// CanonicalContent returns stable JSON bytes for the complete behavior model.
func CanonicalContent(content ReleaseContent) ([]byte, error) {
	normalized, err := normalizeContent(content)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func ContentDigest(content ReleaseContent) (string, error) {
	payload, err := CanonicalContent(content)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func releaseSigningBytes(content ReleaseContent, keyID string) ([]byte, string, error) {
	if !validID(keyID) {
		return nil, "", ErrInvalidRelease
	}
	digest, err := ContentDigest(content)
	if err != nil {
		return nil, "", err
	}
	domain := signatureDomain
	if content.SchemaVersion == ReleaseContractSchemaVersion {
		domain = contractSignatureDomain
	}
	return []byte(domain + keyID + "\x00" + digest), digest, nil
}

func normalizeContract(in RoleContract, role team.Role) (RoleContract, error) {
	if strings.TrimSpace(in.Purpose) == "" || len(in.Purpose) > 8192 || in.Instructions != role.Prompt || in.Purpose != role.Summary {
		return RoleContract{}, ErrInvalidRelease
	}
	wantScope := CapabilityScope{Grant: role.Grant, Scope: role.Scope, OutOfScope: role.OutOfScope, Shortcuts: role.Shortcuts}
	wantRuntime := RuntimePreference{Runtime: role.Runtime, Model: role.ModelID(), Profile: role.Profile}
	if !reflect.DeepEqual(in.CapabilityScope, wantScope) || !reflect.DeepEqual(in.Runtime, wantRuntime) ||
		!reflect.DeepEqual(in.Escalation, role.EscalateTo) || !reflect.DeepEqual(in.Skills, role.Skills) {
		return RoleContract{}, ErrInvalidRelease
	}
	if !validContractHost(in.Host) || (in.Budget.Mode != "none" && in.Budget.Mode != "native-policy") {
		return RoleContract{}, ErrInvalidRelease
	}
	var err error
	in.Host.Capabilities, err = canonicalHostCapabilities(in.Host.Capabilities)
	if err != nil {
		return RoleContract{}, err
	}
	in.Gates, err = canonicalIDs(in.Gates, true)
	if err != nil {
		return RoleContract{}, err
	}
	in.Escalation = cloneStringsPreservingNil(in.Escalation)
	in.Skills = cloneStringsPreservingNil(in.Skills)
	in.MCP, err = canonicalNeeds(in.MCP)
	if err != nil {
		return RoleContract{}, err
	}
	in.Tools, err = canonicalNeeds(in.Tools)
	if err != nil {
		return RoleContract{}, err
	}
	in.Scenarios, err = canonicalScenarios(in.Scenarios)
	if err != nil {
		return RoleContract{}, err
	}
	return in, nil
}

func cloneStringsPreservingNil(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func validContractHost(host HostRequirements) bool {
	return oneOf(host.Sandbox, "cooperative", "required") &&
		oneOf(host.Network, "unrestricted", "disabled") &&
		oneOf(host.Filesystem, "role-scope", "contained") &&
		oneOf(host.Process, "unrestricted", "contained")
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func canonicalIDs(values []string, allowedOnly bool) ([]string, error) {
	out := append([]string(nil), values...)
	for _, value := range out {
		if !validID(value) || allowedOnly && !supportedGate(value) {
			return nil, ErrInvalidRelease
		}
	}
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i-1] == out[i] {
			return nil, ErrInvalidRelease
		}
	}
	return out, nil
}

func canonicalHostCapabilities(values []string) ([]string, error) {
	out := append([]string(nil), values...)
	for _, value := range out {
		if value != "native-policy-observation" && value != "native-swarm-admission" {
			return nil, ErrInvalidRelease
		}
	}
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i-1] == out[i] {
			return nil, ErrInvalidRelease
		}
	}
	return out, nil
}

func supportedGate(value string) bool {
	switch value {
	case "budget", "model-routing", "preflight", "swarm":
		return true
	default:
		return false
	}
}

func canonicalNeeds(values []CapabilityNeed) ([]CapabilityNeed, error) {
	out := append([]CapabilityNeed(nil), values...)
	for _, value := range out {
		if !validID(value.ID) {
			return nil, ErrInvalidRelease
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for i := 1; i < len(out); i++ {
		if out[i-1].ID == out[i].ID {
			return nil, ErrInvalidRelease
		}
	}
	return out, nil
}

func canonicalScenarios(values []Scenario) ([]Scenario, error) {
	out := append([]Scenario(nil), values...)
	for i := range out {
		s := &out[i]
		s.Gates = append([]ScenarioGate(nil), s.Gates...)
		if !validID(s.ID) || len(s.RequestedActions) == 0 || !oneOf(s.BudgetState, "valid", "exhausted") ||
			!oneOf(s.Escalation, "none", "required") || !oneOf(s.ExpectedOutcome, "allowed", "refused", "escalated") {
			return nil, ErrInvalidRelease
		}
		var err error
		if s.RequestedActions, err = canonicalIDs(s.RequestedActions, false); err != nil {
			return nil, err
		}
		if s.AllowedActions, err = canonicalIDs(s.AllowedActions, false); err != nil {
			return nil, err
		}
		if s.ForbiddenActions, err = canonicalIDs(s.ForbiddenActions, false); err != nil {
			return nil, err
		}
		sort.Slice(s.Gates, func(i, j int) bool { return s.Gates[i].ID < s.Gates[j].ID })
		for j, gate := range s.Gates {
			if !supportedGate(gate.ID) || !oneOf(gate.Observed, "current", "missing", "stale", "blocked") || j > 0 && s.Gates[j-1].ID == gate.ID {
				return nil, ErrInvalidRelease
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for i := 1; i < len(out); i++ {
		if out[i-1].ID == out[i].ID {
			return nil, ErrInvalidRelease
		}
	}
	return out, nil
}

// ContractSupported reports whether every required host behavior has a
// current cooperative native admission implementation. It deliberately does
// not claim OS containment or provider spend enforcement.
func ContractSupported(contract *RoleContract) bool {
	if contract == nil {
		return false
	}
	for _, capability := range contract.Host.Capabilities {
		if capability != "native-policy-observation" && capability != "native-swarm-admission" {
			return false
		}
	}
	if contract.Host.Sandbox != "cooperative" || contract.Host.Network != "unrestricted" ||
		contract.Host.Filesystem != "role-scope" || contract.Host.Process != "unrestricted" {
		return false
	}
	for _, need := range contract.MCP {
		if need.Required {
			return false
		}
	}
	for _, need := range contract.Tools {
		if need.Required {
			return false
		}
	}
	return true
}

func (contract RoleContract) HostRequires(capability string) bool {
	for _, required := range contract.Host.Capabilities {
		if required == capability {
			return true
		}
	}
	return false
}

// EvaluateScenario evaluates only the signed declarative fixture. It never
// executes scenario input and returns the exact release and fixture digests.
func EvaluateScenario(release Release, publishers map[string]ed25519.PublicKey, scenarioID string) (ScenarioResult, error) {
	if release.Verify(publishers) != nil || release.Content.SchemaVersion != ReleaseContractSchemaVersion || release.Content.Contract == nil || !ContractSupported(release.Content.Contract) {
		return ScenarioResult{}, ErrInvalidRelease
	}
	for _, scenario := range release.Content.Contract.Scenarios {
		if scenario.ID != scenarioID {
			continue
		}
		raw, err := json.Marshal(scenario)
		if err != nil {
			return ScenarioResult{}, ErrInvalidRelease
		}
		digest := sha256.Sum256(raw)
		outcome := evaluateScenarioFacts(scenario, release.Content.Contract)
		observedAction := "dispatch"
		switch outcome {
		case "refused":
			observedAction = "refuse"
		case "escalated":
			observedAction = "escalate"
		}
		policies := make([]Reference, 0, len(release.Content.References))
		for _, ref := range release.Content.References {
			if ref.Kind == "policy" {
				policies = append(policies, ref)
			}
		}
		return ScenarioResult{RoleID: release.Content.Role.Name, ReleaseDigest: release.ContentDigest, PolicyReferences: policies, ScenarioID: scenario.ID, ScenarioDigest: "sha256:" + hex.EncodeToString(digest[:]), EvaluatedAction: observedAction, ExpectedOutcome: scenario.ExpectedOutcome, EvaluatedOutcome: outcome, MatchesExpected: outcome == scenario.ExpectedOutcome}, nil
	}
	return ScenarioResult{}, ErrInvalidRelease
}

func evaluateScenarioFacts(s Scenario, contract *RoleContract) string {
	allowed := map[string]bool{}
	for _, action := range s.AllowedActions {
		allowed[action] = true
	}
	forbidden := map[string]bool{}
	for _, action := range s.ForbiddenActions {
		forbidden[action] = true
	}
	for _, action := range s.RequestedActions {
		if forbidden[action] || !allowed[action] {
			return "refused"
		}
	}
	if s.BudgetState != "valid" {
		return "refused"
	}
	for _, gate := range s.Gates {
		if gate.Observed != "current" {
			return "refused"
		}
	}
	observedGates := make(map[string]string, len(s.Gates))
	for _, gate := range s.Gates {
		observedGates[gate.ID] = gate.Observed
	}
	for _, required := range contract.Gates {
		if observedGates[required] != "current" {
			return "refused"
		}
	}
	if contract.Budget.Mode == "native-policy" && observedGates["budget"] != "current" {
		return "refused"
	}
	if s.Escalation == "required" {
		return "escalated"
	}
	return "allowed"
}

// Sign constructs a release with the supplied publisher key. It is intended
// for explicit owner/publisher tooling; imports must call Verify before cache.
func Sign(content ReleaseContent, keyID string, privateKey ed25519.PrivateKey) (Release, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Release{}, ErrInvalidRelease
	}
	canonical, err := normalizeContent(content)
	if err != nil {
		return Release{}, err
	}
	message, digest, err := releaseSigningBytes(canonical, keyID)
	if err != nil {
		return Release{}, err
	}
	return Release{Content: canonical, ContentDigest: digest, PublisherKeyID: keyID, Signature: ed25519.Sign(privateKey, message)}, nil
}

// Verify validates strict semver, canonical references, content digest, and
// publisher signature before returning any trusted role content.
func (r Release) Verify(publishers map[string]ed25519.PublicKey) error {
	if len(r.Signature) != ed25519.SignatureSize || !validID(r.PublisherKeyID) {
		return ErrInvalidRelease
	}
	key, ok := publishers[r.PublisherKeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return ErrInvalidRelease
	}
	message, digest, err := releaseSigningBytes(r.Content, r.PublisherKeyID)
	if err != nil || digest != r.ContentDigest || !ed25519.Verify(key, message, r.Signature) {
		return ErrInvalidRelease
	}
	canonical, err := normalizeContent(r.Content)
	if err != nil || !equalContent(canonical, r.Content) {
		return ErrInvalidRelease
	}
	return nil
}

func equalContent(a, b ReleaseContent) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && string(x) == string(y)
}

func statusSigningBytes(s LifecycleStatus) ([]byte, error) {
	if s.SchemaVersion != SchemaVersion || !validID(s.RoleID) || !validVersion(s.Version) || !validDigest(s.ContentDigest) || s.Generation == 0 || (s.State != "active" && s.State != "revoked") || s.IssuedAt.IsZero() || !s.ExpiresAt.After(s.IssuedAt) || s.ExpiresAt.After(s.IssuedAt.Add(24*time.Hour)) || !validID(s.PublisherKeyID) {
		return nil, ErrInvalidStatus
	}
	s.IssuedAt = s.IssuedAt.Round(0).UTC()
	s.ExpiresAt = s.ExpiresAt.Round(0).UTC()
	s.Signature = nil
	payload, err := json.Marshal(s)
	if err != nil {
		return nil, ErrInvalidStatus
	}
	return append([]byte(statusDomain), payload...), nil
}

// LifecycleDigest identifies one signed status in the monotonic history.
func LifecycleDigest(status LifecycleStatus) (string, error) {
	message, err := statusSigningBytes(status)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(message)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func SignStatus(status LifecycleStatus, privateKey ed25519.PrivateKey) (LifecycleStatus, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return LifecycleStatus{}, ErrInvalidStatus
	}
	status.IssuedAt = status.IssuedAt.Round(0).UTC()
	status.ExpiresAt = status.ExpiresAt.Round(0).UTC()
	message, err := statusSigningBytes(status)
	if err != nil {
		return LifecycleStatus{}, err
	}
	status.Signature = ed25519.Sign(privateKey, message)
	return status, nil
}

func (s LifecycleStatus) Verify(publishers map[string]ed25519.PublicKey, now time.Time) error {
	key, ok := publishers[s.PublisherKeyID]
	message, err := statusSigningBytes(s)
	if err != nil || !ok || len(key) != ed25519.PublicKeySize || len(s.Signature) != ed25519.SignatureSize || now.Before(s.IssuedAt) || !now.Before(s.ExpiresAt) || !ed25519.Verify(key, message, s.Signature) {
		return ErrInvalidStatus
	}
	return nil
}
