package nativegovernance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

// readerWritePrivate is isolated so tests can exercise post-issuance storage
// failures without changing authority or filesystem semantics in production.
var readerWritePrivate = writePrivate

func readerAdd(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "workspace", "project", "task", "connection", "credential", "expires")
	if err != nil {
		return err
	}
	for _, key := range []string{"config", "connection", "credential"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s is required", key)
		}
	}
	scopeCount := 0
	for _, key := range []string{"workspace", "project", "task"} {
		if len(f.All(key)) != 0 {
			scopeCount++
		}
	}
	if scopeCount != 1 {
		return clikit.Usagef("exactly one of --workspace, --project or --task is required")
	}
	if len(f.All("workspace")) != 0 && f.Get("workspace") != "true" {
		return clikit.Usagef("--workspace is a bare scope selector")
	}
	if len(f.All("project")) != 0 && f.Get("project") == "" || len(f.All("task")) != 0 && f.Get("task") == "" {
		return clikit.Usagef("--project and --task require exact target values")
	}
	lifetime := time.Hour
	if f.Get("expires") != "" {
		lifetime, err = time.ParseDuration(f.Get("expires"))
		if err != nil || lifetime <= 0 || lifetime > 24*time.Hour {
			return clikit.Usagef("--expires must be positive and at most 24h")
		}
	}
	if filepath.Clean(f.Get("connection")) == filepath.Clean(f.Get("credential")) {
		return clikit.Usagef("--connection and --credential must name different files")
	}

	c, err := config(f.Get("config"))
	if err != nil {
		return err
	}
	w, err := native.ResolveWorkspace(c.Workspace, "")
	if err != nil {
		return denied()
	}
	resource := "workspace:" + w.ID
	if f.Get("project") != "" {
		project, loadErr := store.LoadProject(w, f.Get("project"))
		if loadErr != nil || project.Slug != f.Get("project") {
			return denied()
		}
		resource = "project:" + project.Slug
	} else if f.Get("task") != "" {
		task, findErr := store.FindTask(w, f.Get("task"))
		if findErr != nil || task.ID != f.Get("task") {
			return denied()
		}
		resource = "task:" + task.ID
	}
	owner, err := native.ReadCredential(c.OwnerCredentialFile)
	if err != nil {
		return denied()
	}
	a, err := sessions.OpenCurrent(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if err != nil || a.AuthenticateOwner(owner) != nil {
		return denied()
	}

	// Reserve both new paths before issuing authority. O_EXCL also refuses an
	// existing symlink; no credential is ever written through a caller link.
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
	issued := false
	defer func() {
		if !issued {
			_ = os.Remove(f.Get("credential"))
			_ = os.Remove(f.Get("connection"))
		}
	}()

	actor, token, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read"}, Resources: []string{resource}}, time.Now().Add(lifetime))
	if err != nil {
		return denied()
	}
	issued = true
	if err = readerWritePrivate(credential, []byte(token+"\n")); err != nil {
		return readerPersistenceFailure(actor.ID, f.Get("connection"), f.Get("credential"), err)
	}
	data, err := json.Marshal(native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: c.Authority, WorkspaceID: w.ID, InstallationID: c.InstallationID, SessionID: actor.ID, CredentialFile: f.Get("credential")})
	if err != nil {
		return readerPersistenceFailure(actor.ID, f.Get("connection"), f.Get("credential"), err)
	}
	if err = readerWritePrivate(connection, data); err != nil {
		return readerPersistenceFailure(actor.ID, f.Get("connection"), f.Get("credential"), err)
	}
	if err = credential.Close(); err != nil {
		return readerPersistenceFailure(actor.ID, f.Get("connection"), f.Get("credential"), err)
	}
	if err = connection.Close(); err != nil {
		return readerPersistenceFailure(actor.ID, f.Get("connection"), f.Get("credential"), err)
	}
	fmt.Fprintf(ctx.Stdout, "Native MCP reader %s enrolled with read-only scope %s; connection and credential saved privately. Select this connection profile and restart the MCP process to use it.\n", actor.ID, resource)
	return nil
}

func readerPersistenceFailure(session, connection, credential string, cause error) error {
	return fmt.Errorf("native MCP reader %s was issued but output persistence is uncertain (%v); preserve and reconcile the reserved files %q and %q with the owner authority before retrying", session, cause, connection, credential)
}
