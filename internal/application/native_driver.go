package application

import (
	"context"

	"github.com/mlnomadpy/dacli/internal/governance/driver"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// Run applies one bounded controller-driver request using the private
// selection that was authenticated when this adapter was opened.
func (p *NativeControllerDriver) Run(ctx context.Context, request driver.Request) (driver.Result, error) {
	if p == nil || ctx == nil || ctx.Err() != nil || p.verifyPinnedFiles() != nil {
		return driver.Result{}, sessions.ErrDenied
	}
	if request.SchemaVersion != 1 || request.ProjectID != p.profile.ProjectID || request.InvocationID != p.profile.InvocationID || request.CheckpointID != p.profile.CheckpointID {
		return driver.Result{}, sessions.ErrDenied
	}
	ports := &nativeControllerDriverPorts{selection: p}
	pump, err := driver.New(ports, p.profile.Limits.driverLimits())
	if err != nil {
		return driver.Result{}, err
	}
	return pump.Run(ctx, request)
}
