// Package usage stores bounded, provenance-bound output-token observations from
// native harness/provider callbacks. It never estimates tokens from text, time,
// cost, or a silent runtime.
package usage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

const (
	MaxEvents      = 4096
	MaxEventBytes  = 8192
	MaxStoreBytes  = MaxEvents * MaxEventBytes
	MaxTokenCount  = int64(1_000_000_000_000)
	maxIdentifier  = 256
	maxProviderStr = 128
)

var (
	ErrInvalid     = errors.New("invalid native output-usage observation")
	ErrConflict    = errors.New("conflicting native output-usage event")
	ErrLimit       = errors.New("native output-usage retention limit reached")
	ErrUnavailable = errors.New("native output-usage store unavailable")
)

// Count is an explicit token count. Unit is intentionally required to be
// "tokens"; basis separates a provider's increment from a running total.
type Count struct {
	Value    int64  `json:"value"`
	Unit     string `json:"unit"`
	Basis    string `json:"basis"` // delta or cumulative
	Sequence uint64 `json:"sequence"`
}

// Observation contains metadata only. Prompts, source, credentials and raw
// model output are deliberately not representable in the persisted contract.
type Observation struct {
	SchemaVersion    int       `json:"schema_version"`
	WorkspaceID      string    `json:"workspace_id"`
	ProjectID        string    `json:"project_id"`
	AgentProfileID   string    `json:"agent_profile_id,omitempty"`
	SessionID        string    `json:"session_id"`
	TaskID           string    `json:"task_id"`
	AttemptID        string    `json:"attempt_id"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	StreamID         string    `json:"stream_id"`
	SourceEventID    string    `json:"source_event_id"`
	ObservedAt       time.Time `json:"observed_at"`
	Source           string    `json:"source"`       // native-harness, provider, agent-declared
	Availability     string    `json:"availability"` // measured, declared, unavailable
	UnavailableCause string    `json:"unavailable_cause,omitempty"`
	OutputTokens     *Count    `json:"output_tokens,omitempty"`
	InputTokens      *Count    `json:"input_tokens,omitempty"`
	Supersedes       []string  `json:"supersedes,omitempty"`
}

type Totals struct {
	SchemaVersion           int    `json:"schema_version"`
	WorkspaceID             string `json:"workspace_id"`
	ProjectID               string `json:"project_id"`
	TaskID                  string `json:"task_id"`
	AttemptID               string `json:"attempt_id"`
	MeasuredOutputTokens    int64  `json:"measured_output_tokens"`
	MeasuredInputTokens     int64  `json:"measured_input_tokens"`
	DeclaredOutputTokens    int64  `json:"declared_output_tokens"`
	DeclaredInputTokens     int64  `json:"declared_input_tokens"`
	MeasuredOutputAvailable bool   `json:"measured_output_available"`
	MeasuredInputAvailable  bool   `json:"measured_input_available"`
	DeclaredOutputAvailable bool   `json:"declared_output_available"`
	DeclaredInputAvailable  bool   `json:"declared_input_available"`
	UnavailableObservations int    `json:"unavailable_observations"`
	ObservationCount        int    `json:"observation_count"`
}

// Store writes under the workspace's ignored local usage directory. A store
// instance is scoped to one workspace; event identity paths are derived from
// the validated project, task and attempt binding.
type Store struct{ Root string }

func (s Store) base() (string, error) {
	if s.Root == "" || !filepath.IsAbs(s.Root) {
		return "", ErrInvalid
	}
	return filepath.Join(s.Root, workspace.Dir, "build", "output-usage", "v1"), nil
}

func validID(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > maxIdentifier || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func validLabel(s string, max int) bool {
	if s == "" || len(s) > max || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func validate(o Observation) error {
	if o.SchemaVersion != 1 || !validID(o.WorkspaceID) || !validID(o.ProjectID) || !validID(o.SessionID) || !validID(o.TaskID) || !validID(o.AttemptID) || !validID(o.SourceEventID) || !validID(o.StreamID) || !validLabel(o.Provider, maxProviderStr) || !validLabel(o.Model, maxProviderStr) || o.ObservedAt.IsZero() {
		return ErrInvalid
	}
	if o.AgentProfileID != "" && !validID(o.AgentProfileID) {
		return ErrInvalid
	}
	switch o.Source {
	case "native-harness", "provider":
		if o.Availability == "measured" {
			if o.UnavailableCause != "" || o.OutputTokens == nil && o.InputTokens == nil {
				return ErrInvalid
			}
		} else if o.Availability == "unavailable" {
			if o.OutputTokens != nil || o.InputTokens != nil || !validLabel(o.UnavailableCause, 256) {
				return ErrInvalid
			}
		} else {
			return ErrInvalid
		}
	case "agent-declared":
		if o.Availability != "declared" || o.UnavailableCause != "" || o.OutputTokens == nil && o.InputTokens == nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	for _, c := range []*Count{o.OutputTokens, o.InputTokens} {
		if c == nil {
			continue
		}
		if c.Value < 0 || c.Value > MaxTokenCount || c.Unit != "tokens" || c.Sequence == 0 || c.Basis != "delta" && c.Basis != "cumulative" {
			return ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, id := range o.Supersedes {
		if !validID(id) || id == o.SourceEventID || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	if len(o.Supersedes) > MaxEvents {
		return ErrInvalid
	}
	return nil
}

func identity(o Observation) string {
	return strings.Join([]string{o.WorkspaceID, o.ProjectID, o.TaskID, o.AttemptID, o.SourceEventID}, "\x00")
}

func (s Store) dir(o Observation) (string, error) {
	base, err := s.base()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, o.ProjectID, o.TaskID, o.AttemptID), nil
}

// Append records an observation idempotently. A repeated event ID with changed
// bytes is a conflict; it cannot silently rewrite a prior provider fact.
func (s Store) Append(o Observation) error {
	if err := validate(o); err != nil {
		return err
	}
	dir, err := s.dir(o)
	if err != nil {
		return err
	}
	if dir, err = secureMkdirAll(s.Root, dir); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	unlock, err := s.lock(o)
	if err != nil {
		return err
	}
	defer unlock()
	rows, total, err := s.readAttempt(o)
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(identity(o)))
	path := filepath.Join(dir, hex.EncodeToString(key[:])+".json")
	data, err := json.Marshal(o)
	if err != nil || len(data) > MaxEventBytes {
		return ErrInvalid
	}
	if old, ok := rows[o.SourceEventID]; ok {
		oldData, _ := json.Marshal(old)
		if !bytes.Equal(oldData, data) {
			return ErrConflict
		}
		return nil
	}
	for _, old := range rows {
		if old.SessionID != o.SessionID || old.AgentProfileID != o.AgentProfileID {
			return ErrConflict
		}
	}
	if len(rows) >= MaxEvents || total+int64(len(data)) > MaxStoreBytes {
		return ErrLimit
	}
	if err := validateSupersedes(rows, o); err != nil {
		return err
	}
	for _, oldID := range o.Supersedes {
		for _, old := range rows {
			if supersedes(old, oldID) {
				return ErrConflict
			}
		}
	}
	for _, old := range rows {
		if !sameStream(old, o) || supersedes(o, old.SourceEventID) {
			continue
		}
		if o.OutputTokens != nil && old.OutputTokens != nil && o.OutputTokens.Sequence == old.OutputTokens.Sequence ||
			o.InputTokens != nil && old.InputTokens != nil && o.InputTokens.Sequence == old.InputTokens.Sequence {
			return ErrConflict
		}
	}
	return createExclusiveAtomic(path, data)
}

// lock serializes reads and appends across processes. A leftover lock after a
// crash is intentionally not stolen; an owner must confirm there is no writer
// before removing it, matching the session-authority lock recovery rule.
func (s Store) lock(o Observation) (func(), error) {
	base, err := s.base()
	if err != nil {
		return nil, err
	}
	lockDir := filepath.Join(base, ".locks")
	lockDir, err = secureMkdirAll(s.Root, lockDir)
	if err != nil {
		return nil, ErrUnavailable
	}
	key := sha256.Sum256([]byte(strings.Join([]string{o.WorkspaceID, o.ProjectID, o.TaskID, o.AttemptID}, "\x00")))
	path := filepath.Join(lockDir, hex.EncodeToString(key[:])+".lock")
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, ErrUnavailable
	}
	return func() { _ = os.Remove(path) }, nil
}

func sameStream(left, right Observation) bool {
	return usageClass(left) == usageClass(right) && sourceLane(left) == sourceLane(right) && left.Provider == right.Provider && left.Model == right.Model && left.StreamID == right.StreamID && left.AgentProfileID == right.AgentProfileID
}

// sourceLane keeps agent declarations isolated while intentionally sharing the
// measured sequence lane between provider and native-harness callbacks.
func sourceLane(o Observation) string {
	if o.Source == "agent-declared" {
		return "agent-declared"
	}
	return "measured-callback"
}

func usageClass(o Observation) string {
	if o.Availability == "declared" {
		return "declared"
	}
	return "measured"
}

func supersedes(o Observation, id string) bool {
	for _, previous := range o.Supersedes {
		if previous == id {
			return true
		}
	}
	return false
}

func validateSupersedes(rows map[string]Observation, next Observation) error {
	for _, oldID := range next.Supersedes {
		old, ok := rows[oldID]
		if !ok {
			return ErrInvalid
		}
		if !sameStream(old, next) {
			return ErrConflict
		}
		if old.OutputTokens != nil && (next.OutputTokens == nil && next.Availability != "unavailable" || next.OutputTokens != nil && next.OutputTokens.Sequence < old.OutputTokens.Sequence) {
			return ErrConflict
		}
		if old.InputTokens != nil && (next.InputTokens == nil && next.Availability != "unavailable" || next.InputTokens != nil && next.InputTokens.Sequence < old.InputTokens.Sequence) {
			return ErrConflict
		}
	}
	return nil
}

func secureMkdirAll(root, path string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	requestedRoot := root
	root, err = filepath.EvalSymlinks(requestedRoot)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", ErrUnavailable
	}
	relToRoot, err := filepath.Rel(requestedRoot, path)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || filepath.IsAbs(relToRoot) {
		return "", ErrUnavailable
	}
	path = filepath.Join(root, relToRoot)
	usageRoot := filepath.Join(root, workspace.Dir, "build", "output-usage", "v1")
	rel, err := filepath.Rel(usageRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", ErrUnavailable
	}
	cur := filepath.Join(root, workspace.Dir)
	if err = os.Mkdir(cur, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, statErr := os.Lstat(cur)
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrUnavailable
	}
	for _, part := range strings.Split(filepath.Join("build", "output-usage", "v1", rel), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		if err = os.Mkdir(cur, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, statErr = os.Lstat(cur)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrUnavailable
		}
	}
	return cur, nil
}

func createExclusiveAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".usage-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err = os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			stored, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(stored, data) {
				return nil
			}
			return ErrConflict
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (s Store) readAttempt(o Observation) (map[string]Observation, int64, error) {
	dir, err := s.dir(o)
	if err != nil {
		return nil, 0, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Observation{}, 0, nil
	}
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	if len(entries) > MaxEvents {
		return nil, 0, ErrLimit
	}
	rows := make(map[string]Observation, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil, 0, ErrUnavailable
		}
		path := filepath.Join(dir, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > MaxEventBytes {
			return nil, 0, ErrUnavailable
		}
		total += info.Size()
		if total > MaxStoreBytes {
			return nil, 0, ErrLimit
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil, 0, ErrUnavailable
		}
		b, readErr := io.ReadAll(io.LimitReader(f, MaxEventBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(b) > MaxEventBytes {
			return nil, 0, ErrUnavailable
		}
		var got Observation
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if dec.Decode(&got) != nil || dec.Decode(new(any)) != io.EOF || validate(got) != nil || got.WorkspaceID != o.WorkspaceID || got.ProjectID != o.ProjectID || got.TaskID != o.TaskID || got.AttemptID != o.AttemptID {
			return nil, 0, ErrUnavailable
		}
		key := sha256.Sum256([]byte(identity(got)))
		if entry.Name() != hex.EncodeToString(key[:])+".json" {
			return nil, 0, ErrUnavailable
		}
		if _, exists := rows[got.SourceEventID]; exists {
			return nil, 0, ErrUnavailable
		}
		rows[got.SourceEventID] = got
	}
	supersededBy := map[string]string{}
	for _, row := range rows {
		if validateSupersedes(rows, row) != nil {
			return nil, 0, ErrUnavailable
		}
		for _, previous := range row.Supersedes {
			if _, ok := rows[previous]; !ok {
				return nil, 0, ErrUnavailable
			}
			if prior, duplicate := supersededBy[previous]; duplicate && prior != row.SourceEventID {
				return nil, 0, ErrUnavailable
			}
			supersededBy[previous] = row.SourceEventID
		}
	}
	visitState := map[string]uint8{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visitState[id] == 1 {
			return false
		}
		if visitState[id] == 2 {
			return true
		}
		visitState[id] = 1
		for _, previous := range rows[id].Supersedes {
			if !visit(previous) {
				return false
			}
		}
		visitState[id] = 2
		return true
	}
	for id := range rows {
		if !visit(id) {
			return nil, 0, ErrUnavailable
		}
	}
	return rows, total, nil
}

// Read returns the immutable event history for one exact workspace/project/
// task/attempt binding and its deduplicated totals.
func (s Store) Read(workspaceID, projectID, taskID, attemptID string) ([]Observation, Totals, error) {
	o := Observation{WorkspaceID: workspaceID, ProjectID: projectID, TaskID: taskID, AttemptID: attemptID}
	if !validID(workspaceID) || !validID(projectID) || !validID(taskID) || !validID(attemptID) {
		return nil, Totals{}, ErrInvalid
	}
	unlock, err := s.lock(o)
	if err != nil {
		return nil, Totals{}, err
	}
	defer unlock()
	rows, _, err := s.readAttempt(o)
	if err != nil {
		return nil, Totals{}, err
	}
	observations := make([]Observation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, row)
	}
	sort.Slice(observations, func(i, j int) bool {
		if observations[i].ObservedAt.Equal(observations[j].ObservedAt) {
			return observations[i].SourceEventID < observations[j].SourceEventID
		}
		return observations[i].ObservedAt.Before(observations[j].ObservedAt)
	})
	totals := reconcile(workspaceID, projectID, taskID, attemptID, observations)
	return observations, totals, nil
}

func reconcile(workspaceID, projectID, taskID, attemptID string, rows []Observation) Totals {
	t := Totals{SchemaVersion: 1, WorkspaceID: workspaceID, ProjectID: projectID, TaskID: taskID, AttemptID: attemptID, ObservationCount: len(rows)}
	superseded := map[string]bool{}
	for _, row := range rows {
		for _, id := range row.Supersedes {
			superseded[id] = true
		}
		if row.Availability == "unavailable" {
			t.UnavailableObservations++
		}
	}
	active := make([]Observation, 0, len(rows))
	for _, row := range rows {
		if !superseded[row.SourceEventID] {
			active = append(active, row)
		}
	}
	// A sequence is scoped to one source stream. Independent model/provider
	// streams add together; late callbacks are replayed by provider sequence.
	streams := map[string][]Observation{}
	for _, row := range active {
		key := strings.Join([]string{usageClass(row), row.Provider, row.Model, row.StreamID, row.AgentProfileID}, "\x00")
		streams[key] = append(streams[key], row)
	}
	for _, metric := range []string{"output", "input"} {
		var measured, declared int64
		measuredAvailable, declaredAvailable := false, false
		measuredInvalid, declaredInvalid := false, false
		for _, stream := range streams {
			selected := make([]struct {
				id    string
				count Count
				decl  bool
			}, 0, len(stream))
			for _, row := range stream {
				var c *Count
				if metric == "output" {
					c = row.OutputTokens
				} else {
					c = row.InputTokens
				}
				if c != nil {
					selected = append(selected, struct {
						id    string
						count Count
						decl  bool
					}{row.SourceEventID, *c, row.Availability == "declared"})
				}
			}
			sort.Slice(selected, func(i, j int) bool {
				if selected[i].count.Sequence == selected[j].count.Sequence {
					return selected[i].id < selected[j].id
				}
				return selected[i].count.Sequence < selected[j].count.Sequence
			})
			var seenSeq = map[uint64]string{}
			var streamTotal int64
			streamInvalid := false
			for _, item := range selected {
				if prior, ok := seenSeq[item.count.Sequence]; ok && prior != item.id {
					streamInvalid = true
					break
				}
				seenSeq[item.count.Sequence] = item.id
				streamTotal = applyCount(streamTotal, item.count)
			}
			if len(selected) == 0 {
				continue
			}
			isDeclared := selected[0].decl
			if streamInvalid {
				if isDeclared {
					declaredInvalid = true
				} else {
					measuredInvalid = true
				}
				continue
			}
			if isDeclared {
				declared = addTotals(declared, streamTotal)
				declaredAvailable = true
			} else {
				measured = addTotals(measured, streamTotal)
				measuredAvailable = true
			}
		}
		if measuredInvalid {
			measured, measuredAvailable = 0, false
		}
		if declaredInvalid {
			declared, declaredAvailable = 0, false
		}
		if metric == "output" {
			t.MeasuredOutputTokens, t.DeclaredOutputTokens = measured, declared
			t.MeasuredOutputAvailable, t.DeclaredOutputAvailable = measuredAvailable, declaredAvailable
		} else {
			t.MeasuredInputTokens, t.DeclaredInputTokens = measured, declared
			t.MeasuredInputAvailable, t.DeclaredInputAvailable = measuredAvailable, declaredAvailable
		}
	}
	return t
}

func addTotals(total, add int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if total > maxInt64-add {
		return maxInt64
	}
	return total + add
}

func applyCount(total int64, c Count) int64 {
	if c.Basis == "cumulative" {
		return c.Value
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if total > maxInt64-c.Value {
		return maxInt64
	}
	return total + c.Value
}
