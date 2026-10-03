package native

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

type version struct {
	SchemaVersion int `json:"schema_version"`
}
type projectArgs struct {
	version
	Project string `json:"project"`
}
type taskArgs struct {
	version
	Task string `json:"task"`
}
type listArgs struct {
	version
	Project string `json:"project"`
	Status  string `json:"status"`
}
type registerArgs struct {
	version
	Request sessions.AttemptRequest `json:"request"`
}
type acquireArgs struct {
	version
	Request sessions.AcquireLeaseRequest `json:"request"`
}
type renewArgs struct {
	version
	Request sessions.RenewLeaseRequest `json:"request"`
}
type releaseArgs struct {
	version
	Request sessions.ReleaseLeaseRequest `json:"request"`
}
type publishArgs struct {
	version
	Request sessions.PublishRevisionRequest `json:"request"`
}
type evidenceArgs struct {
	version
	Request sessions.EvidenceRequest `json:"request"`
}
type verificationRequest struct {
	OperationKey  string `json:"operation_key"`
	AttemptID     string `json:"attempt_id"`
	LeaseID       string `json:"lease_id"`
	RevisionID    string `json:"revision_id"`
	CheckID       string `json:"check_id"`
	ExpectedFence uint64 `json:"expected_fence"`
}
type verificationArgs struct {
	version
	Request verificationRequest `json:"request"`
}

func (r verificationRequest) native() application.NativeVerificationRequest {
	return application.NativeVerificationRequest{OperationKey: r.OperationKey, AttemptID: r.AttemptID, LeaseID: r.LeaseID, RevisionID: r.RevisionID, CheckID: r.CheckID, ExpectedFence: r.ExpectedFence}
}

type tool struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	InputSchema  any    `json:"inputSchema"`
	OutputSchema any    `json:"outputSchema"`
}

var arguments = map[string]any{"request_verification": verificationArgs{}, "get_verification_status": verificationArgs{}, "cancel_verification": verificationArgs{}, "identity": version{}, "workspace_status": projectArgs{}, "task_list": listArgs{}, "task_get": taskArgs{}, "register_attempt": registerArgs{}, "acquire_lease": acquireArgs{}, "renew_lease": renewArgs{}, "release_lease": releaseArgs{}, "publish_revision": publishArgs{}, "submit_evidence": evidenceArgs{}}
var names = []string{"identity", "workspace_status", "task_list", "task_get", "register_attempt", "acquire_lease", "renew_lease", "release_lease", "publish_revision", "submit_evidence"}

// The output envelope is identical in structuredContent and JSON text fallback.
type failure struct {
	Code     string `json:"code"`
	ExitCode int    `json:"exit_code"`
	Message  string `json:"message"`
}
type envelope struct {
	SchemaVersion int      `json:"schema_version"`
	OK            bool     `json:"ok"`
	Assurance     string   `json:"assurance"`
	Data          any      `json:"data"`
	Error         *failure `json:"error"`
}

type identityData struct {
	SessionID string         `json:"session_id"`
	Scope     sessions.Scope `json:"scope"`
	ExpiresAt time.Time      `json:"expires_at"`
}
type taskData struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Status  string `json:"status"`
}
type taskResult struct {
	Task taskData `json:"task"`
}
type taskList struct {
	Tasks []taskData `json:"tasks"`
}

func taskView(t *store.Task) taskData {
	if t == nil {
		return taskData{}
	}
	return taskData{t.ID, t.Project, t.Title, string(t.Status)}
}

var outputs = map[string]any{"request_verification": application.VerificationStatus{}, "get_verification_status": application.VerificationStatus{}, "cancel_verification": application.VerificationStatus{}, "identity": identityData{}, "workspace_status": application.WorkspaceStatus{}, "task_list": taskList{}, "task_get": taskResult{}, "register_attempt": sessions.Attempt{}, "acquire_lease": sessions.Lease{}, "renew_lease": sessions.Lease{}, "release_lease": sessions.Lease{}, "publish_revision": sessions.AttemptRevision{}, "submit_evidence": sessions.Evidence{}}

