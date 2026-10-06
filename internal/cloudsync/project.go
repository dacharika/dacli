package cloudsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ProjectConnection contains route/health facts only. Device secrets live in
// the OS credential store and never in the repository or this durable record.
type ProjectConnection struct {
	SchemaVersion   int                    `json:"schema_version"`
	Provider        string                 `json:"provider"`
	LocalProject    string                 `json:"local_project"`
	Binding         ProjectBindingResponse `json:"binding"`
	Connected       bool                   `json:"connected"`
	Generation      uint64                 `json:"generation"`
	ConnectedAt     time.Time              `json:"connected_at"`
	DisconnectedAt  *time.Time             `json:"disconnected_at,omitempty"`
	DeliveryCursor  string                 `json:"delivery_cursor"`
	LastSuccess     *time.Time             `json:"last_success,omitempty"`
	LastFailure     *time.Time             `json:"last_failure,omitempty"`
	LastFailureCode string                 `json:"last_failure_code,omitempty"`
}

var ErrBindingConflict = errors.New("cloud project binding exists; replacement requires separately verified migration")

func LoadProjectConnection(root string) (ProjectConnection, error) {
	var out ProjectConnection
	raw, e := os.ReadFile(filepath.Join(root, "connection.json"))
	if e != nil {
		return out, e
	}
	if len(raw) > 65536 {
		return out, ErrInvalidPayload
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(&struct{}{}) != io.EOF || out.SchemaVersion != 1 || out.Generation == 0 || out.LocalProject == "" || out.Binding.Route.ProjectID == "" {
		return out, ErrInvalidPayload
	}
	return out, nil
}

// Caller serializes connect/sync/disconnect with the shared workspace file lock.
func SaveProjectConnection(root string, next ProjectConnection) error {
	if next.SchemaVersion != 1 || next.Generation == 0 || next.LocalProject == "" {
		return ErrInvalidPayload
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return e
	}
	old, e := LoadProjectConnection(root)
	if e == nil {
		if old.Provider != next.Provider || old.LocalProject != next.LocalProject || old.Binding.Route != next.Binding.Route || old.Binding.RepositoryURL != next.Binding.RepositoryURL || next.Generation < old.Generation {
			return ErrBindingConflict
		}
		// Each disconnect/reconnect preserves an immutable auditable transition.
		if old.Connected != next.Connected {
			if next.Generation != old.Generation+1 {
				return ErrBindingConflict
			}
			history := filepath.Join(root, "history")
			if e = os.MkdirAll(history, 0700); e != nil {
				return e
			}
			transition := filepath.Join(history, oldTransitionName(old))
			if e = createJSON(transition, old); e != nil {
				if !errors.Is(e, os.ErrExist) {
					return e
				}
				raw, err := os.ReadFile(transition)
				if err != nil {
					return err
				}
				var prior ProjectConnection
				if json.Unmarshal(raw, &prior) != nil || !bytes.Equal(mustJSON(prior), mustJSON(old)) {
					return ErrBindingConflict
				}
			}
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return writeJSONAtomic(filepath.Join(root, "connection.json"), next)
}
func oldTransitionName(c ProjectConnection) string { return formatSequence(c.Generation) + ".json" }
func formatSequence(v uint64) string {
	// Decimal generation avoids paths derived from remote metadata.
	return fmt.Sprintf("%020d", v)
}

type ProjectStatus struct {
	Connected            bool       `json:"connected"`
	Health               string     `json:"health"`
	AcknowledgedSequence uint64     `json:"acknowledged_sequence"`
	InboundCursor        uint64     `json:"inbound_cursor"`
	Backlog              int        `json:"backlog"`
	LastSuccess          *time.Time `json:"last_success,omitempty"`
	LastFailure          *time.Time `json:"last_failure,omitempty"`
	LastFailureCode      string     `json:"last_failure_code,omitempty"`
	NextAction           string     `json:"next_action"`
}

func ReadProjectStatus(root string) (ProjectStatus, error) {
	c, e := LoadProjectConnection(root)
	if e != nil {
		return ProjectStatus{}, e
	}
	out := ProjectStatus{Connected: c.Connected, Health: "connected", LastSuccess: c.LastSuccess, LastFailure: c.LastFailure, LastFailureCode: c.LastFailureCode, NextAction: "project sync"}
	var state persistentState
	raw, e := os.ReadFile(filepath.Join(root, "transport", "state.json"))
	if e == nil {
		if json.Unmarshal(raw, &state) != nil {
			return out, ErrInvalidPayload
		}
		out.AcknowledgedSequence = state.AcknowledgedSequence
		out.InboundCursor = state.Cursor.LastSequence
	} else if !errors.Is(e, os.ErrNotExist) {
		return out, e
	}
	entries, e := os.ReadDir(filepath.Join(root, "transport", "outbox"))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return out, e
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(root, "transport", "outbox", entry.Name()))
		if e != nil {
			return out, e
		}
		var envelope Envelope
		if json.Unmarshal(raw, &envelope) != nil {
			return out, ErrInvalidPayload
		}
		if envelope.ProducerSequence > state.AcknowledgedSequence {
			out.Backlog++
		}
	}
	if !c.Connected {
		out.Health = "disconnected"
		out.NextAction = "project connect with the same explicit binding"
	} else if c.LastFailure != nil && (c.LastSuccess == nil || c.LastFailure.After(*c.LastSuccess)) {
		out.Health = "offline-or-refused"
		out.NextAction = "inspect failure code; reconnect device or retry project sync when available"
	}
	return out, nil
}
