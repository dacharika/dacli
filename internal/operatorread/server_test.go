package operatorread

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/mlnomadpy/dacli/contracts/operator/v1"
)

func requestFrame(workspace, project, task string) []byte {
	b, _ := json.Marshal(operatorv1.Request{SchemaVersion: 1, RequestID: "request-1", Operation: "tasks.list", WorkspaceID: workspace, ProjectID: project, TaskID: task})
	return b
}

func TestHandleReauthenticatesAndRejectsCrossWorkspaceBeforeProjection(t *testing.T) {
	authCalls, projectCalls := 0, 0
	s := Server{WorkspaceID: "ws-local"}
	s.Authorize = func(_ context.Context, req operatorv1.Request) (operatorv1.Scope, error) {
		authCalls++
		return operatorv1.Scope{WorkspaceID: req.WorkspaceID, ProjectID: "operator"}, nil
	}
	s.Project = func(context.Context, operatorv1.Request) (Projection, error) {
		projectCalls++
		return Projection{Data: map[string]any{"tasks": []int{1}}, Source: operatorv1.Source{Producer: "dashboard.buildTasks", Schema: "operator-tasks/v1"}}, nil
	}
	for _, workspace := range []string{"ws-local", "ws-foreign"} {
		response := s.Handle(context.Background(), requestFrame(workspace, "operator", ""))
		if workspace == "ws-local" {
			if _, err := operatorv1.DecodeSuccess(response, MaxResponseBytes); err != nil {
				t.Fatal(err)
			}
		} else {
			failure, err := operatorv1.DecodeFailure(response, MaxResponseBytes)
			if err != nil || failure.Error.Code != operatorv1.CodeDenied || strings.Contains(string(response), workspace) {
				t.Fatalf("foreign workspace response disclosed or misclassified: %s %v", response, err)
			}
		}
	}
	if authCalls != 1 || projectCalls != 1 {
		t.Fatalf("auth/project calls = %d/%d, want one authorized read", authCalls, projectCalls)
	}
}

func TestHandleReauthorizesEveryRequestAndObservesRevocation(t *testing.T) {
	authorized := true
	authCalls, projectCalls := 0, 0
	s := Server{WorkspaceID: "ws-local"}
	s.Authorize = func(context.Context, operatorv1.Request) (operatorv1.Scope, error) {
		authCalls++
		if !authorized {
			return operatorv1.Scope{}, ErrDenied
		}
		return operatorv1.Scope{WorkspaceID: "ws-local", ProjectID: "operator"}, nil
	}
	s.Project = func(context.Context, operatorv1.Request) (Projection, error) {
		projectCalls++
		return Projection{Data: map[string]any{"tasks": []int{1}}, Source: operatorv1.Source{Producer: "dashboard.buildTasks", Schema: "operator-tasks/v1"}}, nil
	}
	first := s.Handle(context.Background(), requestFrame("ws-local", "operator", ""))
	if _, err := operatorv1.DecodeSuccess(first, MaxResponseBytes); err != nil {
		t.Fatal(err)
	}
	authorized = false
	second := s.Handle(context.Background(), requestFrame("ws-local", "operator", ""))
	failure, err := operatorv1.DecodeFailure(second, MaxResponseBytes)
	if err != nil || failure.Error.Code != operatorv1.CodeDenied || authCalls != 2 || projectCalls != 1 {
		t.Fatalf("revocation was not observed on the next request: %s calls=%d/%d err=%v", second, authCalls, projectCalls, err)
	}
}

func TestHandleStrictVersionAndAuthenticatedTaskScope(t *testing.T) {
	s := Server{WorkspaceID: "ws-local"}
	authCalls, projectCalls := 0, 0
	s.Authorize = func(_ context.Context, req operatorv1.Request) (operatorv1.Scope, error) {
		authCalls++
		if req.TaskID == "worker-task" {
			return operatorv1.Scope{}, ErrDenied
		}
		return operatorv1.Scope{WorkspaceID: "ws-local", ProjectID: req.ProjectID, TaskID: req.TaskID}, nil
	}
	s.Project = func(context.Context, operatorv1.Request) (Projection, error) {
		projectCalls++
		return Projection{Data: map[string]any{"tasks": []int{1}}, Source: operatorv1.Source{Producer: "dashboard.buildTasks", Schema: "operator-tasks/v1"}}, nil
	}
	base := requestFrame("ws-local", "operator", "")
	unsupported := strings.Replace(string(base), `"schema_version":1`, `"schema_version":2`, 1)
	response := s.Handle(context.Background(), []byte(unsupported))
	failure, err := operatorv1.DecodeFailure(response, MaxResponseBytes)
	if err != nil || failure.Error.Code != operatorv1.CodeUnsupportedVersion {
		t.Fatalf("unsupported version response = %s %v", response, err)
	}
	if authCalls != 0 || projectCalls != 0 {
		t.Fatalf("unsupported version reached authorization or projection: %d/%d", authCalls, projectCalls)
	}
	taskRequest, _ := json.Marshal(operatorv1.Request{SchemaVersion: 1, RequestID: "request-2", Operation: "attempts.timeline", WorkspaceID: "ws-local", ProjectID: "operator", TaskID: "worker-task"})
	response = s.Handle(context.Background(), taskRequest)
	failure, err = operatorv1.DecodeFailure(response, MaxResponseBytes)
	if err != nil || failure.Error.Code != operatorv1.CodeDenied || strings.Contains(string(response), "worker-task") {
		t.Fatalf("task denial was not redacted: %s %v", response, err)
	}
}

