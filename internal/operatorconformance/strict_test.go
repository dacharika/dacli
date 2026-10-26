package operatorconformance

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConsumerRejectsMalformedEnvelopeShapes(t *testing.T) {
	valid := `{"schema_version":1,"request_id":"r","ok":true,"scope":{"workspace_id":"ws","project_id":"p"},"observed_at":"2026-10-25T16:09:02.123456789Z","source":{"producer":"producer","schema":"schema/v1","observed_at":"2026-10-25T16:09:02Z"},"data":{"producer_owned":{"unknown":true}}}`
	if _, err := decodeResponse([]byte(valid), "r", "ws", "p", ""); err != nil {
		t.Fatalf("opaque producer payload should remain accepted: %v", err)
	}
	tests := []struct {
		name string
		raw  string
	}{
		{"boolean-schema-version", strings.Replace(valid, `"schema_version":1`, `"schema_version":true`, 1)},
		{"missing-observed-at", strings.Replace(valid, `,"observed_at":"2026-10-25T16:09:02.123456789Z"`, "", 1)},
		{"invalid-observed-at", strings.Replace(valid, "2026-10-25T16:09:02.123456789Z", "yesterday", 1)},
		{"invalid-source-time", strings.Replace(valid, "2026-10-25T16:09:02Z", "", 1)},
		{"unknown-envelope-field", strings.Replace(valid, `,"data":`, `,"surprise":true,"data":`, 1)},
		{"failure-field-on-success", strings.Replace(valid, `,"data":`, `,"error":{"code":"denied","message":"no","retryable":false},"data":`, 1)},
		{"array-data", strings.Replace(valid, `"data":{"producer_owned":{"unknown":true}}`, `"data":[]`, 1)},
		{"missing-failure-retryable", `{"schema_version":1,"request_id":"r","ok":false,"error":{"code":"denied","message":"no"}}`},
		{"success-field-on-failure", `{"schema_version":1,"request_id":"r","ok":false,"error":{"code":"denied","message":"no","retryable":false},"scope":{"workspace_id":"ws","project_id":"p"}}`},
		{"page-missing-partial", strings.Replace(valid, `"data":{"producer_owned":{"unknown":true}}`, `"data":{},"page":{"limit":1,"truncated":false}`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeResponse([]byte(test.raw), "r", "ws", "p", ""); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}

func TestBoundedFrameReaderRejectsOversizeWithoutNewline(t *testing.T) {
	input := strings.NewReader(strings.Repeat("x", maxResponseFrame+2))
	if _, err := readBoundedFrame(bufio.NewReaderSize(input, 4096), maxResponseFrame); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize response frame result = %v", err)
	}
}

func TestOwnedChildResponseTimeoutAndShutdownAreBounded(t *testing.T) {
	tests := []struct {
		name   string
		script string
	}{
		{"silent", "#!/bin/sh\nexec sleep 30\n"},
		{"no-newline", "#!/bin/sh\nprintf partial\nexec sleep 30\n"},
		{"oversize", "#!/bin/sh\nhead -c 1048578 /dev/zero\nexec sleep 30\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := fakeChild(t, test.script)
			child, err := startChild(path, "opaque-connection")
			if err != nil {
				t.Fatal(err)
			}
			child.responseTimeout = 40 * time.Millisecond
			started := time.Now()
			_, err = child.exchange(request{SchemaVersion: 1, RequestID: "fake", Operation: "tasks.list", WorkspaceID: "ws", ProjectID: "p"}, "ws", "p")
			elapsed := time.Since(started)
			if err == nil || elapsed > time.Second {
				t.Fatalf("unbounded child response: err=%v elapsed=%s", err, elapsed)
			}
			child.shutdownTimeout = 40 * time.Millisecond
			child.killWaitTimeout = 100 * time.Millisecond
			closeStarted := time.Now()
			_ = child.close()
			if time.Since(closeStarted) > time.Second {
				t.Fatal("owned child shutdown exceeded its deadline")
			}
		})
	}
}

func TestOwnedChildClosesItsStdoutDescriptor(t *testing.T) {
	child, err := startChild(fakeChild(t, "#!/bin/sh\nexec sleep 30\n"), "opaque-connection")
	if err != nil {
		t.Fatal(err)
	}
	child.shutdownTimeout = 40 * time.Millisecond
	child.killWaitTimeout = 100 * time.Millisecond
	_ = child.close()
	if _, err := child.stdout.ReadByte(); err == nil {
		t.Fatal("owned stdout descriptor remained open after shutdown")
	}
}

func fakeChild(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-child")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
