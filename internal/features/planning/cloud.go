package planning

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
	"github.com/mlnomadpy/dacli/internal/deviceidentity"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func cloudFlags(args []string, allowed ...string) (*clikit.Flags, error) {
	f, e := clikit.ParseFlags(args)
	if e != nil {
		return nil, e
	}
	if e = f.Reject(allowed...); e != nil {
		return nil, e
	}
	if len(f.Pos) != 1 || !workspace.SafeSegment(f.Pos[0]) {
		return nil, clikit.Usagef("exact local project slug required")
	}
	return f, nil
}
func cloudLocation(ctx *clikit.Ctx, slug string, mutate bool) (*workspace.Workspace, string, error) {
	w, id, e := clikit.OpenWorkspace(ctx)
	if e != nil {
		return nil, "", e
	}
	if _, e = store.LoadProject(w, slug); e != nil {
		return nil, "", e
	}
	if mutate && (id.ID != agentid.RootID || id.Grant != model.GrantRW) {
		return nil, "", clikit.Refusedf("cloud project connection requires the local owner and explicit device identity")
	}
	return w, filepath.Join(w.Root, workspace.Dir, "cloudsync", slug), nil
}
func cloudContext() (context.Context, context.CancelFunc) {
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, deadline := context.WithTimeout(bounded, 2*time.Minute)
	return ctx, func() { deadline(); cancel() }
}
func cloudEmit(ctx *clikit.Ctx, value any) error {
	if ctx.JSON {
		return clikit.EmitJSON(ctx, value)
	}
	fmt.Fprintf(ctx.Stdout, "%+v\n", value)
	return nil
}
func cloudError(e error) error {
	var identityError *deviceidentity.Error
	if errors.As(e, &identityError) && !identityError.Retryable {
		return clikit.Refusedf("device identity refused (%s); inspect login/whoami and the OS credential store", safeDeviceFailure(identityError.Code))
	}

	if errors.Is(e, cloudsync.ErrTransportDenied) || errors.Is(e, cloudsync.ErrBindingConflict) || errors.Is(e, cloudsync.ErrInvalidSignature) || errors.Is(e, cloudsync.ErrUnsupportedVersion) || errors.Is(e, cloudsync.ErrInvalidPayload) {
		return clikit.Refusedf("cloud metadata route, schema, signature or authority refused; existing queue preserved")
	}
	// Do not echo authentication-provider errors or response bodies into logs.
	return fmt.Errorf("cloud metadata operation unavailable; local commands and durable queue remain usable")
}
func safeDeviceFailure(code string) string {
	switch code {
	case "revoked", "access_denied", "invalid_grant", "unsupported_store", "identity_mismatch", "credential_store_refused", "metadata_refused", "identity_denied", "offline", "provider_unavailable":
		return code
	}
	return "device_identity_refused"
}
func cloudKeys(binding cloudsync.ProjectBindingResponse) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for id, text := range binding.TrustedKeys {
		raw, e := base64.StdEncoding.DecodeString(text)
		if e != nil || len(raw) != ed25519.PublicKeySize || id == "" {
			return nil, cloudsync.ErrTransportDenied
		}
		out[id] = ed25519.PublicKey(raw)
	}
	return out, nil
}
func cloudClient(root string, connection cloudsync.ProjectConnection, session deviceidentity.Session) (*cloudsync.Client, error) {
	if session.TenantID != connection.Binding.Route.TenantID || session.DeviceID != connection.Binding.Route.DeviceID {
		return nil, cloudsync.ErrTransportDenied
	}
	keys, e := cloudKeys(connection.Binding)
	if e != nil {
		return nil, e
	}
	for id, key := range keys {
		if !reflect.DeepEqual(session.TrustedKeys[id], key) {
			return nil, cloudsync.ErrTransportDenied
		}
	}
	// Outbox recovery also needs the device's own immutable signature identity.
	private := session.SigningKey()
	if len(private) != ed25519.PrivateKeySize || session.SigningKeyID == "" {
		return nil, cloudsync.ErrTransportDenied
	}
	keys[session.SigningKeyID] = private.Public().(ed25519.PublicKey)
	return cloudsync.Open(filepath.Join(root, "transport"), cloudsync.Config{TenantID: session.TenantID, ProjectID: connection.Binding.Route.ProjectID, ProducerVersion: "0.5.0", SigningKeyID: session.SigningKeyID, SigningKey: session.SigningKey(), TrustedKeys: keys, Cursor: cloudsync.Cursor{MinimumSchemaVersion: 1, MaximumSchemaVersion: 1}})
}
func cloudAuthenticate(ctx context.Context, provider, tenant, device string) (deviceidentity.Session, error) {
	client, e := deviceidentity.Open(provider, tenant, device)
	if e != nil {
		return deviceidentity.Session{}, e
	}
	return client.Authenticate(ctx)
}

