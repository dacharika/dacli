package application

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeRoleRegistryOwner is a private, explicit owner view bound to one
// workspace and project. It reopens current authority and credentials for
// every operation so replacing either private file invalidates the profile.
type NativeRoleRegistryOwner struct {
	owner       *NativeRecoveryAccess
	project     string
	root        string
	workspaceID string
}

type NativeRoleRegistryView struct {
	Project              string                   `json:"project"`
	ProjectGeneration    uint64                   `json:"project_generation"`
	AuthorityVersion     int                      `json:"authority_version"`
	SelectionSupported   bool                     `json:"selection_supported"`
	SelectionNote        string                   `json:"selection_note"`
	CurrentPin           *store.RolePin           `json:"current_pin,omitempty"`
	Target               store.RoleRegistryTarget `json:"target"`
	CanonicalRole        *team.Role               `json:"canonical_role,omitempty"`
	CanonicalRoleMatches bool                     `json:"canonical_role_matches"`
}

type NativeRoleRegistryFieldChange struct {
	Field   string          `json:"field"`
	Current json.RawMessage `json:"current"`
	Target  json.RawMessage `json:"target"`
}

type NativeRoleRegistryDiff struct {
	Project                     string                          `json:"project"`
	ProjectGeneration           uint64                          `json:"project_generation"`
	AuthorityVersion            int                             `json:"authority_version"`
	SelectionSupported          bool                            `json:"selection_supported"`
	SelectionNote               string                          `json:"selection_note"`
	CurrentPin                  *store.RolePin                  `json:"current_pin,omitempty"`
	CurrentRelease              *store.RoleRegistryTarget       `json:"current_release,omitempty"`
	CurrentReleaseAvailable     bool                            `json:"current_release_available"`
	CurrentLifecycleUsable      bool                            `json:"current_lifecycle_usable"`
	CurrentLifecycleObservation string                          `json:"current_lifecycle_observation"`
	Target                      store.RoleRegistryTarget        `json:"target"`
	Changes                     []NativeRoleRegistryFieldChange `json:"changes"`
	CanonicalRole               *team.Role                      `json:"canonical_role,omitempty"`
	CanonicalRoleMatches        bool                            `json:"canonical_role_matches"`
}

type NativeRoleRegistryImportReceipt struct {
	Project             string `json:"project"`
	RoleID              string `json:"role_id"`
	Version             string `json:"version"`
	ContentDigest       string `json:"content_digest"`
	PublisherKeyID      string `json:"publisher_key_id"`
	LifecycleState      string `json:"lifecycle_state"`
	LifecycleGeneration uint64 `json:"lifecycle_generation"`
	ProjectGeneration   uint64 `json:"project_generation"`
	Selected            bool   `json:"selected"`
	ActivationNote      string `json:"activation_note"`
}

// OpenNativeRoleRegistryOwner requires a private recovery owner profile and
// binds the returned capability to one existing workspace project.
func OpenNativeRoleRegistryOwner(configPath, project string) (*NativeRoleRegistryOwner, error) {
	if !workspace.SafeSegment(project) {
		return nil, sessions.ErrDenied
	}
	owner, err := OpenNativeRecovery(configPath)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	if _, _, err = owner.authenticate(); err != nil {
		return nil, sessions.ErrDenied
	}
	w, err := nativeconfig.ResolveWorkspace(owner.config.Workspace, "")
	if err != nil || !filepath.IsAbs(w.ProjectPath(project)) {
		return nil, sessions.ErrDenied
	}
	registry, err := roleRegistryFor(w)
	if err != nil {
		return nil, sessions.ErrDenied
	}
	if _, err = registry.ReadPins(project); err != nil {
		return nil, sessions.ErrDenied
	}
	return &NativeRoleRegistryOwner{owner: owner, project: project, root: w.Root, workspaceID: w.ID}, nil
}

func roleRegistryFor(w *workspace.Workspace) (*store.RoleRegistry, error) {
	keys, err := store.ReadRolePublishers(w)
	if err != nil {
		return nil, store.ErrRoleRegistry
	}
	return store.NewRoleRegistry(w, keys)
}

