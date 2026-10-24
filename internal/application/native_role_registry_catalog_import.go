package application

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
)

// NativeRoleRegistryCatalogExport is the portable JSON shape returned by the
// hosted exact-role export API. It intentionally has no authority or trust
// enrollment fields: local publishers.json remains the sole trust source.
type NativeRoleRegistryCatalogExport struct {
	Selected  NativeRoleRegistryCatalogKey       `json:"Selected"`
	Releases  []NativeRoleRegistryCatalogRelease `json:"Releases"`
	Lifecycle []NativeRoleRegistryCatalogEvent   `json:"Lifecycle"`
}

type NativeRoleRegistryCatalogKey struct {
	Tenant        string `json:"Tenant"`
	CatalogID     string `json:"CatalogID"`
	RoleID        string `json:"RoleID"`
	Version       string `json:"Version"`
	ContentDigest string `json:"ContentDigest"`
}

type NativeRoleRegistryCatalogRelease struct {
	Key      NativeRoleRegistryCatalogKey `json:"Key"`
	Envelope []byte                       `json:"Envelope"`
}

type NativeRoleRegistryCatalogEvent struct {
	Key        NativeRoleRegistryCatalogKey `json:"Key"`
	Sequence   uint64                       `json:"Sequence"`
	State      string                       `json:"State"`
	Status     *rolebundle.LifecycleStatus  `json:"Status"`
	Actor      string                       `json:"Actor"`
	Reason     string                       `json:"Reason"`
	RecordedAt time.Time                    `json:"RecordedAt"`
}

type NativeRoleRegistryCatalogImportReceipt struct {
	Project           string `json:"project"`
	Tenant            string `json:"tenant"`
	CatalogID         string `json:"catalog_id"`
	RoleID            string `json:"role_id"`
	ReleaseCount      int    `json:"release_count"`
	LifecycleCount    int    `json:"lifecycle_count"`
	SelectedVersion   string `json:"selected_version"`
	SelectedDigest    string `json:"selected_digest"`
	SelectedState     string `json:"selected_state"`
	Selected          bool   `json:"selected"`
	ProjectGeneration uint64 `json:"project_generation"`
	ActivationNote    string `json:"activation_note"`
}

const maxNativeRoleCatalogEntries = 4096

// DecodeNativeRoleRegistryCatalogExport strictly decodes the hosted export's
// JSON object, rejecting unknown fields and trailing values.
func DecodeNativeRoleRegistryCatalogExport(data []byte) (NativeRoleRegistryCatalogExport, error) {
	var value NativeRoleRegistryCatalogExport
	if len(data) == 0 || len(data) > 8<<20 {
		return value, store.ErrRoleRegistry
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return NativeRoleRegistryCatalogExport{}, store.ErrRoleRegistry
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return NativeRoleRegistryCatalogExport{}, store.ErrRoleRegistry
	}
	return value, nil
}

// ReadNativeRoleRegistryCatalogExport reads one bounded, regular, non-symlink
// hosted export file before strict decoding.
func ReadNativeRoleRegistryCatalogExport(path string) (NativeRoleRegistryCatalogExport, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 8<<20 {
		return NativeRoleRegistryCatalogExport{}, store.ErrRoleRegistry
	}
	physicalPath, err := filepath.EvalSymlinks(path)
	if err != nil || physicalPath != path {
		return NativeRoleRegistryCatalogExport{}, store.ErrRoleRegistry
	}
	file, err := os.Open(path)
	if err != nil {
		return NativeRoleRegistryCatalogExport{}, store.ErrRoleRegistry
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return NativeRoleRegistryCatalogExport{}, store.ErrRoleRegistry
	}
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 8<<20 {
		return NativeRoleRegistryCatalogExport{}, store.ErrRoleRegistry
	}
	return DecodeNativeRoleRegistryCatalogExport(data)
}

