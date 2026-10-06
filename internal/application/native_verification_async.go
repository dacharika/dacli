package application

import (
	"context"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"sync"
	"time"
)

// VerificationStatus reports durable knowledge, never inferred process progress.
type VerificationStatus struct {
	OperationKey string                             `json:"operation_key"`
	State        string                             `json:"state"`
	Evidence     *sessions.Evidence                 `json:"evidence"`
	Interruption *sessions.VerificationInterruption `json:"interruption,omitempty"`
}
type verificationJob struct {
	request NativeVerificationRequest
	state   string
	cancel  context.CancelFunc
}

// NativeVerificationJobs admits at most two immediate checks. No job queue and
// no completed receipt cache exist; authority remains the durable source.
type NativeVerificationJobs struct {
	selection *NativeVerificationProfile
	mu        sync.Mutex
	jobs      map[string]*verificationJob
	closed    bool
	wg        sync.WaitGroup
}

func (*NativeVerificationJobs) String() string   { return "NativeVerificationJobs" }
func (*NativeVerificationJobs) GoString() string { return "NativeVerificationJobs" }

func NewNativeVerificationJobs(selection *NativeVerificationProfile) (*NativeVerificationJobs, error) {
	if _, e := selection.Authenticate(); e != nil {
		return nil, e
	}
	return &NativeVerificationJobs{selection: selection, jobs: map[string]*verificationJob{}}, nil
}
func jobKey(r NativeVerificationRequest) string { return r.WorkerSession + ":" + r.OperationKey }
func lookupStatus(r NativeVerificationRequest, lookup sessions.VerificationLookup) VerificationStatus {
	state := "absent"
	switch lookup.State {
	case sessions.VerificationCompleted:
		state = "completed"
	case sessions.VerificationInterrupted:
		state = "interrupted"
	case sessions.VerificationPending:
		state = "reconciliation_required"
	}
	return VerificationStatus{OperationKey: r.OperationKey, State: state, Evidence: lookup.Evidence, Interruption: lookup.Interruption}
}
func (m *NativeVerificationJobs) access(r NativeVerificationRequest) (*NativeVerificationAccess, NativeVerificationRequest, sessions.VerificationLookup, error) {
	a, e := m.selection.Authenticate()
	if e != nil {
		return nil, r, sessions.VerificationLookup{}, e
	}
	r.WorkerSession = a.Worker.ID
	lookup, e := a.Lookup(r)
	return a, r, lookup, e
}
func (m *NativeVerificationJobs) Request(r NativeVerificationRequest) (VerificationStatus, error) {
	a, r, lookup, e := m.access(r)
	if e != nil {
		return VerificationStatus{}, e
	}
	if lookup.State == sessions.VerificationCompleted || lookup.State == sessions.VerificationInterrupted {
		return lookupStatus(r, lookup), nil
	}
	key := jobKey(r)
	m.mu.Lock()
	if job := m.jobs[key]; job != nil {
		if job.request != r {
			m.mu.Unlock()
			return VerificationStatus{}, sessions.ErrDenied
		}
		status := VerificationStatus{OperationKey: r.OperationKey, State: job.state}
		m.mu.Unlock()
		return status, nil
	}
	if lookup.State != sessions.VerificationAbsent {
		m.mu.Unlock()
		return lookupStatus(r, lookup), nil
	}
	// Capacity is checked before durable BeginVerification reserves an operation.
	if m.closed || len(m.jobs) >= 2 {
		m.mu.Unlock()
		return VerificationStatus{}, sessions.ErrDenied
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &verificationJob{r, "admitting", cancel}
	m.jobs[key] = job
	m.wg.Add(1)
	m.mu.Unlock()
	prepared, receipt, e := a.Runner.Prepare(a.workerToken, r)
	if e != nil || receipt != nil {
		cancel()
		m.mu.Lock()
		delete(m.jobs, key)
		m.mu.Unlock()
		m.wg.Done()
		if receipt != nil {
			return VerificationStatus{OperationKey: r.OperationKey, State: "completed", Evidence: receipt}, nil
		}
		return VerificationStatus{}, e
	}
	m.mu.Lock()
	if ctx.Err() == nil {
		job.state = "reserved"
	}
	status := VerificationStatus{OperationKey: r.OperationKey, State: job.state}
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer cancel()
		_, _ = prepared.Execute(ctx)
		m.mu.Lock()
		delete(m.jobs, key)
		m.mu.Unlock()
	}()
	return status, nil
}
func (m *NativeVerificationJobs) Status(r NativeVerificationRequest) (VerificationStatus, error) {
	return m.inspect(r, false)
}
func (m *NativeVerificationJobs) Cancel(r NativeVerificationRequest) (VerificationStatus, error) {
	return m.inspect(r, true)
}
func (m *NativeVerificationJobs) inspect(r NativeVerificationRequest, cancel bool) (VerificationStatus, error) {
	_, r, lookup, e := m.access(r)
	if e != nil {
		return VerificationStatus{}, e
	}
	// Durable result and interruption receipts are definitive even while an
	// execution goroutine exits.
	if lookup.State == sessions.VerificationCompleted || lookup.State == sessions.VerificationInterrupted {
		return lookupStatus(r, lookup), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[jobKey(r)]; job != nil {
		if job.request != r {
			return VerificationStatus{}, sessions.ErrDenied
		}
		if cancel {
			job.cancel()
			job.state = "cancellation_requested"
		}
		return VerificationStatus{OperationKey: r.OperationKey, State: job.state}, nil
	}
	return lookupStatus(r, lookup), nil
}

// Close cancels every immediate job and bounds server teardown. Any durable
// reservation without a committed receipt remains owner-reconciled, never reset.
func (m *NativeVerificationJobs) Close() error {
	m.mu.Lock()
	m.closed = true
	for _, job := range m.jobs {
		job.cancel()
		job.state = "cancellation_requested"
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(6 * time.Second):
		return sessions.ErrReconcile
	}
}
