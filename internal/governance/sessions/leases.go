package sessions

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
)

// LeaseAdmission is an owner-configured read adapter. It must freshly resolve
// canonical task readiness in the explicit workspace; it cannot mutate authority.
type LeaseAdmission interface {
	AdmitLease(workspace, project, task string) error
}

type Resource struct {
	Kind string
	Path string
}
type AcquireLeaseRequest struct {
	OperationKey string
	AttemptID    string
	Resources    []Resource
	ExpiresAt    time.Time
}
type RenewLeaseRequest struct {
	OperationKey  string
	AttemptID     string
	LeaseID       string
	ExpectedFence uint64
	ExpiresAt     time.Time
}
type ReleaseLeaseRequest struct {
	OperationKey  string
	AttemptID     string
	LeaseID       string
	ExpectedFence uint64
}

// Lease is historical evidence, never a capability for later mutation.
type Lease struct {
	ID         string
	AttemptID  string
	SessionID  string
	TaskID     string
	Repository string
	Checkout   string
	Resources  []Resource
	Fence      uint64
	ExpiresAt  time.Time
	Released   bool
}
type operation struct {
	Digest                 string
	PublicationDecision    *PublicationDecision    `json:",omitempty"`
	PublicationConsumption *PublicationConsumption `json:",omitempty"`
	Receipt                Lease
	Review                 *Review          `json:",omitempty"`
	Revision               *AttemptRevision `json:",omitempty"`
	Evidence               *Evidence        `json:",omitempty"`
	ResultDigest           string           `json:",omitempty"`
	Pending                bool             `json:",omitempty"`
}

// UpgradeLeases explicitly migrates v2 without dropping sessions or attempts.
func (a *Authority) UpgradeLeases(owner string) error {
	old := *a
	old.version = 2
	return old.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		initializePublication(s)
		s.Version = 6
		initializeEvidence(s)
		initializeReviews(s)
		s.Leases = map[string]Lease{}
		s.Operations = map[string]operation{}
		s.Fence = 0
		return nil
	})
}

