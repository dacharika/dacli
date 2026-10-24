//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package nativegovernance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func asyncRequest(f verificationFixture) application.NativeVerificationRequest {
	r := f.request
	return application.NativeVerificationRequest{OperationKey: r.OperationKey, AttemptID: r.AttemptID, LeaseID: r.LeaseID, RevisionID: r.RevisionID, CheckID: r.CheckID, ExpectedFence: r.ExpectedFence}
}

// Cleaned cancellation is a durable non-evidence receipt, bound to the original
// runner and request. A restart must never turn it into success or rerun it.
func requireAsyncCleanedInterruption(t *testing.T, f verificationFixture, status application.VerificationStatus, err error) {
	t.Helper()
	if err != nil || status.OperationKey != f.request.OperationKey || status.State != "interrupted" || status.Evidence != nil || status.Interruption == nil {
		t.Fatal("cleaned cancellation lost its durable non-evidence receipt", status, err)
	}
	receipt := status.Interruption
	want := sessions.VerificationRequest{OperationKey: f.request.OperationKey, WorkerSession: f.worker.SessionID, AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, RevisionID: f.request.RevisionID, CheckID: f.request.CheckID, GrantID: f.runner.Checks[0].GrantID, ExpectedFence: f.request.ExpectedFence}
	if receipt.Reason != "cancelled_owned_group_cleanup" || receipt.RunnerSession != f.runner.RunnerSessionID || receipt.Request != want || receipt.ResultDigest == "" || receipt.Assurance != "cooperative-native-executor" {
		t.Fatal("interruption does not identify the exact cleaned runner operation", receipt)
	}
}

