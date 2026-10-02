package native

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Serve uses bounded newline-framed stdio and never logs request payloads.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), 1<<20)
	enc := json.NewEncoder(out)
	initialized := false
	for scan.Scan() {
		var q request
		if Decode(scan.Bytes(), &q) != nil || q.JSONRPC != "2.0" || q.Method == "" || !validID(q.ID) {
			if e := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32600, "message": "invalid request"}}); e != nil {
				return e
			}
			continue
		}
		_, _, _, _, authErr := s.authenticate()
		if len(q.ID) == 0 {
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
				value = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "dacli-native-governance", "version": "0.1.0"}, "instructions": "Scoped cooperative governance services. No shell, owner operations, verification runner, review or acceptance."}
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
				value = map[string]any{"tools": catalog()}
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
				data, e := s.call(p.Name, p.Arguments)
				value = result(p.Name, data, e)
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
		if e := enc.Encode(response); e != nil {
			return e
		}
	}
	if scan.Err() != nil {
		return ErrDenied
	}
	return nil
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