func (p *NativeRoleRegistryOwner) current() (*workspace.Workspace, *store.RoleRegistry, string, *sessions.Authority, string, error) {
	if p == nil || p.owner == nil {
		return nil, nil, "", nil, "", sessions.ErrDenied
	}
	authority, credential, err := p.owner.authenticate()
	if err != nil {
		return nil, nil, "", nil, "", sessions.ErrDenied
	}
	w, err := nativeconfig.ResolveWorkspace(p.owner.config.Workspace, p.workspaceID)
	if err != nil || w.Root != p.root {
		return nil, nil, "", nil, "", sessions.ErrDenied
	}
	registry, err := roleRegistryFor(w)
	if err != nil {
		return nil, nil, "", nil, "", sessions.ErrDenied
	}
	return w, registry, "owner:" + p.owner.config.InstallationID, authority, credential, nil
}

// Import caches the exact caller-supplied release and lifecycle objects.
func (p *NativeRoleRegistryOwner) Import(release rolebundle.Release, status rolebundle.LifecycleStatus, now time.Time) (NativeRoleRegistryImportReceipt, error) {
	_, registry, _, _, _, err := p.current()
	if err != nil {
		return NativeRoleRegistryImportReceipt{}, err
	}
	pins, err := registry.ReadPins(p.project)
	if err != nil {
		return NativeRoleRegistryImportReceipt{}, err
	}
	if err := registry.ImportRoleRelease(release, status, now); err != nil {
		return NativeRoleRegistryImportReceipt{}, err
	}
	return NativeRoleRegistryImportReceipt{Project: p.project, RoleID: release.Content.Role.Name, Version: release.Content.Version, ContentDigest: release.ContentDigest, PublisherKeyID: release.PublisherKeyID, LifecycleState: status.State, LifecycleGeneration: status.Generation, ProjectGeneration: pins.Generation, Selected: false, ActivationNote: "Import only caches the supplied signed objects; it never selects a role release."}, nil
}

// Verify returns exact signed provenance and canonical-role comparison facts.
func (p *NativeRoleRegistryOwner) Verify(roleID, version, digest string, now time.Time) (NativeRoleRegistryView, error) {
	w, registry, _, authority, credential, err := p.current()
	if err != nil {
		return NativeRoleRegistryView{}, err
	}
	authorityVersion, err := observeAuthorityVersion(authority, credential)
	if err != nil {
		return NativeRoleRegistryView{}, sessions.ErrDenied
	}
	view, err := p.view(w, registry, roleID, version, digest, now)
	if err != nil {
		return NativeRoleRegistryView{}, err
	}
	view.AuthorityVersion = authorityVersion
	view.SelectionSupported = authorityVersion >= 20
	view.SelectionNote = "Import, verify, and diff never select a release; pin, upgrade, and rollback require authority schema 20."
	return view, nil
}

