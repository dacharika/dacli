package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/pathclaims"
)

const (
	queueDiagnosticsSchema = "native-queue-diagnostics/v1"
	queueDiagnosticsLimit  = 100
	queueDiagnosticsRows   = 20000
	queueDiagnosticsBytes  = 1 << 20
	queueCanonicalBytes    = 32 << 20
	queueDiagnosticsTime   = 15 * time.Second
)

// QueueCanonicalTask is the minimal canonical input required for projection.
// It is internal to the callback boundary and is never returned as wire data.
type QueueCanonicalTask struct {
	TaskID, ProjectID, Status, Reason string
	Resources                         []Resource
}

// QueueCanonicalSnapshot is immutable input for a single bounded projection.
type QueueCanonicalSnapshot struct {
	Digest string
	Tasks  []QueueCanonicalTask
}

// QueueCanonicalReader is supplied by the trusted application adapter.
type QueueCanonicalReader func(context.Context) (QueueCanonicalSnapshot, error)

type QueueDiagnosticsCounts struct {
	Ready       int `json:"ready"`
	Queued      int `json:"queued"`
	Running     int `json:"running"`
	Check       int `json:"check"`
	Review      int `json:"review"`
	Integration int `json:"integration"`
}

type QueueDiagnosticReason struct {
	Code      string `json:"code"`
	Knowledge string `json:"knowledge"`
}

type QueueDiagnosticRow struct {
	TaskID             string                  `json:"task_id"`
	Status             string                  `json:"status"`
	Stage              string                  `json:"stage"`
	Reasons            []QueueDiagnosticReason `json:"reasons"`
	Freshness          string                  `json:"freshness"`
	QueueEnteredAt     *time.Time              `json:"queue_entered_at"`
	QueueAgeSeconds    *int64                  `json:"queue_age_seconds"`
	RetainedProtection bool                    `json:"retained_protection"`
}

type QueueHostDiagnostic struct {
	HostID                     string     `json:"host_id"`
	HostSessionID              string     `json:"host_session_id"`
	ExecutorInstanceID         string     `json:"executor_instance_id"`
	Runtime                    string     `json:"runtime"`
	Build                      string     `json:"build"`
	Mode                       string     `json:"mode"`
	State                      string     `json:"state"`
	Reason                     string     `json:"reason"`
	Freshness                  string     `json:"freshness"`
	Provenance                 string     `json:"provenance"`
	ObservationDigest          string     `json:"observation_digest"`
	ObservationSequence        uint64     `json:"observation_sequence"`
	ObservedAt                 *time.Time `json:"observed_at"`
	ExpiresAt                  *time.Time `json:"expires_at"`
	RequestedSlots             *int       `json:"requested_slots"`
	DeclaredSlots              *int       `json:"declared_slots"`
	QualifiedSlots             *int       `json:"qualified_slots"`
	ObservedOccupied           *int       `json:"observed_occupied"`
	IncludedLocalExecutions    *int       `json:"included_local_executions"`
	ExternalOccupied           *int       `json:"external_occupied"`
	OutstandingExecutionClaims *int       `json:"outstanding_execution_claims"`
	RemainingAdmissibleSlots   *int       `json:"remaining_admissible_slots"`
	RetainedWriterCount        int        `json:"retained_writer_count"`
}

type QueueDiagnosticsSnapshot struct {
	Schema                string                 `json:"schema"`
	DiagnosticOnly        bool                   `json:"diagnostic_only"`
	Project               string                 `json:"project"`
	TaskID                string                 `json:"task_id,omitempty"`
	AuthorityVersion      int                    `json:"authority_version"`
	ObservedAt            time.Time              `json:"observed_at"`
	State                 string                 `json:"state"`
	Counts                QueueDiagnosticsCounts `json:"counts"`
	TotalCount            int                    `json:"total_count"`
	ExcludedTerminalCount int                    `json:"excluded_terminal_count"`
	Rows                  []QueueDiagnosticRow   `json:"rows"`
	Hosts                 []QueueHostDiagnostic  `json:"hosts"`
	Limit                 int                    `json:"limit"`
	OmittedCount          int                    `json:"omitted_count"`
	Limited               bool                   `json:"limited"`
	Continuation          string                 `json:"continuation"`
	ProviderQuotaState    string                 `json:"provider_quota_state"`
	ProviderUsage         *int64                 `json:"provider_usage"`
	ProviderCostUSD       *float64               `json:"provider_cost_usd"`
}

// OpenQueueDiagnosticsAuthority performs only the canonical path checks from
// Open. It deliberately does not select or cache an authority schema.
func OpenQueueDiagnosticsAuthority(path string, binding Binding) (*Authority, error) {
	return Open(path, binding)
}

