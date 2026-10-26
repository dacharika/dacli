// Package operatorread assembles the bounded v1 read protocol around explicit
// authentication and canonical producer callbacks. It owns no credential or
// projection format and has no listener or command parser.
package operatorread

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	operatorv1 "github.com/mlnomadpy/dacli/contracts/operator/v1"
)

const (
	MaxRequestBytes  = 64 << 10
	MaxResponseBytes = 1 << 20
	DefaultDeadline  = 15 * time.Second
)

var (
	ErrDenied      = errors.New("operator read denied")
	ErrUnavailable = errors.New("operator capability unavailable")
	ErrStale       = errors.New("operator source stale")
	ErrNotFound    = errors.New("operator resource not found")
	ErrUnsupported = errors.New("operator pagination unsupported")
)

type Projection struct {
	Data   any
	Source operatorv1.Source
	Page   *operatorv1.Page
}

// Authorize must re-read the private identity and authority on every call.
// The returned scope is the authenticated scope, not a copy of request fields.
type Authorize func(context.Context, operatorv1.Request) (operatorv1.Scope, error)

// Project runs one existing canonical Go producer after authorization.
type Project func(context.Context, operatorv1.Request) (Projection, error)

type Server struct {
	WorkspaceID string
	Authorize   Authorize
	Project     Project
	Deadline    time.Duration
}

