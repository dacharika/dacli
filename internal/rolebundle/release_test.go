package rolebundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/team"
)

func fixtureContent() ReleaseContent {
	return ReleaseContent{SchemaVersion: SchemaVersion, Version: "1.2.3", Role: team.Role{Name: "implementer", Summary: "build", Skills: []string{"go"}, Scope: []string{"internal/**"}, OutOfScope: []string{"secrets/**"}, Shortcuts: []string{"test"}, Grant: "rw", EscalateTo: []string{"reviewer"}, WIP: 2, Kind: "implementer", Runtime: "codex", Model: "model-x", Profile: team.ModelProfile{ID: "model-x", CostTier: 2, MaxTaskPoints: 5, ContextLimit: 80000, CapabilityTags: []string{"code"}}, FallbackTo: []string{"senior"}, MaxPoints: 7, Prompt: "inspect, change, verify"}, References: []Reference{{Kind: "skill", ID: "go", Version: "v2", Digest: "sha256:" + strings.Repeat("a", 64)}, {Kind: "policy", ID: "native", Version: "v1", Digest: "sha256:" + strings.Repeat("b", 64)}}}
}

func TestReleaseSignsNormalizedRoleAndRejectsTampering(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	release, err := Sign(fixtureContent(), "publisher-a", private)
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Verify(map[string]ed25519.PublicKey{"publisher-a": pub}); err != nil {
		t.Fatal(err)
	}
	changed := release
	changed.Content.Role.Prompt += "\nnew instruction"
	if err := changed.Verify(map[string]ed25519.PublicKey{"publisher-a": pub}); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("tampered prompt accepted: %v", err)
	}
	changed = release
	changed.ContentDigest = "sha256:" + strings.Repeat("0", 64)
	if err := changed.Verify(map[string]ed25519.PublicKey{"publisher-a": pub}); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("tampered digest accepted: %v", err)
	}
	if err := release.Verify(map[string]ed25519.PublicKey{"other": pub}); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("unknown publisher accepted: %v", err)
	}
}

func TestEveryExportedRoleBehaviorLeafChangesDigest(t *testing.T) {
	base := fixtureContent()
	baseDigest, err := ContentDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range roleLeafPaths(reflect.TypeOf(team.Role{}), "") {
		t.Run(path, func(t *testing.T) {
			content := fixtureContent()
			v := reflect.ValueOf(&content.Role).Elem()
			for _, part := range strings.Split(path, ".") {
				v = v.FieldByName(part)
			}
			if !mutateLeaf(v) {
				t.Fatalf("add a digest mutation case for %s", path)
			}
			digest, err := ContentDigest(content)
			if err != nil {
				t.Fatal(err)
			}
			if digest == baseDigest {
				t.Fatalf("behavior field %s is omitted from release digest", path)
			}
		})
	}
}

func roleLeafPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		path := field.Name
		if prefix != "" {
			path = prefix + "." + path
		}
		if field.Type.Kind() == reflect.Struct {
			out = append(out, roleLeafPaths(field.Type, path)...)
		} else {
			out = append(out, path)
		}
	}
	return out
}

func mutateLeaf(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-changed")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(v.Float() + 1)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return false
		}
		v.Set(reflect.Append(v, reflect.ValueOf("changed").Convert(v.Type().Elem())))
	default:
		return false
	}
	return true
}

func TestSemverAndReferenceValidation(t *testing.T) {
	for _, version := range []string{"0.0.0", "1.2.3", "v1.2.3", "1.2.3-rc.1+build.2"} {
		if !validVersion(version) {
			t.Errorf("valid semver rejected: %s", version)
		}
	}
	for _, version := range []string{"", "01.2.3", "1.2", "1.2.3-", "1.2.3+", "1.2.3-01", "1.2.3-alpha.01"} {
		if validVersion(version) {
			t.Errorf("invalid semver accepted: %s", version)
		}
	}
	for _, version := range []string{"v1", "v2", "1.2.3", "policy-rev-7"} {
		if !validReferenceVersion(version) {
			t.Errorf("exact existing policy/skill revision rejected: %s", version)
		}
	}
	for _, version := range []string{"latest", "*", ">=1.0.0", "^1.0.0", "~1.0.0", "1.x", "1.2.*"} {
		if validReferenceVersion(version) {
			t.Errorf("floating reference selector accepted: %s", version)
		}
	}
	content := fixtureContent()
	content.References[0].Version = ""
	if _, err := Sign(content, "publisher-a", make(ed25519.PrivateKey, ed25519.PrivateKeySize)); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("unversioned ref accepted: %v", err)
	}
}

func TestLifecycleSignatureAndFreshness(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	status, err := SignStatus(LifecycleStatus{SchemaVersion: SchemaVersion, RoleID: "implementer", Version: "1.2.3", ContentDigest: "sha256:" + strings.Repeat("c", 64), Generation: 4, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "publisher-a"}, private)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"publisher-a": pub}
	if err := status.Verify(keys, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := status.Verify(keys, status.ExpiresAt); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("expired lifecycle accepted: %v", err)
	}
	status.Generation++
	if err := status.Verify(keys, now); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("tampered lifecycle accepted: %v", err)
	}
}
