package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
)

// ImportRoleCatalogHistory imports a complete signed role-wide history without
// changing project selection or canonical role data. Historical statements are
// checked at issue time; the selected exact statement must also be current.
func (r *RoleRegistry) ImportRoleCatalogHistory(releases []rolebundle.Release, releaseEnvelopes [][]byte, statuses []rolebundle.LifecycleStatus, roleID, selectedVersion, selectedDigest string, now time.Time) (rolebundle.LifecycleStatus, error) {
	if r == nil || len(releases) == 0 || len(releases) != len(releaseEnvelopes) || len(statuses) == 0 || roleID == "" || selectedVersion == "" || selectedDigest == "" {
		return rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	releaseKeys := make(map[string]bool, len(releases))
	releaseDigestsByVersion := make(map[string]string, len(releases))
	for i, release := range releases {
		var exact rolebundle.Release
		if len(releaseEnvelopes[i]) == 0 || len(releaseEnvelopes[i]) > 1<<20 || decodeRoleRelease(releaseEnvelopes[i], &exact) != nil ||
			!reflect.DeepEqual(exact, release) || release.Verify(r.publishers) != nil || release.Content.Role.Name != roleID {
			return rolebundle.LifecycleStatus{}, ErrRoleRegistry
		}
		key := release.Content.Version + "\x00" + release.ContentDigest
		if releaseKeys[key] || releaseDigestsByVersion[release.Content.Version] != "" {
			return rolebundle.LifecycleStatus{}, ErrRoleRegistry
		}
		releaseKeys[key] = true
		releaseDigestsByVersion[release.Content.Version] = release.ContentDigest
	}
	var selected *rolebundle.LifecycleStatus
	previousGeneration := uint64(0)
	previousDigest := ""
	for _, status := range statuses {
		if status.RoleID != roleID || status.Verify(r.publishers, status.IssuedAt) != nil ||
			status.Generation != previousGeneration+1 || status.PreviousStatusDigest != previousDigest ||
			!releaseKeys[status.Version+"\x00"+status.ContentDigest] {
			return rolebundle.LifecycleStatus{}, ErrRoleRegistry
		}
		digest, err := rolebundle.LifecycleDigest(status)
		if err != nil {
			return rolebundle.LifecycleStatus{}, ErrRoleRegistry
		}
		previousGeneration, previousDigest = status.Generation, digest
		if status.Version == selectedVersion && status.ContentDigest == selectedDigest {
			copy := status
			selected = &copy
		}
	}
	if selected == nil || selected.Verify(r.publishers, now) != nil {
		return rolebundle.LifecycleStatus{}, fmt.Errorf("%w: selected release lifecycle is not currently fresh", ErrRoleRegistry)
	}
	if _, ok := releaseKeys[selectedVersion+"\x00"+selectedDigest]; !ok {
		return rolebundle.LifecycleStatus{}, ErrRoleRegistry
	}
	returnStatus := *selected
	if err := r.withRegistryLock(func() error {
		if err := r.preflightRoleCatalogImportLocked(releases, statuses); err != nil {
			return err
		}
		for i, release := range releases {
			if err := r.storeCatalogReleaseLocked(release, releaseEnvelopes[i]); err != nil {
				return err
			}
		}
		for _, status := range statuses {
			if err := r.storeHistoricalLifecycleLocked(status); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return rolebundle.LifecycleStatus{}, err
	}
	return returnStatus, nil
}

func (r *RoleRegistry) preflightRoleCatalogImportLocked(releases []rolebundle.Release, statuses []rolebundle.LifecycleStatus) error {
	incomingReleases := make(map[string]bool, len(releases))
	versions := make(map[string]string, len(releases))
	for _, release := range releases {
		version := release.Content.Version
		if previous := versions[version]; previous != "" && previous != release.ContentDigest {
			return ErrRoleRegistry
		}
		versions[version] = release.ContentDigest
		incomingReleases[version+"\x00"+release.ContentDigest] = true
		path, err := r.releasePath(release.Content.Role.Name, version, release.ContentDigest)
		if err != nil {
			return ErrRoleRegistry
		}
		versionDir := filepath.Dir(path)
		if err := safeExistingDir(versionDir, false); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return ErrRoleRegistry
		}
		entries, err := os.ReadDir(versionDir)
		if err != nil {
			return ErrRoleRegistry
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && entry.Name() != filepath.Base(path) {
				return fmt.Errorf("%w: role/version release is immutable", ErrRoleRegistry)
			}
		}
		old, err := readRoleRegistryFile(path)
		if os.IsNotExist(err) {
			continue
		}
		var cached rolebundle.Release
		if err != nil || decodeRoleRelease(old, &cached) != nil || cached.Verify(r.publishers) != nil || !reflect.DeepEqual(cached, release) {
			return fmt.Errorf("%w: exact signed release envelope conflicts with cached data", ErrRoleRegistry)
		}
	}
	dir, err := r.statusHistoryDir(statuses[0].RoleID)
	if err != nil {
		return err
	}
	var existing []rolebundle.LifecycleStatus
	if err := safeExistingDir(dir, false); err == nil {
		existing, err = r.readLifecycleHistory(dir)
		if err != nil {
			return ErrRoleRegistry
		}
	} else if !os.IsNotExist(err) {
		return ErrRoleRegistry
	}
	combined := make(map[uint64]rolebundle.LifecycleStatus, len(existing)+len(statuses))
	for _, status := range existing {
		combined[status.Generation] = status
	}
	for _, status := range statuses {
		if current, ok := combined[status.Generation]; ok && !reflect.DeepEqual(current, status) {
			return ErrRoleRegistry
		}
		combined[status.Generation] = status
	}
	var previousGeneration uint64
	previousDigest := ""
	revoked := make(map[string]bool)
	for generation := uint64(1); generation <= uint64(len(combined)); generation++ {
		status, ok := combined[generation]
		if !ok || status.RoleID != statuses[0].RoleID || status.Verify(r.publishers, status.IssuedAt) != nil || status.Generation != previousGeneration+1 || status.PreviousStatusDigest != previousDigest {
			return ErrRoleRegistry
		}
		key := status.Version + "\x00" + status.ContentDigest
		if !incomingReleases[key] {
			if _, err := r.ReadRelease(status.RoleID, status.Version, status.ContentDigest); err != nil {
				return ErrRoleRegistry
			}
		}
		if status.State == "active" && revoked[status.ContentDigest] {
			return ErrRoleRegistry
		}
		if status.State == "revoked" {
			revoked[status.ContentDigest] = true
		}
		previousDigest, err = rolebundle.LifecycleDigest(status)
		if err != nil {
			return ErrRoleRegistry
		}
		previousGeneration = generation
	}
	return nil
}

func (r *RoleRegistry) storeCatalogReleaseLocked(release rolebundle.Release, envelope []byte) error {
	path, err := r.releasePath(release.Content.Role.Name, release.Content.Version, release.ContentDigest)
	if err != nil || safeExistingDir(filepath.Dir(path), true) != nil {
		return ErrRoleRegistry
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil && !os.IsNotExist(err) {
		return ErrRoleRegistry
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && entry.Name() != filepath.Base(path) {
			return fmt.Errorf("%w: role/version release is immutable", ErrRoleRegistry)
		}
	}
	if old, readErr := readRoleRegistryFile(path); readErr == nil {
		var cached rolebundle.Release
		if decodeRoleRelease(old, &cached) != nil || cached.Verify(r.publishers) != nil || !reflect.DeepEqual(cached, release) {
			return fmt.Errorf("%w: exact signed release envelope is immutable", ErrRoleRegistry)
		}
		return nil
	} else if !os.IsNotExist(readErr) {
		return ErrRoleRegistry
	}
	if safeFilePath(path) != nil {
		return ErrRoleRegistry
	}
	return mdstore.WriteBytes(path, envelope, 0o600)
}

func (r *RoleRegistry) storeHistoricalLifecycleLocked(status rolebundle.LifecycleStatus) error {
	if status.Verify(r.publishers, status.IssuedAt) != nil {
		return ErrRoleRegistry
	}
	dir, err := r.statusHistoryDir(status.RoleID)
	if err != nil || safeExistingDir(dir, true) != nil {
		return ErrRoleRegistry
	}
	path := filepath.Join(dir, fmt.Sprintf("%020d.json", status.Generation))
	if existing, readErr := readRoleRegistryFile(path); readErr == nil {
		var cached rolebundle.LifecycleStatus
		if json.Unmarshal(existing, &cached) != nil || cached.Verify(r.publishers, cached.IssuedAt) != nil || !reflect.DeepEqual(cached, status) {
			return ErrRoleRegistry
		}
		return nil
	} else if !os.IsNotExist(readErr) {
		return ErrRoleRegistry
	}
	history, err := r.readLifecycleHistory(dir)
	if err != nil {
		return ErrRoleRegistry
	}
	previousGeneration := uint64(0)
	previousDigest := ""
	revoked := map[string]bool{}
	for _, item := range history {
		if item.Verify(r.publishers, item.IssuedAt) != nil || item.Generation != previousGeneration+1 || item.PreviousStatusDigest != previousDigest {
			return ErrRoleRegistry
		}
		previousDigest, err = rolebundle.LifecycleDigest(item)
		if err != nil {
			return ErrRoleRegistry
		}
		previousGeneration = item.Generation
		if item.State == "revoked" {
			revoked[item.ContentDigest] = true
		}
	}
	if status.Generation != previousGeneration+1 || status.PreviousStatusDigest != previousDigest || status.State == "active" && revoked[status.ContentDigest] {
		return ErrRoleRegistry
	}
	if _, err := r.ReadRelease(status.RoleID, status.Version, status.ContentDigest); err != nil {
		return err
	}
	data, err := json.Marshal(status)
	if err != nil || safeFilePath(path) != nil {
		return ErrRoleRegistry
	}
	return mdstore.WriteBytes(path, append(data, '\n'), 0o600)
}