// ImportCatalogExport caches a validated complete signed role export. It is
// data-only and cannot enroll keys, change project pins, or rewrite role files.
func (p *NativeRoleRegistryOwner) ImportCatalogExport(export NativeRoleRegistryCatalogExport, now time.Time) (NativeRoleRegistryCatalogImportReceipt, error) {
	if len(export.Releases) == 0 || len(export.Releases) > maxNativeRoleCatalogEntries || len(export.Lifecycle) == 0 || len(export.Lifecycle) > maxNativeRoleCatalogEntries ||
		!validRoleCatalogKey(export.Selected) {
		return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
	}
	_, registry, _, _, _, err := p.current()
	if err != nil {
		return NativeRoleRegistryCatalogImportReceipt{}, err
	}
	pins, err := registry.ReadPins(p.project)
	if err != nil {
		return NativeRoleRegistryCatalogImportReceipt{}, err
	}
	releases := make([]rolebundle.Release, 0, len(export.Releases))
	releaseEnvelopes := make([][]byte, 0, len(export.Releases))
	releaseKeys := make(map[string]bool, len(export.Releases))
	releaseDigestsByVersion := make(map[string]string, len(export.Releases))
	selectedFound := false
	for _, entry := range export.Releases {
		if !sameRoleCatalog(entry.Key, export.Selected) || len(entry.Envelope) == 0 || len(entry.Envelope) > 1<<20 {
			return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
		}
		decoder := json.NewDecoder(bytes.NewReader(entry.Envelope))
		decoder.DisallowUnknownFields()
		var release rolebundle.Release
		if decoder.Decode(&release) != nil {
			return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || release.Content.Role.Name != entry.Key.RoleID || release.Content.Version != entry.Key.Version || release.ContentDigest != entry.Key.ContentDigest {
			return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
		}
		if release.Content.Version == export.Selected.Version && release.ContentDigest == export.Selected.ContentDigest {
			selectedFound = true
		}
		key := release.Content.Version + "\x00" + release.ContentDigest
		if releaseKeys[key] || releaseDigestsByVersion[release.Content.Version] != "" {
			return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
		}
		releaseKeys[key] = true
		releaseDigestsByVersion[release.Content.Version] = release.ContentDigest
		releases = append(releases, release)
		releaseEnvelopes = append(releaseEnvelopes, append([]byte(nil), entry.Envelope...))
	}
	if !selectedFound {
		return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
	}
	statuses := make([]rolebundle.LifecycleStatus, 0, len(export.Lifecycle))
	previousByRelease := make(map[string]NativeRoleRegistryCatalogEvent)
	drafts := make(map[string]bool, len(export.Releases))
	for i := range export.Lifecycle {
		event := export.Lifecycle[i]
		if !sameRoleCatalog(event.Key, export.Selected) || !releaseKeys[event.Key.Version+"\x00"+event.Key.ContentDigest] || event.Sequence != uint64(i+1) || event.RecordedAt.IsZero() || event.RecordedAt.After(now.Add(time.Minute)) ||
			event.Actor == "" || len(event.Actor) > 256 || strings.TrimSpace(event.Actor) != event.Actor || event.Reason == "" || len(event.Reason) > 1024 || strings.TrimSpace(event.Reason) != event.Reason {
			return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
		}
		key := event.Key.Version + "\x00" + event.Key.ContentDigest
		previousEvent, hadPrevious := previousByRelease[key]
		if (!hadPrevious && event.State != "draft") || (hadPrevious && !validHostedLifecycleTransition(previousEvent.State, event.State)) {
			return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
		}
		if event.State == "draft" {
			if event.Status != nil {
				return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
			}
			drafts[key] = true
		} else {
			if event.Status == nil || !releaseKeys[event.Key.Version+"\x00"+event.Key.ContentDigest] {
				return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
			}
			wantState := "active"
			if event.State == "revoked" {
				wantState = "revoked"
			} else if event.State != "released" && event.State != "deprecated" {
				return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
			}
			if event.Status.RoleID != export.Selected.RoleID || event.Status.Version != event.Key.Version || event.Status.ContentDigest != event.Key.ContentDigest || event.Status.State != wantState {
				return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
			}
			statuses = append(statuses, *event.Status)
		}
		previousByRelease[key] = event
	}
	if len(statuses) == 0 || len(drafts) != len(releaseKeys) {
		return NativeRoleRegistryCatalogImportReceipt{}, store.ErrRoleRegistry
	}
	selectedStatus, err := registry.ImportRoleCatalogHistory(releases, releaseEnvelopes, statuses, export.Selected.RoleID, export.Selected.Version, export.Selected.ContentDigest, now)
	if err != nil {
		return NativeRoleRegistryCatalogImportReceipt{}, err
	}
	return NativeRoleRegistryCatalogImportReceipt{Project: p.project, Tenant: export.Selected.Tenant, CatalogID: export.Selected.CatalogID, RoleID: export.Selected.RoleID, ReleaseCount: len(releases), LifecycleCount: len(export.Lifecycle), SelectedVersion: export.Selected.Version, SelectedDigest: export.Selected.ContentDigest, SelectedState: selectedStatus.State, ProjectGeneration: pins.Generation, Selected: false, ActivationNote: "Catalog import caches signed history only; it never enrolls publisher keys, selects a release, or changes canonical role instructions."}, nil
}

func validRoleCatalogKey(key NativeRoleRegistryCatalogKey) bool {
	return key.Tenant != "" && strings.TrimSpace(key.Tenant) == key.Tenant && key.CatalogID != "" && strings.TrimSpace(key.CatalogID) == key.CatalogID &&
		key.RoleID != "" && key.Version != "" && key.ContentDigest != ""
}

func sameRoleCatalog(a, b NativeRoleRegistryCatalogKey) bool {
	return validRoleCatalogKey(a) && a.Tenant == b.Tenant && a.CatalogID == b.CatalogID && a.RoleID == b.RoleID
}

func validHostedLifecycleTransition(from, to string) bool {
	switch from {
	case "draft":
		return to == "draft" || to == "released" || to == "revoked"
	case "released":
		return to == "released" || to == "deprecated" || to == "revoked"
	case "deprecated":
		return to == "deprecated" || to == "released" || to == "revoked"
	default:
		return false
	}
}
