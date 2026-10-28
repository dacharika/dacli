package guidance

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

const PlanSchemaV2 = "dacli-guidance-plan/v2"

type BeforeBundleBinding struct {
	Identity       string `json:"identity"`
	Digest         string `json:"digest"`
	ManifestDigest string `json:"manifest_digest"`
	OriginCommit   string `json:"origin_commit,omitempty"`
}
type PlanOwnerBinding struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	AuthorityPath  string `json:"authority_path"`
}
type PlanOwner struct {
	Binding   PlanOwnerBinding
	Authorize func() error
	Seal      func([]byte) (string, error)
}

func ownerPayload(p Plan) ([]byte, error) {
	p.OwnerSeal = ""
	p.PlanDigest = ""
	b, e := json.Marshal(p)
	if e != nil {
		return nil, e
	}
	return append([]byte("dacli-guidance-plan-owner/v2\x00"), b...), nil
}
func validOwner(o PlanOwner) bool {
	return o.Binding.WorkspaceID != "" && o.Binding.InstallationID != "" && o.Binding.AuthorityPath != "" && o.Authorize != nil && o.Seal != nil
}
func sealPlan(p *Plan, o PlanOwner) error {
	if !validOwner(o) {
		return errors.New("complete authenticated owner is required")
	}
	if err := o.Authorize(); err != nil {
		return err
	}
	p.Owner = &o.Binding
	b, e := ownerPayload(*p)
	if e != nil {
		return e
	}
	p.OwnerSeal, e = o.Seal(b)
	if e != nil {
		return e
	}
	p.PlanDigest = planDigest(*p)
	return nil
}
func verifyOwner(p Plan, o PlanOwner) error {
	if p.Owner == nil || p.BeforeBundle == nil || p.OwnerSeal == "" || !validOwner(o) || *p.Owner != o.Binding {
		return errors.New("plan owner binding is invalid")
	}
	if e := o.Authorize(); e != nil {
		return e
	}
	b, e := ownerPayload(p)
	if e != nil {
		return e
	}
	expected, e := o.Seal(b)
	if e != nil {
		return e
	}
	if len(expected) != len(p.OwnerSeal) || subtle.ConstantTimeCompare([]byte(expected), []byte(p.OwnerSeal)) != 1 {
		return errors.New("plan owner seal is invalid")
	}
	if p.PlanDigest != planDigest(p) {
		return errors.New("plan content digest is invalid")
	}
	return nil
}
func WritePlanOwned(path string, p Plan, o PlanOwner) error {
	if p.Schema != PlanSchemaV2 || p.BeforeBundle == nil || p.Owner == nil || p.OwnerSeal == "" {
		return errors.New("owned writer requires a v2 plan")
	}
	if e := verifyOwner(p, o); e != nil {
		return e
	}
	if _, _, e := validatePlan(p); e != nil {
		return e
	}
	return writePlan(path, p, func() error { return verifyOwner(p, o) })
}

type backupBindingV2 struct {
	Root           string           `json:"root"`
	RootIdentity   string           `json:"root_identity"`
	Owner          PlanOwnerBinding `json:"owner"`
	BeforeIdentity string           `json:"before_identity"`
	BeforeDigest   string           `json:"before_digest"`
	BeforeManifest string           `json:"before_manifest"`
	TargetIdentity string           `json:"target_identity"`
	TargetDigest   string           `json:"target_digest"`
}

func backupIDV2(p Plan) string {
	b, _ := json.Marshal(backupBindingV2{p.Root, p.RootIdentity, *p.Owner, p.BeforeBundle.Identity, p.BeforeBundle.Digest, p.BeforeBundle.ManifestDigest, p.SourceIdentity, p.SourceDigest})
	return digest(b)
}
func PreviewOwned(root, sourceIdentity string, owner PlanOwner) (Plan, error) {
	if !validOwner(owner) {
		return Plan{}, errors.New("complete authenticated owner is required")
	}
	if sourceIdentity != "" && sourceIdentity != ReviewedSourceID {
		return Plan{}, errors.New("replacement source is not the reviewed embedded bundle")
	}
	root = filepath.Clean(root)
	rinfo, err := safeRoot(root)
	if err != nil {
		return Plan{}, err
	}
	current, err := embeddedCurrent()
	if err != nil {
		return Plan{}, err
	}
	bundles, err := reviewedBundles()
	if err != nil {
		return Plan{}, err
	}
	matched := -1
	for i, b := range bundles {
		ok, e := bundleMatch(root, b)
		if e != nil {
			return Plan{}, e
		}
		if ok {
			matched = i
			break
		}
	}
	_, _, isCurrent, e := candidateDigest(root, currentDigests(current))
	if e != nil {
		return Plan{}, e
	}
	if isCurrent {
		return Plan{}, errors.New("selected skill already matches the reviewed current bundle")
	}
	if matched < 0 {
		return Plan{}, errors.New("selected skill is customized, conflicting or unrecognized; preserve it and review manually")
	}
	b := bundles[matched]
	p := Plan{Schema: PlanSchemaV2, Operation: "reviewed-refresh", Root: root, RootIdentity: fileIdentity(rinfo), SourceIdentity: ReviewedSourceID, SourceDigest: digestSource(current), CreatedAt: time.Now().UTC(), Files: []fileRecord{}, BeforeBundle: &BeforeBundleBinding{Identity: b.identity, Digest: b.digest, ManifestDigest: b.manifestDigest, OriginCommit: b.origin}}
	for _, rel := range KnownFiles() {
		before, info, e := checkedRegular(filepath.Join(root, filepath.FromSlash(rel)), maxSkillFile)
		if e != nil {
			return Plan{}, e
		}
		if !guidanceSingleLink(info) {
			return Plan{}, fmt.Errorf("managed target %s must be a single-link regular file", rel)
		}
		parents, e := safeParents(root, rel)
		if e != nil {
			return Plan{}, e
		}
		p.Files = append(p.Files, fileRecord{Path: rel, Before: digest(before), After: digest(current[rel]), TargetID: fileIdentity(info), ParentIDs: parents})
	}
	p.Owner = &owner.Binding
	id := backupIDV2(p)
	for i := range p.Files {
		p.Files[i].BackupPath = filepath.Join(root, backupDirectoryName, id, filepath.FromSlash(p.Files[i].Path))
	}
	if err := sealPlan(&p, owner); err != nil {
		return Plan{}, err
	}
	return p, nil
}
