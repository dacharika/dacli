package verificationrouting

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixturePolicy() Policy {
	gates := []Gate{{ID: "go", Cwd: ".", Argv: []string{"go", "test", "./..."}, Environment: VerificationSanitizedV1, Deadline: time.Minute, Required: true}, {ID: "web", Cwd: "internal/features/dashboard/ui", Argv: []string{"npm", "test", "--", "literal $(no-shell); space"}, Environment: VerificationSanitizedV1, Deadline: time.Minute, Required: true}, {ID: "python", Cwd: "python package", Argv: []string{"python3", "-m", "pytest", "file with spaces.py"}, Environment: VerificationSanitizedV1, Deadline: time.Minute, Required: true}}
	return Policy{SchemaVersion: 1, Gates: gates, Rules: []Rule{{ID: "go", Paths: PatternSet{Include: []string{"**/*.go"}, Exclude: []string{"python package/**"}}, GateIDs: []string{"go"}}, {ID: "web", Paths: PatternSet{Include: []string{"internal/features/dashboard/ui/**"}}, GateIDs: []string{"web"}}, {ID: "python", Paths: PatternSet{Include: []string{"python package/**"}}, GateIDs: []string{"python"}}, {ID: "docs", Paths: PatternSet{Include: []string{"docs/**"}}, IgnoreReason: "owner docs-only no build gates"}, {ID: "contract", Paths: PatternSet{Include: []string{"contract/**"}}, GroupIDs: []string{"languages"}}}, Groups: []Group{{ID: "languages", GateIDs: []string{"web", "go", "python"}}}}
}
func fixtureBinding(p Policy) Binding {
	pd, _ := PolicyDigest(p)
	return Binding{Repository: "/repository/.git", Checkout: "/checkout with spaces", BaseCommit: strings.Repeat("1", 40), RevisionCommit: strings.Repeat("2", 40), RevisionTree: strings.Repeat("3", 40), PolicyDigest: pd, PolicyBytesDigest: strings.Repeat("4", 64)}
}
func TestMonorepoSelectionCompleteAndDeterministic(t *testing.T) {
	p := fixturePolicy()
	original, _ := json.Marshal(p)
	for _, tc := range []struct {
		name    string
		changes []Change
		want    []string
	}{{"go", []Change{{"root.go", "M"}}, []string{"go"}}, {"web", []Change{{"internal/features/dashboard/ui/App.vue", "M"}}, []string{"web"}}, {"python", []Change{{"python package/test file.py", "A"}}, []string{"python"}}, {"docs", []Change{{"docs/design.md", "M"}}, []string{}}, {"shared", []Change{{"contract/types.json", "D"}}, []string{"go", "python", "web"}}, {"mixed", []Change{{"root.go", "M"}, {"contract/types.json", "A"}}, []string{"go", "python", "web"}}, {"empty", []Change{}, []string{}}} {
		t.Run(tc.name, func(t *testing.T) {
			plan, e := Select(p, fixtureBinding(p), tc.changes)
			if e != nil || !reflect.DeepEqual(plan.RequiredGateIDs, tc.want) {
				t.Fatalf("complete path-selected gate set mismatch: %+v err=%v want=%v", plan, e, tc.want)
			}
			for _, s := range plan.Selected {
				if s.Gate.ID == "web" && (s.Gate.Cwd != "internal/features/dashboard/ui" || s.Gate.Argv[3] != "literal $(no-shell); space") {
					t.Fatal("nested cwd or literal argv changed")
				}
				if len(s.RuleIDs) == 0 || len(s.Paths) == 0 || len(s.GateDigest) != 64 {
					t.Fatal("selection provenance missing")
				}
				if tc.name == "shared" && !reflect.DeepEqual(s.GroupIDs, []string{"languages"}) {
					t.Fatal("fanout provenance missing")
				}
			}
			shuffled := fixturePolicy()
			for i, j := 0, len(shuffled.Gates)-1; i < j; i, j = i+1, j-1 {
				shuffled.Gates[i], shuffled.Gates[j] = shuffled.Gates[j], shuffled.Gates[i]
			}
			for i, j := 0, len(shuffled.Rules)-1; i < j; i, j = i+1, j-1 {
				shuffled.Rules[i], shuffled.Rules[j] = shuffled.Rules[j], shuffled.Rules[i]
			}
			reversed := append([]Change(nil), tc.changes...)
			for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
				reversed[i], reversed[j] = reversed[j], reversed[i]
			}
			again, e := Select(shuffled, fixtureBinding(shuffled), reversed)
			if e != nil || !reflect.DeepEqual(plan, again) {
				t.Fatal("shuffled policy/path inputs changed immutable plan")
			}
		})
	}
	after, _ := json.Marshal(p)
	if string(original) != string(after) {
		t.Fatal("selection rewrote owner policy bytes")
	}
	plan, _ := Select(p, fixtureBinding(p), []Change{{"root.go", "M"}})
	plan.Selected[0].Gate.Argv[0] = "changed"
	after, _ = json.Marshal(p)
	if string(original) != string(after) {
		t.Fatal("returned plan aliases owner policy")
	}
}
func TestRequiredUnmatchedAndConflictsRefuse(t *testing.T) {
	p := fixturePolicy()
	if got, e := Select(p, fixtureBinding(p), []Change{{"uncovered/file.rs", "M"}}); !errors.Is(e, ErrUnmatched) || got.Digest != "" {
		t.Fatalf("required unmatched path did not refuse without fallback: %+v %v", got, e)
	}
	p.Rules = append(p.Rules, Rule{ID: "ignore-go", Paths: PatternSet{Include: []string{"**/*.go"}}, IgnoreReason: "skip"})
	if _, e := Select(p, fixtureBinding(p), []Change{{"root.go", "M"}}); !errors.Is(e, ErrConflict) {
		t.Fatal("verify/ignore overlap accepted")
	}
	p = fixturePolicy()
	p.Rules = append(p.Rules, Rule{ID: "ignore-docs", Paths: PatternSet{Include: []string{"docs/**"}}, IgnoreReason: "different"})
	if _, e := Select(p, fixtureBinding(p), []Change{{"docs/a.md", "M"}}); !errors.Is(e, ErrConflict) {
		t.Fatal("conflicting ignore reasons accepted")
	}
}
func TestWholePolicyValidationAndBinding(t *testing.T) {
	for _, name := range []string{"duplicate", "unknown-group", "traversal", "empty-argv", "shell-environment", "malformed-glob", "mixed-ignore", "large", "deadline"} {
		t.Run(name, func(t *testing.T) {
			p := fixturePolicy()
			switch name {
			case "duplicate":
				p.Gates = append(p.Gates, p.Gates[0])
			case "unknown-group":
				p.Rules[0].GroupIDs = []string{"absent"}
			case "traversal":
				p.Gates[0].Cwd = "../escape"
			case "empty-argv":
				p.Gates[0].Argv = nil
			case "shell-environment":
				p.Gates[0].Environment = "anything"
			case "malformed-glob":
				p.Rules[0].Paths.Include = []string{"[broken"}
			case "mixed-ignore":
				p.Rules[0].IgnoreReason = "skip"
			case "large":
				p.Gates[0].Argv = []string{strings.Repeat("x", 16385)}
			case "deadline":
				p.Gates[0].Deadline = 11 * time.Minute
			}
			if _, e := Select(p, fixtureBinding(p), nil); !errors.Is(e, ErrInvalid) {
				t.Fatalf("invalid unreachable policy accepted: %s %v", name, e)
			}
		})
	}
	p := fixturePolicy()
	for _, name := range []string{"base", "tree", "policy", "bytes", "repository"} {
		b := fixtureBinding(p)
		switch name {
		case "base":
			b.BaseCommit = "main"
		case "tree":
			b.RevisionTree = ""
		case "policy":
			b.PolicyDigest = strings.Repeat("0", 64)
		case "bytes":
			b.PolicyBytesDigest = ""
		case "repository":
			b.Repository = "relative"
		}
		if _, e := Select(p, b, nil); !errors.Is(e, ErrInvalid) {
			t.Fatalf("unpinned binding accepted: %s", name)
		}
	}
	for _, changes := range [][]Change{{{"../x", "M"}}, {{"x", "R"}}, {{"x", "A"}, {"x", "D"}}, {{"line\nname", "M"}}, make([]Change, 4097)} {
		if _, e := Select(p, fixtureBinding(p), changes); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid changed path set accepted")
		}
	}
}
func TestGlobComponentsAndExclusion(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{{"**/*.go", "root.go", true}, {"**/*.go", "a/b/c.go", true}, {"a/**/b?.[ch]", "a/b1.c", true}, {"a/**/b?.[ch]", "a/x/y/b1.h", true}, {"a/*", "a/b/c", false}, {"a/**", "ab/x", false}} {
		if got := matches(tc.pattern, tc.path); got != tc.want {
			t.Fatalf("glob %s %s: %v", tc.pattern, tc.path, got)
		}
	}
	p := fixturePolicy()
	p.Rules[1].Paths.Exclude = []string{"**/*.go"}
	plan, e := Select(p, fixtureBinding(p), []Change{{"python package/a.go", "A"}})
	if e != nil || !reflect.DeepEqual(plan.RequiredGateIDs, []string{"python"}) {
		t.Fatal("excluded Go path incorrectly selected root gate")
	}
}
