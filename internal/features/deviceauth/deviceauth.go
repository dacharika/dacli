// Package deviceauth owns explicit user device authentication commands.
package deviceauth

import (
	"context"
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/deviceidentity"
	"os"
	"os/signal"
)

var Commands = []clikit.Command{
	{Path: "auth login", Brief: "Authorize a device through an explicit provider and store secrets in native OS credentials", JSON: true, Mutates: true, Usage: "dacli auth login --provider URL --tenant ID --name NAME [--json]", Run: login},
	{Path: "auth logout", Brief: "Revoke and remove the exact selected device credential", JSON: true, Mutates: true, Usage: "dacli auth logout --provider URL --tenant ID --device ID [--json]", Run: logout},
	{Path: "auth whoami", Brief: "Refresh and read the selected device identity from its provider", JSON: true, Mutates: true, Usage: "dacli auth whoami --provider URL --tenant ID --device ID [--json]", Run: whoami},
}

func AuthenticatesExplicitly(path string) bool {
	return path == "auth login" || path == "auth logout" || path == "auth whoami"
}
func typed(err error) error {
	if err == nil {
		return nil
	}
	var e *deviceidentity.Error
	if errors.As(err, &e) {
		if e.Retryable || e.Code == "cancelled" {
			return e
		}
		return clikit.Refusedf("%s", e)
	}
	return clikit.Refusedf("device identity operation refused; inspect the selected provider and native credential store")
}
func emit(ctx *clikit.Ctx, value any) error {
	ctx.Result = value
	if ctx.JSON {
		return clikit.EmitJSON(ctx, value)
	}
	_, err := fmt.Fprintf(ctx.Stdout, "%+v\n", value)
	return err
}

type publicIdentity struct {
	Provider  string   `json:"provider"`
	TenantID  string   `json:"tenant_id"`
	AccountID string   `json:"account_id"`
	DeviceID  string   `json:"device_id"`
	Scopes    []string `json:"scopes"`
	ExpiresAt any      `json:"expires_at"`
}

func public(m deviceidentity.Metadata) publicIdentity {
	return publicIdentity{m.Provider, m.TenantID, m.AccountID, m.DeviceID, m.Scopes, m.ExpiresAt}
}
func login(ctx *clikit.Ctx, args []string) error {
	f, parseErr := clikit.ParseFlags(args, "provider", "tenant", "name")
	if parseErr != nil {
		return parseErr
	}
	if e := f.Reject("provider", "tenant", "name"); e != nil {
		return e
	}

	if len(f.Pos) != 0 || f.Get("provider") == "" || f.Get("tenant") == "" || f.Get("name") == "" {
		return clikit.Usagef("auth login requires --provider, --tenant and --name with no positional arguments")
	}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	m, e := deviceidentity.Login(bounded, f.Get("provider"), f.Get("tenant"), f.Get("name"), func(c deviceidentity.Challenge) error {
		w := ctx.Stdout
		if ctx.JSON {
			w = ctx.Stderr
		}
		_, err := fmt.Fprintf(w, "Open %s and enter %s (poll interval %ds; expires in %ds)\n", c.VerificationURI, c.UserCode, c.Interval, c.ExpiresIn)
		return err
	})
	if e != nil {
		return typed(e)
	}
	return emit(ctx, public(m))
}
func selected(args []string) (*deviceidentity.Client, error) {
	f, parseErr := clikit.ParseFlags(args, "provider", "tenant", "device")
	if parseErr != nil {
		return nil, parseErr
	}
	if e := f.Reject("provider", "tenant", "device"); e != nil {
		return nil, e
	}

	if len(f.Pos) != 0 || f.Get("provider") == "" || f.Get("tenant") == "" || f.Get("device") == "" {
		return nil, clikit.Usagef("auth device operation requires --provider, --tenant and --device with no positional arguments")
	}
	c, e := deviceidentity.Open(f.Get("provider"), f.Get("tenant"), f.Get("device"))
	return c, typed(e)
}
func logout(ctx *clikit.Ctx, args []string) error {
	c, e := selected(args)
	if e != nil {
		return e
	}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result, e := c.Logout(bounded)
	if output := emit(ctx, result); output != nil {
		return output
	}
	return typed(e)
}
func whoami(ctx *clikit.Ctx, args []string) error {
	c, e := selected(args)
	if e != nil {
		return e
	}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	session, e := c.Authenticate(bounded)
	if e != nil {
		return typed(e)
	}
	return emit(ctx, publicIdentity{session.Provider, session.TenantID, session.AccountID, session.DeviceID, session.Scopes, session.ExpiresAt})
}