func TestHandleDistinguishesStaleUnavailableAndDenied(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code operatorv1.Code
	}{{"stale", ErrStale, operatorv1.CodeStaleSource}, {"unavailable", ErrUnavailable, operatorv1.CodeStorageUnavailable}, {"denied", ErrDenied, operatorv1.CodeDenied}, {"not-found", ErrNotFound, operatorv1.CodeNotFound}, {"unsupported-page", ErrUnsupported, operatorv1.CodeUnsupportedPage}} {
		t.Run(tc.name, func(t *testing.T) {
			s := Server{WorkspaceID: "ws-local", Authorize: func(context.Context, operatorv1.Request) (operatorv1.Scope, error) { return operatorv1.Scope{}, tc.err }, Project: func(context.Context, operatorv1.Request) (Projection, error) { return Projection{}, nil }}
			response := s.Handle(context.Background(), requestFrame("ws-local", "operator", ""))
			failure, err := operatorv1.DecodeFailure(response, MaxResponseBytes)
			if err != nil || failure.Error.Code != tc.code {
				t.Fatalf("mapped error = %s %v", response, err)
			}
		})
	}
}

func TestHandleBoundsProducerResponse(t *testing.T) {
	s := Server{WorkspaceID: "ws-local", Authorize: func(context.Context, operatorv1.Request) (operatorv1.Scope, error) {
		return operatorv1.Scope{WorkspaceID: "ws-local", ProjectID: "operator"}, nil
	}, Project: func(context.Context, operatorv1.Request) (Projection, error) {
		return Projection{Data: map[string]string{"payload": strings.Repeat("x", MaxResponseBytes)}, Source: operatorv1.Source{Producer: "test", Schema: "test/v1"}}, nil
	}}
	response := s.Handle(context.Background(), requestFrame("ws-local", "operator", ""))
	failure, err := operatorv1.DecodeFailure(response, MaxResponseBytes)
	if err != nil || failure.Error.Code != operatorv1.CodeLimitExceeded || strings.Contains(string(response), strings.Repeat("x", 32)) {
		t.Fatalf("oversized response leaked or was accepted: len=%d err=%v", len(response), err)
	}
}

func TestHandleMarksNonresumableTruncationPartial(t *testing.T) {
	request, _ := json.Marshal(operatorv1.Request{SchemaVersion: 1, RequestID: "page", Operation: "tasks.list", WorkspaceID: "ws-local", ProjectID: "operator", Limit: intPointer(2)})
	s := Server{WorkspaceID: "ws-local", Authorize: func(context.Context, operatorv1.Request) (operatorv1.Scope, error) {
		return operatorv1.Scope{WorkspaceID: "ws-local", ProjectID: "operator"}, nil
	}, Project: func(context.Context, operatorv1.Request) (Projection, error) {
		return Projection{Data: map[string]any{"tasks": []int{1, 2, 3}}, Source: operatorv1.Source{Producer: "dashboard.buildTasks", Schema: "operator-tasks/v1"}}, nil
	}}
	response := s.Handle(context.Background(), request)
	decoded, err := operatorv1.DecodeSuccess(response, MaxResponseBytes)
	if err != nil || decoded.Page == nil || decoded.Page.Limit != 2 || !decoded.Page.Truncated || !decoded.Page.Partial {
		t.Fatalf("truncated page was not explicit: %s %v", response, err)
	}
}

func intPointer(value int) *int { return &value }

func TestHandleDeadlineCancellationIsPassedToCallbacks(t *testing.T) {
	s := Server{WorkspaceID: "ws-local", Deadline: 5 * time.Millisecond, Authorize: func(ctx context.Context, _ operatorv1.Request) (operatorv1.Scope, error) {
		<-ctx.Done()
		return operatorv1.Scope{}, ErrStale
	}, Project: func(context.Context, operatorv1.Request) (Projection, error) { return Projection{}, nil }}
	response := s.Handle(context.Background(), requestFrame("ws-local", "operator", ""))
	failure, err := operatorv1.DecodeFailure(response, MaxResponseBytes)
	if err != nil || failure.Error.Code != operatorv1.CodeStaleSource {
		t.Fatalf("deadline callback state = %s %v", response, err)
	}
}
