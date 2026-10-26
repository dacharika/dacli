// Package operatorconformance is an independent consumer for the public operator JSON-lines
// contract. It deliberately does not import the server or contract packages.
package operatorconformance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const protocolVersion = 1

const (
	maxRequestFrame  = 64 * 1024
	maxResponseFrame = 1024 * 1024
)

type request struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Operation     string `json:"operation"`
	WorkspaceID   string `json:"workspace_id"`
	ProjectID     string `json:"project_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	Limit         *int   `json:"limit,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
}

type response struct {
	SchemaVersion int             `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	OK            bool            `json:"ok"`
	Scope         scope           `json:"scope"`
	ObservedAt    string          `json:"observed_at,omitempty"`
	Source        source          `json:"source,omitempty"`
	Error         *wireError      `json:"error,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
	Page          *page           `json:"page,omitempty"`
}

type source struct {
	Producer   string `json:"producer"`
	Schema     string `json:"schema"`
	ObservedAt string `json:"observed_at"`
}

type scope struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	TaskID      string `json:"task_id,omitempty"`
}

type wireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type page struct {
	Limit      int    `json:"limit"`
	NextCursor string `json:"next_cursor,omitempty"`
	Truncated  bool   `json:"truncated"`
	Partial    bool   `json:"partial"`
}

type child struct {
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	stdoutPipe      io.ReadCloser
	stdout          *bufio.Reader
	responseTimeout time.Duration
	shutdownTimeout time.Duration
	killWaitTimeout time.Duration
	waitMu          sync.Mutex
	waitErr         error
	waitDone        chan struct{}
}

func startChild(binary, connection string) (*child, error) {
	if binary == "" || connection == "" {
		return nil, errors.New("runtime and opaque connection path are required")
	}
	cmd := exec.Command(binary, "operator", "serve", "--connection", connection)
	cmd.Stderr = io.Discard // diagnostics are intentionally outside the protocol.
	cmd.WaitDelay = 2 * time.Second
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = stdoutWriter
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, err
	}
	_ = stdoutWriter.Close()
	waitDone := make(chan struct{})
	child := &child{cmd: cmd, stdin: stdin, stdoutPipe: stdoutReader, stdout: bufio.NewReaderSize(stdoutReader, 4096), responseTimeout: 20 * time.Second,
		shutdownTimeout: 2 * time.Second, killWaitTimeout: time.Second, waitDone: waitDone}
	go func() {
		err := cmd.Wait()
		child.waitMu.Lock()
		child.waitErr = err
		child.waitMu.Unlock()
		close(waitDone)
	}()
	return child, nil
}

func (c *child) close() error {
	if c == nil || c.cmd == nil {
		return nil
	}
	_ = c.stdin.Close()
	select {
	case <-c.waitDone:
		_ = c.stdoutPipe.Close()
		return c.exitError()
	case <-time.After(c.shutdownTimeout):
		_ = c.cmd.Process.Kill()
		_ = c.stdoutPipe.Close()
		select {
		case <-c.waitDone:
			return c.exitError()
		case <-time.After(c.killWaitTimeout):
			return errors.New("operator child did not stop after kill")
		}
	}
}

func (c *child) terminate() error {
	_ = c.stdin.Close()
	_ = c.cmd.Process.Kill()
	_ = c.stdoutPipe.Close()
	select {
	case <-c.waitDone:
		return c.exitError()
	case <-time.After(c.killWaitTimeout):
		return errors.New("operator child did not stop after kill")
	}
}

func (c *child) exitError() error {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	return c.waitErr
}

func (c *child) exchange(req request, expectedWorkspace, expectedProject string) (response, error) {
	var got response
	raw, err := json.Marshal(req)
	if err != nil {
		return got, err
	}
	if len(raw)+1 > maxRequestFrame {
		return got, errors.New("request exceeds protocol frame limit")
	}
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := c.stdin.Write(append(raw, '\n'))
		writeDone <- writeErr
	}()
	timer := time.NewTimer(c.responseTimeout)
	defer timer.Stop()
	select {
	case err = <-writeDone:
		if err != nil {
			return got, fmt.Errorf("write protocol request: %w", err)
		}
	case <-timer.C:
		_ = c.terminate()
		return got, errors.New("operator child request write timed out")
	}
	lineDone := make(chan struct {
		line []byte
		err  error
	}, 1)
	go func() {
		line, readErr := readBoundedFrame(c.stdout, maxResponseFrame)
		lineDone <- struct {
			line []byte
			err  error
		}{line: line, err: readErr}
	}()
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(c.responseTimeout)
	select {
	case result := <-lineDone:
		if result.err != nil {
			_ = c.terminate()
			return got, fmt.Errorf("read protocol frame: %w", result.err)
		}
		return decodeResponse(bytes.TrimSpace(result.line), req.RequestID, expectedWorkspace, expectedProject, req.TaskID)
	case <-timer.C:
		_ = c.terminate()
		return got, errors.New("operator child response timed out")
	}
}

func readBoundedFrame(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	frame := make([]byte, 0, 4096)
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > maxBytes+1 {
			return nil, errors.New("response exceeds protocol frame limit")
		}
		frame = append(frame, part...)
		if err == nil {
			return frame[:len(frame)-1], nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil, errors.New("response frame ended before newline")
		}
		return nil, err
	}
}

