package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Serve uses bounded newline-framed stdio and never logs request payloads.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	return s.ServeContext(context.Background(), in, out)
}
func (s *Server) ServeContext(ctx context.Context, in io.Reader, out io.Writer) (serveErr error) {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 1<<20)
	enc := json.NewEncoder(out)
	initialized := false
	var writeMu sync.Mutex
	var calls sync.WaitGroup
	writeErrors := make(chan error, 1)
	slots := make(chan struct{}, 16)
	encode := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		e := enc.Encode(value)
		if e != nil {
			select {
			case writeErrors <- e:
			default:
			}
		}
		return e
	}
	var pendingFrames atomic.Int32
	lines := make(chan []byte)
	readerEnded := make(chan struct{})
	readDone := make(chan error, 1)
	stopRead := make(chan struct{})
	defer close(stopRead)
	go func() {
		defer close(readerEnded)
		for scan.Scan() {
			line := append([]byte(nil), scan.Bytes()...)
			pendingFrames.Add(1)
			select {
			case lines <- line:
			case <-stopRead:
				pendingFrames.Add(-1)
				return
			}
		}
		readDone <- scan.Err()
	}()
	var cleanup sync.Once
	var cleanupErr error
	finish := func() error {
		cleanup.Do(func() {
			cleanupErr = s.Close()
			done := make(chan struct{})
			go func() { calls.Wait(); close(done) }()
			select {
			case <-done:
				select {
				case e := <-writeErrors:
					cleanupErr = e
				default:
				}
			case <-time.After(6 * time.Second):
				cleanupErr = ErrDenied
			}
		})
		return cleanupErr
	}
	defer func() {
		if e := finish(); serveErr == nil {
			serveErr = e
		}
	}()

	defer func() {
		if closer, ok := in.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	// Owned stdio can block on backpressure. Cancellation must reach jobs and
	// close those descriptors independently of the reader/encoder goroutine.
	// A non-closable caller writer must itself support cancellation/unblocking.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			if closer, ok := in.(io.Closer); ok {
				_ = closer.Close()
			}
			if closer, ok := out.(io.Closer); ok {
				_ = closer.Close()
			}
			_ = s.Close()
		case <-watchDone:
		}
	}()
	go func() {
		select {
		case <-readerEnded:
			// EOF must reach checks even if the main loop cannot consume readDone.
			// Give already queued responses a short drain interval before unblocking
			// owned stdout. Requests still outstanding then have unknown outcomes.
			go func() { _ = s.Close() }()
			timer := time.NewTimer(250 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-watchDone:
				return
			case <-timer.C:
			}
			if pendingFrames.Load() > 0 {
				if closer, ok := out.(io.Closer); ok {
					_ = closer.Close()
				}
			}
		case <-watchDone:
		}
	}()
	for {
		var line []byte
		select {
		case e := <-writeErrors:
			_ = finish()
			return e
		case <-ctx.Done():
			return finish()
		case e := <-readDone:
			if e != nil {
				_ = finish()
				return ErrDenied
			}
			return finish()
		case line = <-lines:
		}

		var q request
		if Decode(line, &q) != nil || q.JSONRPC != "2.0" || q.Method == "" || !validID(q.ID) {
			if e := encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32600, "message": "invalid request"}}); e != nil {
				return e
			}
			pendingFrames.Add(-1)
			continue
		}
		_, _, _, _, authErr := s.authenticate()
		if len(q.ID) == 0 {
			pendingFrames.Add(-1)
			if authErr != nil {
				return ErrDenied
			}
			continue
		}
		var value any
		var code int
		message := "request refused"
		if authErr != nil {
			code = -32003
			message = "native session authority denied"
		} else {
			switch q.Method {
			case "initialize":
				var p struct {
					ProtocolVersion string         `json:"protocolVersion"`
					Capabilities    map[string]any `json:"capabilities"`
					ClientInfo      map[string]any `json:"clientInfo"`
					Meta            map[string]any `json:"_meta,omitempty"`
				}
				if initialized || Decode(q.Params, &p) != nil || p.ProtocolVersion == "" {
					code = -32602
					break
				}
				// Metadata is accepted as protocol context only; credentials always
				// come from the pinned private connection, never client labels.
				// A newer client offer negotiates our supported version.
				initialized = true
				value = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "dacli-native-governance", "version": "0.1.0"}, "instructions": "Scoped cooperative governance services. No caller shell, owner operations or acceptance. Named verification or independent review is available only with the corresponding owner-selected private profile."}
			case "ping":
				var p struct {
					Meta map[string]any `json:"_meta,omitempty"`
				}
				raw := q.Params
				if len(raw) == 0 {
					raw = []byte("{}")
				}
				if Decode(raw, &p) != nil {
					code = -32602
					break
				}
				value = map[string]any{}
			case "tools/list":
				var p struct {
					Cursor string         `json:"cursor,omitempty"`
					Meta   map[string]any `json:"_meta,omitempty"`
				}
				raw := q.Params
				if len(raw) == 0 {
					raw = []byte("{}")
				}
				if !initialized || Decode(raw, &p) != nil || p.Cursor != "" {
					code = -32602
					break
				}
				value = map[string]any{"tools": s.catalog()}
			case "tools/call":
				var p struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
					Meta      map[string]any  `json:"_meta,omitempty"`
				}
				if !initialized || Decode(q.Params, &p) != nil {
					code = -32602
					break
				}
				select {
				case slots <- struct{}{}:
					calls.Add(1)
					go func(q request, pName string, pArgs json.RawMessage) {
						defer calls.Done()
						defer pendingFrames.Add(-1)
						defer func() { <-slots }()
						data, e := s.call(pName, pArgs)
						_ = encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result(pName, data, e)})
					}(q, p.Name, p.Arguments)
					continue
				default:
					value = result(p.Name, nil, ErrDenied)
				}

			default:
				code = -32601
				message = "method unavailable"
			}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": q.ID}
		if code != 0 {
			response["error"] = map[string]any{"code": code, "message": message}
		} else {
			response["result"] = value
		}
		if e := encode(response); e != nil {
			pendingFrames.Add(-1)
			return e
		}
		pendingFrames.Add(-1)
	}
}

func validID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var id any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&id) != nil {
		return false
	}
	switch v := id.(type) {
	case string:
		return true
	case json.Number:
		// Integer wire IDs retain exact digits, without floating-point rounding.
		return !strings.ContainsAny(string(v), ".eE")
	}
	return false
}
