package application

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	operatorv1 "github.com/mlnomadpy/dacli/contracts/operator/v1"
	"github.com/mlnomadpy/dacli/internal/operatorread"
)

func transportServer() operatorread.Server {
	return operatorread.Server{
		WorkspaceID: "ws-local",
		Authorize: func(context.Context, operatorv1.Request) (operatorv1.Scope, error) {
			return operatorv1.Scope{WorkspaceID: "ws-local", ProjectID: "operator"}, nil
		},
		Project: func(context.Context, operatorv1.Request) (operatorread.Projection, error) {
			return operatorread.Projection{Data: map[string]any{"tasks": []int{1}}, Source: operatorv1.Source{Producer: "dashboard.buildTasks", Schema: "operator-tasks/v1"}}, nil
		},
	}
}

func TestServeOperatorTransportFramesAndEOFShutdown(t *testing.T) {
	request, _ := json.Marshal(operatorv1.Request{SchemaVersion: 1, RequestID: "one", Operation: "tasks.list", WorkspaceID: "ws-local", ProjectID: "operator"})
	input := append(append(append([]byte(nil), request...), '\n'), request...)
	input = append(input, '\n')
	var output bytes.Buffer
	if err := ServeOperatorTransport(context.Background(), bytes.NewReader(input), &output, transportServer()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d output frames: %q", len(lines), output.String())
	}
	for _, line := range lines {
		if _, err := operatorv1.DecodeSuccess([]byte(line), operatorread.MaxResponseBytes); err != nil {
			t.Fatal(err)
		}
	}
	var empty bytes.Buffer
	if err := ServeOperatorTransport(context.Background(), strings.NewReader(""), &empty, transportServer()); err != nil || empty.Len() != 0 {
		t.Fatalf("EOF shutdown wrote output: %q %v", empty.String(), err)
	}
}

func TestServeOperatorTransportBoundsInputAndExitsAfterDeadline(t *testing.T) {
	var oversized bytes.Buffer
	oversized.WriteString(strings.Repeat("x", operatorread.MaxRequestBytes+1))
	oversized.WriteByte('\n')
	var output bytes.Buffer
	if err := ServeOperatorTransport(context.Background(), &oversized, &output, transportServer()); err != nil {
		t.Fatal(err)
	}
	failure, err := operatorv1.DecodeFailure(bytes.TrimSpace(output.Bytes()), operatorread.MaxResponseBytes)
	if err != nil || failure.Error.Code != operatorv1.CodeLimitExceeded || strings.Contains(output.String(), strings.Repeat("x", 20)) {
		t.Fatalf("oversized frame response = %s %v", output.String(), err)
	}

	server := transportServer()
	server.Deadline = 5 * time.Millisecond
	server.Project = func(context.Context, operatorv1.Request) (operatorread.Projection, error) {
		time.Sleep(100 * time.Millisecond)
		return operatorread.Projection{}, nil
	}
	request, _ := json.Marshal(operatorv1.Request{SchemaVersion: 1, RequestID: "deadline", Operation: "tasks.list", WorkspaceID: "ws-local", ProjectID: "operator"})
	output.Reset()
	err = ServeOperatorTransport(context.Background(), bytes.NewReader(append(request, '\n')), &output, server)
	failure, decodeErr := operatorv1.DecodeFailure(bytes.TrimSpace(output.Bytes()), operatorread.MaxResponseBytes)
	if err == nil || decodeErr != nil || failure.RequestID != "deadline" || failure.Error.Message != "operator read deadline exceeded" {
		t.Fatalf("deadline did not end the owned process with a redacted frame: %s %v %v", output.String(), err, decodeErr)
	}
}

func TestServeOperatorTransportCancellationAndBlockedPipeDeadlines(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeOperatorTransport(ctx, pipeReader, &bytes.Buffer{}, transportServer()) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled idle stdin returned success")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("context cancellation did not interrupt blocked stdin")
	}
	_ = pipeReader.Close()
	_ = pipeWriter.Close()

	outputReader, outputWriter := io.Pipe()
	defer func() { _ = outputReader.Close() }()
	server := transportServer()
	server.Deadline = 25 * time.Millisecond
	request, _ := json.Marshal(operatorv1.Request{SchemaVersion: 1, RequestID: "blocked-output", Operation: "tasks.list", WorkspaceID: "ws-local", ProjectID: "operator"})
	done = make(chan error, 1)
	go func() {
		done <- ServeOperatorTransport(context.Background(), bytes.NewReader(append(request, '\n')), outputWriter, server)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "write deadline") {
			t.Fatalf("blocked stdout result = %v, want write deadline", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("response write did not honor its deadline")
	}
	_ = outputWriter.Close()
}
