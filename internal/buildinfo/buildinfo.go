// Package buildinfo holds build-time identity: the version and the tool's
// own repo (where `dacli report` files bugs). Version is overridable at
// build with -ldflags "-X .../buildinfo.Version=v1.2.3".
package buildinfo

import (
	"runtime/debug"
)

// Version is the dacli release. "dev" until a build stamps it.
var Version = "dev"

// RuntimeIdentity reports build identity embedded by the Go toolchain. Empty
// revision means the binary was not built from a VCS checkout; dirty is nil
// when the toolchain did not record vcs.modified.
type RuntimeIdentity struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Dirty    *bool  `json:"dirty"`
}

func Identity() RuntimeIdentity {
	identity := RuntimeIdentity{Version: Version}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return identity
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			identity.Revision = setting.Value
		case "vcs.modified":
			modified := setting.Value == "true"
			identity.Dirty = &modified
		}
	}
	return identity
}

// Repo is the tool's own GitHub repository — the default target for
// `dacli report`, so agent-hit bugs flow back to the tool, not the user's
// project. Overridable per invocation (--repo) or env (DACLI_REPORT_REPO).
const Repo = "mlnomadpy/dacli"
