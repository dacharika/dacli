package main

import (
	"fmt"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func configureBudget(manifestPath string) error {
	m, config, err := loadOwnerManifest(manifestPath)
	if err != nil {
		return err
	}
	w, err := workspace.Find(m.Workspace)
	if err != nil {
		return err
	}
	admission, err := application.NewLeaseAdmission(w)
	if err != nil {
		return err
	}
	a, err := sessions.Open(m.Authority, sessions.Binding{Workspace: w.ID, Installation: config.InstallationID}, admission)
	if err != nil {
		return err
	}
	owner, err := native.ReadCredential(config.OwnerCredentialFile)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	budget := sessions.BudgetPolicy{SchemaVersion: 1, ProjectID: m.Project, Revision: "budget-594", CycleID: "cycle-594", Harness: "codex", Rationale: "owner-estimated qualification bounds; actual provider usage is unavailable", CycleLimit: 1200, WindowLimit: 1200, RecoveryFloor: 60, Window: time.Hour, WindowOrigin: now.Add(-time.Minute), ExpiresAt: now.Add(45 * time.Minute), Tasks: map[string]sessions.BudgetTask{}}
	for _, k := range []string{"a", "b", "c"} {
		phases := map[string]sessions.BudgetEstimate{}
		for phase, tokens := range map[string]int64{"implementation": 120, "review": 80, "re-review": 40, "correction": 60, "verification": 0, "integration": 40, "recovery": 40} {
			role := "implementation"
			if phase == "review" || phase == "re-review" {
				role = "review"
			}
			phases[phase] = sessions.BudgetEstimate{RoleID: role, Model: "gpt-6-luna", Tokens: tokens, Provenance: "owner-estimate", ObservedAt: now}
		}
		budget.Tasks[m.Tasks[k]] = sessions.BudgetTask{Phases: phases, IntegrationOperationID: "integration-" + k, AcceptanceOperationID: "accept-" + k}
	}
	if err = a.ConfigureBudget(owner, budget); err != nil {
		return fmt.Errorf("configure owner-estimated budget: %w", err)
	}
	m.Policies["budget"] = budget.Revision
	return save(manifestPath, m)
}