func decodeResponse(raw []byte, expectedID, expectedWorkspace, expectedProject, expectedTask string) (response, error) {
	var got response
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		return got, fmt.Errorf("decode response envelope: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return got, errors.New("response contains trailing JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return got, errors.New("response must be one JSON object")
	}
	allowed := map[string]bool{"schema_version": true, "request_id": true, "ok": true, "scope": true, "observed_at": true, "source": true, "error": true, "data": true, "page": true}
	for key := range fields {
		if !allowed[key] {
			return got, fmt.Errorf("unknown response field %q", key)
		}
	}
	for _, key := range []string{"schema_version", "request_id", "ok"} {
		if _, ok := fields[key]; !ok {
			return got, fmt.Errorf("response is missing required field %q", key)
		}
	}
	if !isJSONBool(fields["ok"]) {
		return got, errors.New("response ok must be boolean")
	}
	if got.SchemaVersion != protocolVersion {
		return got, fmt.Errorf("response schema version %d is unsupported", got.SchemaVersion)
	}
	if expectedID == "" || got.RequestID == "" || got.RequestID != expectedID {
		return got, fmt.Errorf("response request_id %q does not match %q", got.RequestID, expectedID)
	}
	if got.OK {
		for _, key := range []string{"scope", "observed_at", "source", "data"} {
			if _, ok := fields[key]; !ok {
				return got, fmt.Errorf("success response is missing required field %q", key)
			}
		}
		for _, key := range []string{"error"} {
			if _, ok := fields[key]; ok {
				return got, fmt.Errorf("success response contains failure field %q", key)
			}
		}
		if _, ok := fields["page"]; ok && bytes.Equal(bytes.TrimSpace(fields["page"]), []byte("null")) {
			return got, errors.New("success page must be an object when present")
		}
		var scopeFields, sourceFields map[string]json.RawMessage
		_ = json.Unmarshal(fields["scope"], &scopeFields)
		_ = json.Unmarshal(fields["source"], &sourceFields)
		if _, ok := scopeFields["workspace_id"]; !ok {
			return got, errors.New("success scope is missing workspace_id")
		}
		if _, ok := scopeFields["project_id"]; !ok {
			return got, errors.New("success scope is missing project_id")
		}
		if rawTask, ok := scopeFields["task_id"]; ok && !isJSONString(rawTask) {
			return got, errors.New("success scope task_id must be a string")
		}
		for _, key := range []string{"producer", "schema", "observed_at"} {
			if _, ok := sourceFields[key]; !ok {
				return got, fmt.Errorf("success source is missing %q", key)
			}
		}
		if got.Scope.WorkspaceID == "" || got.Scope.ProjectID == "" || got.Scope.WorkspaceID != expectedWorkspace || got.Scope.ProjectID != expectedProject || got.Scope.TaskID != expectedTask || got.Source.Producer == "" || got.Source.Schema == "" || len(bytes.TrimSpace(got.Data)) == 0 || bytes.TrimSpace(got.Data)[0] != '{' {
			return got, errors.New("success response has invalid scope or shape")
		}
		if !validTime(got.ObservedAt) || !validTime(got.Source.ObservedAt) {
			return got, errors.New("success response has invalid observation timestamp")
		}
		if got.Page != nil {
			var pageFields map[string]json.RawMessage
			_ = json.Unmarshal(fields["page"], &pageFields)
			for _, key := range []string{"limit", "truncated", "partial"} {
				if _, ok := pageFields[key]; !ok {
					return got, fmt.Errorf("page metadata is missing %q", key)
				}
			}
			if !isJSONBool(pageFields["truncated"]) || !isJSONBool(pageFields["partial"]) {
				return got, errors.New("page truncation flags must be booleans")
			}
			if rawCursor, ok := pageFields["next_cursor"]; ok && !isJSONString(rawCursor) {
				return got, errors.New("page next_cursor must be a string")
			}
			if got.Page.Limit <= 0 || got.Page.Limit > 512 || len(got.Page.NextCursor) > 2048 {
				return got, errors.New("success response has invalid page metadata")
			}
		}
		return got, nil
	}
	if _, ok := fields["error"]; !ok {
		return got, errors.New("failure response is missing error object")
	}
	var errorFields map[string]json.RawMessage
	_ = json.Unmarshal(fields["error"], &errorFields)
	for _, key := range []string{"code", "message", "retryable"} {
		if _, ok := errorFields[key]; !ok {
			return got, fmt.Errorf("failure error object is missing %q", key)
		}
	}
	if !isJSONBool(errorFields["retryable"]) {
		return got, errors.New("failure retryable must be boolean")
	}
	for _, key := range []string{"scope", "observed_at", "source", "data", "page"} {
		if _, ok := fields[key]; ok {
			return got, fmt.Errorf("failure response contains success field %q", key)
		}
	}
	if got.Error == nil || !validFailureCode(got.Error.Code) || got.Error.Message == "" || len(got.Error.Message) > 512 {
		return got, errors.New("failure response has invalid error shape")
	}
	return got, nil
}

func isJSONBool(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false"))
}

func isJSONString(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil && len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '"'
}

func validTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validFailureCode(code string) bool {
	switch code {
	case "unsupported_version", "invalid_request", "unknown_operation", "unknown_field", "denied", "not_found", "stale_source", "limit_exceeded", "unsupported_pagination", "storage_unavailable", "internal":
		return true
	default:
		return false
	}
}

func denied(got response, want string) error {
	if got.OK || got.Error == nil || got.Error.Code != want {
		return fmt.Errorf("response = ok:%v error:%+v, want failure code %q", got.OK, got.Error, want)
	}
	return nil
}
