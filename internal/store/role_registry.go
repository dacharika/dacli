package store

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const roleRegistryMaxFile = 1 << 20

var ErrRoleRegistry = errors.New("role registry refused")

type RolePin struct {
	RoleID     string    `json:"role_id"`
	Version    string    `json:"version"`
	Digest     string    `json:"digest"`
	Generation uint64    `json:"generation"`
	ChangedAt  time.Time `json:"changed_at"`
	ChangedBy  string    `json:"changed_by"`
	Reason     string    `json:"reason"`
}

type RolePinChange struct {
	Generation uint64    `json:"generation"`
	RoleID     string    `json:"role_id"`
	Previous   *RolePin  `json:"previous,omitempty"`
	Selected   RolePin   `json:"selected"`
	ChangedAt  time.Time `json:"changed_at"`
	ChangedBy  string    `json:"changed_by"`
	Reason     string    `json:"reason"`
}

type ProjectRolePins struct {
	Generation uint64             `json:"generation"`
	Pins       map[string]RolePin `json:"pins"`
	History    []RolePinChange    `json:"history"`
}

type RoleRegistry struct {
	workspace  workspace.Workspace
	publishers map[string]ed25519.PublicKey
}

func NewRoleRegistry(w *workspace.Workspace, publishers map[string]ed25519.PublicKey) (*RoleRegistry, error) {
	if w == nil || w.Root == "" || !filepath.IsAbs(w.Root) {
		return nil, ErrRoleRegistry
	}
	physicalRoot, err := filepath.EvalSymlinks(w.Root)
	if err != nil {
		return nil, ErrRoleRegistry
	}
	workspaceCopy := *w
	workspaceCopy.Root = physicalRoot
	keys := make(map[string]ed25519.PublicKey, len(publishers))
	for id, key := range publishers {
		if id == "" || len(key) != ed25519.PublicKeySize {
			return nil, ErrRoleRegistry
		}
		keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	return &RoleRegistry{workspace: workspaceCopy, publishers: keys}, nil
}

// ReadRolePublishers loads the owner-managed local trust roots used to verify
// cached releases. An absent keyring is valid for projects without role pins;
// callers must fail closed if a pinned role needs verification.
func ReadRolePublishers(w *workspace.Workspace) (map[string]ed25519.PublicKey, error) {
	if w == nil || !filepath.IsAbs(w.Root) {
		return nil, ErrRoleRegistry
	}
	root, err := filepath.EvalSymlinks(w.Root)
	if err != nil {
		return nil, ErrRoleRegistry
	}
	path := filepath.Join(root, workspace.Dir, "role-registry", "publishers.json")
	if err := safeExistingDir(filepath.Dir(path), false); err != nil {
		if os.IsNotExist(err) {
			return map[string]ed25519.PublicKey{}, nil
		}
		return nil, ErrRoleRegistry
	}
	data, err := readRoleRegistryFile(path)
	if os.IsNotExist(err) {
		return map[string]ed25519.PublicKey{}, nil
	}
	if err != nil {
		return nil, ErrRoleRegistry
	}
	var encoded map[string][]byte
	if json.Unmarshal(data, &encoded) != nil || len(encoded) == 0 {
		return nil, ErrRoleRegistry
	}
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	for id, key := range encoded {
		if id == "" || len(key) != ed25519.PublicKeySize {
			return nil, ErrRoleRegistry
		}
		keys[id] = ed25519.PublicKey(append([]byte(nil), key...))
	}
	return keys, nil
}

func (r *RoleRegistry) registryDir() string {
	return filepath.Join(r.workspace.Root, workspace.Dir, "role-registry")
}
func (r *RoleRegistry) releasesDir() string  { return filepath.Join(r.registryDir(), "releases") }
func (r *RoleRegistry) lifecycleDir() string { return filepath.Join(r.registryDir(), "lifecycle") }

func (r *RoleRegistry) withRegistryLock(fn func() error) error {
	if err := safeExistingDir(r.registryDir(), true); err != nil {
		return err
	}
	return WithFileLock(filepath.Join(r.registryDir(), ".lock"), fn)
}

func safeExistingDir(path string, create bool) error {
	if !filepath.IsAbs(path) {
		return ErrRoleRegistry
	}
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	current := string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && create {
			if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrRoleRegistry
		}
	}
	return nil
}