// Diff is a read-only exact comparison against both the current project
// selection and canonical team.Role data.
func (p *NativeRoleRegistryOwner) Diff(roleID, version, digest string, now time.Time) (NativeRoleRegistryDiff, error) {
	w, registry, _, authority, credential, err := p.current()
	if err != nil {
		return NativeRoleRegistryDiff{}, err
	}
	authorityVersion, err := observeAuthorityVersion(authority, credential)
	if err != nil {
		return NativeRoleRegistryDiff{}, err
	}
	target, err := registry.VerifyRoleRelease(roleID, version, digest, now)
	if err != nil {
		return NativeRoleRegistryDiff{}, err
	}
	pins, err := registry.ReadPins(p.project)
	if err != nil {
		return NativeRoleRegistryDiff{}, err
	}
	out := NativeRoleRegistryDiff{Project: p.project, ProjectGeneration: pins.Generation, AuthorityVersion: authorityVersion, SelectionSupported: authorityVersion >= 20, SelectionNote: "Diff is read-only; selection requires authority schema 20.", Target: target, Changes: []NativeRoleRegistryFieldChange{}}
	currentPin, hasPin := pins.Pins[roleID]
	if hasPin {
		copy := currentPin
		out.CurrentPin = &copy
		currentRelease, readErr := registry.ReadRelease(roleID, currentPin.Version, currentPin.Digest)
		if readErr == nil {
			current := store.RoleRegistryTarget{Release: currentRelease}
			if lifecycle, lifecycleErr := registry.LifecycleForRelease(roleID, currentPin.Version, currentPin.Digest, now); lifecycleErr == nil {
				current.Lifecycle = lifecycle
				out.CurrentLifecycleUsable = true
				out.CurrentLifecycleObservation = "fresh active lifecycle available"
			} else {
				out.CurrentLifecycleObservation = "cached pin lifecycle is stale, revoked, missing, or unavailable"
			}
			out.CurrentRelease = &current
			out.CurrentReleaseAvailable = true
			out.Changes = roleReleaseChanges(currentRelease, target.Release)
		} else {
			out.CurrentLifecycleObservation = "cached pinned release is missing or invalid"
		}
	} else {
		out.CurrentLifecycleObservation = "project has no current pin for this role"
	}
	roles, err := store.ReadCheckpointRoles(w)
	if err != nil {
		return NativeRoleRegistryDiff{}, err
	}
	for i := range roles {
		if roles[i].Name == roleID {
			copy := roles[i]
			out.CanonicalRole = &copy
			out.CanonicalRoleMatches = store.RoleFileMatches(target.Release, roles)
			break
		}
	}
	return out, nil
}

func observeAuthorityVersion(authority *sessions.Authority, credential string) (int, error) {
	storage, err := authority.ObserveStorage(credential)
	if err != nil {
		return 0, sessions.ErrDenied
	}
	version := storage.AuthorityVersion
	for i := range storage.PrivateBytes {
		storage.PrivateBytes[i] = 0
	}
	storage.PrivateBytes = nil
	return version, nil
}

func roleReleaseChanges(current, target rolebundle.Release) []NativeRoleRegistryFieldChange {
	changes := []NativeRoleRegistryFieldChange{}
	add := func(field string, before, after any) {
		if reflect.DeepEqual(before, after) {
			return
		}
		oldJSON, _ := json.Marshal(before)
		newJSON, _ := json.Marshal(after)
		changes = append(changes, NativeRoleRegistryFieldChange{Field: field, Current: oldJSON, Target: newJSON})
	}
	add("content.schema_version", current.Content.SchemaVersion, target.Content.SchemaVersion)
	add("content.version", current.Content.Version, target.Content.Version)
	add("content.contract", current.Content.Contract, target.Content.Contract)
	add("role.name", current.Content.Role.Name, target.Content.Role.Name)
	add("role.summary", current.Content.Role.Summary, target.Content.Role.Summary)
	add("role.skills", current.Content.Role.Skills, target.Content.Role.Skills)
	add("role.scope", current.Content.Role.Scope, target.Content.Role.Scope)
	add("role.out_of_scope", current.Content.Role.OutOfScope, target.Content.Role.OutOfScope)
	add("role.shortcuts", current.Content.Role.Shortcuts, target.Content.Role.Shortcuts)
	add("role.grant", current.Content.Role.Grant, target.Content.Role.Grant)
	add("role.escalate_to", current.Content.Role.EscalateTo, target.Content.Role.EscalateTo)
	add("role.wip", current.Content.Role.WIP, target.Content.Role.WIP)
	add("role.kind", current.Content.Role.Kind, target.Content.Role.Kind)
	add("role.runtime", current.Content.Role.Runtime, target.Content.Role.Runtime)
	add("role.model", current.Content.Role.Model, target.Content.Role.Model)
	add("role.model_profile", current.Content.Role.Profile, target.Content.Role.Profile)
	add("role.fallback_to", current.Content.Role.FallbackTo, target.Content.Role.FallbackTo)
	add("role.max_points", current.Content.Role.MaxPoints, target.Content.Role.MaxPoints)
	add("role.prompt", current.Content.Role.Prompt, target.Content.Role.Prompt)
	add("references", current.Content.References, target.Content.References)
	add("content_digest", current.ContentDigest, target.ContentDigest)
	add("publisher_key_id", current.PublisherKeyID, target.PublisherKeyID)
	return changes
}

