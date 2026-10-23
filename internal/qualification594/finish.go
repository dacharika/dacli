package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

// prepareIntegration pins the actual current Git target before fresh trusted
// verification and independent review. Advancing trunk requires a new epoch.
func prepareIntegration(manifestPath, key string) error {
	m, config, err := loadOwnerManifest(manifestPath)
	if err != nil {
		return err
	}
	if m.Tasks[key] == "" {
		return fmt.Errorf("unknown task key")
	}
	observed, err := checkout.Observe(m.Workspace)
	if err != nil {
		return fmt.Errorf("target must be clean: %w", err)
	}
	policyPath := filepath.Join(filepath.Dir(config.Authority), "integration-"+key+".json")
	reviewPath, err := qualificationReviewPolicy(m, key)
	if err != nil {
		return err
	}
	if reviewPath == "" {
		return fmt.Errorf("review policy missing for %s", key)
	}
	w, err := native.ResolveWorkspace(m.Workspace, "")
	if err != nil {
		return err
	}
	authority, _, err := application.OpenNativeReviewAuthority(m.Workspace, w.ID, m.Authority, config.InstallationID, reviewPath)
	if err != nil {
		return err
	}
	ownerToken, err := native.ReadCredential(config.OwnerCredentialFile)
	if err != nil {
		return err
	}
	if err = authority.ConfigureNativePolicy(ownerToken, sessions.NativePolicy{SchemaVersion: 1, ProjectID: m.Project, TaskID: m.Tasks[key], Revision: "native-" + key, Allow: true, RequireApproval: true, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		return err
	}
	policy := sessions.NativeIntegrationPolicy{SchemaVersion: 1, ProjectID: m.Project, TaskID: m.Tasks[key], Revision: "integration-" + key + "-" + observed.Commit[:12], Repository: observed.Repository, TargetCheckout: m.Workspace, TargetRef: "refs/heads/main", ExpectedCommit: observed.Commit, ExpectedTree: observed.Tree, NativePolicyRevision: "native-" + key, Allow: true, ExpiresAt: time.Now().Add(time.Hour)}
	if err = save(policyPath, policy); err != nil {
		return err
	}
	owner, err := application.OpenNativeIntegrationOwner(m.OwnerConfig, reviewPath, policyPath)
	if err != nil {
		return err
	}
	receipt, err := owner.Configure(context.Background())
	if err != nil {
		return err
	}
	m.Policies["integration-"+key] = policyPath
	if err = save(manifestPath, m); err != nil {
		return err
	}
	if contractPath := m.Profiles["contract_"+key]; contractPath != "" {
		var contract application.NativePhaseContract
		raw, readErr := native.ReadPrivate(contractPath, 16384)
		if readErr != nil || json.Unmarshal(raw, &contract) != nil {
			return fmt.Errorf("phase contract unavailable")
		}
		contract.IntegrationBase = observed.Commit
		rawPolicy, readErr := os.ReadFile(policyPath)
		if readErr != nil {
			return readErr
		}
		contract.IntegrationPolicyDigest = sum(rawPolicy)
		if err = save(contractPath, contract); err != nil {
			return err
		}
	}
	return save(filepath.Join(filepath.Dir(manifestPath), "prepared-"+key+".json"), receipt)
}

// finishIntegration consumes the separate reviewer's candidate. It performs real
// Git integration, separately approved canonical acceptance and exact readback.
func finishIntegration(manifestPath, key, candidatePath, controllerStatusPath string) error {
	m, config, err := loadOwnerManifest(manifestPath)
	if err != nil {
		return err
	}
	reviewPath, err := qualificationReviewPolicy(m, key)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(candidatePath)
	if err != nil {
		return err
	}
	var wrapped struct {
		Candidate sessions.NativeAcceptanceCandidate `json:"candidate"`
	}
	if json.Unmarshal(raw, &wrapped) != nil || wrapped.Candidate.Input.TaskID != m.Tasks[key] {
		return fmt.Errorf("candidate does not bind selected task")
	}
	input := wrapped.Candidate.Input
	var controller sessions.ControllerStatus
	statusRaw, readErr := native.ReadPrivate(controllerStatusPath, 65536)
	if readErr != nil || json.Unmarshal(statusRaw, &controller) != nil || controller.ProjectID != m.Project {
		return fmt.Errorf("current controller integration descriptor required")
	}
	integrationOperation := ""
	for _, action := range controller.Actions {
		if action.TaskID != input.TaskID {
			continue
		}
		if action.Kind != "integration_checkpoint" || action.ReviewID != input.ReviewID || action.AttemptID != input.AttemptID || action.LeaseID != input.LeaseID || action.RevisionID != input.RevisionID || action.Fence != input.ExpectedFence || action.OperationKey == "" {
			return fmt.Errorf("integration descriptor does not bind exact independent candidate")
		}
		integrationOperation = action.OperationKey
	}
	if integrationOperation == "" {
		return fmt.Errorf("task integration descriptor missing")
	}
	input.OperationKey = "accept-" + key
	var integrationPolicy sessions.NativeIntegrationPolicy
	policyPath := m.Policies["integration-"+key]
	raw, err = native.ReadPrivate(policyPath, 65536)
	if err != nil || json.Unmarshal(raw, &integrationPolicy) != nil {
		return fmt.Errorf("prepared integration policy missing")
	}
	integration, err := application.OpenNativeIntegrationOwner(m.OwnerConfig, reviewPath, policyPath)
	if err != nil {
		return err
	}
	request := sessions.NativeIntegrationInput{OperationKey: integrationOperation, PolicyRevision: integrationPolicy.Revision, Candidate: input}
	integrated, err := integration.Apply(context.Background(), request)
	if err != nil || integrated.Record.State != "integrated" {
		return fmt.Errorf("actual integration did not finish: %w", err)
	}
	if err = save(filepath.Join(filepath.Dir(manifestPath), "integrated-"+key+".json"), integrated); err != nil {
		return err
	}
	acceptance, err := application.OpenNativeAcceptanceOwner(m.OwnerConfig, reviewPath)
	if err != nil {
		return err
	}
	decision, err := acceptance.EvaluatePolicy(sessions.NativePolicyEvaluationRequest{OperationKey: "decision-" + key, Input: input, ExpiresAt: time.Now().Add(15 * time.Minute)})
	if err != nil {
		return err
	}
	ownerToken, err := native.ReadCredential(config.OwnerCredentialFile)
	if err != nil {
		return err
	}
	w, err := native.ResolveWorkspace(m.Workspace, "")
	if err != nil {
		return err
	}
	authority, _, err := application.OpenNativeReviewAuthority(m.Workspace, w.ID, m.Authority, config.InstallationID, reviewPath)
	if err != nil {
		return err
	}
	if _, err = authority.ApproveNativePolicy(ownerToken, sessions.NativePolicyApprovalRequest{OperationKey: "approve-" + key, DecisionID: decision.ID, ExpiresAt: decision.ExpiresAt}); err != nil {
		return err
	}
	result, err := acceptance.ApplyWithPolicy(input, "", decision.ID)
	if err != nil || !result.Applied || result.State != "closed" {
		return fmt.Errorf("actual canonical acceptance did not finish: %w", err)
	}
	t, err := store.NativeAcceptanceTask(w, input.TaskID, m.Project)
	if err != nil || t.Status != model.StatusDone || !store.LogHasStamp(t, "completed by ") {
		return fmt.Errorf("canonical DONE readback absent: %w", err)
	}
	status, err := authority.ReadNativePolicyStatus(ownerToken, decision.ID)
	if err != nil || status.Consumption == nil || status.Outcome == nil || status.Outcome.State != "closed" || status.Outcome.ObservedOutcomeDigest != decision.Prepared.CanonicalOutcomeDigest {
		return fmt.Errorf("policy consumption/outcome mismatch: %w", err)
	}
	// Duplicate operations must be historical and cannot produce another Git or
	// canonical effect. Their producers use the exact saved request and decision.
	again, err := integration.Apply(context.Background(), request)
	if err != nil || !again.Duplicate {
		return fmt.Errorf("integration duplicate not historical: %w", err)
	}
	duplicate, err := acceptance.ApplyWithPolicy(input, "", decision.ID)
	if err != nil || !duplicate.Duplicate || !duplicate.Applied {
		return fmt.Errorf("acceptance duplicate not historical: %w", err)
	}
	return save(filepath.Join(filepath.Dir(manifestPath), "accepted-"+key+".json"), struct {
		Result                                    sessions.NativeAcceptanceResult
		Policy                                    sessions.NativePolicyStatus
		TaskStatus                                string
		IntegrationDuplicate, AcceptanceDuplicate bool
	}{result, status, string(t.Status), again.Duplicate, duplicate.Duplicate})
}

func loadOwnerManifest(path string) (manifest, ownerConfig, error) {
	var m manifest
	var c ownerConfig
	b, e := native.ReadPrivate(path, 65536)
	if e != nil {
		return m, c, e
	}
	if e = json.Unmarshal(b, &m); e != nil {
		return m, c, e
	}
	b, e = native.ReadPrivate(m.OwnerConfig, 16384)
	if e != nil {
		return m, c, e
	}
	e = json.Unmarshal(b, &c)
	return m, c, e
}

func qualificationReviewPolicy(m manifest, key string) (string, error) {
	var profile application.NativeReviewerProfile
	b, e := native.ReadPrivate(m.Policies["review-"+key], 16384)
	if e != nil {
		return "", e
	}
	if e = json.Unmarshal(b, &profile); e != nil {
		return "", e
	}
	if profile.TaskID != m.Tasks[key] || profile.PolicyFile == "" {
		return "", fmt.Errorf("reviewer profile task mismatch")
	}
	return profile.PolicyFile, nil
}

// releaseIntegrated retains the separate owner recovery boundary. A canonical
// close does not itself claim the host process was killed or release a lease.
func releaseIntegrated(manifestPath, key, observationPath string) error {
	m, _, err := loadOwnerManifest(manifestPath)
	if err != nil {
		return err
	}
	w, err := native.ResolveWorkspace(m.Workspace, "")
	if err != nil {
		return err
	}
	task, err := store.NativeAcceptanceTask(w, m.Tasks[key], m.Project)
	if err != nil || task.Status != model.StatusDone {
		return fmt.Errorf("canonical DONE required before recovery")
	}
	var observation sessions.LifecycleObservation
	raw, err := native.ReadPrivate(observationPath, 65536)
	if err != nil || json.Unmarshal(raw, &observation) != nil || observation.Binding.TaskID != task.ID || observation.State != "completed" {
		return fmt.Errorf("independent completed host observation required")
	}
	recovery, err := application.OpenNativeRecovery(m.OwnerConfig)
	if err != nil {
		return err
	}
	policy := sessions.RecoveryPolicy{SchemaVersion: 1, ProjectID: m.Project, Revision: "qualification-completed-native-child", Rationale: "Owner accepts cooperative native child completion after actual review, integration and canonical DONE; no OS containment or automatic host termination claimed", AcceptCooperativeTerminal: true}
	if err = recovery.Configure(policy); err != nil {
		return err
	}
	target, err := recovery.Preview("lease", observation.Binding.LeaseID)
	if err != nil {
		return err
	}
	result, err := recovery.Apply(sessions.RecoveryRequest{OperationKey: "recover-completed-" + key, Target: target, Mode: "authenticated-terminal", Rationale: "Actual native child completed; exact task is canonically DONE and integration has been verified", PolicyRevision: policy.Revision, ObservationSequence: observation.Sequence, EvidenceDigest: observation.EvidenceDigest})
	if err != nil {
		return err
	}
	return save(filepath.Join(filepath.Dir(manifestPath), "released-"+key+".json"), result)
}
