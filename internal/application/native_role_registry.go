package application

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// validateRoleRegistry enforces project pins at every native admission
// observation. Pinless projects keep their existing local-role behavior.
func (a *LeaseAdmission) validateRoleRegistry(project string, roles []team.Role, now time.Time) ([]sessions.RoleRegistryProvenance, error) {
	keys, keyErr := store.ReadRolePublishers(&a.workspace)
	registry, err := store.NewRoleRegistry(&a.workspace, keys)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	pins, err := registry.ReadPins(project)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	if len(pins.Pins) == 0 {
		if pins.Generation != 0 || len(pins.History) != 0 {
			return nil, sessions.ErrDenied
		}
		return nil, nil
	}
	if keyErr != nil {
		return nil, sessions.ErrDenied
	}
	if roles == nil {
		roles, err = store.ReadCheckpointRoles(&a.workspace)
		if err != nil {
			return nil, sessions.ErrDenied
		}
	}
	roleByID := make(map[string]team.Role, len(roles))
	for _, role := range roles {
		if _, exists := roleByID[role.Name]; exists {
			return nil, sessions.ErrDenied
		}
		roleByID[role.Name] = role
	}
	ids := make([]string, 0, len(pins.Pins))
	for id := range pins.Pins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]sessions.RoleRegistryProvenance, 0, len(ids))
	for _, id := range ids {
		release, pin, status, err := registry.Resolve(project, id, now)
		if err != nil || !store.RoleFileMatches(release, roles) {
			return nil, sessions.ErrDenied
		}
		role, ok := roleByID[id]
		policyRefs, refErr := validateReleaseReferences(&a.workspace, role, release)
		if !ok || refErr != nil {
			return nil, sessions.ErrDenied
		}
		lifecycleDigest, err := rolebundle.LifecycleDigest(status)
		if err != nil {
			return nil, sessions.ErrDenied
		}
		out = append(out, sessions.RoleRegistryProvenance{RoleID: id, Version: pin.Version, ContentDigest: pin.Digest, PublisherKeyID: release.PublisherKeyID, LifecycleDigest: lifecycleDigest, LifecycleExpiresAt: status.ExpiresAt.UTC(), LifecycleGeneration: status.Generation, PinGeneration: pin.Generation, ProjectGeneration: pins.Generation, PolicyReferences: policyRefs})
	}
	return out, nil
}

func validateReleaseReferences(w *workspace.Workspace, role team.Role, release rolebundle.Release) ([]sessions.RolePolicyReference, error) {
	refs := make(map[string]rolebundle.Reference, len(release.Content.References))
	policyRefs := []sessions.RolePolicyReference{}
	for _, ref := range release.Content.References {
		if ref.Kind == "policy" {
			if !rolePolicyKind(ref.ID) {
				return nil, sessions.ErrDenied
			}
			policyRefs = append(policyRefs, sessions.RolePolicyReference{Kind: ref.ID, Version: ref.Version, Digest: ref.Digest})
			continue
		}
		if ref.Kind != "skill" || !skillReferenceMatches(w.SkillsLibDir(), ref) {
			return nil, sessions.ErrDenied
		}
		refs[ref.ID] = ref
	}
	for _, id := range role.Skills {
		if _, ok := refs[id]; !ok {
			return nil, sessions.ErrDenied
		}
	}
	sort.Slice(policyRefs, func(i, j int) bool { return policyRefs[i].Kind < policyRefs[j].Kind })
	return policyRefs, nil
}

func rolePolicyKind(id string) bool {
	switch id {
	case "native-swarm", "native-preflight", "native-budget", "native-model-routing":
		return true
	default:
		return false
	}
}

func skillReferenceMatches(root string, ref rolebundle.Reference) bool {
	if ref.Kind != "skill" || !safeRoleReferenceID(ref.ID) {
		return false
	}
	dir := filepath.Join(root, ref.ID)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	var manifest string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "skill.md") {
			manifest = filepath.Join(dir, entry.Name())
			break
		}
	}
	if manifest == "" {
		return false
	}
	manifestInfo, err := os.Lstat(manifest)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
		return false
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(data)
	return "sha256:"+hex.EncodeToString(digest[:]) == ref.Digest && store.FileVersion(manifest) == ref.Version
}

func safeRoleReferenceID(v string) bool {
	return v != "" && v != "." && v != ".." && filepath.Base(v) == v && !strings.ContainsAny(v, "/\\\x00\r\n")
}