func (s Server) Handle(ctx context.Context, raw []byte) []byte {
	req, err := operatorv1.DecodeRequest(raw, MaxRequestBytes)
	if err != nil {
		return encodeFailure(requestID(raw), mapValidationError(err))
	}
	if s.WorkspaceID == "" || req.WorkspaceID != s.WorkspaceID || s.Authorize == nil || s.Project == nil {
		return encodeFailure(req.RequestID, deniedError())
	}
	deadline := s.Deadline
	if deadline <= 0 {
		deadline = DefaultDeadline
	}
	workCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	scope, err := s.Authorize(workCtx, req)
	if err != nil {
		return encodeFailure(req.RequestID, mapReadError(err))
	}
	if scope.WorkspaceID != req.WorkspaceID || scope.ProjectID != req.ProjectID || req.TaskID != "" && scope.TaskID != req.TaskID {
		return encodeFailure(req.RequestID, deniedError())
	}
	projection, err := s.Project(workCtx, req)
	if err != nil {
		return encodeFailure(req.RequestID, mapReadError(err))
	}
	data, err := json.Marshal(projection.Data)
	if err != nil {
		return encodeFailure(req.RequestID, operatorv1.ErrorBody{Code: operatorv1.CodeInternal, Message: "operator read failed", Retryable: false})
	}
	data, projection.Page, err = applyPage(req, data, projection.Page)
	if err != nil {
		return encodeFailure(req.RequestID, operatorv1.ErrorBody{Code: operatorv1.CodeLimitExceeded, Message: "response exceeds the bounded operator frame", Retryable: false})
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if projection.Source.ObservedAt == "" {
		projection.Source.ObservedAt = now
	}
	if projection.Source.Producer == "" || projection.Source.Schema == "" {
		return encodeFailure(req.RequestID, operatorv1.ErrorBody{Code: operatorv1.CodeInternal, Message: "canonical producer identity is unavailable", Retryable: false})
	}
	response := operatorv1.Success{SchemaVersion: operatorv1.Version, RequestID: req.RequestID, OK: true, Scope: scope, ObservedAt: now, Source: projection.Source, Data: data, Page: projection.Page}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > MaxResponseBytes {
		return encodeFailure(req.RequestID, operatorv1.ErrorBody{Code: operatorv1.CodeLimitExceeded, Message: "response exceeds the bounded operator frame", Retryable: false})
	}
	return encoded
}

func applyPage(req operatorv1.Request, data []byte, existing *operatorv1.Page) ([]byte, *operatorv1.Page, error) {
	limit := 50
	if req.Operation == "governance.snapshot" || req.Operation == "evidence.list" {
		limit = 512
	}
	if req.Limit != nil {
		limit = *req.Limit
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, nil, err
	}
	page := operatorv1.Page{Limit: limit}
	truncated := false
	if req.Operation == "activity.list" {
		var source struct {
			NextCursor string `json:"next_cursor"`
			Truncated  bool   `json:"truncated"`
			Partial    bool   `json:"partial"`
		}
		_ = json.Unmarshal(data, &source)
		page.NextCursor = source.NextCursor
		truncated = source.Truncated
		page.Partial = source.Partial
	}
	for _, field := range pageFields(req.Operation) {
		raw := object[field]
		if len(raw) == 0 {
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			continue
		}
		if len(items) > limit {
			truncated = true
			items = items[:limit]
			encoded, err := json.Marshal(items)
			if err != nil {
				return nil, nil, err
			}
			object[field] = encoded
		}
	}
	if existing != nil {
		page.NextCursor = existing.NextCursor
		truncated = truncated || existing.Truncated
		page.Partial = existing.Partial
	}
	page.Truncated = truncated
	if truncated && page.NextCursor == "" {
		page.Partial = true
	}
	if len(pageFields(req.Operation)) > 0 || truncated || page.Partial {
		encoded, err := json.Marshal(object)
		return encoded, &page, err
	}
	return data, nil, nil
}

func pageFields(operation string) []string {
	switch operation {
	case "tasks.list":
		return []string{"tasks"}
	case "activity.list":
		return []string{"events"}
	case "attempts.timeline":
		return []string{"attempts"}
	case "attention.list":
		return []string{"alerts"}
	case "governance.snapshot", "evidence.list":
		return []string{"records", "attention"}
	default:
		return nil
	}
}

func requestID(raw []byte) string {
	var partial struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(raw, &partial)
	if len(partial.RequestID) > 128 {
		return ""
	}
	return partial.RequestID
}

func encodeFailure(requestID string, body operatorv1.ErrorBody) []byte {
	if len(body.Message) > 512 {
		body.Message = "operator read failed"
	}
	data, err := json.Marshal(operatorv1.Failure{SchemaVersion: operatorv1.Version, RequestID: requestID, OK: false, Error: body})
	if err != nil || len(data) > MaxResponseBytes {
		return []byte(`{"schema_version":1,"request_id":"","ok":false,"error":{"code":"internal","message":"operator read failed","retryable":false}}`)
	}
	return data
}

// FailureFrame returns a redacted terminal protocol error for framing and
// lifecycle failures that occur before a request can be handled.
func FailureFrame(requestID string, code operatorv1.Code, message string) []byte {
	return encodeFailure(requestID, operatorv1.ErrorBody{Code: code, Message: message, Retryable: false})
}

func deniedError() operatorv1.ErrorBody {
	return operatorv1.ErrorBody{Code: operatorv1.CodeDenied, Message: "operator read denied", Retryable: false}
}

func mapValidationError(err error) operatorv1.ErrorBody {
	var validation *operatorv1.ValidationError
	if errors.As(err, &validation) {
		return operatorv1.ErrorBody{Code: validation.Code, Message: string(validation.Code), Retryable: false}
	}
	return operatorv1.ErrorBody{Code: operatorv1.CodeInvalidRequest, Message: "invalid operator request", Retryable: false}
}

func mapReadError(err error) operatorv1.ErrorBody {
	switch {
	case errors.Is(err, ErrDenied):
		return deniedError()
	case errors.Is(err, ErrStale):
		return operatorv1.ErrorBody{Code: operatorv1.CodeStaleSource, Message: "operator source is stale", Retryable: true}
	case errors.Is(err, ErrNotFound):
		return operatorv1.ErrorBody{Code: operatorv1.CodeNotFound, Message: "operator resource not found", Retryable: false}
	case errors.Is(err, ErrUnsupported):
		return operatorv1.ErrorBody{Code: operatorv1.CodeUnsupportedPage, Message: "operator pagination is unsupported for this source", Retryable: false}
	case errors.Is(err, ErrUnavailable):
		return operatorv1.ErrorBody{Code: operatorv1.CodeStorageUnavailable, Message: "operator capability is unavailable", Retryable: false}
	default:
		return operatorv1.ErrorBody{Code: operatorv1.CodeInternal, Message: "operator read failed", Retryable: false}
	}
}
