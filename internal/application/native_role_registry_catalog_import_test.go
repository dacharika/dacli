package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
)

func TestNativeRoleRegistryCatalogImportIsExactLocalDataOnly(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	rolesBefore, err := os.ReadFile(f.w.RolePath("builder"))
	if err != nil {
		t.Fatal(err)
	}
	projectBefore, err := os.ReadFile(f.w.ProjectPath("core"))
	if err != nil {
		t.Fatal(err)
	}
	keysBefore, err := os.ReadFile(f.w.Root + "/.dacli/role-registry/publishers.json")
	if err != nil {
		t.Fatal(err)
	}
	first, second, export := makeCatalogTransferFixture(t, f)
	receipt, err := owner.ImportCatalogExport(export, f.now)
	if err != nil || receipt.Selected || receipt.ReleaseCount != 2 || receipt.LifecycleCount != 4 || receipt.SelectedVersion != "2.0.0" || receipt.SelectedState != "active" {
		t.Fatalf("complete hosted export import failed or selected implicitly: %+v %v", receipt, err)
	}
	verified, err := owner.Verify("builder", second.Content.Version, second.ContentDigest, f.now)
	if err != nil || verified.Target.Lifecycle.Generation != 2 {
		t.Fatalf("imported exact current release failed local verification: %+v %v", verified, err)
	}
	if _, err := owner.Verify("builder", first.Content.Version, first.ContentDigest, f.now); err == nil {
		t.Fatal("expired historical lifecycle was incorrectly treated as fresh for selection")
	}
	rolesAfter, _ := os.ReadFile(f.w.RolePath("builder"))
	projectAfter, _ := os.ReadFile(f.w.ProjectPath("core"))
	keysAfter, _ := os.ReadFile(f.w.Root + "/.dacli/role-registry/publishers.json")
	if !bytes.Equal(rolesBefore, rolesAfter) || !bytes.Equal(projectBefore, projectAfter) || !bytes.Equal(keysBefore, keysAfter) {
		t.Fatal("import changed canonical role, project pin state, or local publisher trust")
	}
	pins, err := f.registry.ReadPins("core")
	if err != nil || pins.Generation != 0 || len(pins.Pins) != 0 || len(pins.History) != 0 {
		t.Fatalf("import altered project selection: %+v %v", pins, err)
	}
	oldRelease, err := f.registry.ReadRelease("builder", first.Content.Version, first.ContentDigest)
	if err != nil || oldRelease.ContentDigest != first.ContentDigest {
		t.Fatalf("old exact signed release missing: %v", err)
	}
	expectedEnvelope := export.Releases[0].Envelope
	actualEnvelope, err := os.ReadFile(filepath.Join(f.w.Root, ".dacli", "role-registry", "releases", "builder", first.Content.Version, first.ContentDigest[len("sha256:"):]+".json"))
	if err != nil || !bytes.Equal(expectedEnvelope, actualEnvelope) {
		t.Fatalf("hosted release envelope bytes were not preserved exactly: %v", err)
	}
}

func TestNativeRoleRegistryCatalogImportRejectsMixedCatalogAndMalformedJSON(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	_, _, export := makeCatalogTransferFixture(t, f)
	foreign := export
	foreign.Lifecycle = append([]NativeRoleRegistryCatalogEvent(nil), export.Lifecycle...)
	foreign.Lifecycle[1].Key.CatalogID = "other-catalog"
	if _, err := owner.ImportCatalogExport(foreign, f.now); !errors.Is(err, store.ErrRoleRegistry) {
		t.Fatalf("mixed catalog lifecycle history accepted: %v", err)
	}
	gapped := export
	gapped.Lifecycle = append([]NativeRoleRegistryCatalogEvent(nil), export.Lifecycle...)
	gapped.Lifecycle[1].Sequence = 7
	if _, err := owner.ImportCatalogExport(gapped, f.now); !errors.Is(err, store.ErrRoleRegistry) {
		t.Fatalf("gapped hosted lifecycle sequence accepted: %v", err)
	}
	dangling := export
	dangling.Lifecycle = append([]NativeRoleRegistryCatalogEvent(nil), export.Lifecycle...)
	dangling.Lifecycle[0].Key.Version = "9.0.0"
	if _, err := owner.ImportCatalogExport(dangling, f.now); !errors.Is(err, store.ErrRoleRegistry) {
		t.Fatalf("lifecycle entry without a matching exported release accepted: %v", err)
	}
	tampered := export
	tampered.Releases = append([]NativeRoleRegistryCatalogRelease(nil), export.Releases...)
	tampered.Releases[0].Envelope = append([]byte(nil), export.Releases[0].Envelope...)
	var release rolebundle.Release
	if err := json.Unmarshal(tampered.Releases[0].Envelope, &release); err != nil {
		t.Fatal(err)
	}
	release.Signature[0] ^= 0xff
	tampered.Releases[0].Envelope, err = json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ImportCatalogExport(tampered, f.now); !errors.Is(err, store.ErrRoleRegistry) {
		t.Fatalf("tampered signed release envelope accepted: %v", err)
	}
	for _, input := range [][]byte{
		[]byte(`{"Selected":{},"Releases":[],"Lifecycle":[],"unexpected":true}`),
		[]byte(`{"Selected":{},"Releases":[],"Lifecycle":[]} {}`),
	} {
		if _, err := DecodeNativeRoleRegistryCatalogExport(input); !errors.Is(err, store.ErrRoleRegistry) {
			t.Fatalf("unknown or trailing JSON was accepted: %v", err)
		}
	}
}

