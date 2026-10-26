package nativegovernance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// CapacityCommands is separately registered by nativegovernance.Commands.
// This keeps command registration and its cross-cutting invariants in the
// owner's shared integration scope.
var CapacityCommands = []clikit.Command{
	{Path: "native capacity upgrade", Brief: "Owner-upgrade native authority schema 20 to 21 for host capacity facts", Mutates: true, JSON: true, Usage: "dacli native capacity upgrade --config <private-owner-file> --json", Run: capacityUpgrade},
	{Path: "native capacity observer add", Brief: "Owner-enroll an exact independent host capacity observer", Mutates: true, JSON: true, Usage: "dacli native capacity observer add --config <private-owner-file> --request <private-file> --credential <new-private-file> --json", Run: capacityObserverAdd},
	{Path: "native capacity policy configure", Brief: "Owner-configure requested concurrency as a separate capacity fact", Mutates: true, JSON: true, Usage: "dacli native capacity policy configure --config <private-owner-file> --request <private-file> --json", Run: capacityPolicyConfigure},
	{Path: "native capacity qualify", Brief: "Owner-record separately trusted host concurrency qualification", Mutates: true, JSON: true, Usage: "dacli native capacity qualify --config <private-owner-file> --request <private-file> --json", Run: capacityQualify},
	{Path: "native capacity observe", Brief: "Record one authenticated host capacity observation", Mutates: true, JSON: true, Usage: "dacli native capacity observe --connection <private-file> --request <private-file> --json", Run: capacityObserve},
	{Path: "native capacity status", Brief: "Read four independent host capacity facts without admitting work", JSON: true, Usage: "dacli native capacity status --connection <private-file> --host-session <id> --json", Run: capacityStatus},
}

func capacityOwner(ctx *clikit.Ctx, configPath string, requireCapacity bool) (*sessions.Authority, string, error) {
	c, err := config(configPath)
	if err != nil {
		return nil, "", err
	}
	w, err := native.ResolveWorkspace(c.Workspace, "")
	if err != nil {
		return nil, "", denied()
	}
	owner, err := native.ReadCredential(c.OwnerCredentialFile)
	if err != nil {
		return nil, "", denied()
	}
	var a *sessions.Authority
	if requireCapacity {
		a, err = sessions.OpenHostCapacity(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	} else {
		a, err = sessions.OpenCurrent(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	}
	if err != nil || a.AuthenticateOwner(owner) != nil {
		return nil, "", denied()
	}
	return a, owner, nil
}

func capacityUpgrade(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config")
	if err != nil {
		return err
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config is required")
	}
	a, owner, err := capacityOwner(ctx, f.Get("config"), false)
	if err != nil {
		return err
	}
	if err = a.UpgradeHostCapacity(owner); err != nil {
		return clikit.Refusedf("native capacity upgrade requires schema 20 and valid private owner authority; preserve storage for owner reconciliation")
	}
	fmt.Fprintln(ctx.Stdout, "{\"schema_version\":1,\"authority_schema\":21,\"upgraded\":true}")
	return nil
}

func capacityObserverAdd(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "request", "credential")
	if err != nil {
		return err
	}
	for _, name := range []string{"config", "request", "credential"} {
		if f.Get(name) == "" {
			return clikit.Usagef("--%s is required", name)
		}
	}
	if !filepath.IsAbs(f.Get("credential")) {
		return clikit.Usagef("--credential must be an absolute new private file")
	}
	a, owner, err := capacityOwner(ctx, f.Get("config"), true)
	if err != nil {
		return err
	}
	data, err := native.ReadPrivate(f.Get("request"), 16384)
	var observer sessions.HostCapacityObserver
	if err != nil || native.Decode(data, &observer) != nil || observer.SchemaVersion != 1 {
		return denied()
	}
	credential, err := reserve(f.Get("credential"))
	if err != nil {
		return err
	}
	defer credential.Close()
	enrolled, token, err := a.EnrollHostCapacityObserver(owner, observer)
	if err != nil {
		_ = os.Remove(f.Get("credential"))
		return clikit.Refusedf("capacity observer enrollment requires exact current host/session scope")
	}
	if err = writePrivate(credential, []byte(token+"\n")); err != nil {
		return err
	}
	out := map[string]any{"observer": enrolled, "credential_file": f.Get("credential")}
	b, _ := json.Marshal(out)
	fmt.Fprintln(ctx.Stdout, string(b))
	return nil
}

func capacityPolicyConfigure(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "request")
	if err != nil {
		return err
	}
	if f.Get("config") == "" || f.Get("request") == "" {
		return clikit.Usagef("--config and --request are required")
	}
	a, owner, err := capacityOwner(ctx, f.Get("config"), true)
	if err != nil {
		return err
	}
	data, err := native.ReadPrivate(f.Get("request"), 16384)
	var policy sessions.HostCapacityPolicy
	if err != nil || native.Decode(data, &policy) != nil || policy.SchemaVersion != 1 {
		return denied()
	}
	out, err := a.ConfigureHostCapacityPolicy(owner, policy)
	if err != nil {
		return clikit.Refusedf("capacity policy requires exact current owner, workspace, installation and host-session bindings")
	}
	b, _ := json.Marshal(out)
	fmt.Fprintln(ctx.Stdout, string(b))
	return nil
}

