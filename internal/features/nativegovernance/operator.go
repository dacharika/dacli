package nativegovernance

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

// operatorAdd provisions a distinct read-only principal, rather than treating a
// coding worker's read grant as operator authority (#1147). No schema upgrade or
// worker/task grant mutation occurs. Failed publication revokes the orphan actor.
func operatorAdd(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "project", "connection", "credential", "expires")
	if err != nil {
		return err
	}
	for _, key := range []string{"config", "project", "connection", "credential"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s is required", key)
		}
	}
	lifetime := time.Hour
	if f.Get("expires") != "" {
		lifetime, err = time.ParseDuration(f.Get("expires"))
		if err != nil || lifetime <= 0 || lifetime > 24*time.Hour {
			return clikit.Usagef("--expires must be positive and at most 24h")
		}
	}
	c, err := config(f.Get("config"))
	if err != nil {
		return err
	}
	w, err := native.ResolveWorkspace(c.Workspace, "")
	if err != nil {
		return denied()
	}
	project, err := store.LoadProject(w, f.Get("project"))
	if err != nil || project.Slug != f.Get("project") {
		return denied()
	}
	owner, err := native.ReadCredential(c.OwnerCredentialFile)
	if err != nil {
		return denied()
	}
	a, err := sessions.OpenCurrent(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if err != nil || a.AuthenticateOwner(owner) != nil {
		return denied()
	}
	credential, err := reserve(f.Get("credential"))
	if err != nil {
		return err
	}
	defer func() { _ = credential.Close() }()
	connection, err := reserve(f.Get("connection"))
	if err != nil {
		_ = os.Remove(f.Get("credential"))
		return err
	}
	defer func() { _ = connection.Close() }()
	published := false
	var actor sessions.Session
	defer func() {
		if !published {
			if actor.ID != "" {
				_ = a.Revoke(owner, actor.ID)
			}
			_ = os.Remove(f.Get("credential"))
			_ = os.Remove(f.Get("connection"))
		}
	}()
	actor, token, err := a.Enroll(owner, sessions.Scope{
		Actions:   []string{"read", "operator.read"},
		Resources: []string{"workspace:" + w.ID, "project:" + project.Slug},
	}, time.Now().Add(lifetime))
	if err != nil {
		return denied()
	}
	if err = writePrivate(credential, []byte(token+"\n")); err != nil {
		return err
	}
	data, err := json.Marshal(native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: c.Authority, WorkspaceID: w.ID, InstallationID: c.InstallationID, SessionID: actor.ID, CredentialFile: f.Get("credential")})
	if err != nil {
		return denied()
	}
	if err = writePrivate(connection, data); err != nil {
		return err
	}
	published = true
	fmt.Fprintf(ctx.Stdout, "Native operator reader %s enrolled for project %s; connection and credential saved privately.\n", actor.ID, project.Slug)
	return nil
}