// Paths use a conservative portable alphabet and lowercase only. Rejecting case
// variants avoids aliases on case-insensitive filesystems without folding names.
func canonicalResources(root, task string, in []Resource) ([]Resource, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil || canonicalRoot != root {
		return nil, ErrDenied
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, ErrDenied
	}
	out := []Resource{{Kind: "task", Path: task}}
	for _, r := range in {
		switch r.Kind {
		case "task":
			if r.Path != task {
				return nil, ErrDenied
			}
		case "checkout":
			if r.Path != "" {
				return nil, ErrDenied
			}
		case "path":
			if r.Path == "" || filepath.IsAbs(r.Path) || strings.ToLower(r.Path) != r.Path || filepath.ToSlash(filepath.Clean(r.Path)) != r.Path {
				return nil, ErrDenied
			}
			for _, seg := range strings.Split(r.Path, "/") {
				if seg == "." || seg == ".." || seg == ".git" || seg == "" {
					return nil, ErrDenied
				}
				for _, c := range seg {
					if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
						return nil, ErrDenied
					}
				}
			}
			p := root
			for _, seg := range strings.Split(r.Path, "/") {
				p = filepath.Join(p, seg)
				info, err := os.Lstat(p)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil || info.Mode()&os.ModeSymlink != 0 {
					return nil, ErrDenied
				}
			}
		default:
			return nil, ErrDenied
		}
		out = append(out, r)
	}
	if len(in) == 0 {
		return nil, ErrDenied
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind == out[j].Kind {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	unique := out[:0]
	for _, r := range out {
		if len(unique) == 0 || unique[len(unique)-1] != r {
			unique = append(unique, r)
		}
	}
	return unique, nil
}
func prefix(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func overlaps(a, b Lease) bool {
	if a.Checkout == b.Checkout {
		return true
	} // one cooperative writer per checkout
	for _, x := range a.Resources {
		for _, y := range b.Resources {
			if x.Kind == "task" && y.Kind == "task" && x.Path == y.Path {
				return true
			}
			if a.Repository == b.Repository && x.Kind == "path" && y.Kind == "path" && prefix(x.Path, y.Path) {
				return true
			}
			if a.Repository == b.Repository && (x.Kind == "checkout" || y.Kind == "checkout") {
				return true
			}
		}
	}
	return false
}
func attemptFor(s *state, id, session string) (Attempt, error) {
	for _, a := range s.Attempts {
		if a.ID == id && a.SessionID == session {
			return a, nil
		}
	}
	return Attempt{}, ErrDenied
}
func leaseAuth(s *state, token, session, attemptID, action string, now time.Time) (record, Attempt, error) {
	r, err := authenticate(s, token, session, now)
	if err != nil {
		return record{}, Attempt{}, err
	}
	at, err := attemptFor(s, attemptID, session)
	if err != nil {
		return record{}, Attempt{}, err
	}
	if workerTaskWithdrawn(s, session, at.Request.TaskID) {
		return record{}, Attempt{}, ErrDenied
	}
	if !subset([]string{action}, r.Scope.Actions) || !subset([]string{"task:" + at.Request.TaskID}, r.Scope.Resources) {
		return record{}, Attempt{}, ErrDenied
	}
	return r, at, nil
}
func identity(action, session string, req any) string {
	b, _ := json.Marshal(req)
	return hash(action + "\x00" + session + "\x00" + string(b))
}
func retry(s *state, key, digest string) (Lease, bool, error) {
	op, ok := s.Operations[key]
	if ok && op.Digest != digest {
		return Lease{}, true, ErrDenied
	}
	return op.Receipt, ok, nil
}

// checkLeaseCheckout is called only after authentication and historical retry
// recovery, while the authority lock is held. Observation does not freeze the
// filesystem: cooperative checkout isolation remains required.
func checkLeaseCheckout(at Attempt) error {
	obs, err := checkout.ObserveRepositoryIdentity(at.Observation.Locator)
	if err != nil || obs.Locator != at.Observation.Locator || obs.Repository != at.Observation.Repository {
		return ErrDenied
	}
	return nil
}

func (a *Authority) AcquireLease(token, session string, req AcquireLeaseRequest) (Lease, error) {
	var result Lease
	err := a.transaction(false, true, func(s *state) error {
		now := time.Now()
		r, at, err := leaseAuth(s, token, session, req.AttemptID, "lease.acquire", now)
		if err != nil {
			return err
		}
		if !validID(req.OperationKey) {
			return ErrDenied
		}
		resources, err := canonicalResources(at.Observation.Locator, at.Request.TaskID, req.Resources)
		if err != nil {
			return err
		}
		req.Resources = resources
		req.ExpiresAt = req.ExpiresAt.UTC()
		for _, x := range resources {
			if x.Kind != "task" && !subset([]string{x.Kind + ":" + x.Path}, r.Scope.Resources) {
				return ErrDenied
			}
		}
		digest := identity("acquire", session, req)
		if old, ok, e := retry(s, req.OperationKey, digest); ok {
			result = old
			return e
		}
		if !req.ExpiresAt.After(now) || req.ExpiresAt.After(r.ExpiresAt) || a.admission == nil {
			return ErrDenied
		}
		if err := a.admission.AdmitLease(s.Binding.Workspace, at.ProjectID, at.Request.TaskID); err != nil {
			return ErrDenied
		}
		if err := checkLeaseCheckout(at); err != nil {
			return err
		}
		// Admission and checkout observation may take time. Recheck the temporal boundary before reserving.
		now = time.Now()
		if _, err := authenticate(s, token, session, now); err != nil {
			return err
		}
		if !req.ExpiresAt.After(now) {
			return ErrDenied
		}
		result = Lease{AttemptID: at.ID, SessionID: session, TaskID: at.Request.TaskID, Repository: at.Observation.Repository, Checkout: at.Observation.Locator, Resources: resources, ExpiresAt: req.ExpiresAt}
		// Timeout/revocation do not prove stopped writers. Only authenticated release
		// frees reservations. Fresh isolated checkouts may acquire disjoint resources.
		for _, old := range s.Leases {
			if !old.Released && overlaps(result, old) {
				return ErrDenied
			}
		}
		for _, dispatch := range s.Dispatches {
			if reservationRetained(s, dispatch) && overlaps(result, dispatch.Reservation) {
				return ErrDenied
			}
		}
		if s.Fence == math.MaxUint64 {
			return ErrDenied
		}
		id, err := mint()
		if err != nil {
			return err
		}
		s.Fence++
		result.ID = id
		result.Fence = s.Fence
		s.Leases[id] = result
		s.Operations[req.OperationKey] = operation{Digest: digest, Receipt: result}
		return nil
	})
	if err != nil {
		return Lease{}, err
	}
	return result, nil
}
func (a *Authority) RenewLease(token, session string, req RenewLeaseRequest) (Lease, error) {
	req.ExpiresAt = req.ExpiresAt.UTC()
	return a.changeLease(token, session, req.OperationKey, req.AttemptID, req.LeaseID, req.ExpectedFence, req.ExpiresAt, false, identity("renew", session, req))
}
func (a *Authority) ReleaseLease(token, session string, req ReleaseLeaseRequest) (Lease, error) {
	return a.changeLease(token, session, req.OperationKey, req.AttemptID, req.LeaseID, req.ExpectedFence, time.Time{}, true, identity("release", session, req))
}
func (a *Authority) changeLease(token, session, key, attempt, id string, fence uint64, expires time.Time, release bool, digest string) (Lease, error) {
	var result Lease
	action := "lease.renew"
	if release {
		action = "lease.release"
	}
	err := a.transaction(false, true, func(s *state) error {
		now := time.Now()
		r, at, err := leaseAuth(s, token, session, attempt, action, now)
		if err != nil {
			return err
		}
		if !validID(key) {
			return ErrDenied
		}
		old, ok := s.Leases[id]
		if !ok || old.SessionID != session || old.AttemptID != attempt || old.Fence != fence {
			return ErrDenied
		}
		if receipt, ok, e := retry(s, key, digest); ok {
			result = receipt
			return e
		}
		if err := checkLeaseCheckout(at); err != nil {
			return err
		}
		now = time.Now()
		if _, _, err := leaseAuth(s, token, session, attempt, action, now); err != nil {
			return err
		}
		if !currentLease(s, old, now) {
			return ErrDenied
		}
		if release {
			old.Released = true
		} else {
			if !expires.After(now) || expires.After(r.ExpiresAt) {
				return ErrDenied
			}
			old.ExpiresAt = expires
		}
		result = old
		s.Leases[id] = old
		s.Operations[key] = operation{Digest: digest, Receipt: old}
		return nil
	})
	if err != nil {
		return Lease{}, err
	}
	return result, nil
}

func currentLease(s *state, l Lease, now time.Time) bool {
	return !l.Released && now.Before(l.ExpiresAt) && !workerTaskWithdrawn(s, l.SessionID, l.TaskID)
}
