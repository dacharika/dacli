// Package driver implements a finite, serial controller effect pump. It owns no
// durable state: authority, checkpoint and producer records remain canonical.
package driver

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const ResultSchema = "native-controller-driver/v1"

var (
	ErrInvalidRequest = errors.New("invalid controller driver request")
	ErrInvalidLimits  = errors.New("invalid controller driver limits")
	ErrUnsupported    = errors.New("controller driver operation unsupported")
	ErrWorkLimit      = errors.New("controller driver work limit")
	ErrEffectLimit    = errors.New("controller driver effect limit")
)

// Limits are per invocation. MaxSequence is an absolute durable sequence
// ceiling, so resuming a run cannot reset the controller's progress bound.
type Limits struct {
	MaxTransitions, MaxEffects, MaxWork, MaxNoProgress uint64
	MaxElapsed, OperationTimeout                       time.Duration
	MaxSequence                                        uint64
}

type operationKeyIdentity struct {
	Project, Invocation, Checkpoint string
	Sequence                        uint64
	Purpose, OriginalOperationKey   string
}

// Request selects one exact current controller invocation and checkpoint.
type Request struct {
	SchemaVersion    int    `json:"schema_version"`
	ProjectID        string `json:"project_id"`
	InvocationID     string `json:"invocation_id"`
	CheckpointID     string `json:"checkpoint_id"`
	ExpectedSequence uint64 `json:"expected_sequence"`
	Mode             string `json:"mode"`
	CancelDispatchID string `json:"cancel_dispatch_id,omitempty"`
}

// OriginalOperation is the safe public description of a durable producer key.
type OriginalOperation struct {
	Kind         string `json:"kind"`
	OperationKey string `json:"operation_key"`
	State        string `json:"state"`
}

// Result is intentionally a summary. Unknown token, cost and resource facts
// remain null instead of being inferred as zero or false.
type Result struct {
	Schema             string              `json:"schema"`
	ProjectID          string              `json:"project_id"`
	InvocationID       string              `json:"invocation_id"`
	CheckpointID       string              `json:"checkpoint_id"`
	Sequence           uint64              `json:"sequence"`
	CheckpointSequence uint64              `json:"checkpoint_sequence"`
	Outcome            string              `json:"outcome"`
	Reason             string              `json:"reason"`
	NextAction         string              `json:"next_action"`
	Transitions        uint64              `json:"transitions"`
	Effects            uint64              `json:"effects"`
	Work               uint64              `json:"work"`
	NoProgress         uint64              `json:"no_progress"`
	ElapsedMS          int64               `json:"elapsed_ms"`
	OriginalOperations []OriginalOperation `json:"original_operations"`
	ResourcesRetained  *bool               `json:"resources_retained"`
	ProviderTokens     *uint64             `json:"provider_tokens"`
	ProviderCostUSD    *float64            `json:"provider_cost_usd"`
}

// Driver pins trusted in-process ports and finite limits; it has no durable
// counters or host/provider launcher.
type Driver struct {
	ports  Ports
	limits Limits
}

func New(ports Ports, limits Limits) (*Driver, error) {
	if ports == nil || limits.MaxTransitions == 0 || limits.MaxTransitions > 256 || limits.MaxEffects == 0 || limits.MaxEffects > 256 || limits.MaxWork == 0 || limits.MaxWork > 4096 || limits.MaxNoProgress == 0 || limits.MaxNoProgress > 100 || limits.MaxElapsed <= 0 || limits.MaxElapsed > 15*time.Minute || limits.OperationTimeout <= 0 || limits.OperationTimeout > 30*time.Second || limits.MaxSequence == 0 {
		return nil, ErrInvalidLimits
	}
	return &Driver{ports: ports, limits: limits}, nil
}

// OperationKey is the single shared deterministic key contract for driver
// transitions and purpose-specific suboperations. It returns only an opaque
// identifier; it grants no authority and never changes an original host key.
func OperationKey(project, invocation, checkpoint string, sequence uint64, purpose, originalOperationKey string) (string, error) {
	if !driverID(project) || !driverID(invocation) || !driverID(checkpoint) {
		return "", ErrInvalidRequest
	}
	switch purpose {
	case "step", "stop":
		if sequence == 0 || originalOperationKey != "" {
			return "", ErrInvalidRequest
		}
	case "correlate", "ack", "cancel", "cancel-ack":
		if sequence != 0 || !driverID(originalOperationKey) {
			return "", ErrInvalidRequest
		}
	case "observe":
		if sequence == 0 || !driverID(originalOperationKey) {
			return "", ErrInvalidRequest
		}
	default:
		return "", ErrInvalidRequest
	}
	encoded, err := encodeOperationKey(operationKeyIdentity{project, invocation, checkpoint, sequence, purpose, originalOperationKey})
	if err != nil {
		return "", err
	}
	return "driver-" + encoded, nil
}

func encodeOperationKey(identity operationKeyIdentity) (string, error) {
	var data bytes.Buffer
	data.WriteString("dacli.controller-driver.operation/v1\x00")
	for _, field := range []string{identity.Project, identity.Invocation, identity.Checkpoint} {
		if err := writeOperationKeyField(&data, []byte(field)); err != nil {
			return "", err
		}
	}
	var sequence [8]byte
	binary.BigEndian.PutUint64(sequence[:], identity.Sequence)
	data.Write(sequence[:])
	for _, field := range []string{identity.Purpose, identity.OriginalOperationKey} {
		if err := writeOperationKeyField(&data, []byte(field)); err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256(data.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func writeOperationKeyField(dst *bytes.Buffer, field []byte) error {
	if len(field) > 256 {
		return ErrInvalidRequest
	}
	var length [4]byte
	// #nosec G115 -- the length is bounded to 256 immediately above.
	binary.BigEndian.PutUint32(length[:], uint32(len(field)))
	dst.Write(length[:])
	dst.Write(field)
	return nil
}

func driverID(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\x00\r\n")
}

func validateRequest(request Request) error {
	if request.SchemaVersion != 1 || !driverID(request.ProjectID) || !driverID(request.InvocationID) || !driverID(request.CheckpointID) || request.ExpectedSequence == 0 {
		return ErrInvalidRequest
	}
	switch request.Mode {
	case "drive", "stop":
		if request.CancelDispatchID != "" {
			return ErrInvalidRequest
		}
	case "cancel":
		if !driverID(request.CancelDispatchID) {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}
