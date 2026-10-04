package acceptance

import (
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

const nativeApplyUsage = "dacli accept apply-native --config <owner> --review-policy <policy> --request <input>"

func cmdAcceptApplyNative(ctx *clikit.Ctx, args []string) error {
	f, e := clikit.ParseFlags(args)
	if e != nil {
		return e
	}
	if e = f.Reject("config", "review-policy", "request"); e != nil {
		return e
	}
	if len(f.Pos) != 0 || f.Get("config") == "" || f.Get("review-policy") == "" || f.Get("request") == "" {
		return clikit.Usagef("usage: %s", nativeApplyUsage)
	}
	owner, e := application.OpenNativeAcceptanceOwner(f.Get("config"), f.Get("review-policy"))
	if e != nil {
		return nativeAcceptanceError(e)
	}
	raw, e := nativeconfig.ReadPrivate(f.Get("request"), 65536)
	var input sessions.NativeAcceptanceInput
	if e != nil || nativeconfig.Decode(raw, &input) != nil {
		return clikit.Refusedf("native acceptance input unavailable or invalid")
	}
	out, e := owner.Apply(input, "")
	if e != nil {
		return nativeAcceptanceError(e)
	}
	result := struct {
		Schema string                          `json:"schema"`
		Result sessions.NativeAcceptanceResult `json:"result"`
	}{"native-acceptance/v1", out}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "Native canonical acceptance %s (duplicate %v).\n", out.State, out.Duplicate)
	return nil
}
func nativeAcceptanceError(e error) error {
	if errors.Is(e, sessions.ErrDenied) && errors.Is(e, sessions.ErrReconcile) {
		return clikit.Refusedf("native acceptance partial canonical effect requires owner reconciliation; closing retirement and resources retained")
	}
	if errors.Is(e, sessions.ErrDenied) {
		return clikit.Refusedf("native acceptance authority or current producer refused")
	}
	if errors.Is(e, sessions.ErrReconcile) {
		return fmt.Errorf("native acceptance reconciliation required; retained closing retirement and resources: %w", e)
	}
	return e
}
