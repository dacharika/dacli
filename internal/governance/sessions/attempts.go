package sessions

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
)

// TaskBinding is an owner-attested reference, never a task status authority.
type TaskBinding struct {
	TaskID           string
	ProjectID        string
	Repository       string
	AdmissionEnabled bool
}

type AttemptRequest struct {
	RequestID string
	TaskID    string
	Checkout  string
	HostLabel string // untrusted correlation only
}

type Attempt struct {
	ID           string
	SessionID    string
	Request      AttemptRequest
	ProjectID    string
	Observation  checkout.Observation
	RegisteredAt time.Time
}

// Upgrade explicitly migrates v1 under the same lock, preserving all hashes,
// session IDs and revocations. Current operations refuse v1 and unknown schemas.
func (a *Authority) Upgrade(owner string) error {
	legacy := *a
	legacy.version = 1
	return legacy.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		initializePublication(s)
		s.Version = 6
		initializeEvidence(s)
		initializeReviews(s)
		s.Leases = map[string]Lease{}
		s.Operations = map[string]operation{}
		s.Tasks = map[string]TaskBinding{}
		s.Attempts = map[string]Attempt{}
		return nil
	})
}

func validID(id string) bool {
	return id != "" && len(id) <= 256 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "/\\\x00\r\n")
}

// BindTask requires owner credentials. Existing references are immutable except
// admission policy, so a task cannot silently change repository or project identity.
func (a *Authority) BindTask(owner string, b TaskBinding) error {
	if !validID(b.TaskID) || !validID(b.ProjectID) || !filepath.IsAbs(b.Repository) {
		return ErrDenied
	}
	return a.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		if old, ok := s.Tasks[b.TaskID]; ok && (old.ProjectID != b.ProjectID || old.Repository != b.Repository) {
			return ErrDenied
		}
		if s.Tasks == nil {
			return ErrStorage
		}
		s.Tasks[b.TaskID] = b
		return nil
	})
}

// RegisterAttempt observes an externally created checkout, then authenticates
// current credentials, checks exact scope and persists under one authority lock.
// It accepts no authenticated snapshot or caller-supplied digests.
func (a *Authority) RegisterAttempt(token, sessionID string, req AttemptRequest) (Attempt, error) {
	if !validID(req.RequestID) || !validID(req.TaskID) || len(req.HostLabel) > 256 {
		return Attempt{}, ErrDenied
	}
	// Refuse unauthenticated observation requests before invoking even bounded
	// local Git. This preflight is not a reusable authorization snapshot: the
	// persistence transaction below reauthenticates and rechecks every binding.
	if err := a.transaction(false, false, func(s *state) error {
		r, err := authenticate(s, token, sessionID, time.Now())
		if err != nil {
			return err
		}
		if !subset([]string{"attempt.register"}, r.Scope.Actions) || !subset([]string{"task:" + req.TaskID}, r.Scope.Resources) {
			return ErrDenied
		}
		b, ok := s.Tasks[req.TaskID]
		if !ok || !b.AdmissionEnabled {
			return ErrDenied
		}
		return nil
	}); err != nil {
		return Attempt{}, err
	}
	obs, err := checkout.Observe(req.Checkout)
	if err != nil {
		return Attempt{}, ErrDenied
	}
	var result Attempt
	err = a.transaction(false, true, func(s *state) error {
		r, err := authenticate(s, token, sessionID, time.Now())
		if err != nil {
			return err
		}
		if !subset([]string{"attempt.register"}, r.Scope.Actions) || !subset([]string{"task:" + req.TaskID}, r.Scope.Resources) {
			return ErrDenied
		}
		b, ok := s.Tasks[req.TaskID]
		if !ok || !b.AdmissionEnabled || b.Repository != obs.Repository {
			return ErrDenied
		}
		if s.Attempts == nil {
			return ErrStorage
		}
		if old, ok := s.Attempts[req.RequestID]; ok {
			if old.SessionID != sessionID || old.Request != req || old.Observation.Locator != obs.Locator || old.Observation.Commit != obs.Commit || old.Observation.Tree != obs.Tree {
				return ErrDenied
			}
			result = old
			return nil
		}
		id, err := mint()
		if err != nil {
			return err
		}
		result = Attempt{id, sessionID, req, b.ProjectID, obs, time.Now().UTC()}
		s.Attempts[req.RequestID] = result
		return nil
	})
	return result, err
}