func (a *Authority) ReadQueueDiagnostics(ctx context.Context, token, session, project, task string, limit int, readCanonical QueueCanonicalReader) (QueueDiagnosticsSnapshot, error) {
	if a == nil || !validID(project) || (task != "" && !validID(task)) || limit < 1 || limit > queueDiagnosticsLimit || readCanonical == nil {
		return QueueDiagnosticsSnapshot{}, ErrDenied
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, queueDiagnosticsTime)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return QueueDiagnosticsSnapshot{}, ErrDenied
	}
	firstRaw, firstState, _, err := a.queueReadAuthority(ctx, token, session, project)
	if err != nil {
		return QueueDiagnosticsSnapshot{}, err
	}
	canonical, err := readCanonical(ctx)
	if ctx.Err() != nil {
		return QueueDiagnosticsSnapshot{}, ErrDenied
	}
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return QueueDiagnosticsSnapshot{}, ErrDenied
		}
		return QueueDiagnosticsSnapshot{}, ErrStorage
	}
	if err := validateQueueCanonical(ctx, canonical); err != nil {
		return QueueDiagnosticsSnapshot{}, err
	}
	if ctx.Err() != nil {
		return QueueDiagnosticsSnapshot{}, ErrDenied
	}
	now := time.Now().UTC()
	out := QueueDiagnosticsSnapshot{
		Schema: queueDiagnosticsSchema, DiagnosticOnly: true, Project: project, TaskID: task,
		AuthorityVersion: firstState.Version, ObservedAt: now, State: "ready", Rows: []QueueDiagnosticRow{}, Hosts: []QueueHostDiagnostic{},
		Limit: limit, Continuation: "unsupported", ProviderQuotaState: "unknown",
	}
	if err := ctx.Err(); err != nil {
		return QueueDiagnosticsSnapshot{}, ErrDenied
	}
	rows, terminal, err := queueProjectRows(ctx, &firstState, canonical, project, task, now, &out.Counts)
	if err != nil {
		return QueueDiagnosticsSnapshot{}, err
	}
	out.ExcludedTerminalCount = terminal
	out.TotalCount = out.Counts.Ready + out.Counts.Queued + out.Counts.Running + out.Counts.Check + out.Counts.Review + out.Counts.Integration
	out.Hosts = queueHostDiagnostics(&firstState, project, now)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out.Rows = rows
	out.OmittedCount = out.TotalCount - len(rows)
	out.Limited = out.OmittedCount > 0
	if out.Limited {
		out.State = "limited"
	}
	// The DTO has a fixed bound; shrink only the row window, retaining exact
	// full-scope counts and omitted-row arithmetic.
	for {
		encoded, marshalErr := json.Marshal(out)
		if marshalErr == nil && len(encoded) <= queueDiagnosticsBytes {
			break
		}
		if len(out.Rows) == 0 {
			return QueueDiagnosticsSnapshot{}, ErrStorage
		}
		out.Rows = out.Rows[:len(out.Rows)-1]
		out.OmittedCount = out.TotalCount - len(out.Rows)
		out.Limited = out.OmittedCount > 0
		if out.Limited {
			out.State = "limited"
		}
	}
	if ctx.Err() != nil {
		return QueueDiagnosticsSnapshot{}, ErrDenied
	}
	secondRaw, _, _, err := a.queueReadAuthority(ctx, token, session, project)
	if err != nil || !bytes.Equal(firstRaw, secondRaw) {
		return QueueDiagnosticsSnapshot{}, ErrDenied
	}
	return out, nil
}

func (a *Authority) queueReadAuthority(ctx context.Context, token, session, project string) ([]byte, state, Session, error) {
	if a == nil || !validID(project) || !filepathIsAbsolute(a.path) {
		return nil, state{}, Session{}, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, state{}, Session{}, ErrDenied
	}
	raw, err := nativeconfig.ReadPrivate(a.path, MaxAuthorityBytes)
	if err != nil {
		return nil, state{}, Session{}, ErrStorage
	}
	if err := ctx.Err(); err != nil {
		return nil, state{}, Session{}, ErrDenied
	}
	var header struct{ Version int }
	if json.Unmarshal(raw, &header) != nil || header.Version < 6 || header.Version > 22 {
		return nil, state{}, Session{}, ErrStorage
	}
	s, err := decodeAuthorityStateMode(raw, a.binding, header.Version, true)
	if err != nil {
		return nil, state{}, Session{}, ErrStorage
	}
	if err := ctx.Err(); err != nil {
		return nil, state{}, Session{}, ErrDenied
	}
	if err := validateAuthorityMaps(&s); err != nil {
		return nil, state{}, Session{}, ErrStorage
	}
	if err := ctx.Err(); err != nil {
		return nil, state{}, Session{}, ErrDenied
	}
	if !authenticateOperator(s, token, session, project) {
		return nil, state{}, Session{}, ErrDenied
	}
	actor, err := authenticate(&s, token, session, time.Now())
	if err != nil {
		return nil, state{}, Session{}, ErrDenied
	}
	return raw, s, actor.Session, nil
}

// filepathIsAbsolute is kept local so the read path does not normalize or
// resolve a caller-selected path after the canonical Open-time checks.
func filepathIsAbsolute(path string) bool { return filepath.IsAbs(path) }

func authenticateOperator(s state, token, session, project string) bool {
	actor, err := authenticate(&s, token, session, time.Now())
	return err == nil && subset([]string{"operator.read"}, actor.Scope.Actions) && subset([]string{"workspace:" + s.Binding.Workspace}, actor.Scope.Resources) && subset([]string{"project:" + project}, actor.Scope.Resources)
}

func validateQueueCanonical(ctx context.Context, in QueueCanonicalSnapshot) error {
	if len(in.Tasks) > queueDiagnosticsRows || !validID(in.Digest) || len(in.Digest) > 128 {
		return ErrStorage
	}
	seen := make(map[string]bool, len(in.Tasks))
	var totalBytes int64 = int64(len(in.Digest))
	for _, task := range in.Tasks {
		if err := ctx.Err(); err != nil {
			return ErrDenied
		}
		if !validID(task.TaskID) || !validID(task.ProjectID) || len(task.Status) > 64 || len(task.Reason) > 1024 || seen[task.TaskID] || len(task.Resources) > 256 {
			return ErrStorage
		}
		totalBytes += int64(len(task.TaskID) + len(task.ProjectID) + len(task.Status) + len(task.Reason))
		seen[task.TaskID] = true
		for _, resource := range task.Resources {
			if err := ctx.Err(); err != nil {
				return ErrDenied
			}
			if len(resource.Kind) > 128 || len(resource.Path) > 4096 {
				return ErrStorage
			}
			totalBytes += int64(len(resource.Kind) + len(resource.Path))
			if totalBytes > queueCanonicalBytes {
				return ErrStorage
			}
		}
		if totalBytes > queueCanonicalBytes {
			return ErrStorage
		}
	}
	return nil
}