func asyncJobs(t *testing.T, f verificationFixture) *application.NativeVerificationJobs {
	t.Helper()
	p, e := application.OpenNativeVerificationProfile(f.connection, f.runnerProfile)
	if e != nil {
		t.Fatal(e)
	}
	m, e := application.NewNativeVerificationJobs(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := m.Close(); e != nil {
			t.Error(e)
		}
	})
	return m
}
func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(path); e == nil && len(b) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("check did not start")
}
func completed(t *testing.T, m *application.NativeVerificationJobs, r application.NativeVerificationRequest) application.VerificationStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, e := m.Status(r)
		if e != nil {
			t.Fatal(e)
		}
		if s.State == "completed" {
			return s
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("no committed completion")
	return application.VerificationStatus{}
}
func TestAsyncVerificationBoundsBeforeReservationAndReplays(t *testing.T) {
	d := t.TempDir()
	marker := filepath.Join(d, "count")
	gate := filepath.Join(d, "gate")
	f := setupVerification(t, "printf x >> '"+marker+"'; while [ ! -e '"+gate+"' ]; do sleep .05; done; printf completed", true)
	m := asyncJobs(t, f)
	one := asyncRequest(f)
	two := one
	two.OperationKey = "second"
	third := one
	third.OperationKey = "overflow"
	for _, r := range []application.NativeVerificationRequest{one, two} {
		s, e := m.Request(r)
		if e != nil || s.State != "reserved" || s.Evidence != nil {
			t.Fatal(s, e)
		}
	}
	awaitFile(t, marker)
	if _, e := m.Request(third); e == nil {
		t.Fatal("overflow was admitted")
	}
	s, e := m.Status(third)
	if e != nil || s.State != "absent" {
		t.Fatal("overflow reserved durable operation", s, e)
	}
	s, e = m.Request(one)
	if e != nil || s.State != "reserved" {
		t.Fatal("exact duplicate not same job", s, e)
	}
	for _, field := range []string{"lease", "revision", "fence", "check"} {
		bad := one
		switch field {
		case "lease":
			bad.LeaseID = "different"
		case "revision":
			bad.RevisionID = "different"
		case "fence":
			bad.ExpectedFence++
		case "check":
			bad.CheckID = "different"
		}
		for _, call := range []func(application.NativeVerificationRequest) (application.VerificationStatus, error){m.Status, m.Cancel, m.Request} {
			if _, e = call(bad); e == nil {
				t.Fatal("changed identity accepted", field)
			}
		}
	}
	if e = os.WriteFile(gate, []byte("release"), 0600); e != nil {
		t.Fatal(e)
	}
	first := completed(t, m, one)
	completed(t, m, two)
	if first.Evidence == nil || first.Evidence.ExitCode != 0 || first.Evidence.Provenance != "trusted-local-runner" {
		t.Fatal(first)
	}
	fresh := asyncJobs(t, f)
	s, e = fresh.Request(one)
	if e != nil || s.State != "completed" || s.Evidence.ID != first.Evidence.ID {
		t.Fatal("restart lost committed receipt", s, e)
	}
	b, e := os.ReadFile(marker)
	if e != nil || string(b) != "xx" {
		t.Fatal("duplicate reexecuted", string(b), e)
	}
	for _, token := range []string{f.workerToken, f.owner, f.runner.Checks[0].Command, "completed"} {
		b, _ := json.Marshal(s)
		if strings.Contains(string(b), token) && token != "completed" {
			t.Fatal("private material exposed")
		}
	}
}
func TestAsyncVerificationCancelShutdownAndRestartPending(t *testing.T) {
	for _, action := range []string{"cancel", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			d := t.TempDir()
			marker := filepath.Join(d, "started")
			f := setupVerification(t, "printf started > '"+marker+"'; sleep 30", true)
			m := asyncJobs(t, f)
			r := asyncRequest(f)
			if _, e := m.Request(r); e != nil {
				t.Fatal(e)
			}
			awaitFile(t, marker)
			start := time.Now()
			if action == "cancel" {
				s, e := m.Cancel(r)
				if e != nil || s.State != "cancellation_requested" {
					t.Fatal(s, e)
				}
			}
			if e := m.Close(); e != nil {
				t.Fatal(e)
			}
			if time.Since(start) > 7*time.Second {
				t.Fatal("shutdown unbounded")
			}
			fresh := asyncJobs(t, f)
			for _, call := range []func(application.NativeVerificationRequest) (application.VerificationStatus, error){fresh.Status, fresh.Cancel, fresh.Request} {
				s, e := call(r)
				requireAsyncCleanedInterruption(t, f, s, e)
			}
			b, _ := os.ReadFile(marker)
			if string(b) != "started" {
				t.Fatal("pending reran")
			}
		})
	}
}

type asyncWire struct {
	in        *io.PipeWriter
	responses chan map[string]json.RawMessage
	done      chan error
}

