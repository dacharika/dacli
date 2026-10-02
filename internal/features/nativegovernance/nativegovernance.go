// Package nativegovernance adapts explicit native authority; never legacy ambient identity.
package nativegovernance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

var Commands = []clikit.Command{
	{Path: "native serve", Brief: "Serve scoped native governance MCP using a private explicit session connection", Usage: "dacli native serve --connection <private-file>", Run: serve},
	{Path: "native bootstrap", Brief: "Initialize an absent native authority and privately save its owner credential", Mutates: true, Usage: "dacli native bootstrap --config <private-file>", Run: bootstrap},
	{Path: "native enroll", Brief: "Owner-enroll a scoped native session for an existing canonical task", Mutates: true, Usage: "dacli native enroll --config <private-file> --task <ref> --connection <new-private-file> --credential <new-private-file> [--path <relative-path>]... [--expires <duration>]", Run: enroll},
}

// AuthenticatesExplicitly lists exactly the handlers exempt from the legacy grant gate.
func AuthenticatesExplicitly(path string) bool {
	switch path {
	case "native bootstrap", "native enroll":
		return true
	}
	return false
}
func flags(args []string, allowed ...string) (*clikit.Flags, error) {
	f, parseErr := clikit.ParseFlags(args)
	if parseErr != nil {
		return nil, parseErr
	}
	if e := f.Reject(allowed...); e != nil {
		return nil, e
	}
	if len(f.Pos) != 0 {
		return nil, clikit.Usagef("unexpected positional arguments")
	}
	for _, k := range allowed {
		if k != "path" && len(f.All(k)) > 1 {
			return nil, clikit.Usagef("flag --%s must appear once", k)
		}
	}
	return f, nil
}
func denied() error {
	return clikit.Refusedf("native authority unavailable or refused; verify private configuration and credentials")
}
func serve(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "connection")
	if e != nil {
		return e
	}
	if f.Get("connection") == "" {
		return clikit.Usagef("--connection is required")
	}
	s, e := native.Open(f.Get("connection"))
	if e != nil {
		return denied()
	}
	if e = s.Serve(os.Stdin, ctx.Stdout); e != nil {
		return denied()
	}
	return nil
}

type ownerConfig struct {
	SchemaVersion       int    `json:"schema_version"`
	Workspace           string `json:"workspace"`
	Authority           string `json:"authority"`
	InstallationID      string `json:"installation_id"`
	OwnerCredentialFile string `json:"owner_credential_file"`
}

func config(path string) (ownerConfig, error) {
	var c ownerConfig
	b, e := native.ReadPrivate(path, 16384)
	if e != nil || native.Decode(b, &c) != nil || c.SchemaVersion != 1 || c.InstallationID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.OwnerCredentialFile) {
		return c, denied()
	}
	return c, nil
}

// reserve never truncates an existing credential. Failed issuance leaves authority
// history intact; no recovery path resets or deletes the authority aggregate.
func reserve(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, denied()
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, denied()
	}
	return f, nil
}
func writePrivate(f *os.File, data []byte) error {
	if _, e := f.Write(data); e != nil {
		return denied()
	}
	if e := f.Sync(); e != nil {
		return denied()
	}
	return nil
}
func bootstrap(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config is required")
	}
	c, e := config(f.Get("config"))
	if e != nil {
		return e
	}
	w, e := native.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return denied()
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	credential, e := reserve(c.OwnerCredentialFile)
	if e != nil {
		return e
	}
	defer func() { _ = credential.Close() }()
	token, e := a.Bootstrap()
	if e != nil {
		_ = os.Remove(c.OwnerCredentialFile)
		return denied()
	}
	if e = writePrivate(credential, []byte(token+"\n")); e != nil {
		return e
	}
	fmt.Fprintln(ctx.Stdout, "Native authority initialized; owner credential saved privately.")
	return nil
}
func enroll(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config", "task", "connection", "credential", "path", "expires")
	if e != nil {
		return e
	}
	for _, k := range []string{"config", "task", "connection", "credential"} {
		if f.Get(k) == "" {
			return clikit.Usagef("--%s is required", k)
		}
	}
	lifetime := time.Hour
	if f.Get("expires") != "" {
		lifetime, e = time.ParseDuration(f.Get("expires"))
		if e != nil || lifetime <= 0 || lifetime > 24*time.Hour {
			return clikit.Usagef("--expires must be positive and at most 24h")
		}
	}
	c, e := config(f.Get("config"))
	if e != nil {
		return e
	}
	w, e := native.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return denied()
	}
	owner, e := native.ReadCredential(c.OwnerCredentialFile)
	if e != nil {
		return denied()
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	task, e := store.FindTask(w, f.Get("task"))
	if e != nil {
		return denied()
	}
	obs, e := checkout.ObserveRepositoryIdentity(w.Root)
	if e != nil {
		return denied()
	}
	paths := []string{"workspace:" + w.ID, "task:" + task.ID}
	for _, p := range f.All("path") {
		if p == "" || filepath.IsAbs(p) || filepath.ToSlash(filepath.Clean(p)) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
			return denied()
		}
		paths = append(paths, "path:"+p)
	}
	credential, e := reserve(f.Get("credential"))
	if e != nil {
		return e
	}
	defer func() { _ = credential.Close() }()
	connection, e := reserve(f.Get("connection"))
	if e != nil {
		_ = os.Remove(f.Get("credential"))
		return e
	}
	defer func() { _ = connection.Close() }()
	issued := false
	defer func() {
		if !issued {
			_ = os.Remove(f.Get("credential"))
			_ = os.Remove(f.Get("connection"))
		}
	}()
	if e = a.BindTask(owner, sessions.TaskBinding{TaskID: task.ID, ProjectID: task.Project, Repository: obs.Repository, AdmissionEnabled: true}); e != nil {
		return denied()
	}
	s, token, e := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit"}, Resources: paths}, time.Now().Add(lifetime))
	if e != nil {
		return denied()
	}
	issued = true
	if e = writePrivate(credential, []byte(token+"\n")); e != nil {
		return e
	}
	b, e := json.Marshal(native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: c.Authority, WorkspaceID: w.ID, InstallationID: c.InstallationID, SessionID: s.ID, CredentialFile: f.Get("credential")})
	if e != nil {
		return denied()
	}
	if e = writePrivate(connection, b); e != nil {
		return e
	}
	fmt.Fprintf(ctx.Stdout, "Native session %s enrolled; connection and credential saved privately.\n", s.ID)
	return nil
}