func queueProjectRows(ctx context.Context, s *state, canonical QueueCanonicalSnapshot, project, filter string, now time.Time, counts *QueueDiagnosticsCounts) ([]QueueDiagnosticRow, int, error) {
	index, err := buildQueueProjectionIndex(ctx, s, project, now, canonical.Tasks)
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[string]QueueCanonicalTask, len(canonical.Tasks))
	for _, t := range canonical.Tasks {
		if err := ctx.Err(); err != nil {
			return nil, 0, ErrDenied
		}
		if t.ProjectID == project {
			byID[t.TaskID] = t
		} else if filter != "" && t.TaskID == filter {
			return nil, 0, ErrDenied
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		if filter == "" || id == filter {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	rows := make([]QueueDiagnosticRow, 0, len(ids))
	terminal := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, 0, ErrDenied
		}
		t := byID[id]
		if queueTerminalStatus(t.Status) {
			terminal++
			continue
		}
		stage, reasons, fresh := queueClassifyIndexed(s, t, index, now)
		row := QueueDiagnosticRow{TaskID: t.TaskID, Status: safeQueueStatus(t.Status), Stage: stage, Reasons: reasons, Freshness: fresh, RetainedProtection: index.retained[t.TaskID]}
		rows = append(rows, row)
		switch stage {
		case "ready":
			counts.Ready++
		case "queued":
			counts.Queued++
		case "running":
			counts.Running++
		case "check":
			counts.Check++
		case "review":
			counts.Review++
		case "integration":
			counts.Integration++
		}
	}
	return rows, terminal, nil
}

func queueTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "complete", "completed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func safeQueueStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open", "ready", "queued", "active", "in_progress", "blocked", "review", "check", "integration", "done", "complete", "completed", "cancelled", "canceled":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "unknown"
	}
}

func queueClassifyTask(s *state, task QueueCanonicalTask, now time.Time) (string, []QueueDiagnosticReason, string) {
	index, _ := buildQueueProjectionIndex(context.Background(), s, task.ProjectID, now, []QueueCanonicalTask{task})
	return queueClassifyIndexed(s, task, index, now)
}

type queueProjectionIndex struct {
	running             map[string]bool
	retained            map[string]bool
	stages              map[string]map[string]bool
	pathHolders         map[string]*queuePathNode // repo -> exact path trie
	taskHolders         map[string]map[string]bool
	anyClaims           map[string]map[string]bool
	checkoutClaims      map[string]map[string]bool
	checkoutHolders     map[string]map[string]bool // exact checkout path -> task IDs
	roleUsed            map[string]int
	verificationUnknown map[string]bool
	hostView            HostSlotAdmissionView
}

type queuePathNode struct {
	children map[string]*queuePathNode
	holders  map[string]bool
}

