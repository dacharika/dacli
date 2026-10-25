// Package pathclaims owns the portable repository-relative path grammar shared
// by native lease admission and owner task-claim changes.
package pathclaims

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// ValidatePath matches the preexisting native lease path grammar. Keep this
// lexical contract stable: native admission has historically compacted exact
// duplicate resources after validation.
func ValidatePath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.ToLower(path) != path || filepath.ToSlash(filepath.Clean(path)) != path {
		return fmt.Errorf("noncanonical repository-relative path %q", path)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." || segment == ".git" {
			return fmt.Errorf("unsafe repository-relative path %q", path)
		}
		for _, r := range segment {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return fmt.Errorf("unsupported character in repository-relative path %q", path)
			}
		}
	}
	return nil
}

// ValidateClaims validates an owner-declared replacement set. Unlike native
// resource admission's historical deduplication, owner input with duplicates
// is rejected so the requested set is explicit and unambiguous.
func ValidateClaims(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one path claim is required")
	}
	out := append([]string(nil), paths...)
	seen := make(map[string]struct{}, len(out))
	for _, path := range out {
		if err := ValidatePath(path); err != nil {
			return nil, err
		}
		if _, exists := seen[path]; exists {
			return nil, fmt.Errorf("duplicate path claim %q", path)
		}
		seen[path] = struct{}{}
	}
	slices.Sort(out)
	return out, nil
}