// Repository equality does not identify a workspace: two checkouts can share
// an origin while their canonical workspace IDs and selected projects differ.
// Check before signing, changing a tombstone or accessing device credentials.
func cloudLocalBinding(w *workspace.Workspace, slug string, connection cloudsync.ProjectConnection) error {
	if connection.Binding.Route.WorkspaceID != w.ID || connection.LocalProject != slug {
		return clikit.Refusedf("project connection belongs to another local workspace or project; connect the exact local binding")
	}
	return nil
}

func cmdProjectConnect(ctx *clikit.Ctx, args []string) error {
	f, e := cloudFlags(args, "provider", "tenant", "device", "remote-project", "environment", "repository")
	if e != nil {
		return e
	}
	for _, name := range []string{"provider", "tenant", "device", "remote-project", "environment", "repository"} {
		if f.Get(name) == "" {
			return clikit.Usagef("--%s required", name)
		}
	}
	w, root, e := cloudLocation(ctx, f.Pos[0], true)
	if e != nil {
		return e
	}
	repository, e := cloudRepository(w.Root)
	if e != nil {
		return e
	}
	bounded, cancel := cloudContext()
	defer cancel()
	session, e := cloudAuthenticate(bounded, f.Get("provider"), f.Get("tenant"), f.Get("device"))
	if e != nil {
		return cloudError(e)
	}
	route := cloudsync.SyncRoute{WorkspaceID: w.ID, TenantID: f.Get("tenant"), ProjectID: f.Get("remote-project"), EnvironmentID: f.Get("environment"), RepositoryID: f.Get("repository"), DeviceID: f.Get("device")}
	if session.TenantID != route.TenantID || session.DeviceID != route.DeviceID {
		return cloudError(cloudsync.ErrTransportDenied)
	}
	transport := &cloudsync.HTTPTransport{Provider: f.Get("provider"), Authorization: session.Authorization()}
	binding, e := transport.ObserveBinding(bounded, route)
	if e != nil {
		return cloudError(e)
	}
	remote, e := normalizeCloudRepository(binding.RepositoryURL)
	if e != nil || remote != repository {
		return clikit.Refusedf("remote repository binding does not match the unambiguous local repository")
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return e
	}
	return store.WithFileLock(filepath.Join(root, "project.lock"), func() error {
		old, readErr := cloudsync.LoadProjectConnection(root)
		now := time.Now().UTC()
		next := cloudsync.ProjectConnection{SchemaVersion: 1, Provider: f.Get("provider"), LocalProject: f.Pos[0], Binding: binding, Connected: true, Generation: 1, ConnectedAt: now}
		if readErr == nil {
			if old.Provider != next.Provider || old.LocalProject != next.LocalProject || !reflect.DeepEqual(old.Binding, next.Binding) {
				return cloudError(cloudsync.ErrBindingConflict)
			}
			next = old
			if !old.Connected {
				next.Connected = true
				next.Generation++
				next.ConnectedAt = now
				next.DisconnectedAt = nil
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return cloudError(readErr)
		}
		client, e := cloudClient(root, next, session)
		if e != nil {
			return cloudError(e)
		}
		// Durable metadata precedes connection publication. Repeating the same
		// generation resolves the immutable row rather than duplicating it.
		if e = queueCloudTransitions(client, root, next); e != nil {
			return cloudError(e)
		}
		if e = cloudsync.SaveProjectConnection(root, next); e != nil {
			return cloudError(e)
		}
		status, e := cloudsync.ReadProjectStatus(root)
		if e != nil {
			return cloudError(e)
		}
		return cloudEmit(ctx, status)
	})
}
func cmdProjectDisconnect(ctx *clikit.Ctx, args []string) error {
	f, e := cloudFlags(args)
	if e != nil {
		return e
	}
	w, root, e := cloudLocation(ctx, f.Pos[0], true)
	if e != nil {
		return e
	}
	return store.WithFileLock(filepath.Join(root, "project.lock"), func() error {
		connection, e := cloudsync.LoadProjectConnection(root)
		if e != nil {
			return cloudError(e)
		}
		if e = cloudLocalBinding(w, f.Pos[0], connection); e != nil {
			return e
		}
		if connection.Connected {
			now := time.Now().UTC()
			connection.Connected = false
			connection.Generation++
			connection.DisconnectedAt = &now
			if e = cloudsync.SaveProjectConnection(root, connection); e != nil {
				return cloudError(e)
			}
		}
		if local, err := deviceidentity.Open(connection.Provider, connection.Binding.Route.TenantID, connection.Binding.Route.DeviceID); err == nil {
			if session, err := local.LocalSession(); err == nil {
				if client, err := cloudClient(root, connection, session); err == nil {
					_ = queueCloudRegistration(client, connection, "disconnected")
				}
			}
		}
		status, e := cloudsync.ReadProjectStatus(root)
		if e != nil {
			return cloudError(e)
		}
		return cloudEmit(ctx, status)
	})
}
func cmdProjectStatus(ctx *clikit.Ctx, args []string) error {
	f, e := cloudFlags(args)
	if e != nil {
		return e
	}
	_, root, e := cloudLocation(ctx, f.Pos[0], false)
	if e != nil {
		return e
	}
	status, e := cloudsync.ReadProjectStatus(root)
	if errors.Is(e, os.ErrNotExist) {
		return cloudEmit(ctx, cloudsync.ProjectStatus{Health: "not-connected", NextAction: "project connect"})
	}
	if e != nil {
		return cloudError(e)
	}
	return cloudEmit(ctx, status)
}
func cmdProjectSync(ctx *clikit.Ctx, args []string) error {
	f, e := cloudFlags(args)
	if e != nil {
		return e
	}
	w, root, e := cloudLocation(ctx, f.Pos[0], true)
	if e != nil {
		return e
	}
	bounded, cancel := cloudContext()
	defer cancel()
	return store.WithFileLock(filepath.Join(root, "project.lock"), func() error {
		connection, e := cloudsync.LoadProjectConnection(root)
		if e != nil {
			return cloudError(e)
		}
		if e = cloudLocalBinding(w, f.Pos[0], connection); e != nil {
			return e
		}
		if !connection.Connected {
			return clikit.Refusedf("project disconnected; preserve tombstone and reconnect the same verified binding")
		}
		repository, err := cloudRepository(w.Root)
		expected, parseErr := normalizeCloudRepository(connection.Binding.RepositoryURL)
		if err != nil || parseErr != nil || repository != expected {
			return cloudFailure(root, connection, "repository-changed", cloudsync.ErrBindingConflict)
		}
		// Signing local metadata does not require online/fresh bearer authority.
		// The OS key remains private, and transport starts only after Authenticate.
		identity, e := deviceidentity.Open(connection.Provider, connection.Binding.Route.TenantID, connection.Binding.Route.DeviceID)
		if e != nil {
			return cloudFailure(root, connection, "device-unavailable", e)
		}
		local, e := identity.LocalSession()
		if e != nil {
			return cloudFailure(root, connection, "device-unavailable", e)
		}
		client, e := cloudClient(root, connection, local)
		if e != nil {
			return cloudFailure(root, connection, "binding-refused", e)
		}
		if e = queueCloudTransitions(client, root, connection); e != nil {
			return cloudFailure(root, connection, "queue-unavailable", e)
		}
		if e = queueCloudTasks(client, w, f.Pos[0]); e != nil {
			return cloudFailure(root, connection, "queue-unavailable", e)
		}
		session, e := identity.Authenticate(bounded)
		if e != nil {
			return cloudFailure(root, connection, "device-unavailable", e)
		}
		if session.TenantID != connection.Binding.Route.TenantID || session.DeviceID != connection.Binding.Route.DeviceID {
			return cloudFailure(root, connection, "binding-refused", cloudsync.ErrTransportDenied)
		}
		transport := &cloudsync.HTTPTransport{Provider: connection.Provider, Authorization: session.Authorization(), Binding: connection.Binding, DeliveryCursor: connection.DeliveryCursor, AcknowledgedSequence: client.AcknowledgedSequence()}
		fresh, e := transport.ObserveBinding(bounded, connection.Binding.Route)
		if e != nil {
			return cloudFailure(root, connection, "remote-unavailable", e)
		}
		if !reflect.DeepEqual(fresh, connection.Binding) {
			return cloudFailure(root, connection, "binding-changed", cloudsync.ErrBindingConflict)
		}
		if e = client.Sync(bounded, transport); e != nil {
			return cloudFailure(root, connection, "exchange-unavailable", e)
		}
		now := time.Now().UTC()
		connection.LastSuccess = &now
		connection.LastFailureCode = ""
		connection.DeliveryCursor = transport.DeliveryCursor
		if e = cloudsync.SaveProjectConnection(root, connection); e != nil {
			return cloudError(e)
		}
		status, e := cloudsync.ReadProjectStatus(root)
		if e != nil {
			return cloudError(e)
		}
		return cloudEmit(ctx, status)
	})
}
func cloudFailure(root string, c cloudsync.ProjectConnection, code string, cause error) error {
	var identityError *deviceidentity.Error
	if errors.As(cause, &identityError) {
		code = safeDeviceFailure(identityError.Code)
	}
	now := time.Now().UTC()
	c.LastFailure = &now
	c.LastFailureCode = code
	if e := cloudsync.SaveProjectConnection(root, c); e != nil {
		return cloudError(e)
	}
	return cloudError(cause)
}

// Replay immutable connection generations in order, including an offline
// disconnect that could not sign at the time. No tombstone is dropped.
func queueCloudTransitions(client *cloudsync.Client, root string, current cloudsync.ProjectConnection) error {
	entries, e := os.ReadDir(filepath.Join(root, "history"))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(root, "history", entry.Name()))
		if e != nil {
			return e
		}
		var historical cloudsync.ProjectConnection
		if json.Unmarshal(raw, &historical) != nil || historical.Binding.Route != current.Binding.Route || historical.Provider != current.Provider {
			return cloudsync.ErrBindingConflict
		}
		state := "disconnected"
		if historical.Connected {
			state = "connected"
		}
		if e = queueCloudRegistration(client, historical, state); e != nil {
			return e
		}
	}
	state := "disconnected"
	if current.Connected {
		state = "connected"
	}
	return queueCloudRegistration(client, current, state)
}
func queueCloudRegistration(client *cloudsync.Client, c cloudsync.ProjectConnection, state string) error {
	_, e := client.Enqueue("project_registration", fmt.Sprintf("project-registration:%s:%d", c.LocalProject, c.Generation), struct {
		RegistrationID string `json:"registration_id"`
		WorkspaceID    string `json:"workspace_id"`
		RepositoryID   string `json:"repository_id"`
		EnvironmentID  string `json:"environment_id"`
		DefaultBranch  string `json:"default_branch"`
		State          string `json:"state"`
		Version        uint64 `json:"version"`
	}{fmt.Sprintf("connection-%d", c.Generation), c.Binding.Route.WorkspaceID, c.Binding.Route.RepositoryID, c.Binding.Route.EnvironmentID, c.Binding.DefaultBranch, state, c.Generation})
	return e
}
func queueCloudTasks(client *cloudsync.Client, w *workspace.Workspace, slug string) error {
	tasks, e := store.ListTasks(w, slug, "")
	if e != nil {
		return e
	}
	for _, task := range tasks {
		status := string(task.Status)
		switch status {
		case "open", "active", "blocked", "done":
		default:
			return cloudsync.ErrInvalidPayload
		}
		// Titles, acceptance prose, prompts and source paths never enter this DTO.
		// The stable opaque identity/status digest permits immutable offline retry.
		info, e := os.Stat(task.Path)
		if e != nil {
			return e
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", task.ID, status, info.ModTime().UnixNano())))
		id := hex.EncodeToString(digest[:])
		_, e = client.Enqueue("event_summary", "task-status:"+id, struct {
			SummaryID  string    `json:"summary_id"`
			EventKind  string    `json:"event_kind"`
			ObjectType string    `json:"object_type"`
			ObjectID   string    `json:"object_id"`
			Status     string    `json:"status"`
			OccurredAt time.Time `json:"occurred_at"`
			Version    int       `json:"version"`
		}{id, "task_status", "task", task.ID, status, info.ModTime().UTC(), 1})
		if e != nil {
			return e
		}
	}
	return nil
}
func normalizeCloudRepository(raw string) (string, error) {
	if strings.HasPrefix(raw, "git@") {
		parts := strings.SplitN(strings.TrimPrefix(raw, "git@"), ":", 2)
		if len(parts) != 2 {
			return "", cloudsync.ErrTransportDenied
		}
		raw = "https://" + parts[0] + "/" + parts[1]
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return "", cloudsync.ErrTransportDenied
	}
	if u.User != nil && (u.Scheme != "ssh" || u.User.Username() != "git") {
		return "", cloudsync.ErrTransportDenied
	}
	path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	if path == "" || strings.Contains(path, "..") {
		return "", cloudsync.ErrTransportDenied
	}
	return "https://" + strings.ToLower(u.Host) + path, nil
}
func cloudRepository(root string) (string, error) {
	remotes, e := gitx.Run(root, "remote")
	if e != nil {
		return "", clikit.Refusedf("repository identity unavailable")
	}
	identities := map[string]bool{}
	for _, name := range strings.Fields(remotes) {
		raw, e := gitx.Run(root, "remote", "get-url", "--all", name)
		if e != nil {
			return "", clikit.Refusedf("repository identity unavailable")
		}
		for _, address := range strings.Fields(raw) {
			identity, e := normalizeCloudRepository(address)
			if e != nil {
				return "", clikit.Refusedf("repository remote identity is unsupported")
			}
			identities[identity] = true
		}
	}
	if len(identities) != 1 {
		return "", clikit.Refusedf("repository identity is ambiguous; exactly one canonical remote repository required")
	}
	for value := range identities {
		return value, nil
	}
	return "", clikit.Refusedf("repository identity unavailable")
}
