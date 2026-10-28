package guidance

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed assets/predecessors/*.json
var predecessorFiles embed.FS

type predecessorManifest struct {
	Schema             string         `json:"schema"`
	Identity           string         `json:"identity"`
	OriginCommit       string         `json:"origin_commit"`
	BeforeBundleDigest string         `json:"before_bundle_digest"`
	TargetBundleDigest string         `json:"target_bundle_digest"`
	Files              []manifestFile `json:"files"`
}
type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type bundle struct {
	identity, digest, manifestDigest, origin string
	files                                    map[string]string
}

func reviewedBundles() ([]bundle, error) {
	current, err := embeddedCurrent()
	if err != nil {
		return nil, err
	}
	target := digestSource(current)
	legacy := make(map[string]string, len(legacyDigests))
	for k, v := range legacyDigests {
		legacy[k] = v
	}
	out := []bundle{{identity: "embedded-reviewed-dacli-skill/legacy", digest: digestDigestMap(legacy), files: legacy}}
	entries, err := fs.ReadDir(predecessorFiles, "assets/predecessors")
	if err != nil {
		return nil, err
	}
	seenID, seenDigest := map[string]bool{}, map[string]bool{}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			return nil, errors.New("invalid predecessor registry entry")
		}
		b, err := predecessorFiles.ReadFile("assets/predecessors/" + ent.Name())
		if err != nil {
			return nil, err
		}
		m, files, err := parsePredecessorManifest(b, target)
		if err != nil {
			return nil, err
		}
		if seenID[m.Identity] || seenDigest[m.BeforeBundleDigest] {
			return nil, errors.New("duplicate predecessor registration")
		}
		seenID[m.Identity] = true
		seenDigest[m.BeforeBundleDigest] = true
		out = append(out, bundle{m.Identity, m.BeforeBundleDigest, digest(b), m.OriginCommit, files})
	}
	return out, nil
}

func parsePredecessorManifest(data []byte, target string) (predecessorManifest, map[string]string, error) {
	var zero predecessorManifest
	if e := uniqueJSONKeys(data); e != nil {
		return zero, nil, e
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var m predecessorManifest
	if e := d.Decode(&m); e != nil {
		return zero, nil, e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return zero, nil, errors.New("trailing predecessor JSON")
	}
	if m.Schema != "dacli-guidance-predecessor/v1" || !strings.HasPrefix(m.Identity, "embedded-reviewed-dacli-skill/") || strings.ContainsAny(m.Identity, "\\ \t\n") || !validCommit(m.OriginCommit) || !validSHA(m.BeforeBundleDigest) || !validSHA(m.TargetBundleDigest) || m.TargetBundleDigest != target || len(m.Files) != len(KnownFiles()) {
		return zero, nil, errors.New("unsupported predecessor manifest binding")
	}
	files := map[string]string{}
	keys := KnownFiles()
	for i, f := range m.Files {
		if f.Path != keys[i] || !validSHA(f.SHA256) {
			return zero, nil, errors.New("predecessor manifest paths or digests invalid")
		}
		files[f.Path] = f.SHA256
	}
	if digestDigestMap(files) != m.BeforeBundleDigest {
		return zero, nil, errors.New("predecessor bundle digest mismatch")
	}
	return m, files, nil
}

func uniqueJSONKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		tok, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON object key")
				}
				seen[key] = true
				if e = value(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		case '[':
			for d.More() {
				if e := value(); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing predecessor JSON")
	}
	return nil
}
func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validCommit(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func digestDigestMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(m[k])
		b.WriteByte(0)
	}
	return digest(b.Bytes())
}
func bundleMatch(root string, b bundle) (bool, error) {
	got := map[string]string{}
	for _, rel := range KnownFiles() {
		if _, e := safeParents(root, rel); e != nil {
			return false, e
		}
		data, info, e := checkedRegular(filepath.Join(root, filepath.FromSlash(rel)), maxSkillFile)
		if e != nil {
			if os.IsNotExist(e) {
				return false, nil
			}
			return false, e
		}
		if !guidanceSingleLink(info) {
			return false, fmt.Errorf("managed target %s must be single-link", rel)
		}
		got[rel] = digest(data)
	}
	for k, v := range b.files {
		if got[k] != v {
			return false, nil
		}
	}
	return true, nil
}
