package application

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"reflect"
	"strings"
)

// NativeExternalProfile holds the GitHub credential selection in a private owner
// profile. Worker connections cannot configure or execute external authority.
type NativeExternalProfile struct {
	SchemaVersion  int                           `json:"schema_version"`
	CredentialFile string                        `json:"credential_file"`
	Policy         sessions.NativeExternalPolicy `json:"policy"`
}
type NativeExternalOwner struct {
	acceptance       *NativeAcceptanceOwner
	profilePath      string
	profile          NativeExternalProfile
	credentialDigest string
}

func (NativeExternalOwner) String() string   { return "NativeExternalOwner" }
func (NativeExternalOwner) GoString() string { return "NativeExternalOwner" }
func externalCredential(path string) (string, error) {
	raw, e := nativeconfig.ReadPrivate(path, 16384)
	token := strings.TrimSpace(string(raw))
	if e != nil || token == "" || strings.ContainsAny(token, "\x00\r\n \t") {
		return "", sessions.ErrDenied
	}
	return token, nil
}
func externalCredentialDigest(v string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(v)))
}
func OpenNativeExternalOwner(config, reviewPolicy, profilePath string) (*NativeExternalOwner, error) {
	acceptance, e := OpenNativeAcceptanceOwner(config, reviewPolicy)
	if e != nil {
		return nil, e
	}
	var profile NativeExternalProfile
	if verificationPrivateJSON(profilePath, &profile) != nil || profile.SchemaVersion != 1 || profile.Policy.SchemaVersion != 1 || profile.Policy.ProjectID != acceptance.policy.ProjectID || profile.Policy.TaskID != acceptance.policy.TaskID || !profile.Policy.ConfiguredAt.IsZero() {
		return nil, sessions.ErrDenied
	}
	token, e := externalCredential(profile.CredentialFile)
	if e != nil {
		return nil, e
	}
	p := &NativeExternalOwner{acceptance: acceptance, profilePath: profilePath, profile: profile, credentialDigest: externalCredentialDigest(token)}
	if _, _, e = p.authenticate(); e != nil {
		return nil, e
	}
	return p, nil
}
func (p *NativeExternalOwner) selectionCurrent(owner string) (string, error) {
	if p == nil || p.acceptance == nil {
		return "", sessions.ErrDenied
	}
	if e := p.acceptance.ownerSelectionCurrent(); e != nil {
		return "", e
	}
	var profile NativeExternalProfile
	if verificationPrivateJSON(p.profilePath, &profile) != nil || !reflect.DeepEqual(profile, p.profile) {
		return "", sessions.ErrDenied
	}
	credential, e := externalCredential(profile.CredentialFile)
	if e != nil || externalCredentialDigest(credential) != p.credentialDigest {
		return "", sessions.ErrDenied
	}
	current, e := nativeconfig.ReadCredential(p.acceptance.owner.config.OwnerCredentialFile)
	if e != nil || current != owner {
		return "", sessions.ErrDenied
	}
	return credential, nil
}
func (p *NativeExternalOwner) authenticate() (*sessions.Authority, string, error) {
	if p == nil {
		return nil, "", sessions.ErrDenied
	}
	a, owner, _, e := p.acceptance.authenticate()
	if e != nil {
		return nil, "", e
	}
	if _, e = p.selectionCurrent(owner); e != nil {
		return nil, "", e
	}
	return a, owner, nil
}
func (p *NativeExternalOwner) target(credential string) (store.NativeExternalTarget, error) {
	w, e := nativeconfig.ResolveWorkspace(p.acceptance.owner.config.Workspace, p.acceptance.policy.WorkspaceID)
	if e != nil {
		return store.NativeExternalTarget{}, e
	}
	q := p.profile.Policy
	return store.NativeExternalTarget{Workspace: w, Project: q.ProjectID, Task: q.TaskID, Repository: q.Repository, Checkout: q.Checkout, GitHubRepository: q.GitHubRepository, SourceBranch: q.SourceBranch, TargetBranch: q.TargetBranch, Commit: q.Commit, Tree: q.Tree, ExpectedTrunk: q.ExpectedTrunk, ExpectedRemoteHead: q.ExpectedRemoteHead, Generation: q.Generation, SliceGeneration: q.SliceGeneration, RequiredChecks: append([]string(nil), q.RequiredChecks...), Credential: credential}, nil
}
func (p *NativeRecoveryAccess) UpgradeExternal() error {
	a, o, e := p.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradeNativeExternal(o)
}
func (p *NativeExternalOwner) Configure(ctx context.Context) (sessions.NativeExternalPolicy, error) {
	a, owner, e := p.authenticate()
	if e != nil {
		return sessions.NativeExternalPolicy{}, e
	}
	credential, e := p.selectionCurrent(owner)
	if e != nil {
		return sessions.NativeExternalPolicy{}, e
	}
	target, e := p.target(credential)
	if e != nil {
		return sessions.NativeExternalPolicy{}, e
	}
	observed, e := store.ObserveNativeExternal(ctx, target)
	if e != nil || observed.Trunk != p.profile.Policy.ExpectedTrunk || observed.RemoteHead != p.profile.Policy.ExpectedRemoteHead {
		return sessions.NativeExternalPolicy{}, sessions.ErrDenied
	}
	if _, _, e = p.authenticate(); e != nil {
		return sessions.NativeExternalPolicy{}, e
	}
	return a.ConfigureNativeExternal(owner, p.profile.Policy)
}
func (p *NativeExternalOwner) Inspect(key string) (sessions.NativeExternalRecord, error) {
	a, o, e := p.authenticate()
	if e != nil {
		return sessions.NativeExternalRecord{}, e
	}
	r, e := a.ReadNativeExternal(o, key)
	if e != nil || r.Policy.TaskID != p.profile.Policy.TaskID || r.Policy.ProjectID != p.profile.Policy.ProjectID {
		return sessions.NativeExternalRecord{}, sessions.ErrDenied
	}
	return r, nil
}
func (p *NativeExternalOwner) InspectPolicy() (sessions.NativeExternalPolicy, error) {
	a, o, e := p.authenticate()
	if e != nil {
		return sessions.NativeExternalPolicy{}, e
	}
	return a.ReadNativeExternalPolicy(o, p.profile.Policy.TaskID)
}
