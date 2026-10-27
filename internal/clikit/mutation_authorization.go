package clikit

import (
	"os"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/model"
)

// AuthorizeCommandMutation applies the shared mutation policy before a
// command's handler runs. Preview and read exemptions come only from explicit
// command metadata and flags parsed using that command's value-taking schema.
func AuthorizeCommandMutation(ctx *Ctx, cmd *Command, args []string) error {
	if cmd == nil || !cmd.Mutates {
		return nil
	}

	_, identity, err := OpenWorkspace(ctx)
	if err != nil {
		// A present token is an inherited identity claim. It must never become
		// local-owner authority when the workspace or token cannot be resolved.
		// Keep the token and underlying resolution details out of the refusal.
		if _, present := os.LookupEnv(agentid.EnvVar); present {
			return Refusedf("%s cannot run with an unresolved inherited identity", cmd.Path)
		}
		// With no inherited token, preserve owner bootstrap and let the handler
		// report ordinary missing/corrupt-workspace errors itself.
		return nil
	}

	if cmd.EffectFreeDryRun || cmd.ReadOnlyWhen != nil {
		flags, parseErr := ParseFlags(args, cmd.ValueFlags...)
		if parseErr != nil {
			return parseErr
		}
		if cmd.EffectFreeDryRun && flags.Bool("dry-run") {
			return nil
		}
		if cmd.ReadOnlyWhen != nil && identity.Grant == model.GrantRO && cmd.ReadOnlyWhen(flags) {
			return nil
		}
	}

	return RequireRW(identity, cmd.Path)
}