func buildQueueProjectionIndex(ctx context.Context, s *state, project string, now time.Time, canonical []QueueCanonicalTask) (*queueProjectionIndex, error) {
	idx := &queueProjectionIndex{running: map[string]bool{}, retained: map[string]bool{}, stages: map[string]map[string]bool{}, pathHolders: map[string]*queuePathNode{}, taskHolders: map[string]map[string]bool{}, anyClaims: map[string]map[string]bool{}, checkoutClaims: map[string]map[string]bool{}, checkoutHolders: map[string]map[string]bool{}, roleUsed: map[string]int{}, verificationUnknown: map[string]bool{}}
	if s.Version >= 22 {
		idx.hostView = hostSlotAdmissionView(s, project, now)
	}
	if policy, ok := s.SwarmPolicies[project]; ok {
		active := make([]SwarmCandidate, 0)
		for _, task := range canonical {
			if err := ctx.Err(); err != nil {
				return nil, ErrDenied
			}
			if task.ProjectID == project && strings.EqualFold(task.Status, "active") {
				active = append(active, SwarmCandidate{TaskID: task.TaskID, Status: "active"})
			}
		}
		idx.roleUsed, _, _ = swarmOccupancy(s, policy, SwarmSnapshot{Candidates: active})
	}
	for id, d := range s.Dispatches {
		if err := ctx.Err(); err != nil {
			return nil, ErrDenied
		}
		if !validID(d.Reservation.TaskID) {
			continue
		}
		task := d.Reservation.TaskID
		if d.ProjectID == project && queueFreshRunningDispatch(s, id, d, now) {
			idx.running[task] = true
		}
		if reservationRetained(s, d) {
			if d.ProjectID == project {
				idx.retained[task] = true
			}
			if err := queueIndexWriterPaths(ctx, idx, d.Reservation.Repository, task, d.Reservation.Checkout, d.Reservation.Resources); err != nil {
				return nil, err
			}
		}
	}
	for _, l := range s.Leases {
		if err := ctx.Err(); err != nil {
			return nil, ErrDenied
		}
		binding, ok := s.Tasks[l.TaskID]
		if !l.Released && ok {
			if binding.ProjectID == project {
				idx.retained[l.TaskID] = true
			}
			if err := queueIndexWriterPaths(ctx, idx, l.Repository, l.TaskID, l.Checkout, l.Resources); err != nil {
				return nil, err
			}
		}
	}
	if s.Version >= 16 {
		for _, invocation := range s.ControllerInvocations {
			if err := ctx.Err(); err != nil {
				return nil, ErrDenied
			}
			iv := invocation
			st := iv.Status
			if st.ProjectID != project || st.Terminal || st.Historical {
				continue
			}
			for _, action := range st.Actions {
				if err := ctx.Err(); err != nil {
					return nil, ErrDenied
				}
				d, ok := s.Dispatches[action.DispatchID]
				if !ok || d.ProjectID != project || d.Reservation.TaskID != action.TaskID || d.Reservation.Fence != action.ReservationFence || queueDispatchRetired(s, d) || controllerIntegrationActionHistorical(s, iv, action) {
					continue
				}
				if !validID(action.ID) || !validID(action.OperationKey) {
					continue
				}
				switch action.Kind {
				case "integration_checkpoint", "canonical_acceptance":
					queueIndexStage(idx, action.TaskID, "integration")
				case "independent_review":
					queueIndexStage(idx, action.TaskID, "review")
				case "verification":
					if action.Verification.OperationKey == action.OperationKey && action.Verification.AttemptID == action.AttemptID && action.Verification.LeaseID == action.LeaseID && action.Verification.ExpectedFence == action.Fence {
						queueIndexStage(idx, action.TaskID, "check")
					}
				}
			}
		}
	}
	for key, op := range s.Operations {
		if err := ctx.Err(); err != nil {
			return nil, ErrDenied
		}
		binding := op.CheckpointVerification
		if op.Pending && binding != nil && binding.ProjectID == project && binding.TaskID != "" && binding.Request.OperationKey == key && validID(key) && validID(binding.TaskID) && validID(binding.Request.AttemptID) && validID(binding.Request.LeaseID) && binding.Request.ExpectedFence > 0 {
			if attempt, ok := s.Attempts[binding.Request.AttemptID]; ok && attempt.ID == binding.Request.AttemptID && attempt.ProjectID == project && attempt.Request.TaskID == binding.TaskID {
				if queueVerificationRetired(s, key, op, *binding) {
					idx.verificationUnknown[binding.TaskID] = true
				} else if queueVerificationLeaseCurrent(s, key, op, *binding, now) {
					queueIndexStage(idx, binding.TaskID, "check")
				} else {
					idx.verificationUnknown[binding.TaskID] = true
				}
			}
		}
	}
	if s.Version >= 18 {
		for key, rec := range s.NativeIntegrationOperations {
			if err := ctx.Err(); err != nil {
				return nil, ErrDenied
			}
			if rec.OperationKey == key && rec.Input.OperationKey == key && rec.Input.Candidate.ProjectID == project && rec.Input.Candidate.TaskID != "" && rec.State == "pending" {
				queueIndexStage(idx, rec.Input.Candidate.TaskID, "integration")
			}
		}
	}
	if s.Version >= 19 {
		for key, rec := range s.NativeExternalOperations {
			if err := ctx.Err(); err != nil {
				return nil, ErrDenied
			}
			if rec.OperationKey == key && rec.Input.OperationKey == key && rec.Input.Candidate.ProjectID == project && rec.Input.Candidate.TaskID != "" && rec.State == "pending" {
				queueIndexStage(idx, rec.Input.Candidate.TaskID, "integration")
			}
		}
	}
	if s.Version >= 14 {
		for key, rec := range s.NativeAcceptanceOperations {
			if err := ctx.Err(); err != nil {
				return nil, ErrDenied
			}
			if rec.OperationKey == key && rec.Candidate.Input.ProjectID == project && rec.Candidate.Input.TaskID != "" && rec.State == "closing" {
				queueIndexStage(idx, rec.Candidate.Input.TaskID, "integration")
			}
		}
	}
	return idx, nil
}

func queueVerificationRequestMatches(key string, op operation, binding CheckpointVerificationBinding) bool {
	req := binding.Request
	return validID(key) && req.OperationKey == key && validID(req.AttemptID) && validID(req.LeaseID) && validID(req.WorkerSession) && req.ExpectedFence > 0 && binding.DescriptorDigest != "" && binding.RunnerSession != "" && op.Digest == identity("run:"+binding.DescriptorDigest, binding.RunnerSession, req)
}

func queueVerificationLeaseCurrent(s *state, key string, op operation, binding CheckpointVerificationBinding, now time.Time) bool {
	if !queueVerificationRequestMatches(key, op, binding) {
		return false
	}
	req := binding.Request
	lease, ok := s.Leases[req.LeaseID]
	if !ok || lease.Released || lease.ID != req.LeaseID || lease.Fence != req.ExpectedFence || lease.AttemptID != req.AttemptID || lease.SessionID != req.WorkerSession || lease.TaskID != binding.TaskID || !now.Before(lease.ExpiresAt) {
		return false
	}
	attempt, ok := s.Attempts[req.AttemptID]
	return ok && attempt.ID == req.AttemptID && attempt.ProjectID == binding.ProjectID && attempt.Request.TaskID == binding.TaskID && attempt.SessionID == req.WorkerSession
}

func queueVerificationRetired(s *state, key string, op operation, binding CheckpointVerificationBinding) bool {
	if !queueVerificationRequestMatches(key, op, binding) {
		return false
	}
	req := binding.Request
	lease, ok := s.Leases[req.LeaseID]
	if !ok || !lease.Released || lease.ID != req.LeaseID || lease.Fence != req.ExpectedFence || lease.AttemptID != req.AttemptID || lease.SessionID != req.WorkerSession || lease.TaskID != binding.TaskID {
		return false
	}
	attempt, ok := s.Attempts[req.AttemptID]
	if !ok || attempt.ID != req.AttemptID || attempt.ProjectID != binding.ProjectID || attempt.Request.TaskID != binding.TaskID || attempt.SessionID != req.WorkerSession {
		return false
	}
	recovery, retired := checkpointRetiredLease(s, lease.ID)
	target := recovery.Target
	return retired && target.Workspace == s.Binding.Workspace && target.Kind == "lease" && target.ProtectionID == lease.ID && target.ExpectedFence == lease.Fence && target.AttemptID == lease.AttemptID && target.SessionID == lease.SessionID && target.TaskID == lease.TaskID && target.Repository == lease.Repository && target.Checkout == lease.Checkout
}

