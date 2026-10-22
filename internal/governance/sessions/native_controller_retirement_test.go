package sessions

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// Exercise actual producer admission and owner recovery through their APIs. The
// original pending operation is retained; a restart is a new invocation, not a
// reexecution of unknown work or a new checkpoint accounting origin (core633).
func TestNativeControllerRestartAfterExactOwnerVerificationRetirement(t *testing.T) {
	for _, mode := range []string{"exact recovery", "released only", "wrong fence", "wrong attempt", "wrong worker", "wrong task", "missing lease", "missing producer binding", "wrong producer project", "wrong producer task", "wrong producer runner", "wrong producer request", "wrong producer digest"} {
		t.Run(mode, func(t *testing.T) {
			f, active := controllerCompletedWaveFrom(t, pluginControllerSetup(t))
			pending := map[string]operation{}
			for _, action := range active.Actions {
				route := f.start.Routes[action.TaskID]
				request := action.Verification
				var grant VerificationGrant
				if err := f.a.transaction(false, false, func(s *state) error {
					grant = s.Grants[request.GrantID]
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := f.a.BeginVerification(f.observer.tokens[action.TaskID], f.runnerTokens[route.RunnerSession], route.RunnerSession, request, grant.CommandDigest); err != nil {
					t.Fatal("actual verifier reservation", err)
				}
			}
			stopped, err := f.a.StopController(f.token, f.s.ID, ControllerStepRequest{OperationKey: "stop-for-recovery", ProjectID: active.ProjectID, InvocationID: active.ID, ExpectedSequence: active.Sequence})
			if err != nil || !stopped.Terminal {
				t.Fatal("actual stop", stopped, err)
			}
			unrecovered := f.start
			unrecovered.OperationKey, unrecovered.InvocationID = "start-before-recovery", "controller-before-recovery"
			if _, err = f.a.StartController(f.owner, unrecovered); !errors.Is(err, ErrDenied) {
				t.Fatal("stopped but unrecovered pending work admitted restart", err)
			}
			var history []CheckpointStatus
			if err = f.a.transaction(false, false, func(s *state) error {
				history = append([]CheckpointStatus(nil), s.Checkpoints[active.CheckpointID]...)
				for _, action := range active.Actions {
					pending[action.Verification.OperationKey] = s.Operations[action.Verification.OperationKey]
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(history) == 0 || history[len(history)-1].Cycle == 0 {
				t.Fatal("fixture must have actual checkpoint cycle accounting")
			}
			for _, action := range active.Actions {
				if mode == "released only" {
					route := f.start.Routes[action.TaskID]
					if _, err = f.a.ReleaseLease(f.observer.tokens[action.TaskID], route.WorkerSession, ReleaseLeaseRequest{OperationKey: "release-" + action.TaskID, AttemptID: action.AttemptID, LeaseID: action.LeaseID, ExpectedFence: action.Fence}); err != nil {
						t.Fatal(err)
					}
					continue
				}
				target, err := f.a.OwnerRecoveryTarget(f.owner, "lease", action.LeaseID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.a.Recover(f.owner, RecoveryRequest{OperationKey: "recover-" + action.TaskID, Target: target, Mode: "owner-reconciliation", Rationale: "retire isolated fixture protection", Uncertainty: "original verification outcome remains unknown", ReleaseRisk: "owner accepts old cooperative writer risk"}); err != nil {
					t.Fatal("actual owner recovery", err)
				}
			}
			// Simulate damaged/mismatched historical records. They cannot be used
			// as a permissive release receipt by the real restart admission API.
			if mode != "exact recovery" && mode != "released only" {
				if err = f.a.transaction(false, true, func(s *state) error {
					id := active.Actions[0].LeaseID
					recovery := s.Recoveries["lease:"+id]
					switch mode {
					case "wrong fence":
						recovery.Target.ExpectedFence++
					case "wrong attempt":
						recovery.Target.AttemptID = "another-attempt"
					case "wrong worker":
						recovery.Target.SessionID = "another-worker"
					case "wrong task":
						recovery.Target.TaskID = "another-task"
					case "missing lease":
						delete(s.Leases, id)
					}
					s.Recoveries["lease:"+id] = recovery
					key := active.Actions[0].Verification.OperationKey
					op := s.Operations[key]
					switch mode {
					case "missing producer binding":
						op.CheckpointVerification = nil
					case "wrong producer project":
						op.CheckpointVerification.ProjectID = "another-project"
					case "wrong producer task":
						op.CheckpointVerification.TaskID = "another-task"
					case "wrong producer runner":
						op.CheckpointVerification.RunnerSession = "another-runner"
					case "wrong producer request":
						op.CheckpointVerification.Request.OperationKey = "another-operation"
					case "wrong producer digest":
						op.Digest = identity("run:another-descriptor", op.CheckpointVerification.RunnerSession, op.CheckpointVerification.Request)
					}
					s.Operations[key] = op
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err = f.a.transaction(false, false, func(s *state) error {
				for key := range pending {
					pending[key] = s.Operations[key]
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			next := retirementReplacementRoutes(t, f)
			reopened, err := Open(f.a.path, f.a.binding, f.observer)
			if err != nil {
				t.Fatal(err)
			}
			out, err := reopened.StartController(f.owner, next)
			if mode == "exact recovery" {
				if err != nil || out.CheckpointID != active.CheckpointID || out.ID != next.InvocationID {
					t.Fatal("exact owner-retired pending verification blocked same-checkpoint restart", out, err)
				}
			} else if !errors.Is(err, ErrDenied) {
				t.Fatal("unknown or mismatched protection admitted restart", mode, out, err)
			}
			if err = reopened.transaction(false, false, func(s *state) error {
				if !reflect.DeepEqual(history, s.Checkpoints[active.CheckpointID]) || !reflect.DeepEqual(stopped, s.ControllerInvocations[stopped.ID].Status) {
					t.Fatal("restart reset original checkpoint accounting or stopped invocation")
				}
				for key, original := range pending {
					if !original.Pending || original.Evidence != nil || !reflect.DeepEqual(original, s.Operations[key]) {
						t.Fatal("restart rewrote unknown original verification", key)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func retirementReplacementRoutes(t *testing.T, f controllerFixture) ControllerStartRequest {
	t.Helper()
	next := f.start
	next.OperationKey, next.InvocationID = "start-recovered", "controller-recovered"
	next.Routes = map[string]ControllerTaskRoute{}
	for task, route := range f.start.Routes {
		worker, token, err := f.a.Enroll(f.owner, f.observer.workers[task].Scope, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		observation := f.observer.observations[task]
		observation.Worker = PreflightActor{SessionID: worker.ID, CredentialHash: hash(token)}
		f.a.reviewObserver = checkpointReviewObservation{observation.ReviewObservation}
		grant, err := f.a.GrantReview(f.owner, ReviewGrantRequest{ReviewerSession: route.ReviewerSession, WorkerSession: worker.ID, TaskID: task, Repository: observation.RunnerRepository, ExpiresAt: time.Now().Add(20 * time.Minute)})
		f.a.reviewObserver = nil
		if err != nil {
			t.Fatal("independent reviewer grant for replacement worker", err)
		}
		observation.Reviewer.GrantID = grant.ID
		f.observer.observations[task] = observation
		f.observer.workers[task], f.observer.tokens[task] = worker, token
		route.WorkerSession, route.ReviewGrantID = worker.ID, grant.ID
		next.Routes[task] = route
	}
	return next
}
