package nativegovernance

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

func swarmUpgrade(ctx *clikit.Ctx, args []string) error {
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
	owner, e := native.ReadCredential(c.OwnerCredentialFile)
	if e != nil {
		return denied()
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	if e = a.UpgradeSwarms(owner); e != nil {
		return clikit.Refusedf("native swarm upgrade requires schema 6 and valid private owner authority; preserve storage for owner reconciliation")
	}
	fmt.Fprintln(ctx.Stdout, "Native authority upgraded from schema 6 to 7; existing authority and receipts preserved.")
	return nil
}
func swarmConfigure(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config", "policy-file")
	if e != nil {
		return e
	}
	for _, key := range []string{"config", "policy-file"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s is required", key)
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
	a, e := sessions.OpenSwarm(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID}, nil)
	if e != nil || a.AuthenticateOwner(owner) != nil {
		return denied()
	}
	data, e := native.ReadPrivate(f.Get("policy-file"), 65536)
	var p sessions.SwarmPolicy
	if e != nil || native.Decode(data, &p) != nil {
		return denied()
	}
	// Authenticate before bounded local Git and validate all proposed owner
	// bindings in the same transaction as policy; never reset storage.
	bindings := []sessions.TaskBinding{}
	for id, placement := range p.Placements {
		task, e := store.FindTask(w, id)
		if e != nil || task.ID != id || task.Project != p.ProjectID {
			return denied()
		}
		obs, e := checkout.Observe(placement.Checkout)
		if e != nil {
			return denied()
		}
		bindings = append(bindings, sessions.TaskBinding{TaskID: id, ProjectID: p.ProjectID, Repository: obs.Repository, AdmissionEnabled: true})
	}
	if e = a.ConfigureSwarmWithBindings(owner, p, bindings); e != nil {
		return denied()
	}
	fmt.Fprintln(ctx.Stdout, "Owner-selected native swarm policy configured; dispatch capabilities still require fresh trusted host observation.")
	return nil
}
func swarmControllerAdd(ctx *clikit.Ctx, args []string) error { return swarmActorAdd(ctx, args, false) }
func dispatchBridgeAdd(ctx *clikit.Ctx, args []string) error  { return swarmActorAdd(ctx, args, true) }
func swarmActorAdd(ctx *clikit.Ctx, args []string, bridge bool) error {
	return swarmScopedActorAdd(ctx, args, bridge, false)
}
func swarmScopedActorAdd(ctx *clikit.Ctx, args []string, bridge, lifecycle bool) error {
	f, e := flags(args, "config", "project", "connection", "credential", "expires")
	if e != nil {
		return e
	}
	for _, key := range []string{"config", "project", "connection", "credential"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s is required", key)
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
	a, e := sessions.OpenSwarm(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID}, nil)
	if e != nil {
		return denied()
	}
	p, e := a.OwnerSwarmPolicy(owner, f.Get("project"))
	if e != nil {
		return denied()
	}
	if lifecycle {
		if _, e = a.OwnerLifecyclePolicy(owner, p.ProjectID); e != nil {
			return denied()
		}
	}
	admission, e := application.NewLeaseAdmission(w)
	if e != nil {
		return denied()
	}
	snapshot, e := admission.ObserveSwarm(w.ID, p.ProjectID)
	if e != nil {
		return denied()
	}
	resources := []string{"workspace:" + w.ID, "project:" + p.ProjectID}
	seen := map[string]bool{}
	for _, candidate := range snapshot.Candidates {
		if _, ok := p.Placements[candidate.TaskID]; !ok {
			continue
		}
		for _, value := range append([]sessions.Resource{{Kind: "task", Path: candidate.TaskID}}, candidate.Resources...) {
			key := value.Kind + ":" + value.Path
			if !seen[key] {
				seen[key] = true
				resources = append(resources, key)
			}
		}
	}
	lifetime := 8 * time.Hour
	if f.Get("expires") != "" {
		lifetime, e = time.ParseDuration(f.Get("expires"))
		if e != nil || lifetime <= 0 || lifetime > 24*time.Hour {
			return clikit.Usagef("--expires must be positive and at most 24h")
		}
	}
	credential, e := reserve(f.Get("credential"))
	if e != nil {
		return e
	}
	issued := false
	defer func() {
		_ = credential.Close()
		if !issued {
			_ = os.Remove(f.Get("credential"))
		}
	}()
	connection, e := reserve(f.Get("connection"))
	if e != nil {
		return e
	}
	defer func() {
		_ = connection.Close()
		if !issued {
			_ = os.Remove(f.Get("connection"))
		}
	}()
	actions := []string{"read", "swarm.plan", "swarm.reserve", "swarm.dispatch"}
	if bridge {
		actions = []string{"read", "dispatch.correlate"}
	}
	if lifecycle {
		if bridge {
			actions = append(actions, "dispatch.observe")
		} else {
			actions = append(actions, "dispatch.cancel")
		}
	}
	s, token, e := a.Enroll(owner, sessions.Scope{Actions: actions, Resources: resources}, time.Now().Add(lifetime))
	if e != nil {
		return denied()
	}
	issued = true
	completed := false
	defer func() {
		if completed {
			return
		}
		fmt.Fprintf(ctx.Stderr, "Native controller enrollment %s retained; preserve private connection %s and credential %s for owner reconciliation.\n", s.ID, f.Get("connection"), f.Get("credential"))
	}()
	if e = writePrivate(credential, []byte(token+"\n")); e != nil {
		return e
	}
	data, e := json.Marshal(native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: c.Authority, WorkspaceID: w.ID, InstallationID: c.InstallationID, SessionID: s.ID, CredentialFile: f.Get("credential")})
	if e != nil {
		return denied()
	}
	if e = writePrivate(connection, data); e != nil {
		return e
	}
	completed = true
	if bridge {
		fmt.Fprintf(ctx.Stdout, "Native bridge %s enrolled with correlation scope.\n", s.ID)
	} else {
		fmt.Fprintf(ctx.Stdout, "Native controller %s enrolled with planning and dispatch admission scope; no worker or owner authority.\n", s.ID)
	}
	return nil
}