func queueIndexStage(idx *queueProjectionIndex, task, stage string) {
	if idx.stages[task] == nil {
		idx.stages[task] = map[string]bool{}
	}
	idx.stages[task][stage] = true
}

func queueIndexWriterPaths(ctx context.Context, idx *queueProjectionIndex, repository, task, checkout string, resources []Resource) error {
	if err := ctx.Err(); err != nil {
		return ErrDenied
	}
	if checkout != "" {
		if idx.checkoutHolders[checkout] == nil {
			idx.checkoutHolders[checkout] = map[string]bool{}
		}
		idx.checkoutHolders[checkout][task] = true
	}
	for _, resource := range resources {
		if err := ctx.Err(); err != nil {
			return ErrDenied
		}
		if resource.Kind == "task" {
			if idx.taskHolders[resource.Path] == nil {
				idx.taskHolders[resource.Path] = map[string]bool{}
			}
			idx.taskHolders[resource.Path][task] = true
		}
		if repository == "" {
			continue
		}
		if idx.anyClaims[repository] == nil {
			idx.anyClaims[repository] = map[string]bool{}
		}
		idx.anyClaims[repository][task] = true
		if resource.Kind == "checkout" {
			if idx.checkoutClaims[repository] == nil {
				idx.checkoutClaims[repository] = map[string]bool{}
			}
			idx.checkoutClaims[repository][task] = true
		}
		if resource.Kind == "path" && pathclaims.ValidatePath(resource.Path) == nil {
			queueInsertPath(idx, repository, resource.Path, task)
		}
	}
	return nil
}

func queueInsertPath(idx *queueProjectionIndex, repository, path, task string) {
	if idx.pathHolders[repository] == nil {
		idx.pathHolders[repository] = &queuePathNode{children: map[string]*queuePathNode{}, holders: map[string]bool{}}
	}
	node := idx.pathHolders[repository]
	for _, part := range strings.Split(path, "/") {
		if node.children[part] == nil {
			node.children[part] = &queuePathNode{children: map[string]*queuePathNode{}, holders: map[string]bool{}}
		}
		node = node.children[part]
	}
	node.holders[task] = true
}

func queuePathConflict(node *queuePathNode, path, task string) bool {
	if node == nil {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		for holder := range node.holders {
			if holder != task {
				return true
			}
		}
		node = node.children[part]
		if node == nil {
			return false
		}
	}
	var hasOther func(*queuePathNode) bool
	hasOther = func(current *queuePathNode) bool {
		for holder := range current.holders {
			if holder != task {
				return true
			}
		}
		for _, child := range current.children {
			if hasOther(child) {
				return true
			}
		}
		return false
	}
	return hasOther(node)
}

func queueHasPathHold(idx *queueProjectionIndex, repository, task string, resources []Resource) bool {
	for _, resource := range resources {
		if resource.Kind == "path" && pathclaims.ValidatePath(resource.Path) == nil && queuePathConflict(idx.pathHolders[repository], resource.Path, task) {
			return true
		}
		if resource.Kind == "task" {
			for holder := range idx.taskHolders[resource.Path] {
				if holder != task {
					return true
				}
			}
		}
	}
	return false
}

func queueHasCheckoutHold(idx *queueProjectionIndex, repository, checkout, task string, resources []Resource) bool {
	for holder := range idx.checkoutHolders[checkout] {
		if checkout != "" && holder != task {
			return true
		}
	}
	for _, resource := range resources {
		if resource.Kind == "checkout" {
			for holder := range idx.anyClaims[repository] {
				if holder != task {
					return true
				}
			}
		}
		if repository != "" {
			for holder := range idx.checkoutClaims[repository] {
				if holder != task {
					return true
				}
			}
		}
	}
	return false
}

