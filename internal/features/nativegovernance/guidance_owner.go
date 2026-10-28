package nativegovernance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/guidance"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

func canonicalGuidanceAuthority(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", denied()
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(path)))
	if e != nil {
		return "", e
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func guidancePlanOwner(configPath string) (guidance.PlanOwner, error) {
	c, e := config(configPath)
	if e != nil {
		return guidance.PlanOwner{}, denied()
	}
	credential, e := native.ReadCredential(c.OwnerCredentialFile)
	if e != nil {
		return guidance.PlanOwner{}, denied()
	}
	workspace, e := native.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return guidance.PlanOwner{}, denied()
	}
	authority, e := canonicalGuidanceAuthority(c.Authority)
	if e != nil {
		return guidance.PlanOwner{}, denied()
	}
	binding := guidance.PlanOwnerBinding{WorkspaceID: workspace.ID, InstallationID: c.InstallationID, AuthorityPath: authority}
	authorize := func() error {
		current, err := config(configPath)
		if err != nil || current != c {
			return denied()
		}
		key, err := native.ReadCredential(current.OwnerCredentialFile)
		if err != nil || key != credential {
			return denied()
		}
		resolved, err := native.ResolveWorkspace(current.Workspace, "")
		if err != nil || resolved.ID != binding.WorkspaceID {
			return denied()
		}
		selected, err := canonicalGuidanceAuthority(current.Authority)
		if err != nil || selected != binding.AuthorityPath {
			return denied()
		}
		a, err := sessions.OpenCurrent(selected, sessions.Binding{Workspace: resolved.ID, Installation: current.InstallationID})
		if err != nil || a.AuthenticateOwner(key) != nil {
			return denied()
		}
		return nil
	}
	seal := func(payload []byte) (string, error) {
		if err := authorize(); err != nil {
			return "", err
		}
		mac := hmac.New(sha256.New, []byte(credential))
		_, _ = mac.Write(payload)
		return hex.EncodeToString(mac.Sum(nil)), nil
	}
	owner := guidance.PlanOwner{Binding: binding, Authorize: authorize, Seal: seal}
	if authorize() != nil {
		return guidance.PlanOwner{}, denied()
	}
	return owner, nil
}
