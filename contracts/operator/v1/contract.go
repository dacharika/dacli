// Package operatorv1 defines the portable read-only operator wire contract.
// It validates frames only; it does not authenticate, access a workspace, or
// implement a transport (issue #1146).
package operatorv1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const Version = 1

// Code is a stable protocol failure category.
type Code string

const (
	CodeUnsupportedVersion Code = "unsupported_version"
	CodeInvalidRequest     Code = "invalid_request"
	CodeUnknownOperation   Code = "unknown_operation"
	CodeUnknownField       Code = "unknown_field"
	CodeDenied             Code = "denied"
	CodeNotFound           Code = "not_found"
	CodeStaleSource        Code = "stale_source"
	CodeLimitExceeded      Code = "limit_exceeded"
	CodeUnsupportedPage    Code = "unsupported_pagination"
	CodeStorageUnavailable Code = "storage_unavailable"
	CodeInternal           Code = "internal"
)

// ValidationError identifies a portable contract failure without echoing input.
type ValidationError struct {
	Code Code
}

func (e *ValidationError) Error() string { return string(e.Code) }

// Request is one independent JSON-lines request. Unknown fields are rejected.
type Request struct {
	SchemaVersion int     `json:"schema_version"`
	RequestID     string  `json:"request_id"`
	Operation     string  `json:"operation"`
	WorkspaceID   string  `json:"workspace_id"`
	ProjectID     string  `json:"project_id,omitempty"`
	TaskID        string  `json:"task_id,omitempty"`
	Limit         *int    `json:"limit,omitempty"`
	Cursor        string  `json:"cursor,omitempty"`
	Filters       Filters `json:"filters,omitempty"`
}

type Filters struct {
	Kind  string `json:"kind,omitempty"`
	State string `json:"state,omitempty"`
	Range string `json:"range,omitempty"`
}

type Scope struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	TaskID      string `json:"task_id,omitempty"`
}

type Source struct {
	Producer   string `json:"producer"`
	Schema     string `json:"schema"`
	ObservedAt string `json:"observed_at"`
}

type Page struct {
	Limit      int    `json:"limit"`
	NextCursor string `json:"next_cursor,omitempty"`
	Truncated  bool   `json:"truncated"`
	Partial    bool   `json:"partial"`
}