func queueClassifyIndexed(s *state, task QueueCanonicalTask, idx *queueProjectionIndex, now time.Time) (string, []QueueDiagnosticReason, string) {
	reasons := []QueueDiagnosticReason{}
	retained := idx.retained[task.TaskID]
	if idx.running[task.TaskID] {
		return "running", reasons, "current"
	}
	if idx.stages[task.TaskID]["integration"] {
		return "integration", []QueueDiagnosticReason{{Code: "integration_pending", Knowledge: "known"}}, "current"
	}
	if idx.stages[task.TaskID]["review"] || strings.EqualFold(task.Status, "review") {
		return "review", []QueueDiagnosticReason{{Code: "review_pending", Knowledge: "known"}}, "current"
	}
	if idx.stages[task.TaskID]["check"] || strings.EqualFold(task.Status, "check") {
		return "check", []QueueDiagnosticReason{{Code: "verification_pending", Knowledge: "known"}}, "current"
	}
	if retained {
		reasons = append(reasons, QueueDiagnosticReason{Code: "retained_protection", Knowledge: "known"})
	}
	if idx.verificationUnknown[task.TaskID] {
		reasons = append(reasons, QueueDiagnosticReason{Code: "verification_unknown", Knowledge: "unknown"})
	}
	if code := canonicalQueueReason(task.Reason); code != "" {
		reasons = append(reasons, QueueDiagnosticReason{Code: code, Knowledge: "known"})
	}
	binding, bound := s.Tasks[task.TaskID]
	if !bound || binding.ProjectID != task.ProjectID || !binding.AdmissionEnabled {
		reasons = append(reasons, QueueDiagnosticReason{Code: "owner_binding_missing", Knowledge: "known"})
	}
	claimsValid := len(task.Resources) > 0
	for _, resource := range task.Resources {
		switch resource.Kind {
		case "path":
			if pathclaims.ValidatePath(resource.Path) != nil {
				claimsValid = false
			}
		case "task":
			if resource.Path != task.TaskID {
				claimsValid = false
			}
		case "checkout":
			if resource.Path != "" {
				claimsValid = false
			}
		default:
			claimsValid = false
		}
	}
	if len(task.Resources) == 0 {
		reasons = append(reasons, QueueDiagnosticReason{Code: "claims_missing", Knowledge: "known"})
	} else if !claimsValid {
		reasons = append(reasons, QueueDiagnosticReason{Code: "claims_invalid", Knowledge: "known"})
	}
	if bound && binding.ProjectID == task.ProjectID && queueHasPathHold(idx, binding.Repository, task.TaskID, task.Resources) {
		reasons = append(reasons, QueueDiagnosticReason{Code: "path_hold", Knowledge: "known"})
	}
	placement := s.SwarmPolicies[task.ProjectID].Placements[task.TaskID]
	if bound && binding.ProjectID == task.ProjectID && queueHasCheckoutHold(idx, binding.Repository, placement.Checkout, task.TaskID, task.Resources) {
		reasons = append(reasons, QueueDiagnosticReason{Code: "checkout_hold", Knowledge: "known"})
	}
	if code, knowledge := queueRolePolicyReasonWithOccupancy(s, task.ProjectID, task.TaskID, now, idx.roleUsed); code != "" {
		reasons = append(reasons, QueueDiagnosticReason{Code: code, Knowledge: knowledge})
	}
	if s.Version >= 11 {
		// Schema 11+ admission requires a configured, unexpired task spec and a
		// fresh observer result. The read path cannot run that observer or inspect
		// profiles; missing, expired, or merely historical reports stay unknown.
		reasons = append(reasons, QueueDiagnosticReason{Code: "preflight_unknown", Knowledge: "unknown"})
	}
	if !strings.EqualFold(task.Status, "ready") && !strings.EqualFold(task.Status, "open") && !strings.EqualFold(task.Status, "queued") {
		reasons = append(reasons, QueueDiagnosticReason{Code: "canonical_status_blocked", Knowledge: "known"})
	}
	freshness := "current"
	if s.Version < 22 {
		reasons = append(reasons, QueueDiagnosticReason{Code: "host_capacity_unsupported", Knowledge: "unsupported"})
		freshness = "unsupported"
	} else if policy, ok := s.SwarmPolicies[task.ProjectID]; !ok || policy.HostSlots == nil {
		reasons = append(reasons, QueueDiagnosticReason{Code: "host_capacity_unknown", Knowledge: "unknown"})
		freshness = "unknown"
	} else if policy.HostSlots.Mode != HostSlotSingleAuthority {
		reasons = append(reasons, QueueDiagnosticReason{Code: "host_capacity_unsupported", Knowledge: "unsupported"})
		freshness = "unsupported"
	} else {
		view := idx.hostView
		if !view.Known {
			code, age := "host_capacity_unknown", "unknown"
			if view.Reason == "fresh_census_required" {
				code, age, freshness = "host_observation_stale", "unknown", "stale"
			}
			reasons = append(reasons, QueueDiagnosticReason{Code: code, Knowledge: age})
		} else if view.RemainingAdmissibleSlots == 0 {
			reasons = append(reasons, QueueDiagnosticReason{Code: "host_cap_exhausted", Knowledge: "known"})
		}
	}
	if len(reasons) > 0 {
		return "queued", boundedQueueReasons(reasons), freshness
	}
	return "ready", []QueueDiagnosticReason{}, freshness
}

func queueRolePolicyReason(s *state, project, task string, now time.Time) (string, string) {
	policy, ok := s.SwarmPolicies[project]
	used := map[string]int{}
	if ok {
		used, _, _ = swarmOccupancy(s, policy, SwarmSnapshot{})
	}
	return queueRolePolicyReasonWithOccupancy(s, project, task, now, used)
}

