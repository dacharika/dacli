package pathclaims

import (
	"reflect"
	"testing"
)

func TestValidateClaimsRejectsUnsafeNoncanonicalAndDuplicatePaths(t *testing.T) {
	for _, input := range [][]string{
		{"../outside"}, {"/absolute"}, {"./src"}, {"src/../other"}, {"Src"}, {".git/config"}, {"src\\file"}, {"src//file"}, {"src/file", "src/file"},
	} {
		if _, err := ValidateClaims(input); err == nil {
			t.Errorf("ValidateClaims(%q) accepted invalid claim set", input)
		}
	}
}

func TestValidateClaimsReturnsCanonicalStableOrder(t *testing.T) {
	got, err := ValidateClaims([]string{"tests/unit", "internal/store"})
	if err != nil || !reflect.DeepEqual(got, []string{"internal/store", "tests/unit"}) {
		t.Fatalf("ValidateClaims = %v, %v", got, err)
	}
}

func TestValidatePathPreservesLegacyDACLIAllowance(t *testing.T) {
	if err := ValidatePath(".dacli/runs"); err != nil {
		t.Fatalf("native path grammar changed for .dacli: %v", err)
	}
}
