// Package verificationrouting selects owner-declared checks without I/O or authority.
// A selected plan is an observation, never an execution or completion permit.
package verificationrouting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid   = errors.New("verification routing policy or binding invalid")
	ErrUnmatched = errors.New("changed path has no owner verification rule")
	ErrConflict  = errors.New("verification routing rules conflict")
)

// Error names the finite input and owner remedy without falling back to root checks.
type Error struct {
	Code, Path, Rule, Message string
	cause                     error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: path=%q rule=%q: %s", e.Code, e.Path, e.Rule, e.Message)
}
func (e *Error) Unwrap() error { return e.cause }
func invalid(message string) error {
	return &Error{Code: "invalid", Message: message, cause: ErrInvalid}
}

// EnvironmentPolicy names a supported executor sanitation contract, not arbitrary env overrides.
type EnvironmentPolicy string

const VerificationSanitizedV1 EnvironmentPolicy = "verification-sanitized-v1"

// Gate is a literal argv/cwd descriptor. Filesystem containment is checked by the executor.
type Gate struct {
	ID          string            `json:"id"`
	Cwd         string            `json:"cwd"`
	Argv        []string          `json:"argv"`
	Environment EnvironmentPolicy `json:"environment"`
	Deadline    time.Duration     `json:"deadline"`
	Required    bool              `json:"required"`
}

// PatternSet unions includes and subtracts excludes within one rule.
type PatternSet struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// Rule selects gates/groups or explicitly ignores a surface with an owner rationale.
type Rule struct {
	ID           string     `json:"id"`
	Paths        PatternSet `json:"paths"`
	GateIDs      []string   `json:"gate_ids"`
	GroupIDs     []string   `json:"group_ids"`
	IgnoreReason string     `json:"ignore_reason,omitempty"`
}

// Group is a nonrecursive complete fanout list of declared gates.
type Group struct {
	ID      string   `json:"id"`
	GateIDs []string `json:"gate_ids"`
}

// Policy contains the whole finite owner declaration; unreachable invalid rules still refuse.
type Policy struct {
	SchemaVersion int     `json:"schema_version"`
	Gates         []Gate  `json:"gates"`
	Rules         []Rule  `json:"rules"`
	Groups        []Group `json:"groups"`
}

// Binding pins genuine Git objects and immutable owner policy bytes. It is not an authorization.
type Binding struct {
	Repository        string `json:"repository"`
	Checkout          string `json:"checkout"`
	BaseCommit        string `json:"base_commit"`
	RevisionCommit    string `json:"revision_commit"`
	RevisionTree      string `json:"revision_tree"`
	PolicyDigest      string `json:"policy_digest"`
	PolicyBytesDigest string `json:"policy_bytes_digest"`
}

// Change records both sides of renames as deletion/addition, preserving path bytes.
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// Selection explains all rules, groups and surfaces selecting the exact gate.
type Selection struct {
	Gate       Gate     `json:"gate"`
	GateDigest string   `json:"gate_digest"`
	RuleIDs    []string `json:"rule_ids"`
	GroupIDs   []string `json:"group_ids"`
	Paths      []string `json:"paths"`
}

// IgnoredPath is an explicit owner decision, not inferred language coverage.
type IgnoredPath struct {
	Path   string `json:"path"`
	RuleID string `json:"rule_id"`
	Reason string `json:"reason"`
}

// Plan is the complete deterministic selected set for one pinned revision/policy.
type Plan struct {
	Binding         Binding       `json:"binding"`
	Changes         []Change      `json:"changes"`
	Selected        []Selection   `json:"selected"`
	RequiredGateIDs []string      `json:"required_gate_ids"`
	Ignored         []IgnoredPath `json:"ignored"`
	Digest          string        `json:"digest"`
}