func (p *NativeRoleRegistryOwner) view(w *workspace.Workspace, registry *store.RoleRegistry, roleID, version, digest string, now time.Time) (NativeRoleRegistryView, error) {
	target, err := registry.VerifyRoleRelease(roleID, version, digest, now)
	if err != nil {
		return NativeRoleRegistryView{}, err
	}
	pins, err := registry.ReadPins(p.project)
	if err != nil {
		return NativeRoleRegistryView{}, err
	}
	out := NativeRoleRegistryView{Project: p.project, ProjectGeneration: pins.Generation, Target: target}
	if current, ok := pins.Pins[roleID]; ok {
		copy := current
		out.CurrentPin = &copy
	}
	roles, err := store.ReadCheckpointRoles(w)
	if err != nil {
		return NativeRoleRegistryView{}, err
	}
	for i := range roles {
		if roles[i].Name == roleID {
			copy := roles[i]
			out.CanonicalRole = &copy
			out.CanonicalRoleMatches = store.RoleFileMatches(target.Release, roles)
			break
		}
	}
	return out, nil
}

// Select applies one deliberate pin, upgrade, or rollback after checking the
// caller's observed project generation and current selection.
func (p *NativeRoleRegistryOwner) Select(roleID, version, digest string, expectedGeneration uint64, expectedVersion, expectedDigest, reason, action string, now time.Time) (store.ProjectRolePins, error) {
	w, registry, actor, authority, credential, err := p.current()
	if err != nil {
		return store.ProjectRolePins{}, err
	}
	if action != "pin" && action != "upgrade" && action != "rollback" {
		return store.ProjectRolePins{}, sessions.ErrDenied
	}
	authorityVersion, err := observeAuthorityVersion(authority, credential)
	if err != nil {
		return store.ProjectRolePins{}, sessions.ErrDenied
	}
	if authorityVersion < 20 {
		return store.ProjectRolePins{}, sessions.ErrDenied
	}
	currentPins, err := registry.ReadPins(p.project)
	if err != nil || currentPins.Generation != expectedGeneration {
		return store.ProjectRolePins{}, store.ErrRoleRegistry
	}
	current, exists := currentPins.Pins[roleID]
	var expected *store.RolePin
	if exists {
		if expectedVersion == "" || expectedDigest == "" || current.Version != expectedVersion || current.Digest != expectedDigest {
			return store.ProjectRolePins{}, store.ErrRoleRegistry
		}
		copy := current
		expected = &copy
	} else if expectedVersion != "" || expectedDigest != "" || action != "pin" {
		return store.ProjectRolePins{}, store.ErrRoleRegistry
	}
	if action == "pin" && exists || action != "pin" && !exists {
		return store.ProjectRolePins{}, store.ErrRoleRegistry
	}
	target, err := registry.VerifyRoleRelease(roleID, version, digest, now)
	if err != nil {
		return store.ProjectRolePins{}, err
	}
	roles, err := store.ReadCheckpointRoles(w)
	if err != nil || !store.RoleFileMatches(target.Release, roles) {
		return store.ProjectRolePins{}, store.ErrRoleRegistry
	}
	if action == "upgrade" && !store.RoleVersionGreater(version, current.Version) {
		return store.ProjectRolePins{}, store.ErrRoleRegistry
	}
	return registry.CompareAndSelect(p.project, roleID, version, digest, expectedGeneration, expected, actor, reason, action, now)
}

// IsNativeRoleRegistryRefusal lets callers distinguish a policy refusal
// from malformed command usage.
func IsNativeRoleRegistryRefusal(err error) bool {
	return errors.Is(err, sessions.ErrDenied) || errors.Is(err, store.ErrRoleRegistry)
}