func safeFilePath(path string) error {
	if err := safeExistingDir(filepath.Dir(path), false); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrRoleRegistry
	}
	return nil
}

func readRoleRegistryFile(path string) ([]byte, error) {
	if err := safeFilePath(path); err != nil {
		return nil, err
	}
	file, err := checkpointOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > roleRegistryMaxFile {
		return nil, ErrRoleRegistry
	}
	data, err := io.ReadAll(io.LimitReader(file, roleRegistryMaxFile+1))
	if err != nil || len(data) > roleRegistryMaxFile {
		return nil, ErrRoleRegistry
	}
	return data, nil
}

func (r *RoleRegistry) releasePath(roleID, version, digest string) (string, error) {
	if !workspace.SafeSegment(roleID) || !workspace.SafeSegment(version) || !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return "", ErrRoleRegistry
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return "", ErrRoleRegistry
	}
	return filepath.Join(r.releasesDir(), roleID, version, hexDigest+".json"), nil
}

func (r *RoleRegistry) StoreRelease(release rolebundle.Release) error {
	if err := release.Verify(r.publishers); err != nil {
		return ErrRoleRegistry
	}
	return r.withRegistryLock(func() error { return r.storeReleaseLocked(release) })
}

func (r *RoleRegistry) storeReleaseLocked(release rolebundle.Release) error {
	path, err := r.releasePath(release.Content.Role.Name, release.Content.Version, release.ContentDigest)
	if err != nil {
		return err
	}
	roleVersionDir := filepath.Dir(path)
	if err := safeExistingDir(roleVersionDir, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(roleVersionDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Name() != filepath.Base(path) {
			return fmt.Errorf("%w: role/version release is immutable", ErrRoleRegistry)
		}
	}
	data, err := json.Marshal(release)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if old, err := readRoleRegistryFile(path); err == nil {
		var cached rolebundle.Release
		if decodeRoleRelease(old, &cached) != nil || cached.ContentDigest != release.ContentDigest || cached.Content.Role.Name != release.Content.Role.Name || cached.Content.Version != release.Content.Version {
			return ErrRoleRegistry
		}
		if err := cached.Verify(r.publishers); err != nil {
			return ErrRoleRegistry
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := safeFilePath(path); err != nil {
		return err
	}
	return mdstore.WriteBytes(path, data, 0o600)
}

func (r *RoleRegistry) ReadRelease(roleID, version, digest string) (rolebundle.Release, error) {
	path, err := r.releasePath(roleID, version, digest)
	if err != nil {
		return rolebundle.Release{}, err
	}
	data, err := readRoleRegistryFile(path)
	if err != nil {
		return rolebundle.Release{}, err
	}
	var release rolebundle.Release
	if decodeRoleRelease(data, &release) != nil || release.Content.Role.Name != roleID || release.Content.Version != version || release.ContentDigest != digest || release.Verify(r.publishers) != nil {
		return rolebundle.Release{}, ErrRoleRegistry
	}
	return release, nil
}

func decodeRoleRelease(data []byte, release *rolebundle.Release) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(release); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrRoleRegistry
	}
	return nil
}

func (r *RoleRegistry) statusHistoryDir(roleID string) (string, error) {
	if !workspace.SafeSegment(roleID) {
		return "", ErrRoleRegistry
	}
	return filepath.Join(r.lifecycleDir(), roleID), nil
}

func (r *RoleRegistry) StoreLifecycle(status rolebundle.LifecycleStatus, now time.Time) error {
	if status.Verify(r.publishers, now) != nil {
		return ErrRoleRegistry
	}
	return r.withRegistryLock(func() error { return r.storeLifecycleLocked(status, now) })
}

func (r *RoleRegistry) storeLifecycleLocked(status rolebundle.LifecycleStatus, now time.Time) error {
	dir, err := r.statusHistoryDir(status.RoleID)
	if err != nil {
		return err
	}
	if err := safeExistingDir(dir, true); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("%020d.json", status.Generation))
	if existing, err := readRoleRegistryFile(path); err == nil {
		var cached rolebundle.LifecycleStatus
		if json.Unmarshal(existing, &cached) != nil || !reflect.DeepEqual(cached, status) || cached.Verify(r.publishers, cached.IssuedAt) != nil {
			return ErrRoleRegistry
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	history, err := r.readLifecycleHistory(dir)
	if err != nil {
		return err
	}
	var previous *rolebundle.LifecycleStatus
	revoked := map[string]bool{}
	for i := range history {
		item := history[i]
		if item.Verify(r.publishers, item.IssuedAt) != nil {
			return ErrRoleRegistry
		}
		if item.State == "revoked" {
			revoked[item.ContentDigest] = true
		}
		if previous == nil || item.Generation > previous.Generation {
			copy := item
			previous = &copy
		}
	}
	if previous == nil {
		if status.Generation != 1 || status.PreviousStatusDigest != "" {
			return ErrRoleRegistry
		}
	} else {
		previousDigest, err := rolebundle.LifecycleDigest(*previous)
		if err != nil || status.Generation != previous.Generation+1 || status.PreviousStatusDigest != previousDigest {
			return ErrRoleRegistry
		}
	}
	if status.State == "active" && revoked[status.ContentDigest] {
		return ErrRoleRegistry
	}
	if _, err := r.ReadRelease(status.RoleID, status.Version, status.ContentDigest); err != nil {
		return err
	}
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := safeFilePath(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return ErrRoleRegistry
	}
	return mdstore.WriteBytes(path, data, 0o600)
}

func (r *RoleRegistry) readLifecycleHistory(dir string) ([]rolebundle.LifecycleStatus, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]rolebundle.LifecycleStatus, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, ErrRoleRegistry
		}
		data, err := readRoleRegistryFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var status rolebundle.LifecycleStatus
		if json.Unmarshal(data, &status) != nil || fmt.Sprintf("%020d.json", status.Generation) != entry.Name() {
			return nil, ErrRoleRegistry
		}
		out = append(out, status)
	}
	return out, nil
}