func text(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func id(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
func relative(s string) bool {
	return s != "" && text(s, 4096) && !strings.Contains(s, "\\") && !strings.HasPrefix(s, "/") && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}
func pattern(s string) bool {
	if !relative(s) || s == "." || len(s) > 1024 {
		return false
	}
	for _, c := range strings.Split(s, "/") {
		if strings.Contains(c, "**") && c != "**" {
			return false
		}
		if c != "**" {
			if _, e := path.Match(c, "x"); e != nil {
				return false
			}
		}
	}
	return true
}
func refs(ids []string, known map[string]bool) bool {
	seen := map[string]bool{}
	for _, v := range ids {
		if !known[v] || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func gateValid(g Gate) bool {
	if !id(g.ID) || !relative(g.Cwd) || g.Environment != VerificationSanitizedV1 || g.Deadline <= 0 || g.Deadline > 10*time.Minute || len(g.Argv) == 0 || len(g.Argv) > 128 || strings.TrimSpace(g.Argv[0]) == "" {
		return false
	}
	size := 0
	for _, a := range g.Argv {
		size += len(a)
		if !text(a, 16384) {
			return false
		}
	}
	return size <= 16384
}

// ValidatePolicy validates every declaration and reference before path selection.
func ValidatePolicy(p Policy) error {
	raw, e := json.Marshal(p)
	if e != nil || len(raw) > 256<<10 || p.SchemaVersion != 1 || len(p.Gates) > 64 || len(p.Rules) == 0 || len(p.Rules) > 256 || len(p.Groups) > 32 {
		return invalid("provide a bounded schema1 routing policy")
	}
	gates, groups, rules := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, g := range p.Gates {
		if !gateValid(g) || gates[g.ID] {
			return invalid("invalid or duplicate gate descriptor")
		}
		gates[g.ID] = true
	}
	for _, g := range p.Groups {
		if !id(g.ID) || groups[g.ID] || len(g.GateIDs) == 0 || !refs(g.GateIDs, gates) {
			return invalid("invalid fanout group")
		}
		groups[g.ID] = true
	}
	for _, r := range p.Rules {
		if !id(r.ID) || rules[r.ID] || len(r.Paths.Include) == 0 || len(r.Paths.Include) > 32 || len(r.Paths.Exclude) > 32 || !refs(r.GateIDs, gates) || !refs(r.GroupIDs, groups) || !text(r.IgnoreReason, 1024) {
			return invalid("invalid rule or references")
		}
		if (r.IgnoreReason != "") == (len(r.GateIDs)+len(r.GroupIDs) > 0) {
			return invalid("rule must select gates or explicitly ignore, never both")
		}
		if r.IgnoreReason != "" && strings.TrimSpace(r.IgnoreReason) == "" {
			return invalid("ignore requires an owner rationale")
		}
		for _, patterns := range [][]string{r.Paths.Include, r.Paths.Exclude} {
			seen := map[string]bool{}
			for _, s := range patterns {
				if !pattern(s) || seen[s] {
					return invalid("invalid or duplicate path pattern")
				}
				seen[s] = true
			}
		}
		rules[r.ID] = true
	}
	return nil
}
func normalized(p Policy) Policy {
	// JSON copies every nested slice: sorting must not rewrite selected private policy.
	raw, _ := json.Marshal(p)
	var c Policy
	_ = json.Unmarshal(raw, &c)
	sort.Slice(c.Gates, func(i, j int) bool { return c.Gates[i].ID < c.Gates[j].ID })
	sort.Slice(c.Rules, func(i, j int) bool { return c.Rules[i].ID < c.Rules[j].ID })
	sort.Slice(c.Groups, func(i, j int) bool { return c.Groups[i].ID < c.Groups[j].ID })
	for i := range c.Rules {
		r := &c.Rules[i]
		sort.Strings(r.Paths.Include)
		sort.Strings(r.Paths.Exclude)
		sort.Strings(r.GateIDs)
		sort.Strings(r.GroupIDs)
	}
	for i := range c.Groups {
		sort.Strings(c.Groups[i].GateIDs)
	}
	return c
}
func digest(domain string, v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(append([]byte(domain+"\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

// PolicyDigest canonicalizes sets without changing argv order or input policy bytes.
func PolicyDigest(p Policy) (string, error) {
	if e := ValidatePolicy(p); e != nil {
		return "", e
	}
	return digest("verificationrouting/policy/v1", normalized(p)), nil
}

// GateDigest has a new domain and never changes historical shell CheckDigest identities.
func GateDigest(g Gate) (string, error) {
	if !gateValid(g) {
		return "", invalid("invalid gate")
	}
	g.Argv = append([]string(nil), g.Argv...)
	return digest("verificationrouting/gate/v1", g), nil
}
func hexID(s string, n int) bool { return len(s) == n && strings.Trim(s, "0123456789abcdef") == "" }
func bindingValid(b Binding, pd string) bool {
	n := len(b.RevisionCommit)
	return (n == 40 || n == 64) && hexID(b.BaseCommit, n) && hexID(b.RevisionCommit, n) && hexID(b.RevisionTree, n) && hexID(b.PolicyBytesDigest, 64) && b.PolicyDigest == pd && filepath.IsAbs(b.Repository) && filepath.IsAbs(b.Checkout) && filepath.Clean(b.Repository) == b.Repository && filepath.Clean(b.Checkout) == b.Checkout
}
func matches(pattern, filename string) bool {
	a, b := strings.Split(pattern, "/"), strings.Split(filename, "/")
	dp := make([]bool, len(b)+1)
	dp[0] = true
	for _, component := range a {
		next := make([]bool, len(b)+1)
		if component == "**" {
			next[0] = dp[0]
			for j := 1; j <= len(b); j++ {
				next[j] = dp[j] || next[j-1]
			}
		} else {
			for j := 1; j <= len(b); j++ {
				ok, _ := path.Match(component, b[j-1])
				next[j] = dp[j-1] && ok
			}
		}
		dp = next
	}
	return dp[len(b)]
}
func ruleMatches(r Rule, p string) bool {
	included := false
	for _, s := range r.Paths.Include {
		included = included || matches(s, p)
	}
	for _, s := range r.Paths.Exclude {
		if matches(s, p) {
			return false
		}
	}
	return included
}
func unique(s []string) []string {
	sort.Strings(s)
	out := make([]string, 0, len(s))
	for _, v := range s {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// Select unions all matching rules and complete groups. Unmatched required paths refuse.
// Production callers must supply an authoritative checkout observation, not caller path lists.
func Select(p Policy, b Binding, changes []Change) (Plan, error) {
	pd, e := PolicyDigest(p)
	if e != nil {
		return Plan{}, e
	}
	if !bindingValid(b, pd) {
		return Plan{}, invalid("pin repository, base, revision/tree and immutable policy digests")
	}
	if len(changes) > 4096 {
		return Plan{}, invalid("changed path count exceeds4096")
	}
	c := append([]Change(nil), changes...)
	seen := map[string]bool{}
	size := 0
	for _, v := range c {
		size += len(v.Path)
		if !relative(v.Path) || v.Path == "." || seen[v.Path] || (v.Kind != "A" && v.Kind != "D" && v.Kind != "M" && v.Kind != "T") || size > 1<<20 {
			return Plan{}, invalid("invalid, duplicate or oversized changed paths")
		}
		seen[v.Path] = true
	}
	sort.Slice(c, func(i, j int) bool { return c[i].Path < c[j].Path })
	p = normalized(p)
	gates := map[string]Gate{}
	groups := map[string][]string{}
	selected := map[string]*Selection{}
	for _, g := range p.Gates {
		gates[g.ID] = g
	}
	for _, g := range p.Groups {
		groups[g.ID] = g.GateIDs
	}
	out := Plan{Binding: b, Changes: c, Selected: []Selection{}, RequiredGateIDs: []string{}, Ignored: []IgnoredPath{}}
	for _, v := range c {
		matched := []Rule{}
		verify, ignore := false, false
		reason := ""
		for _, r := range p.Rules {
			if ruleMatches(r, v.Path) {
				matched = append(matched, r)
				if r.IgnoreReason != "" {
					ignore = true
					if reason != "" && reason != r.IgnoreReason {
						return Plan{}, &Error{Code: "conflict", Path: v.Path, Rule: r.ID, Message: "conflicting owner ignore reasons", cause: ErrConflict}
					}
					reason = r.IgnoreReason
				} else {
					verify = true
				}
			}
		}
		if len(matched) == 0 {
			return Plan{}, &Error{Code: "unmatched", Path: v.Path, Message: "declare a verification rule or explicit owner ignore for this surface", cause: ErrUnmatched}
		}
		if verify && ignore {
			return Plan{}, &Error{Code: "conflict", Path: v.Path, Message: "remove overlapping verify and ignore rules", cause: ErrConflict}
		}
		for _, r := range matched {
			if ignore {
				out.Ignored = append(out.Ignored, IgnoredPath{v.Path, r.ID, r.IgnoreReason})
				continue
			}
			ids := append([]string(nil), r.GateIDs...)
			for _, group := range r.GroupIDs {
				ids = append(ids, groups[group]...)
			}
			for _, id := range unique(ids) {
				s := selected[id]
				if s == nil {
					g := gates[id]
					gd, _ := GateDigest(g)
					s = &Selection{Gate: g, GateDigest: gd}
					selected[id] = s
				}
				s.RuleIDs = append(s.RuleIDs, r.ID)
				s.Paths = append(s.Paths, v.Path)
				for _, group := range r.GroupIDs {
					for _, member := range groups[group] {
						if member == id {
							s.GroupIDs = append(s.GroupIDs, group)
						}
					}
				}
			}
		}
	}
	for _, s := range selected {
		s.RuleIDs = unique(s.RuleIDs)
		s.GroupIDs = unique(s.GroupIDs)
		s.Paths = unique(s.Paths)
		out.Selected = append(out.Selected, *s)
		if s.Gate.Required {
			out.RequiredGateIDs = append(out.RequiredGateIDs, s.Gate.ID)
		}
	}
	sort.Slice(out.Selected, func(i, j int) bool { return out.Selected[i].Gate.ID < out.Selected[j].Gate.ID })
	sort.Strings(out.RequiredGateIDs)
	out.Digest = digest("verificationrouting/plan/v1", out)
	return out, nil
}
