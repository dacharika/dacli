package store

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// RoleRegistryTarget binds a read or selection to one exact signed release.
type RoleRegistryTarget struct {
	Release   rolebundle.Release         `json:"release"`
	Lifecycle rolebundle.LifecycleStatus `json:"lifecycle"`
}

// ImportRoleRelease caches only the supplied signed objects. It never selects
// a project pin or changes the canonical team.Role files.
func (r *RoleRegistry) ImportRoleRelease(release rolebundle.Release, status rolebundle.LifecycleStatus, now time.Time) error {
	if release.Verify(r.publishers) != nil || status.Verify(r.publishers, now) != nil ||
		status.RoleID != release.Content.Role.Name || status.Version != release.Content.Version || status.ContentDigest != release.ContentDigest {
		return ErrRoleRegistry
	}
	return r.withRegistryLock(func() error {
		if err := r.storeReleaseLocked(release); err != nil {
			return err
		}
		return r.storeLifecycleLocked(status, now)
	})
}

// VerifyRoleRelease verifies one exact cached release and its current
// non-revoked lifecycle statement without writing registry or project state.
func (r *RoleRegistry) VerifyRoleRelease(roleID, version, digest string, now time.Time) (RoleRegistryTarget, error) {
	release, err := r.ReadRelease(roleID, version, digest)
	if err != nil {
		return RoleRegistryTarget{}, err
	}
	status, err := r.LifecycleForRelease(roleID, version, digest, now)
	if err != nil {
		return RoleRegistryTarget{}, err
	}
	return RoleRegistryTarget{Release: release, Lifecycle: status}, nil
}

// CompareAndSelect applies an owner-selected exact release only while the
// caller's observed project generation and current pin still match. The
// registry lock and project lock cover the comparison and write together.
// action chooses pin, upgrade, or rollback. Rollback accepts only a selection recorded in
// earlier project history for this role.
func (r *RoleRegistry) CompareAndSelect(project, roleID, version, digest string, expectedGeneration uint64, expectedCurrent *RolePin, changedBy, reason, action string, now time.Time) (ProjectRolePins, error) {
	if !workspace.SafeSegment(project) || !workspace.SafeSegment(roleID) || changedBy == "" || strings.TrimSpace(reason) == "" || len(reason) > 2048 {
		return ProjectRolePins{}, ErrRoleRegistry
	}
	if action != "pin" && action != "upgrade" && action != "rollback" {
		return ProjectRolePins{}, ErrRoleRegistry
	}
	var updated ProjectRolePins
	err := r.withRegistryLock(func() error {
		target, err := r.VerifyRoleRelease(roleID, version, digest, now)
		if err != nil {
			return err
		}
		projectDir := r.workspace.ProjectDir(project)
		if err := safeExistingDir(projectDir, false); err != nil {
			return err
		}
		return WithFileLock(filepath.Join(projectDir, ".role-registry.lock"), func() error {
			pins, err := r.ReadPins(project)
			if err != nil {
				return err
			}
			if pins.Generation != expectedGeneration || pins.Generation == ^uint64(0) {
				return fmt.Errorf("%w: project role registry generation changed", ErrRoleRegistry)
			}
			current, exists := pins.Pins[roleID]
			if (expectedCurrent == nil) != !exists || (expectedCurrent != nil && current != *expectedCurrent) {
				return fmt.Errorf("%w: current role pin changed", ErrRoleRegistry)
			}
			if action == "pin" && exists || action != "pin" && !exists {
				return ErrRoleRegistry
			}
			if action == "upgrade" && !RoleVersionGreater(version, current.Version) {
				return ErrRoleRegistry
			}
			if action == "rollback" {
				found := false
				for _, change := range pins.History {
					if change.RoleID == roleID && change.Selected.Version == version && change.Selected.Digest == digest {
						found = true
					}
				}
				if !found || (exists && current.Version == version && current.Digest == digest) {
					return fmt.Errorf("%w: rollback target is not a prior selection", ErrRoleRegistry)
				}
			}
			roles, err := ReadCheckpointRoles(&r.workspace)
			if err != nil || !RoleFileMatches(target.Release, roles) {
				return fmt.Errorf("%w: signed release differs from canonical team.Role", ErrRoleRegistry)
			}
			var previous *RolePin
			if exists {
				copy := current
				previous = &copy
			}
			generation := pins.Generation + 1
			changedAt := now.Round(0).UTC()
			selected := RolePin{RoleID: roleID, Version: version, Digest: digest, Generation: generation, ChangedAt: changedAt, ChangedBy: changedBy, Reason: reason}
			pins.Generation = generation
			pins.Pins[roleID] = selected
			pins.History = append(pins.History, RolePinChange{Generation: generation, RoleID: roleID, Previous: previous, Selected: selected, ChangedAt: changedAt, ChangedBy: changedBy, Reason: reason})
			path := r.workspace.ProjectPath(project)
			doc, err := mdstore.ReadFile(path)
			if err != nil {
				return err
			}
			pinsJSON, err := json.Marshal(pins.Pins)
			if err != nil {
				return err
			}
			historyJSON, err := json.Marshal(pins.History)
			if err != nil {
				return err
			}
			doc.Front.Set("role_registry_generation", fmt.Sprint(generation))
			doc.Front.Set("role_registry_pins", string(pinsJSON))
			doc.Front.Set("role_registry_history", string(historyJSON))
			if err := mdstore.WriteFile(path, doc); err != nil {
				return err
			}
			updated = pins
			return nil
		})
	})
	if err != nil {
		return ProjectRolePins{}, err
	}
	return updated, nil
}

const roleVersionIdentifier = `(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`

var roleVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(` + roleVersionIdentifier + `(?:\.` + roleVersionIdentifier + `)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// RoleVersionGreater compares strict SemVer precedence without integer
// overflow. Build metadata does not affect precedence.
func RoleVersionGreater(target, current string) bool {
	parse := func(version string) ([]string, []string, bool) {
		match := roleVersionPattern.FindStringSubmatch(version)
		if match == nil {
			return nil, nil, false
		}
		var pre []string
		if match[4] != "" {
			pre = strings.Split(match[4], ".")
		}
		return match[1:4], pre, true
	}
	aCore, aPre, aOK := parse(target)
	bCore, bPre, bOK := parse(current)
	if !aOK || !bOK {
		return false
	}
	for i := range aCore {
		if aCore[i] != bCore[i] {
			return decimalGreater(aCore[i], bCore[i])
		}
	}
	if len(aPre) == 0 || len(bPre) == 0 {
		return len(aPre) == 0 && len(bPre) != 0
	}
	for i := 0; i < len(aPre) && i < len(bPre); i++ {
		aNumeric, bNumeric := decimalIdentifier(aPre[i]), decimalIdentifier(bPre[i])
		switch {
		case aPre[i] == bPre[i]:
			continue
		case aNumeric && bNumeric:
			return decimalGreater(aPre[i], bPre[i])
		case aNumeric:
			return false
		case bNumeric:
			return true
		default:
			return aPre[i] > bPre[i]
		}
	}
	return len(aPre) > len(bPre)
}

func decimalIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func decimalGreater(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// ReadRoleRegistryImport reads one bounded, regular, non-symlink JSON object.
// It is exported for the feature slice's explicit-file import handler.
func ReadRoleRegistryImport(path string, value any) error {
	data, err := readRoleRegistryFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if dec.Decode(value) != nil {
		return ErrRoleRegistry
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrRoleRegistry
	}
	return nil
}