func TestNativeRoleRegistryCatalogImportAcceptsRepeatedHostedDraftHistory(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	_, _, export := makeCatalogTransferFixture(t, f)
	events := []NativeRoleRegistryCatalogEvent{export.Lifecycle[0], export.Lifecycle[0], export.Lifecycle[1], export.Lifecycle[2], export.Lifecycle[3]}
	for i := range events {
		events[i].Sequence = uint64(i + 1)
	}
	export.Lifecycle = events
	receipt, err := owner.ImportCatalogExport(export, f.now)
	if err != nil || receipt.LifecycleCount != 5 || receipt.Selected {
		t.Fatalf("legitimate repeated draft event was rejected or activated: %+v %v", receipt, err)
	}
}

func TestDecodeNativeRoleRegistryCatalogExportMatchesHostedTypedJSON(t *testing.T) {
	type hostedKey struct {
		Tenant, CatalogID, RoleID, Version, ContentDigest string
	}
	type hostedRelease struct {
		Key      hostedKey
		Envelope []byte
	}
	type hostedEvent struct {
		Key        hostedKey
		Sequence   uint64
		State      string
		Status     *rolebundle.LifecycleStatus
		Actor      string
		Reason     string
		RecordedAt time.Time
	}
	type hostedExport struct {
		Selected  hostedKey
		Releases  []hostedRelease
		Lifecycle []hostedEvent
	}
	key := hostedKey{"tenant-a", "catalog-a", "builder", "1.0.0", "sha256:" + strings.Repeat("a", 64)}
	raw, err := json.Marshal(hostedExport{Selected: key, Releases: []hostedRelease{{Key: key, Envelope: []byte(`{"signed":true}`)}}, Lifecycle: []hostedEvent{{Key: key, Sequence: 1, State: "released", Actor: "account-a", Reason: "test", RecordedAt: time.Now().UTC()}}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeNativeRoleRegistryCatalogExport(raw)
	if err != nil || decoded.Selected.CatalogID != key.CatalogID || decoded.Releases[0].Key.ContentDigest != key.ContentDigest || string(decoded.Releases[0].Envelope) != `{"signed":true}` || decoded.Lifecycle[0].Actor != "account-a" {
		t.Fatalf("hosted typed JSON did not round-trip through local import DTO: %+v %v", decoded, err)
	}
}

func TestNativeRoleRegistryCatalogImportKeepsExactOldSelectionUsableAfterUnrelatedStatusExpires(t *testing.T) {
	f := newRoleRegistryOwnerFixture(t)
	owner, err := OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	first, second, export := makeCatalogTransferFixture(t, f)
	firstStatus, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: first.Content.Version, ContentDigest: first.ContentDigest, Generation: 1, State: "active", IssuedAt: f.now, ExpiresAt: f.now.Add(time.Hour), PublisherKeyID: "publisher"}, f.private)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := rolebundle.LifecycleDigest(firstStatus)
	if err != nil {
		t.Fatal(err)
	}
	secondStatus, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: second.Content.Version, ContentDigest: second.ContentDigest, Generation: 2, PreviousStatusDigest: firstDigest, State: "active", IssuedAt: f.now.Add(-2 * time.Hour), ExpiresAt: f.now.Add(-time.Hour), PublisherKeyID: "publisher"}, f.private)
	if err != nil {
		t.Fatal(err)
	}
	export.Selected = export.Releases[0].Key
	export.Lifecycle[1].Status = &firstStatus
	export.Lifecycle[3].Status = &secondStatus
	if _, err := owner.ImportCatalogExport(export, f.now); err != nil {
		t.Fatalf("exact fresh selected release was blocked by another release's expired status: %v", err)
	}
	if _, err := owner.Verify("builder", first.Content.Version, first.ContentDigest, f.now); err != nil {
		t.Fatalf("fresh exact selected release stopped verifying: %v", err)
	}
	if _, err := owner.Verify("builder", second.Content.Version, second.ContentDigest, f.now); err == nil {
		t.Fatal("unrelated expired lifecycle was treated as current for its own exact release")
	}
	upgradeAuthorityTo20(t, f, f.owner)
	owner, err = OpenNativeRoleRegistryOwner(f.config, "core")
	if err != nil {
		t.Fatal(err)
	}
	if pins, err := owner.Select("builder", first.Content.Version, first.ContentDigest, 0, "", "", "keep selected exact release", "pin", f.now); err != nil || pins.Generation != 1 {
		t.Fatalf("pin of still-fresh exact old release failed after unrelated status expiry: %+v %v", pins, err)
	}
}

