//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package nativegovernance

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

// A separate harness keeps the completion result available to the test. The
// ordinary asyncWire cleanup consumes it, which would hide Serve's shutdown
// error from this regression.
func startShutdownWire(t *testing.T, f verificationFixture) (asyncWire, func() error) {
	t.Helper()
	server, err := native.OpenWithRunner(f.connection, f.runnerProfile)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	w := asyncWire{inW, make(chan map[string]json.RawMessage, 32), make(chan error, 1)}
	go func() {
		err := server.Serve(inR, outW)
		_ = outW.Close()
		w.done <- err
	}()
	go func() {
		scan := bufio.NewScanner(outR)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			var response map[string]json.RawMessage
			if json.Unmarshal(scan.Bytes(), &response) == nil {
				w.responses <- response
			}
		}
		_ = outR.Close()
	}()

	var once sync.Once
	var serveErr error
	stop := func() error {
		once.Do(func() {
			_ = inW.Close()
			select {
			case serveErr = <-w.done:
			case <-time.After(10 * time.Second):
				serveErr = errors.New("protocol shutdown did not finish")
			}
			_ = outR.Close()
		})
		return serveErr
	}
	t.Cleanup(func() {
		if err := stop(); err != nil && !errors.Is(err, sessions.ErrReconcile) {
			t.Error(err)
		}
	})
	return w, stop
}

func sendShutdownInitialize(t *testing.T, w asyncWire) {
	t.Helper()
	w.send(t, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{}})
	w.receive(t, 1)
}

func sendShutdownCall(t *testing.T, w asyncWire, id int, method string, request map[string]any) applicationStatus {
	t.Helper()
	w.send(t, id, "tools/call", map[string]any{"name": method, "arguments": map[string]any{"schema_version": 1, "request": request}})
	return applicationStatusFromWire(t, w.receive(t, id))
}

func applicationStatusFromWire(t *testing.T, response map[string]json.RawMessage) applicationStatus {
	t.Helper()
	var result struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Data struct {
				OperationKey string `json:"operation_key"`
				State        string `json:"state"`
				Evidence     any    `json:"evidence"`
				Interruption any    `json:"interruption"`
			} `json:"data"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(response["result"], &result); err != nil || result.IsError {
		t.Fatalf("request refused: %s (%v)", response["result"], err)
	}
	d := result.Structured.Data
	return applicationStatus{operationKey: d.OperationKey, state: d.State, evidence: d.Evidence, interruption: d.Interruption}
}

type applicationStatus struct {
	operationKey string
	state        string
	evidence     any
	interruption any
}

func TestAsyncNativeProtocolShutdownReportsInterruptionCommitFailure(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	marker := filepath.Join(dir, "started")
	f := setupVerification(t, "printf x >> '"+count+"'; printf started > '"+marker+"'; sleep 30", true)
	w, stop := startShutdownWire(t, f)
	sendShutdownInitialize(t, w)
	request := map[string]any{
		"operation_key":  f.request.OperationKey,
		"attempt_id":     f.request.AttemptID,
		"lease_id":       f.request.LeaseID,
		"revision_id":    f.request.RevisionID,
		"check_id":       f.request.CheckID,
		"expected_fence": f.request.ExpectedFence,
	}
	reserved := sendShutdownCall(t, w, 2, "request_verification", request)
	if reserved.operationKey != f.request.OperationKey || reserved.state != "reserved" || reserved.evidence != nil || reserved.interruption != nil {
		t.Fatalf("verification did not reserve the exact pending operation: %+v", reserved)
	}
	awaitFile(t, marker)

	// ReadPrivate rejects any group/world permission bits. This is a fixture
	// storage failure after launch and before shutdown cancellation can commit
	// an interruption; it does not alter live authority or mint a receipt.
	if err := os.Chmod(f.worker.Authority, 0644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil && !errors.Is(err, sessions.ErrReconcile) {
			t.Errorf("stop protocol after interrupted test: %v", err)
		}
		if err := os.Chmod(f.worker.Authority, 0600); err != nil {
			t.Errorf("restore fixture authority permissions: %v", err)
		}
	}()

	serveErr := stop()
	if !errors.Is(serveErr, sessions.ErrReconcile) {
		t.Fatalf("shutdown must report that cancellation could not be durably reconciled; got %v", serveErr)
	}
	if err := os.Chmod(f.worker.Authority, 0600); err != nil {
		t.Fatal(err)
	}

	fresh, freshStop := startShutdownWire(t, f)
	sendShutdownInitialize(t, fresh)
	for i, method := range []string{"get_verification_status", "cancel_verification", "request_verification"} {
		got := sendShutdownCall(t, fresh, 10+i, method, request)
		if got.operationKey != f.request.OperationKey || got.state != "reconciliation_required" || got.evidence != nil || got.interruption != nil {
			t.Fatalf("%s invented a terminal result for uncertain execution: %+v", method, got)
		}
	}
	if err := freshStop(); err != nil {
		t.Fatalf("fresh server shutdown: %v", err)
	}
	b, err := os.ReadFile(count)
	if err != nil || string(b) != "x" {
		t.Fatalf("uncertain operation reexecuted or count unavailable: %q, %v", b, err)
	}
}
