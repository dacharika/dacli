package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"time"

	"github.com/mlnomadpy/dacli/internal/eventlog"
)

func (a *LeaseAdmission) ObserveCheckpoint(w, project string, tasks []string) (sessions.CheckpointObservation, error) {
	if a == nil || w != a.workspace.ID {
		return sessions.CheckpointObservation{}, sessions.ErrDenied
	}
	snapshot, e := a.ObserveSwarm(w, project)
	if e != nil {
		return sessions.CheckpointObservation{Digest: safeDigest([]string{project, "canonical-unavailable"}), Findings: []sessions.CheckpointFinding{{Class: "inconsistent_task_record", Knowledge: "unknown", Reference: project, NextAction: "repair canonical duplicates, malformed records or dependency graph"}}}, nil
	}
	projectRecord, projectErr := store.LoadProject(&a.workspace, project)
	if projectErr != nil {
		return sessions.CheckpointObservation{Digest: safeDigest(snapshot), Findings: []sessions.CheckpointFinding{{Class: "inconsistent_task_record", Knowledge: "unknown", Reference: project, NextAction: "repair the canonical project landing policy"}}}, nil
	}
	base := ""
	if projectErr == nil {
		base = projectRecord.Landing.Base
	}
	trunkRef, trunkCommit, gitErr := sessions.ObserveCheckpointTrunk(a.workspace.Root, base)
	if projectErr != nil || gitErr != nil {
		return sessions.CheckpointObservation{Digest: safeDigest(snapshot), Findings: []sessions.CheckpointFinding{{Class: "transient_infrastructure", Knowledge: "unknown", Reference: project, NextAction: "restore the configured local landing base ref; workspace HEAD is not trunk", Retryable: true}}}, nil
	}
	projection, e := store.LocalDeliveryProjection(&a.workspace, project, time.Time{})
	if e != nil {
		return sessions.CheckpointObservation{Digest: safeDigest(snapshot), Trunk: trunkCommit, TrunkRef: trunkRef, Findings: []sessions.CheckpointFinding{{Class: "inconsistent_task_record", Knowledge: "unknown", Reference: project, NextAction: "restore readable local task/event/run projection"}}}, nil
	}
	allowed := map[string]bool{}
	scopedObjects := map[string]string{}
	for _, task := range tasks {
		allowed[task] = true
		scopedObjects[task] = task
	}
	events, eventErr := eventlog.List(&a.workspace, eventlog.Query{})
	if eventErr != nil {
		return sessions.CheckpointObservation{Digest: safeDigest(snapshot), Trunk: trunkCommit, TrunkRef: trunkRef, Findings: []sessions.CheckpointFinding{{Class: "inconsistent_task_record", Knowledge: "unknown", Reference: project, NextAction: "restore a readable canonical event log"}}}, nil
	}
	for _, event := range events {
		if allowed[event.About] {
			scopedObjects[event.ID] = event.About
		}
	}
	runs, runErr := sessions.CheckpointRunTasks(a.workspace.RunsDir())
	if runErr != nil {
		return sessions.CheckpointObservation{}, runErr
	}
	for id, task := range runs {
		if allowed[task] {
			scopedObjects[id] = task
		}
	}
	findings := []sessions.CheckpointFinding{}
	for _, f := range projection.Findings {
		task, scoped := scopedObjects[f.ObjectID]
		if !scoped && f.ObjectID != project {
			continue
		}
		if f.Severity == "info" {
			continue
		}
		class := "inconsistent_task_record"
		if f.Classification == "terminal-task-event" || f.Classification == "terminal-task-pr-event" {
			class = "terminal_task_event"
		}
		findings = append(findings, sessions.CheckpointFinding{Class: class, Knowledge: string(f.Confidence), TaskID: task, Reference: f.ObjectID, NextAction: f.NextAction, Retryable: f.Retryable})
	}
	return sessions.CheckpointObservation{Digest: safeDigest(struct {
		LandingRef    string
		LandingPolicy any
		Snapshot      sessions.SwarmSnapshot
		Local         store.DeliveryProjection
	}{trunkRef, projectRecord.Landing, snapshot, projection}), Trunk: trunkCommit, TrunkRef: trunkRef, Findings: findings}, nil
}

type NativeCheckpointOwner struct{ owner *NativePreflightOwner }

func OpenNativeCheckpointOwner(path string) (*NativeCheckpointOwner, error) {
	p, e := OpenNativePreflightOwner(path)
	if e != nil {
		return nil, e
	}
	return &NativeCheckpointOwner{p}, nil
}
func (p *NativeCheckpointOwner) Upgrade() error {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.UpgradeCheckpoints(o)
}
func (p *NativeCheckpointOwner) Configure(r sessions.CheckpointPolicy) error {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return e
	}
	return a.ConfigureCheckpoints(o, r)
}
func (p *NativeSwarmAccess) RecordCheckpoint(r sessions.CheckpointRequest) (sessions.CheckpointStatus, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.CheckpointStatus{}, e
	}
	return a.RecordCheckpoint(t, p.connection.SessionID, r)
}
func (p *NativeSwarmAccess) ReconcileCheckpoint(r sessions.CheckpointRequest) (sessions.CheckpointStatus, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.CheckpointStatus{}, e
	}
	return a.ReconcileCheckpoint(t, p.connection.SessionID, r)
}
func (p *NativeSwarmAccess) CheckpointStatus(project, id string) (sessions.CheckpointStatus, error) {
	a, t, e := p.authenticate()
	if e != nil {
		return sessions.CheckpointStatus{}, e
	}
	return a.CheckpointStatus(t, p.connection.SessionID, project, id)
}