func startAsyncWire(t *testing.T, f verificationFixture, allowedCloseErrors ...error) asyncWire {
	t.Helper()
	s, e := native.OpenWithRunner(f.connection, f.runnerProfile)
	if e != nil {
		t.Fatal(e)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	w := asyncWire{inW, make(chan map[string]json.RawMessage, 32), make(chan error, 1)}
	go func() { e := s.Serve(inR, outW); _ = outW.Close(); w.done <- e }()
	go func() {
		scan := bufio.NewScanner(outR)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			var r map[string]json.RawMessage
			if json.Unmarshal(scan.Bytes(), &r) == nil {
				w.responses <- r
			}
		}
		_ = outR.Close()
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case e := <-w.done:
			if e != nil {
				allowed := false
				for _, expected := range allowedCloseErrors {
					allowed = allowed || errors.Is(e, expected)
				}
				if !allowed {
					t.Error(e)
				}
			}
		case <-time.After(8 * time.Second):
			t.Error("protocol shutdown hung")
		}
	})
	return w
}
func (w asyncWire) send(t *testing.T, id int, method string, params any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, e := w.in.Write(append(b, '\n')); e != nil {
		t.Fatal(e)
	}
}
func (w asyncWire) receive(t *testing.T, id int) map[string]json.RawMessage {
	t.Helper()
	select {
	case r := <-w.responses:
		var got int
		_ = json.Unmarshal(r["id"], &got)
		if got != id {
			t.Fatal("unexpected response id", got, id)
		}
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("protocol blocked behind check")
		return nil
	}
}
func wireStatus(t *testing.T, r map[string]json.RawMessage) application.VerificationStatus {
	t.Helper()
	var result struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Data application.VerificationStatus `json:"data"`
		} `json:"structuredContent"`
	}
	if e := json.Unmarshal(r["result"], &result); e != nil || result.IsError {
		t.Fatal("request refused", string(r["result"]), e)
	}
	return result.Structured.Data
}
func TestAsyncNativeProtocolResponsiveAndStrict(t *testing.T) {
	d := t.TempDir()
	marker := filepath.Join(d, "started")
	f := setupVerification(t, "printf started > '"+marker+"'; sleep 30", true)
	// Revocation below can win the cancellation-receipt race. Only this test
	// permits the honest reconciliation error; ordinary cleanup stays strict.
	// The deterministic persistence-failure regression requires ErrReconcile.
	w := startAsyncWire(t, f, sessions.ErrReconcile)
	w.send(t, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{}})
	w.receive(t, 1)
	w.send(t, 2, "tools/list", map[string]any{})
	r := w.receive(t, 2)
	var listing struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r["result"], &listing); err != nil {
		t.Fatalf("decode explicit runner catalog: %v", err)
	}
	wantTools := map[string]struct{}{
		"get_policy_status": {}, "identity": {}, "workspace_status": {}, "task_list": {},
		"task_get": {}, "task_brief": {}, "ready_frontier": {}, "register_attempt": {},
		"acquire_lease": {}, "renew_lease": {}, "release_lease": {}, "publish_revision": {},
		"submit_evidence": {}, "request_verification": {}, "get_verification_status": {}, "cancel_verification": {},
	}
	gotTools := make([]string, 0, len(listing.Tools))
	seenTools := make(map[string]struct{}, len(listing.Tools))
	for _, tool := range listing.Tools {
		if _, exists := seenTools[tool.Name]; exists {
			t.Fatalf("duplicate tool in explicit runner catalog: %q", tool.Name)
		}
		seenTools[tool.Name] = struct{}{}
		gotTools = append(gotTools, tool.Name)
	}
	sort.Strings(gotTools)
	missingTools := make([]string, 0)
	for name := range wantTools {
		if _, exists := seenTools[name]; !exists {
			missingTools = append(missingTools, name)
		}
	}
	sort.Strings(missingTools)
	unexpectedTools := make([]string, 0)
	for name := range seenTools {
		if _, exists := wantTools[name]; !exists {
			unexpectedTools = append(unexpectedTools, name)
		}
	}
	sort.Strings(unexpectedTools)
	if len(missingTools) != 0 || len(unexpectedTools) != 0 {
		t.Fatalf("explicit runner catalog mismatch: got=%v missing=%v unexpected=%v", gotTools, missingTools, unexpectedTools)
	}
	request := map[string]any{"operation_key": f.request.OperationKey, "attempt_id": f.request.AttemptID, "lease_id": f.request.LeaseID, "revision_id": f.request.RevisionID, "check_id": f.request.CheckID, "expected_fence": f.request.ExpectedFence}
	params := func(name string) any {
		return map[string]any{"name": name, "arguments": map[string]any{"schema_version": 1, "request": request}}
	}
	w.send(t, 3, "tools/call", params("request_verification"))
	s := wireStatus(t, w.receive(t, 3))
	if s.State != "reserved" {
		t.Fatal(s)
	}
	awaitFile(t, marker)
	w.send(t, 4, "tools/call", map[string]any{"name": "identity", "arguments": map[string]any{"schema_version": 1}})
	w.receive(t, 4)
	w.send(t, 5, "tools/call", params("get_verification_status"))
	if s = wireStatus(t, w.receive(t, 5)); s.State != "reserved" {
		t.Fatal(s)
	}
	for i, key := range []string{"command", "credential", "worker_session", "runner_config", "grant_id", "cwd"} {
		request[key] = "caller-override"
		w.send(t, 10+i, "tools/call", params("request_verification"))
		r = w.receive(t, 10+i)
		if !strings.Contains(string(r["result"]), `"isError":true`) {
			t.Fatal("override accepted", key)
		}
		delete(request, key)
	}
	w.send(t, 20, "tools/call", params("cancel_verification"))
	if s = wireStatus(t, w.receive(t, 20)); s.State != "cancellation_requested" {
		t.Fatal(s)
	}
	if e := f.authority.Revoke(f.owner, f.worker.SessionID); e != nil {
		t.Fatal(e)
	}
	w.send(t, 21, "tools/call", params("get_verification_status"))
	r = w.receive(t, 21)
	if !strings.Contains(string(r["error"]), "-32003") {
		t.Fatal("revoked caller disclosed status", r)
	}
}
func TestAsyncVerificationFreshPrivateProfileRequired(t *testing.T) {
	for _, kind := range []string{"profile", "equivalent-deadline", "credential", "grant"} {
		t.Run(kind, func(t *testing.T) {
			f := setupVerification(t, "", true)
			m := asyncJobs(t, f)
			switch kind {
			case "profile":
				f.runner.Checks[0].Command = "printf substituted"
				verificationJSON(t, f.runnerProfile, f.runner)
			case "equivalent-deadline":
				f.runner.Checks[0].Deadline = "4000ms"
				verificationJSON(t, f.runnerProfile, f.runner)
			case "credential":
				_ = os.Chmod(f.worker.CredentialFile, 0644)
			case "grant":
				if e := f.authority.RevokeVerification(f.owner, f.runner.Checks[0].GrantID); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := m.Request(asyncRequest(f)); e == nil {
				t.Fatal("changed private authority accepted")
			}
			f.assertNotRun(t)
		})
	}
}

// Output failure and revoked notifications terminate a live transport without
// waiting for another input line, while its uncommitted run stays unknown.
type failingAsyncWriter struct {
	out  io.Writer
	fail atomic.Bool
}

func (w *failingAsyncWriter) Write(b []byte) (int, error) {
	if w.fail.Load() {
		return 0, errors.New("broken transport")
	}
	return w.out.Write(b)
}
func TestAsyncNativeTransportFailureCancelsRealChild(t *testing.T) {
	for _, kind := range []string{"writer", "revoked-notification"} {
		t.Run(kind, func(t *testing.T) {
			d := t.TempDir()
			pidFile := filepath.Join(d, "child")
			f := setupVerification(t, "sleep 30 & child=$!; printf '%s %s' $$ \"$child\" > '"+pidFile+"'; wait \"$child\"", true)
			server, e := native.OpenWithRunner(f.connection, f.runnerProfile)
			if e != nil {
				t.Fatal(e)
			}
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			writer := &failingAsyncWriter{out: outW}
			w := asyncWire{inW, make(chan map[string]json.RawMessage, 16), make(chan error, 1)}
			go func() { e := server.Serve(inR, writer); _ = outW.Close(); w.done <- e }()
			go func() {
				scan := bufio.NewScanner(outR)
				for scan.Scan() {
					var r map[string]json.RawMessage
					_ = json.Unmarshal(scan.Bytes(), &r)
					w.responses <- r
				}
				_ = outR.Close()
			}()
			defer func() { _ = inW.Close(); _ = outR.Close() }()
			w.send(t, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{}})
			w.receive(t, 1)
			request := map[string]any{"operation_key": f.request.OperationKey, "attempt_id": f.request.AttemptID, "lease_id": f.request.LeaseID, "revision_id": f.request.RevisionID, "check_id": f.request.CheckID, "expected_fence": f.request.ExpectedFence}
			w.send(t, 2, "tools/call", map[string]any{"name": "request_verification", "arguments": map[string]any{"schema_version": 1, "request": request}})
			wireStatus(t, w.receive(t, 2))
			awaitFile(t, pidFile)
			raw, e := os.ReadFile(pidFile)
			if e != nil {
				t.Fatal(e)
			}
			pid := asyncOwnedChild(t, raw)
			if kind == "writer" {
				writer.fail.Store(true)
				w.send(t, 3, "tools/call", map[string]any{"name": "identity", "arguments": map[string]any{"schema_version": 1}})
			} else {
				if e = f.authority.Revoke(f.owner, f.worker.SessionID); e != nil {
					t.Fatal(e)
				}
				b := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n")
				if _, e = inW.Write(b); e != nil {
					t.Fatal(e)
				}
			}
			select {
			case e := <-w.done:
				if e == nil {
					t.Fatal("transport failure returned success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("transport failure did not shut down running check")
			}
			deadline := time.Now().Add(time.Second)
			for asyncChildRunning(t, pid) {
				if time.Now().After(deadline) {
					t.Fatal("verification child survives transport shutdown", pid)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if kind == "writer" {
				fresh := asyncJobs(t, f)
				s, e := fresh.Status(asyncRequest(f))
				requireAsyncCleanedInterruption(t, f, s, e)
			}
		})
	}
}

func TestAsyncNativeContextCancelUnblocksStdioBackpressure(t *testing.T) {
	testAsyncNativeBackpressure(t, false)
}
func TestAsyncNativeEOFUnblocksStdioBackpressure(t *testing.T) { testAsyncNativeBackpressure(t, true) }
func testAsyncNativeBackpressure(t *testing.T, eof bool) {
	d := t.TempDir()
	pidFile := filepath.Join(d, "child")
	f := setupVerification(t, "sleep 30 & child=$!; printf '%s %s' $$ \"$child\" > '"+pidFile+"'; wait \"$child\"", true)
	server, e := native.OpenWithRunner(f.connection, f.runnerProfile)
	if e != nil {
		t.Fatal(e)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer func() { _ = inW.Close(); _ = outR.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.ServeContext(ctx, inR, outW) }()
	send := func(id int, method string, params any) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, e := inW.Write(append(b, '\n')); e != nil {
			t.Fatal(e)
		}
	}
	decoder := json.NewDecoder(outR)
	receive := func() {
		t.Helper()
		var response map[string]any
		if e := decoder.Decode(&response); e != nil {
			t.Fatal(e)
		}
	}
	send(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{}})
	receive()
	request := map[string]any{"operation_key": f.request.OperationKey, "attempt_id": f.request.AttemptID, "lease_id": f.request.LeaseID, "revision_id": f.request.RevisionID, "check_id": f.request.CheckID, "expected_fence": f.request.ExpectedFence}
	send(2, "tools/call", map[string]any{"name": "request_verification", "arguments": map[string]any{"schema_version": 1, "request": request}})
	receive()
	awaitFile(t, pidFile)
	raw, _ := os.ReadFile(pidFile)
	pid := asyncOwnedChild(t, raw)
	// No reader consumes ping's response. The protocol writer now blocks.
	send(3, "ping", map[string]any{})
	time.Sleep(20 * time.Millisecond)
	if eof {
		_ = inW.Close()
	} else {
		cancel()
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not unblock owned stdio")
	}
	if asyncChildRunning(t, pid) {
		t.Fatal("child survives context cancellation", pid)
	}
	fresh := asyncJobs(t, f)
	s, e := fresh.Status(asyncRequest(f))
	requireAsyncCleanedInterruption(t, f, s, e)
}

func asyncChildRunning(t *testing.T, pid int) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid))
	cmd.WaitDelay = 250 * time.Millisecond
	out, e := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("process observer timed out")
	}
	if e != nil {
		var exit *exec.ExitError
		if errors.As(e, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(string(out)) == "" {
			return false
		}
		t.Fatal("process observer unavailable", e, string(out))
	}
	state := strings.TrimSpace(string(out))
	return state != "" && !strings.HasPrefix(state, "Z")
}
func asyncOwnedChild(t *testing.T, raw []byte) int {
	t.Helper()
	var shell, child int
	if n, e := fmt.Sscanf(string(raw), "%d %d", &shell, &child); n != 2 || e != nil || shell <= 0 || child <= 0 {
		t.Fatal("no owned real child", string(raw), e)
	}
	t.Cleanup(func() {
		if group, e := syscall.Getpgid(child); e == nil && group == shell {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
	})
	return child
}

func TestAsyncVerificationStatusAndCancelRequireOriginalWorker(t *testing.T) {
	f := setupVerification(t, "", true)
	original := asyncJobs(t, f)
	r := asyncRequest(f)
	if _, e := original.Request(r); e != nil {
		t.Fatal(e)
	}
	completed(t, original, r)
	private := t.TempDir()
	connection := filepath.Join(private, "second-worker.json")
	credential := filepath.Join(private, "second-worker.token")
	if out, e := verificationRun(t, f.w, enroll, "--config", f.config, "--task", f.task.ID, "--connection", connection, "--credential", credential, "--path", "src", "--verification"); e != nil {
		t.Fatal(e, out)
	}
	second := f
	second.connection = connection
	other := asyncJobs(t, second)
	for _, call := range []func(application.NativeVerificationRequest) (application.VerificationStatus, error){other.Status, other.Cancel, other.Request} {
		if _, e := call(r); e == nil {
			t.Fatal("another worker acquired original operation")
		}
	}
	if s, e := original.Status(r); e != nil || s.State != "completed" {
		t.Fatal("original receipt altered", s, e)
	}
}

func TestAsyncNativeProtocolResponsesDoNotWaitForAdmission(t *testing.T) {
	f := setupVerification(t, "", true)
	w := startAsyncWire(t, f)
	w.send(t, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{}})
	w.receive(t, 1)
	actualGit, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	d := t.TempDir()
	gate := filepath.Join(d, "gate")
	started := filepath.Join(d, "metadata-started")
	if e = os.WriteFile(gate, []byte("hold"), 0600); e != nil {
		t.Fatal(e)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	// Hold the bounded trusted metadata subprocess, rather than merely a check
	// whose request already returned. This measures independent wire responses.
	script := "#!/bin/sh\nif [ -e " + quote(gate) + " ]; then printf started > " + quote(started) + "; while [ -e " + quote(gate) + " ]; do sleep .02; done; fi\nexec " + quote(actualGit) + " \"$@\"\n"
	if e = os.WriteFile(filepath.Join(d, "git"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { _ = os.Remove(gate) })
	request := map[string]any{"operation_key": f.request.OperationKey, "attempt_id": f.request.AttemptID, "lease_id": f.request.LeaseID, "revision_id": f.request.RevisionID, "check_id": f.request.CheckID, "expected_fence": f.request.ExpectedFence}
	w.send(t, 2, "tools/call", map[string]any{"name": "request_verification", "arguments": map[string]any{"schema_version": 1, "request": request}})
	awaitFile(t, started)
	w.send(t, 3, "tools/call", map[string]any{"name": "identity", "arguments": map[string]any{"schema_version": 1}})
	w.receive(t, 3)
	if e = os.Remove(gate); e != nil {
		t.Fatal(e)
	}
	status := wireStatus(t, w.receive(t, 2))
	// This test measures responses during admission, not cancellation. Let the
	// admitted check reach its expected durable completion before strict cleanup
	// so returning from the test does not cancel its metadata preflight.
	deadline := time.Now().Add(5 * time.Second)
	for id := 4; status.State != "completed"; id++ {
		if time.Now().After(deadline) {
			t.Fatalf("admitted verification did not complete: %+v", status)
		}
		if status.State != "reserved" && status.State != "admitting" {
			t.Fatalf("unexpected admitted verification outcome: %+v", status)
		}
		w.send(t, id, "tools/call", map[string]any{"name": "get_verification_status", "arguments": map[string]any{"schema_version": 1, "request": request}})
		status = wireStatus(t, w.receive(t, id))
		if status.State != "completed" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if status.Evidence == nil || status.Interruption != nil {
		t.Fatalf("normal admitted check has no durable completion evidence: %+v", status)
	}
}