type ErrorBody struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Success struct {
	SchemaVersion int             `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	OK            bool            `json:"ok"`
	Scope         Scope           `json:"scope"`
	ObservedAt    string          `json:"observed_at"`
	Source        Source          `json:"source"`
	Data          json.RawMessage `json:"data"`
	Page          *Page           `json:"page,omitempty"`
}

type Failure struct {
	SchemaVersion int       `json:"schema_version"`
	RequestID     string    `json:"request_id"`
	OK            bool      `json:"ok"`
	Error         ErrorBody `json:"error"`
}

var errUnknownField = errors.New("unknown_field")

// DecodeRequest parses one bounded request frame. maxBytes must be a positive
// caller-selected limit; the stdio adapter is expected to enforce its own line
// bound before calling this function.
func DecodeRequest(raw []byte, maxBytes int) (Request, error) {
	var req Request
	if maxBytes <= 0 || len(raw) > maxBytes {
		return req, &ValidationError{Code: CodeLimitExceeded}
	}
	if err := decodeStrict(raw, &req); err != nil {
		if errors.Is(err, errUnknownField) {
			return req, &ValidationError{Code: CodeUnknownField}
		}
		return req, &ValidationError{Code: CodeInvalidRequest}
	}
	if req.SchemaVersion != Version {
		return req, &ValidationError{Code: CodeUnsupportedVersion}
	}
	if err := req.Validate(); err != nil {
		return req, err
	}
	return req, nil
}

// DecodeSuccess and DecodeFailure apply strict decoding, version, and shape
// checks to response frames. Data remains producer-owned JSON by design.
func DecodeSuccess(raw []byte, maxBytes int) (Success, error) {
	var got Success
	if err := decodeBoundedStrict(raw, maxBytes, &got); err != nil {
		return got, err
	}
	if got.SchemaVersion != Version {
		return got, &ValidationError{Code: CodeUnsupportedVersion}
	}
	if !got.OK || got.RequestID == "" || got.Scope.WorkspaceID == "" || got.Scope.ProjectID == "" || got.Source.Producer == "" || got.Source.Schema == "" || len(got.Data) == 0 || !validTime(got.ObservedAt) || !validTime(got.Source.ObservedAt) {
		return got, &ValidationError{Code: CodeInvalidRequest}
	}
	if got.Page != nil && !validPage(*got.Page) {
		return got, &ValidationError{Code: CodeInvalidRequest}
	}
	return got, nil
}

func DecodeFailure(raw []byte, maxBytes int) (Failure, error) {
	var got Failure
	if err := decodeBoundedStrict(raw, maxBytes, &got); err != nil {
		return got, err
	}
	if got.SchemaVersion != Version {
		return got, &ValidationError{Code: CodeUnsupportedVersion}
	}
	if got.OK || !validErrorCode(got.Error.Code) || got.Error.Message == "" || len(got.Error.Message) > 512 {
		return got, &ValidationError{Code: CodeInvalidRequest}
	}
	return got, nil
}

func decodeBoundedStrict(raw []byte, maxBytes int, dst any) error {
	if maxBytes <= 0 || len(raw) > maxBytes {
		return &ValidationError{Code: CodeLimitExceeded}
	}
	if err := decodeStrict(raw, dst); err != nil {
		if errors.Is(err, errUnknownField) {
			return &ValidationError{Code: CodeUnknownField}
		}
		return &ValidationError{Code: CodeInvalidRequest}
	}
	return nil
}

func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return fmt.Errorf("%w", errUnknownField)
		}
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func (r Request) Validate() error {
	if !validIdentifier(r.RequestID, 128) || !validIdentifier(r.WorkspaceID, 128) || len(r.ProjectID) > 128 || len(r.TaskID) > 128 || len(r.Cursor) > 2048 || len(r.Filters.Kind) > 64 || r.ProjectID != "" && !validIdentifier(r.ProjectID, 128) || r.TaskID != "" && !validIdentifier(r.TaskID, 128) {
		return &ValidationError{Code: CodeInvalidRequest}
	}
	switch r.Operation {
	case "tasks.list":
		if r.ProjectID == "" {
			return &ValidationError{Code: CodeInvalidRequest}
		}
	case "tasks.get", "attempts.timeline":
		if r.ProjectID == "" || r.TaskID == "" {
			return &ValidationError{Code: CodeInvalidRequest}
		}
	case "activity.list", "attention.list", "evidence.list":
		if r.ProjectID == "" {
			return &ValidationError{Code: CodeInvalidRequest}
		}
	case "governance.snapshot":
		if r.ProjectID == "" {
			return &ValidationError{Code: CodeInvalidRequest}
		}
	default:
		return &ValidationError{Code: CodeUnknownOperation}
	}
	if r.Limit != nil && (*r.Limit <= 0 || *r.Limit > 500) {
		return &ValidationError{Code: CodeLimitExceeded}
	}
	if r.Cursor != "" && r.Operation != "activity.list" {
		return &ValidationError{Code: CodeUnsupportedPage}
	}
	if r.Filters.State != "" && r.Filters.State != "all" && r.Filters.State != "pending" && r.Filters.State != "applied" {
		return &ValidationError{Code: CodeInvalidRequest}
	}
	if r.Filters.Range != "" && r.Filters.Range != "24h" && r.Filters.Range != "7d" && r.Filters.Range != "30d" && r.Filters.Range != "all" {
		return &ValidationError{Code: CodeInvalidRequest}
	}
	return nil
}

func validPage(p Page) bool       { return p.Limit > 0 && p.Limit <= 512 && len(p.NextCursor) <= 2048 }
func validTime(value string) bool { _, err := time.Parse(time.RFC3339Nano, value); return err == nil }

func validIdentifier(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func validErrorCode(code Code) bool {
	switch code {
	case CodeUnsupportedVersion, CodeInvalidRequest, CodeUnknownOperation, CodeUnknownField, CodeDenied, CodeNotFound, CodeStaleSource, CodeLimitExceeded, CodeUnsupportedPage, CodeStorageUnavailable, CodeInternal:
		return true
	default:
		return false
	}
}