func makeCatalogTransferFixture(t *testing.T, f roleRegistryOwnerFixture) (rolebundle.Release, rolebundle.Release, NativeRoleRegistryCatalogExport) {
	t.Helper()
	roles, err := store.ReadCheckpointRoles(f.w)
	if err != nil || len(roles) != 1 {
		t.Fatalf("read canonical role: %v", err)
	}
	first, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: "1.0.0", Role: roles[0]}, "publisher", f.private)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: 1, Version: "2.0.0", Role: roles[0]}, "publisher", f.private)
	if err != nil {
		t.Fatal(err)
	}
	oldStatus, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: first.Content.Version, ContentDigest: first.ContentDigest, Generation: 1, State: "active", IssuedAt: f.now.Add(-2 * time.Hour), ExpiresAt: f.now.Add(-time.Hour), PublisherKeyID: "publisher"}, f.private)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest, err := rolebundle.LifecycleDigest(oldStatus)
	if err != nil {
		t.Fatal(err)
	}
	currentStatus, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: "builder", Version: second.Content.Version, ContentDigest: second.ContentDigest, Generation: 2, PreviousStatusDigest: oldDigest, State: "active", IssuedAt: f.now, ExpiresAt: f.now.Add(time.Hour), PublisherKeyID: "publisher"}, f.private)
	if err != nil {
		t.Fatal(err)
	}
	firstEnvelope, _ := json.Marshal(first)
	firstEnvelope = append(firstEnvelope, '\n', ' ', '\n')
	secondEnvelope, _ := json.Marshal(second)
	key1 := NativeRoleRegistryCatalogKey{Tenant: "tenant-a", CatalogID: "catalog-a", RoleID: "builder", Version: first.Content.Version, ContentDigest: first.ContentDigest}
	key2 := NativeRoleRegistryCatalogKey{Tenant: "tenant-a", CatalogID: "catalog-a", RoleID: "builder", Version: second.Content.Version, ContentDigest: second.ContentDigest}
	entry := func(key NativeRoleRegistryCatalogKey, sequence uint64, state string, status *rolebundle.LifecycleStatus) NativeRoleRegistryCatalogEvent {
		return NativeRoleRegistryCatalogEvent{Key: key, Sequence: sequence, State: state, Status: status, Actor: "account-owner", Reason: "qualification fixture", RecordedAt: f.now}
	}
	return first, second, NativeRoleRegistryCatalogExport{Selected: key2, Releases: []NativeRoleRegistryCatalogRelease{{Key: key1, Envelope: firstEnvelope}, {Key: key2, Envelope: secondEnvelope}}, Lifecycle: []NativeRoleRegistryCatalogEvent{entry(key1, 1, "draft", nil), entry(key1, 2, "released", &oldStatus), entry(key2, 3, "draft", nil), entry(key2, 4, "released", &currentStatus)}}
}