// Requests carry no owner grant, command, harness, role or policy overrides.
func swarmOperation(ctx *clikit.Ctx, args []string, kind string) error {
	f, e := flags(args, "connection", "capability-file", "request", "project", "json")
	if e != nil {
		return e
	}
	for _, key := range []string{"connection", "capability-file"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s is required", key)
		}
	}
	if kind == "plan" {
		if f.Get("project") == "" || f.Get("request") != "" {
			return clikit.Usagef("plan requires --project and no --request")
		}
	} else if f.Get("request") == "" || f.Get("project") != "" {
		return clikit.Usagef("mutation requires --request and no --project")
	}
	access, e := application.OpenNativeSwarm(f.Get("connection"), f.Get("capability-file"))
	if e != nil {
		return denied()
	}
	var result any
	if kind == "plan" {
		result, e = access.Plan(f.Get("project"))
	} else {
		data, readErr := native.ReadPrivate(f.Get("request"), 1<<20)
		if readErr != nil {
			return denied()
		}
		switch kind {
		case "dispatch-request":
			var req sessions.RequestDispatchIntent
			if native.Decode(data, &req) != nil {
				return denied()
			}
			result, e = access.RequestDispatch(req)
		case "dispatch-correlate":
			var req sessions.CorrelateDispatchIntent
			if native.Decode(data, &req) != nil {
				return denied()
			}
			result, e = access.CorrelateDispatch(req)
		case "dispatch-acknowledge":
			var req sessions.AcknowledgeDispatchIntent
			if native.Decode(data, &req) != nil {
				return denied()
			}
			result, e = access.AcknowledgeDispatch(req)
		case "dispatch-status":
			var req struct{ DispatchID string }
			if native.Decode(data, &req) != nil {
				return denied()
			}
			result, e = access.DispatchStatus(req.DispatchID)
		case "reserve":
			var req sessions.ReserveWaveRequest
			if native.Decode(data, &req) != nil {
				return denied()
			}
			result, e = access.Reserve(req)
		case "admit":
			var req sessions.AdmitDispatchRequest
			if native.Decode(data, &req) != nil {
				return denied()
			}
			result, e = access.Admit(req)
		case "handoff":
			var req sessions.HandoffDispatchRequest
			if native.Decode(data, &req) != nil {
				return denied()
			}
			result, e = access.Handoff(req)
		default:
			return denied()
		}
	}
	if e != nil {
		return nativePreflightFailure(e, "native swarm admission refused: scope, plan, resources, current owner policy or fresh manual-handoff capability unavailable")
	}
	return json.NewEncoder(ctx.Stdout).Encode(result)
}
