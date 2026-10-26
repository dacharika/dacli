package nativegovernance

import "testing"

func TestCapacityCommandsDeclareMutationAndJSONCapabilities(t *testing.T) {
	want := map[string]bool{
		"native capacity upgrade":          true,
		"native capacity observer add":     true,
		"native capacity policy configure": true,
		"native capacity qualify":          true,
		"native capacity observe":          true,
		"native capacity status":           false,
	}
	seen := map[string]bool{}
	for _, command := range CapacityCommands {
		mutates, ok := want[command.Path]
		if !ok {
			t.Fatalf("unexpected capacity command %q", command.Path)
		}
		if seen[command.Path] {
			t.Fatalf("duplicate capacity command %q", command.Path)
		}
		seen[command.Path] = true
		if command.Mutates != mutates || !command.JSON {
			t.Fatalf("%s capabilities: mutates=%v JSON=%v", command.Path, command.Mutates, command.JSON)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("capacity command count=%d want=%d", len(seen), len(want))
	}
}
