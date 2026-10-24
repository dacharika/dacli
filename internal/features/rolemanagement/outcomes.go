package rolemanagement

import (
	"encoding/json"
	"fmt"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

const outcomeCommandPath = "role registry outcomes"

type outcomesReport struct {
	Assurance         string                   `json:"assurance"`
	SuppressionPolicy string                   `json:"suppression_policy"`
	OutcomeMeaning    string                   `json:"outcome_meaning"`
	Cohorts           []applicationRoleOutcome `json:"cohorts"`
}

// Keep the JSON boundary closed and prevent internal authority fields from
// becoming visible if the low-level aggregate later gains new attributes.
type applicationRoleOutcome struct {
	RoleID              string                            `json:"role_id"`
	Version             string                            `json:"version"`
	ContentDigest       string                            `json:"content_digest"`
	PolicySetDigest     string                            `json:"policy_set_digest"`
	AdoptionTaskSamples int                               `json:"adoption_task_samples"`
	DispatchSamples     int                               `json:"dispatch_samples"`
	OutcomeCounts       *sessions.NativeRoleOutcomeCounts `json:"outcome_dispatch_counts"`
	Suppression         string                            `json:"suppression,omitempty"`
}

// OutcomeCommands exposes the owner-only, fixed-project aggregate report.
func OutcomeCommands() []clikit.Command {
	return []clikit.Command{{Path: outcomeCommandPath, Brief: "Report private cohort-safe signed-role adoption and cooperative outcomes", JSON: true,
		Usage: "dacli role registry outcomes --config <private-owner> --project <slug> [--json]",
		Run:   runOutcomes}}
}

// AuthenticatesOutcomeExplicitly keeps the command out of legacy unauthenticated routes.
func AuthenticatesOutcomeExplicitly(path string) bool { return path == outcomeCommandPath }

func runOutcomes(ctx *clikit.Ctx, args []string) error {
	f, err := parseFlags(args, "config", "project", "json")
	if err != nil {
		return err
	}
	for _, name := range []string{"config", "project"} {
		if f.Get(name) == "" {
			return clikit.Usagef("--%s is required", name)
		}
	}
	owner, err := application.OpenNativeRoleRegistryOwner(f.Get("config"), f.Get("project"))
	if err != nil {
		return denied()
	}
	rows, err := owner.Outcomes()
	if err != nil {
		return denied()
	}
	cohorts := make([]applicationRoleOutcome, 0, len(rows))
	for _, row := range rows {
		cohorts = append(cohorts, applicationRoleOutcome{RoleID: row.RoleID, Version: row.Version, ContentDigest: row.ContentDigest, PolicySetDigest: row.PolicySetDigest, AdoptionTaskSamples: row.AdoptionTaskSamples, DispatchSamples: row.DispatchSamples, OutcomeCounts: row.OutcomeCounts, Suppression: row.Suppression})
	}
	report := outcomesReport{Assurance: "cooperative host lifecycle observations", SuppressionPolicy: "fixed minimum of five distinct tasks; deterministic cohort suppression is not differential privacy", OutcomeMeaning: "counts by acknowledged dispatch; not provider measurements, rates, rankings, or productivity scores", Cohorts: cohorts}
	ctx.Result = report
	if ctx.JSON {
		enc := json.NewEncoder(ctx.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(report)
	}
	fmt.Fprintln(ctx.Stdout, report.Assurance)
	fmt.Fprintln(ctx.Stdout, report.SuppressionPolicy)
	fmt.Fprintln(ctx.Stdout, report.OutcomeMeaning)
	for _, row := range report.Cohorts {
		fmt.Fprintf(ctx.Stdout, "%s %s (%s), policy set %s: %d distinct task adoptions, %d acknowledged dispatch samples", row.RoleID, row.Version, row.ContentDigest, row.PolicySetDigest, row.AdoptionTaskSamples, row.DispatchSamples)
		if row.OutcomeCounts == nil {
			fmt.Fprintf(ctx.Stdout, "; %s\n", row.Suppression)
			continue
		}
		fmt.Fprintf(ctx.Stdout, "; completed %d, failed %d, unknown %d\n", row.OutcomeCounts.Completed, row.OutcomeCounts.Failed, row.OutcomeCounts.Unknown)
	}
	return nil
}