func (r *RoleRegistry) CurrentLifecycle(roleID string, now time.Time) (rolebundle.LifecycleStatus, error) {
	dir, err := r.statusHistoryDir(roleID)
	if err != nil {
		return rolebundle.LifecycleStatus{}, err
	}
	history, err := r.readLifecycleHistory(dir)
	if err != nil || len(history) == 0 {
		return rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	previousGeneration := uint64(0)
	var previousDigest string
	for i, status := range history {
		// Historical signatures remain verifiable after their short freshness
		// window. Only the selected latest statement needs to be fresh now.
		verifyAt := status.IssuedAt
		if i == len(history)-1 {
			verifyAt = now
		}
		if status.Verify(r.publishers, verifyAt) != nil || status.Generation != previousGeneration+1 || status.PreviousStatusDigest != previousDigest {
			return rolebundle.LifecycleStatus{}, ErrRoleRegistry
		}
		previousDigest, err = rolebundle.LifecycleDigest(status)
		if err != nil {
			return rolebundle.LifecycleStatus{}, ErrRoleRegistry
		}
		previousGeneration = status.Generation
	}
	current := history[len(history)-1]
	if current.State != "active" {
		return rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	if _, err := r.ReadRelease(roleID, current.Version, current.ContentDigest); err != nil {
		return rolebundle.LifecycleStatus{}, err
	}
	return current, nil
}

// LifecycleForRelease returns the latest fresh lifecycle statement for one
// exact release. Publishing another release does not invalidate existing
// project pins; revocation remains terminal for the exact content digest.
func (r *RoleRegistry) LifecycleForRelease(roleID, version, digest string, now time.Time) (rolebundle.LifecycleStatus, error) {
	dir, err := r.statusHistoryDir(roleID)
	if err != nil {
		return rolebundle.LifecycleStatus{}, err
	}
	history, err := r.readLifecycleHistory(dir)
	if err != nil || len(history) == 0 {
		return rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	previousGeneration := uint64(0)
	previousDigest := ""
	var selected *rolebundle.LifecycleStatus
	for _, status := range history {
		if status.Verify(r.publishers, status.IssuedAt) != nil || status.Generation != previousGeneration+1 || status.PreviousStatusDigest != previousDigest {
			return rolebundle.LifecycleStatus{}, ErrRoleRegistry
		}
		previousDigest, err = rolebundle.LifecycleDigest(status)
		if err != nil {
			return rolebundle.LifecycleStatus{}, ErrRoleRegistry
		}
		previousGeneration = status.Generation
		if status.RoleID == roleID && status.Version == version && status.ContentDigest == digest {
			copy := status
			selected = &copy
		}
	}
	if selected == nil || selected.State != "active" {
		return rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	if selected.Verify(r.publishers, now) != nil {
		return rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	if _, err := r.ReadRelease(roleID, version, digest); err != nil {
		return rolebundle.LifecycleStatus{}, err
	}
	return *selected, nil
}

func (r *RoleRegistry) ReadPins(project string) (ProjectRolePins, error) {
	if !workspace.SafeSegment(project) {
		return ProjectRolePins{}, ErrRoleRegistry
	}
	path := r.workspace.ProjectPath(project)
	if err := safeFilePath(path); err != nil {
		return ProjectRolePins{}, err
	}
	doc, err := mdstore.ReadFile(path)
	if err != nil {
		return ProjectRolePins{}, err
	}
	out := ProjectRolePins{Pins: map[string]RolePin{}, History: []RolePinChange{}}
	if raw, ok := doc.Front.Get("role_registry_pins"); ok {
		if json.Unmarshal([]byte(raw), &out.Pins) != nil || out.Pins == nil {
			return ProjectRolePins{}, ErrRoleRegistry
		}
	}
	if raw, ok := doc.Front.Get("role_registry_generation"); ok {
		if _, err := fmt.Sscan(raw, &out.Generation); err != nil {
			return ProjectRolePins{}, ErrRoleRegistry
		}
	}
	if raw, ok := doc.Front.Get("role_registry_history"); ok {
		if json.Unmarshal([]byte(raw), &out.History) != nil || out.History == nil {
			return ProjectRolePins{}, ErrRoleRegistry
		}
	}
	if err := validatePinHistory(out); err != nil {
		return ProjectRolePins{}, err
	}
	return out, nil
}

func validatePinHistory(pins ProjectRolePins) error {
	if pins.Pins == nil || pins.History == nil || pins.Generation != uint64(len(pins.History)) {
		return ErrRoleRegistry
	}
	for i, entry := range pins.History {
		if entry.Generation != uint64(i+1) || entry.Selected.Generation != entry.Generation || entry.RoleID != entry.Selected.RoleID || entry.ChangedBy == "" || entry.Reason == "" {
			return ErrRoleRegistry
		}
		if i > 0 {
			prior, ok := pins.History[i-1].Selected, false
			for j := i - 1; j >= 0; j-- {
				if pins.History[j].RoleID == entry.RoleID {
					prior, ok = pins.History[j].Selected, true
					break
				}
			}
			if ok && (entry.Previous == nil || *entry.Previous != prior) {
				return ErrRoleRegistry
			}
			if !ok && entry.Previous != nil {
				return ErrRoleRegistry
			}
		}
	}
	latest := map[string]RolePin{}
	for _, entry := range pins.History {
		latest[entry.RoleID] = entry.Selected
	}
	if len(latest) != len(pins.Pins) {
		return ErrRoleRegistry
	}
	for id, pin := range pins.Pins {
		if latest[id] != pin {
			return ErrRoleRegistry
		}
	}
	return nil
}

// Pin binds an owner-selected exact release. Every change, including explicit
// rollback to an older release, increments the project generation.
func (r *RoleRegistry) Pin(project, roleID, version, digest, changedBy, reason string, now time.Time) (ProjectRolePins, error) {
	if changedBy == "" || strings.TrimSpace(reason) == "" || len(reason) > 2048 {
		return ProjectRolePins{}, ErrRoleRegistry
	}
	var updated ProjectRolePins
	err := r.withRegistryLock(func() error {
		if _, err := r.ReadRelease(roleID, version, digest); err != nil {
			return err
		}
		if _, err := r.LifecycleForRelease(roleID, version, digest, now); err != nil {
			return ErrRoleRegistry
		}
		return r.pinLocked(project, roleID, version, digest, changedBy, reason, now, &updated)
	})
	if err != nil {
		return ProjectRolePins{}, err
	}
	return updated, nil
}

func (r *RoleRegistry) pinLocked(project, roleID, version, digest, changedBy, reason string, now time.Time, updated *ProjectRolePins) error {
	projectDir := r.workspace.ProjectDir(project)
	err := WithFileLock(filepath.Join(projectDir, ".role-registry.lock"), func() error {
		pins, err := r.ReadPins(project)
		if err != nil {
			return err
		}
		if pins.Generation == ^uint64(0) {
			return ErrRoleRegistry
		}
		var previous *RolePin
		if old, ok := pins.Pins[roleID]; ok {
			copy := old
			previous = &copy
		}
		pins.Generation++
		changedAt := now.Round(0).UTC()
		selected := RolePin{RoleID: roleID, Version: version, Digest: digest, Generation: pins.Generation, ChangedAt: changedAt, ChangedBy: changedBy, Reason: reason}
		pins.Pins[roleID] = selected
		pins.History = append(pins.History, RolePinChange{Generation: pins.Generation, RoleID: roleID, Previous: previous, Selected: selected, ChangedAt: changedAt, ChangedBy: changedBy, Reason: reason})
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
		doc.Front.Set("role_registry_generation", fmt.Sprint(pins.Generation))
		doc.Front.Set("role_registry_pins", string(pinsJSON))
		doc.Front.Set("role_registry_history", string(historyJSON))
		if err := mdstore.WriteFile(path, doc); err != nil {
			return err
		}
		*updated = pins
		return nil
	})
	return err
}

func (r *RoleRegistry) Resolve(project, roleID string, now time.Time) (rolebundle.Release, RolePin, rolebundle.LifecycleStatus, error) {
	pins, err := r.ReadPins(project)
	if err != nil {
		return rolebundle.Release{}, RolePin{}, rolebundle.LifecycleStatus{}, err
	}
	pin, ok := pins.Pins[roleID]
	if !ok {
		return rolebundle.Release{}, RolePin{}, rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	release, err := r.ReadRelease(roleID, pin.Version, pin.Digest)
	if err != nil {
		return rolebundle.Release{}, RolePin{}, rolebundle.LifecycleStatus{}, err
	}
	status, err := r.LifecycleForRelease(roleID, pin.Version, pin.Digest, now)
	if err != nil {
		return rolebundle.Release{}, RolePin{}, rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	return release, pin, status, nil
}

func RoleFileMatches(release rolebundle.Release, canonicalRoles []team.Role) bool {
	for _, role := range canonicalRoles {
		if role.Name != release.Content.Role.Name {
			continue
		}
		left, _ := json.Marshal(role)
		right, _ := json.Marshal(release.Content.Role)
		return string(left) == string(right)
	}
	return false
}

func RoleReleaseDigest(content rolebundle.ReleaseContent) (string, error) {
	digest, err := rolebundle.ContentDigest(content)
	if err != nil {
		return "", err
	}
	return digest, nil
}