func capacityQualify(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "request")
	if err != nil {
		return err
	}
	if f.Get("config") == "" || f.Get("request") == "" {
		return clikit.Usagef("--config and --request are required")
	}
	a, owner, err := capacityOwner(ctx, f.Get("config"), true)
	if err != nil {
		return err
	}
	data, err := native.ReadPrivate(f.Get("request"), 16384)
	var qualification sessions.HostCapacityQualification
	if err != nil || native.Decode(data, &qualification) != nil || qualification.SchemaVersion != 1 {
		return denied()
	}
	out, err := a.QualifyHostCapacity(owner, qualification)
	if err != nil {
		return clikit.Refusedf("capacity qualification requires exact owner authority and a separate evidence digest")
	}
	b, _ := json.Marshal(out)
	fmt.Fprintln(ctx.Stdout, string(b))
	return nil
}

func capacityConnection(path string) (nativeconfig.Connection, *workspace.Workspace, error) {
	data, err := native.ReadPrivate(path, 16384)
	var c nativeconfig.Connection
	if err != nil || native.Decode(data, &c) != nil || c.SessionID == "" || c.InstallationID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.CredentialFile) {
		return c, nil, denied()
	}
	w, err := native.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if err != nil {
		return c, nil, denied()
	}
	return c, w, nil
}

func capacityObserve(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "connection", "request")
	if err != nil {
		return err
	}
	if f.Get("connection") == "" || f.Get("request") == "" {
		return clikit.Usagef("--connection and --request are required")
	}
	_, w, err := capacityConnection(f.Get("connection"))
	if err != nil {
		return err
	}
	access, err := application.OpenNativeCapacity(f.Get("connection"), w)
	if err != nil {
		return denied()
	}
	data, err := native.ReadPrivate(f.Get("request"), 16384)
	if err != nil {
		return denied()
	}
	report, err := executor.DecodeCapacityReport(data)
	if err != nil {
		return clikit.Refusedf("unknown or unsupported capacity report contract")
	}
	receipt, duplicate, err := access.Observe(report)
	if err != nil {
		return clikit.Refusedf("capacity observation refused by current observer enrollment or freshness checks")
	}
	b, _ := json.Marshal(map[string]any{"receipt": receipt, "duplicate": duplicate})
	fmt.Fprintln(ctx.Stdout, string(b))
	return nil
}

func capacityStatus(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "connection", "host-session")
	if err != nil {
		return err
	}
	if f.Get("connection") == "" || f.Get("host-session") == "" {
		return clikit.Usagef("--connection and --host-session are required")
	}
	_, w, err := capacityConnection(f.Get("connection"))
	if err != nil {
		return err
	}
	access, err := application.OpenNativeCapacity(f.Get("connection"), w)
	if err != nil {
		return denied()
	}
	facts, err := access.Read(f.Get("host-session"))
	if err != nil {
		return clikit.Refusedf("capacity facts are unavailable to this observer")
	}
	out := map[string]any{"facts": facts, "positive_capacity_evidence": executor.PositiveCapacityEvidence(facts, time.Now())}
	b, _ := json.Marshal(out)
	fmt.Fprintln(ctx.Stdout, string(b))
	return nil
}