func queueRolePolicyReasonWithOccupancy(s *state, project, task string, now time.Time, used map[string]int) (string, string) {
	if s.Version < 7 {
		return "role_model_unknown", "unsupported"
	}
	policy, ok := s.SwarmPolicies[project]
	if !ok {
		return "role_model_unknown", "unknown"
	}
	if policy.Stopped {
		return "canonical_status_blocked", "known"
	}
	placement, ok := policy.Placements[task]
	if !ok {
		return "compatible_role_missing", "known"
	}
	implementation, review := false, false
	implementKinds, reviewKinds := []SwarmRole{}, []SwarmRole{}
	for _, role := range policy.Roles {
		if role.Harness != policy.Harness || role.Capacity < 1 {
			continue
		}
		if role.Kind == placement.ImplementKind && role.ID != "" {
			implementKinds = append(implementKinds, role)
		}
		if role.Kind == placement.ReviewKind && role.ID != "" {
			reviewKinds = append(reviewKinds, role)
		}
	}
	for _, implementRole := range implementKinds {
		for _, reviewRole := range reviewKinds {
			if implementRole.ID != reviewRole.ID && used[implementRole.ID] < implementRole.Capacity && used[reviewRole.ID] < reviewRole.Capacity {
				implementation, review = true, true
				break
			}
		}
		if implementation && review {
			break
		}
	}
	if !implementation || !review {
		return "compatible_role_missing", "known"
	}
	if routing, routed := s.ModelRoutingPolicies[project]; routed {
		taskRouting, present := routing.Tasks[task]
		if !present || len(taskRouting.Candidates) == 0 || routing.ProjectID != project || routing.Revision == "" || routing.ExpiresAt.IsZero() || !now.Before(routing.ExpiresAt) {
			return "role_model_unknown", "unknown"
		}
		available, knownUnavailable, unknown := false, false, false
		for _, candidate := range taskRouting.Candidates {
			fresh := !candidate.ObservedAt.IsZero() && !candidate.ExpiresAt.IsZero() && !candidate.ObservedAt.After(now.Add(time.Minute)) && now.Before(candidate.ExpiresAt) && now.Before(candidate.ObservedAt.Add(time.Hour))
			if !fresh {
				unknown = true
				continue
			}
			if candidate.Available && candidate.Model != "" {
				available = true
			} else if !candidate.Available {
				knownUnavailable = true
			} else {
				unknown = true
			}
		}
		if available {
			return "", ""
		}
		if unknown {
			return "role_model_unknown", "unknown"
		}
		if knownUnavailable {
			return "model_unavailable", "known"
		}
		return "role_model_unknown", "unknown"
	}
	return "", ""
}

func canonicalQueueReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "dependencies_unsatisfied", "dependency_invalid", "nonimplementation_task", "priority_wont", "canonical_status_blocked", "owner_binding_missing", "claims_missing", "claims_invalid", "path_hold", "checkout_hold", "compatible_role_missing", "model_unavailable", "role_model_unknown", "preflight_unknown", "host_cap_exhausted", "host_capacity_unknown", "host_capacity_unsupported", "host_observation_stale", "verification_pending", "verification_failed", "verification_unknown", "review_pending", "review_unknown", "integration_pending", "execution_state_unknown":
		return strings.ToLower(strings.TrimSpace(reason))
	default:
		return ""
	}
}