func outputSchema(name string) any {
	data := map[string]any{"type": "object", "additionalProperties": false}
	if t, ok := outputs[name]; ok {
		data = schema(reflect.TypeOf(t)).(map[string]any)
	}
	branch := func(ok bool) any {
		payload := any(data)
		errorSchema := any(map[string]any{"type": "null"})
		if !ok {
			payload = map[string]any{"type": "object", "additionalProperties": false}
			errorSchema = schema(reflect.TypeOf(failure{}))
		}
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"schema_version", "ok", "assurance", "data", "error"}, "properties": map[string]any{"schema_version": map[string]any{"type": "integer", "const": 1}, "ok": map[string]any{"type": "boolean", "const": ok}, "assurance": map[string]any{"type": "string", "const": "cooperative-session"}, "data": payload, "error": errorSchema}}
	}
	return map[string]any{"type": "object", "oneOf": []any{branch(true), branch(false)}}
}
func schema(t reflect.Type) any {
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Int, reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Pointer:
		return map[string]any{"oneOf": []any{map[string]any{"type": "null"}, schema(t.Elem())}}
	case reflect.Slice:
		return map[string]any{"type": []string{"array", "null"}, "items": schema(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				props["schema_version"] = map[string]any{"type": "integer", "const": 1}
				required = append(required, "schema_version")
				continue
			}
			n := strings.Split(f.Tag.Get("json"), ",")[0]
			if n == "" {
				n = f.Name
			}
			props[n] = schema(f.Type)
			if !strings.Contains(f.Tag.Get("json"), ",omitempty") {
				required = append(required, n)
			}
		}
		if t == reflect.TypeOf(version{}) {
			props["schema_version"] = map[string]any{"type": "integer", "const": 1}
		}
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	}
	return map[string]any{"type": "object"}
}
func catalog() []tool {
	out := []tool{}
	for _, n := range names {
		out = append(out, tool{n, "Explicit scoped native governance operation; no host enforcement or acceptance authority.", schema(reflect.TypeOf(arguments[n])), outputSchema(n)})
	}
	return out
}
func (s *Server) catalog() []tool {
	out := catalog()
	if s.verification != nil {
		for _, n := range []string{"request_verification", "get_verification_status", "cancel_verification"} {
			out = append(out, tool{n, "Owner-configured named check; exact fenced identity; no caller commands or authority.", schema(reflect.TypeOf(arguments[n])), outputSchema(n)})
		}
	}
	return out
}
func (s *Server) call(name string, raw json.RawMessage) (any, error) {
	w, a, token, principal, e := s.authenticate()
	if e != nil {
		return nil, ErrDenied
	}
	template, ok := arguments[name]
	if !ok {
		return nil, ErrDenied
	}
	arg := reflect.New(reflect.TypeOf(template)).Interface()
	if Decode(raw, arg) != nil || !validJSON(schema(reflect.TypeOf(template)), raw) {
		return nil, ErrDenied
	}
	var v version
	if json.Unmarshal(raw, &v) != nil || v.SchemaVersion != 1 {
		return nil, ErrDenied
	}
	// Reads require fresh exact workspace scope, not just session possession.
	var ctx application.RequestContext
	if name == "workspace_status" || name == "task_list" || name == "task_get" {
		ctx, e = application.NewNativeContext(w, a, token, principal.ID)
		if e != nil {
			return nil, ErrDenied
		}
	}
	switch x := arg.(type) {
	case *verificationArgs:
		if s.verification == nil {
			return nil, ErrDenied
		}
		switch name {
		case "request_verification":
			return s.verification.Request(x.Request.native())
		case "get_verification_status":
			return s.verification.Status(x.Request.native())
		case "cancel_verification":
			return s.verification.Cancel(x.Request.native())
		}
		return nil, ErrDenied
	case *version:
		return identityData{principal.ID, principal.Scope, principal.ExpiresAt}, nil
	case *projectArgs:
		return application.Status(ctx, x.Project)
	case *taskArgs:
		t, e := application.GetTask(ctx, x.Task)
		return taskResult{taskView(t)}, e
	case *listArgs:
		if x.Status != "" && x.Status != "open" && x.Status != "active" && x.Status != "blocked" && x.Status != "done" {
			return nil, ErrDenied
		}
		ts, e := application.ListTasks(ctx, x.Project, model.Status(x.Status))
		views := []taskData{}
		for _, t := range ts {
			views = append(views, taskView(t))
		}
		return taskList{views}, e
	case *registerArgs:
		return a.RegisterAttempt(token, principal.ID, x.Request)
	case *acquireArgs:
		return a.AcquireLease(token, principal.ID, x.Request)
	case *renewArgs:
		return a.RenewLease(token, principal.ID, x.Request)
	case *releaseArgs:
		return a.ReleaseLease(token, principal.ID, x.Request)
	case *publishArgs:
		return a.PublishAttemptRevision(token, principal.ID, x.Request)
	case *evidenceArgs:
		return a.SubmitEvidence(token, principal.ID, x.Request)
	}
	return nil, ErrDenied
}
func result(name string, data any, err error) any {
	e := envelope{1, err == nil, "cooperative-session", data, nil}
	if err != nil {
		e.Data = map[string]any{}
		e.Error = &failure{"refused", 3, "native governance request refused; correct explicit authority or request before retrying"}
	}
	b, encodeErr := json.Marshal(e)
	if encodeErr != nil || !validJSON(outputSchema(name), b) {
		if err != nil {
			panic("invalid native refusal schema")
		}
		return result(name, nil, ErrDenied)
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "structuredContent": e, "isError": err != nil}
}