func boundedQueueReasons(in []QueueDiagnosticReason) []QueueDiagnosticReason {
	seen := map[string]string{}
	for _, r := range in {
		if !validQueueReasonCode(r.Code) || (r.Knowledge != "known" && r.Knowledge != "unknown" && r.Knowledge != "unsupported") {
			continue
		}
		if old, ok := seen[r.Code]; !ok || r.Knowledge < old {
			seen[r.Code] = r.Knowledge
		}
	}
	codes := make([]string, 0, len(seen))
	for code := range seen {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	out := make([]QueueDiagnosticReason, 0, 8)
	for _, code := range codes {
		if len(out) == 7 {
			out = append(out, QueueDiagnosticReason{Code: "additional_diagnostics_omitted", Knowledge: "unknown"})
			break
		}
		out = append(out, QueueDiagnosticReason{Code: code, Knowledge: seen[code]})
	}
	return out
}

func validQueueReasonCode(code string) bool {
	switch code {
	case "dependencies_unsatisfied", "dependency_invalid", "nonimplementation_task", "priority_wont", "canonical_status_blocked", "owner_binding_missing", "claims_missing", "claims_invalid", "path_hold", "checkout_hold", "retained_protection", "compatible_role_missing", "model_unavailable", "role_model_unknown", "preflight_unknown", "host_cap_exhausted", "host_capacity_unknown", "host_capacity_unsupported", "host_observation_stale", "provider_quota_unknown", "verification_pending", "verification_failed", "verification_unknown", "review_pending", "review_unknown", "integration_pending", "execution_state_unknown", "additional_diagnostics_omitted":
		return true
	default:
		return false
	}
}

func queueFreshRunningDispatch(s *state, dispatchID string, dispatch DispatchReservation, now time.Time) bool {
	if s.Version < 9 {
		return false
	}
	project, task := dispatch.ProjectID, dispatch.Reservation.TaskID
	policy, ok := s.LifecyclePolicies[project]
	if !ok || policy.Freshness <= 0 {
		return false
	}
	if dispatch.ProjectID != project || !validID(task) || queueDispatchRetired(s, dispatch) || hostSlotTerminalProof(s, dispatch) {
		return false
	}
	intent, ok := s.DispatchIntents[dispatchID]
	if !ok || intent.DispatchID != dispatchID || intent.ID == "" || intent.Reservation.ID != dispatch.ID || intent.Reservation.Reservation.Fence != dispatch.Reservation.Fence || intent.HostAgentID == "" || (intent.State != "host_correlated" && intent.State != "acknowledged") {
		return false
	}
	bridge, ok := s.Sessions[intent.BridgeSessionID]
	if !ok || authenticateRecord(s, intent.BridgeSessionID, now) != nil || !subset([]string{"dispatch.observe"}, bridge.Scope.Actions) || !subset([]string{"project:" + project}, bridge.Scope.Resources) {
		return false
	}
	history := s.LifecycleObservations[dispatchID]
	if len(history) == 0 {
		return false
	}
	last := history[len(history)-1]
	return last.Sequence == uint64(len(history)) && last.State == "running" && last.Source == policy.Source && last.Assurance == "cooperative-owner-provisioned-bridge" && validDigest(last.EvidenceDigest) && reflect.DeepEqual(last.Binding, lifecycleBinding(intent)) && !last.SourceTime.IsZero() && !last.ReceivedAt.IsZero() && !last.SourceTime.After(now.Add(time.Minute)) && now.Before(last.SourceTime.Add(policy.Freshness)) && now.Before(last.ReceivedAt.Add(policy.Freshness))
}

func queueDispatchRetired(s *state, dispatch DispatchReservation) bool {
	if _, terminal := s.TaskTerminals[dispatch.Reservation.TaskID]; terminal {
		return true
	}
	lease := dispatch.Reservation
	intent, hasIntent := s.DispatchIntents[dispatch.ID]
	if hasIntent && intent.Lease != nil {
		if stored, ok := s.Leases[intent.Lease.ID]; ok {
			lease = stored
		}
	}
	if _, retired := checkpointRetiredDispatch(s, dispatch, lease); retired {
		return true
	}
	if !dispatch.Converted {
		return !reservationRetained(s, dispatch)
	}
	if hasIntent && intent.Lease != nil {
		stored, ok := s.Leases[intent.Lease.ID]
		return !ok || stored.Released || stored.Fence != intent.Lease.Fence || stored.TaskID != dispatch.Reservation.TaskID || stored.SessionID != intent.WorkerSessionID
	}
	for _, stored := range s.Leases {
		if !stored.Released && stored.TaskID == dispatch.Reservation.TaskID && stored.SessionID == dispatch.WorkerSessionID && stored.Fence > 0 {
			return false
		}
	}
	return true
}

func queueTaskRetained(s *state, project, task string) bool {
	for _, dispatch := range s.Dispatches {
		if dispatch.ProjectID == project && dispatch.Reservation.TaskID == task && reservationRetained(s, dispatch) {
			return true
		}
	}
	for _, lease := range s.Leases {
		if !lease.Released && lease.TaskID == task && s.Tasks[task].ProjectID == project {
			return true
		}
	}
	return false
}

func queueHostDiagnostics(s *state, project string, now time.Time) []QueueHostDiagnostic {
	if s.Version < 22 {
		return []QueueHostDiagnostic{}
	}
	policy, ok := s.SwarmPolicies[project]
	if !ok || policy.HostSlots == nil {
		return []QueueHostDiagnostic{{State: "unknown", Reason: "host_capacity_unknown", Freshness: "unknown", Provenance: "unavailable", Mode: "unavailable", RetainedWriterCount: controllerOccupancy(s, project)}}
	}
	p := policy.HostSlots
	view := hostSlotAdmissionView(s, project, now)
	h := QueueHostDiagnostic{HostID: p.HostID, HostSessionID: p.HostSessionID, ExecutorInstanceID: p.ExecutorInstanceID, Runtime: p.Runtime, Build: p.Build, Mode: p.Mode, State: "unknown", Reason: safeHostReason(view.Reason), Freshness: "unknown", Provenance: "unavailable", RetainedWriterCount: view.RetainedWriterCount}
	if p.Mode != HostSlotSingleAuthority {
		h.State, h.Reason, h.Freshness = "unsupported", "host_capacity_unsupported", "unsupported"
		return []QueueHostDiagnostic{h}
	}
	if view.Known {
		h.State, h.Reason, h.Freshness, h.Provenance = "current", "admissible", "current", "authenticated-authority-census"
		latest, found := latestHostSlotObservation(s, *p)
		if found {
			capacity := s.CapacityPolicies[capacityFactKey(p.HostSessionID, p.ExecutorInstanceID)]
			qualification := s.CapacityQualifications[capacityFactKey(p.HostSessionID, p.ExecutorInstanceID)]
			expiry := latest.ReceivedAt.Add(capacity.Freshness)
			if latest.ObservedAt.Add(capacity.Freshness).Before(expiry) {
				expiry = latest.ObservedAt.Add(capacity.Freshness)
			}
			if observer, ok := s.CapacityObservers[latest.ObserverSessionID]; ok && observer.ExpiresAt.Before(expiry) {
				expiry = observer.ExpiresAt
			}
			if qualification := s.CapacityQualifications[capacityFactKey(p.HostSessionID, p.ExecutorInstanceID)]; !qualification.ExpiresAt.IsZero() && qualification.ExpiresAt.Before(expiry) {
				expiry = qualification.ExpiresAt
			}
			observed := latest.ObservedAt.UTC()
			h.ObservationDigest, h.ObservationSequence = latest.InputDigest, latest.Sequence
			h.ObservedAt, h.ExpiresAt = &observed, &expiry
			h.RequestedSlots = queueInt(capacity.RequestedSlots)
			h.DeclaredSlots = queueInt(latest.DeclaredSlots)
			h.QualifiedSlots = queueInt(qualification.QualifiedSlots)
			h.ObservedOccupied = queueInt(view.ObservedOccupied)
			h.IncludedLocalExecutions = queueInt(view.IncludedLocalExecutions)
			h.ExternalOccupied = queueInt(view.ExternalOccupied)
			h.OutstandingExecutionClaims = queueInt(queueOutstandingExecutionClaims(s, p.AuthorityDomainID))
			h.RemainingAdmissibleSlots = queueInt(view.RemainingAdmissibleSlots)
		}
	} else if view.Reason == "fresh_census_required" {
		h.Reason, h.Freshness = "host_observation_stale", "stale"
	}
	return []QueueHostDiagnostic{h}
}

func queueOutstandingExecutionClaims(s *state, domain string) int {
	return hostSlotClaimCount(s, domain)
}

func safeHostReason(reason string) string {
	switch reason {
	case "legacy_manual", "manual", "host_enrollment_unavailable", "policy_or_qualification_unknown", "fresh_census_required", "contradictory_census_or_capacity", "admissible":
		return reason
	default:
		return "unknown"
	}
}
func queueInt(n int) *int { v := n; return &v }
