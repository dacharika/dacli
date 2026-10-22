// Package sessions authenticates scoped bearer credentials independently of hosts.
// Bootstrap is a trusted owner-only operation: the caller must establish the
// human's authority out of band and protect the explicit absolute storage path.
// Host names, cwd and environment variables never establish identity. Credentials
// are returned once; only SHA-256 hashes are persisted. Anyone able to replace
// local authority files can forge authority: this is cooperative local-file
// integrity, not protection against a hostile OS user. Revocation prevents future
// authentication, not already-running actions; consumers must authenticate at
// their write boundary. A crashed writer can leave a lock: an owner may remove
// it only after confirming no writer remains. Locks are never stolen automatically.
package sessions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

var (
	ErrDenied  = errors.New("session authority denied")
	ErrStorage = errors.New("session authority storage unavailable")
)

// Binding is supplied explicitly at every authentication boundary.
type Binding struct {
	Workspace    string
	Installation string
}
type Scope struct {
	Actions   []string
	Resources []string
}
type Session struct {
	ID        string
	Parent    string
	Scope     Scope
	ExpiresAt time.Time
	Revoked   bool
}
type record struct {
	Session
	Hash string
}
type state struct {
	NativeExternalPolicies   map[string]NativeExternalPolicy
	NativeExternalHistory    map[string]NativeExternalPolicy
	NativeExternalOperations map[string]NativeExternalRecord

	NativeIntegrationPolicies   map[string]NativeIntegrationPolicy
	NativeIntegrationHistory    map[string]NativeIntegrationPolicy
	NativeIntegrationOperations map[string]NativeIntegrationRecord

	ModelRoutingPolicies   map[string]NativeModelRoutingPolicy
	ModelRoutingHistory    map[string]NativeModelRoutingPolicy
	ModelRoutingSelections map[string]NativeModelRoutingSelection

	ControllerPolicies      map[string]ControllerPolicy
	ControllerPolicyHistory map[string]ControllerPolicy
	ControllerInvocations   map[string]controllerInvocation
	ControllerHistory       map[string][]ControllerStatus
	ControllerOperations    map[string]controllerReceipt

	NativePolicies             map[string]NativePolicy
	NativePolicyHistory        map[string]NativePolicy
	NativePolicyDecisions      map[string]NativePolicyDecision
	NativePolicyApprovals      map[string]NativePolicyApproval
	NativePolicyConsumptions   map[string]NativePolicyConsumption
	NativePolicyOutcomes       map[string]NativePolicyOutcome
	TaskTerminals              map[string]NativeAcceptanceRecord
	NativeAcceptanceOperations map[string]NativeAcceptanceRecord

	BudgetPolicies      map[string]BudgetPolicy
	BudgetPolicyHistory map[string]map[string]BudgetPolicy
	BudgetAllocations   map[string]BudgetAllocation
	BudgetOperations    map[string]BudgetReceipt

	CheckpointPolicies      map[string]CheckpointPolicy
	CheckpointPolicyHistory map[string]map[string]CheckpointPolicy
	Checkpoints             map[string][]CheckpointStatus
	CheckpointOperations    map[string]CheckpointReceipt

	PreflightPolicies      map[string]PreflightPolicy
	PreflightPolicyHistory map[string]map[string]PreflightPolicy
	CapacityOverrides      map[string]CapacityOverride
	PreflightOperations    map[string]PreflightReceipt
	PreflightRetryBudgets  map[string]int

	RecoveryPolicies      map[string]RecoveryPolicy
	RecoveryPolicyHistory map[string]map[string]RecoveryPolicy
	Recoveries            map[string]RecoveryReceipt
	RecoveryOperations    map[string]RecoveryReceipt

	LifecyclePolicyHistory map[string]map[string]LifecyclePolicy
	LifecyclePolicies      map[string]LifecyclePolicy
	LifecycleObservations  map[string][]LifecycleObservation
	LifecycleCancellations map[string]LifecycleCancellation
	LifecycleOperations    map[string]LifecycleReceipt

	DispatchIntents          map[string]DispatchIntent
	DispatchIntentOperations map[string]DispatchIntentReceipt

	SwarmPolicies        map[string]SwarmPolicy
	Dispatches           map[string]DispatchReservation
	SwarmOperations      map[string]SwarmReceipt
	Version              int
	Binding              Binding
	OwnerHash            string
	Sessions             map[string]record
	Tasks                map[string]TaskBinding
	Attempts             map[string]Attempt
	Leases               map[string]Lease
	Operations           map[string]operation
	Fence                uint64
	Revisions            map[string][]AttemptRevision
	Grants               map[string]VerificationGrant
	Evidence             map[string]Evidence
	ReviewGrants         map[string]ReviewGrant
	Reviews              map[string]Review
	PublicationPolicies  map[string][]PublicationPolicy
	PublicationDecisions map[string]PublicationDecision
	PublicationConsumed  map[string]PublicationConsumption
}

// Authority contains no cached authority snapshot; each operation reloads under lock.
type Authority struct {
	path            string
	binding         Binding
	version         int
	admission       LeaseAdmission
	reviewObserver  ReviewObserver
	persistOverride func([]byte) error // private fault-injection seam; nil uses atomic file persistence
}

func Open(path string, binding Binding, admission ...LeaseAdmission) (*Authority, error) {
	if !filepath.IsAbs(path) || binding.Workspace == "" || binding.Installation == "" {
		return nil, ErrDenied
	}
	// Parent-directory aliases must share both authority and lock paths.
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, ErrStorage
	}
	path = filepath.Join(parent, filepath.Base(path))
	if err := regularOrAbsent(path); err != nil {
		return nil, err
	}
	a := &Authority{path: path, binding: binding, version: 6}
	if len(admission) > 1 {
		return nil, ErrDenied
	}
	if len(admission) == 1 {
		a.admission = admission[0]
	}
	return a, nil
}

// An authority-file alias is different from a directory alias: atomic rename
// would replace the symlink and silently fork the authority. Reject it, also
// at transaction time in case it appeared after Open. These checks prevent
// accidental aliasing, not replacement by a hostile local filesystem writer.
func regularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return ErrStorage
	}
	return nil
}

func mint() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", ErrStorage
	}
	return hex.EncodeToString(b), nil
}
func hash(token string) string { h := sha256.Sum256([]byte(token)); return hex.EncodeToString(h[:]) }
func validScope(s Scope) bool {
	if len(s.Actions) == 0 || len(s.Resources) == 0 {
		return false
	}
	for _, set := range [][]string{s.Actions, s.Resources} {
		seen := map[string]bool{}
		for _, v := range set {
			if v == "" || seen[v] {
				return false
			}
			seen[v] = true
		}
	}
	return true
}
func subset(child, parent []string) bool {
	for _, v := range child {
		found := false
		for _, p := range parent {
			if v == p {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (a *Authority) transaction(create bool, write bool, fn func(*state) error) error {
	lock := a.path + ".lock"
	var f *os.File
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := regularOrAbsent(lock); err != nil {
			return err
		}
		f, err = os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			break
		}
		if !os.IsExist(err) || time.Now().After(deadline) {
			return ErrStorage
		}
		time.Sleep(time.Millisecond * 5)
	}
	_ = f.Close()
	defer func() { _ = os.Remove(lock) }()
	if err := regularOrAbsent(a.path); err != nil {
		return err
	}
	s := state{ReviewGrants: map[string]ReviewGrant{}, Reviews: map[string]Review{}, Revisions: map[string][]AttemptRevision{}, Grants: map[string]VerificationGrant{}, Evidence: map[string]Evidence{}, Leases: map[string]Lease{}, Operations: map[string]operation{}, Version: a.version, Tasks: map[string]TaskBinding{}, Attempts: map[string]Attempt{}, Binding: a.binding, Sessions: map[string]record{}}
	initializePublication(&s)
	if a.version >= 7 {
		initializeSwarms(&s)
	}
	data, err := os.ReadFile(a.path)
	if create {
		if !os.IsNotExist(err) {
			return ErrDenied
		}
	} else {
		s = state{}
		if err != nil {
			return ErrStorage
		}
		s, err = decodeAuthorityState(data, a.binding, a.version)
		if err != nil {
			return err
		}
	}
	if err = validateAuthorityMaps(&s); err != nil {
		return err
	}
	if err = fn(&s); err != nil {
		return err
	}
	if !write {
		return nil
	}
	if !create && s.OwnerHash == "" {
		return ErrStorage
	}
	data, err = json.Marshal(s)
	if err != nil {
		return ErrStorage
	}
	if a.persistOverride != nil {
		return a.persistOverride(data)
	}
	return a.persist(data)
}

func (a *Authority) persist(data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(a.path), ".sessions-*")
	if err != nil {
		return ErrStorage
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil || closeErr != nil {
		return ErrStorage
	}
	if err := regularOrAbsent(a.path); err != nil {
		return err
	}
	if os.Rename(name, a.path) != nil {
		return ErrStorage
	}
	dir, err := os.Open(filepath.Dir(a.path))
	if err != nil {
		return ErrStorage
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil || closeErr != nil {
		return ErrStorage
	}
	return nil
}

// Bootstrap initializes an absent authority exactly once. It never adopts host labels.
func (a *Authority) Bootstrap() (string, error) {
	token, err := mint()
	if err != nil {
		return "", err
	}
	err = a.transaction(true, true, func(s *state) error { s.OwnerHash = hash(token); return nil })
	if err != nil {
		return "", err
	}
	return token, nil
}
func authenticate(s *state, token, id string, now time.Time) (record, error) {
	r, ok := s.Sessions[id]
	if !ok || len(token) != 64 || hash(token) != r.Hash {
		return record{}, ErrDenied
	}
	seen := map[string]bool{}
	for cur := r; ; {
		if seen[cur.ID] || cur.Revoked || !now.Before(cur.ExpiresAt) {
			return record{}, ErrDenied
		}
		seen[cur.ID] = true
		if cur.Parent == "" {
			break
		}
		var exists bool
		cur, exists = s.Sessions[cur.Parent]
		if !exists {
			return record{}, ErrDenied
		}
	}
	return r, nil
}

// Enroll permits the owner credential to mint an independent session.
func (a *Authority) Enroll(owner string, scope Scope, expires time.Time) (Session, string, error) {
	return a.issue(owner, "", scope, expires)
}

// Delegate requires the parent's credential and cannot widen its scope or lifetime.
func (a *Authority) Delegate(token, parent string, scope Scope, expires time.Time) (Session, string, error) {
	return a.issue(token, parent, scope, expires)
}
func (a *Authority) issue(credential, parent string, scope Scope, expires time.Time) (Session, string, error) {
	token, err := mint()
	if err != nil {
		return Session{}, "", err
	}
	id, err := mint()
	if err != nil {
		return Session{}, "", err
	}
	session := Session{ID: id, Parent: parent, Scope: scope, ExpiresAt: expires}
	err = a.transaction(false, true, func(s *state) error {
		now := time.Now()
		if !validScope(scope) || !expires.After(now) {
			return ErrDenied
		}
		if parent == "" {
			if len(credential) != 64 || hash(credential) != s.OwnerHash {
				return ErrDenied
			}
		} else {
			p, err := authenticate(s, credential, parent, now)
			if err != nil {
				return err
			}
			if expires.After(p.ExpiresAt) || !subset(scope.Actions, p.Scope.Actions) || !subset(scope.Resources, p.Scope.Resources) {
				return ErrDenied
			}
		}
		s.Sessions[id] = record{Session: session, Hash: hash(token)}
		return nil
	})
	if err != nil {
		return Session{}, "", err
	}
	return session, token, nil
}

// Authenticate returns a fresh session snapshot for this authority's explicit binding.
func (a *Authority) Authenticate(token, id string) (Session, error) {
	var result Session
	err := a.transaction(false, false, func(s *state) error { r, err := authenticate(s, token, id, time.Now()); result = r.Session; return err })
	return result, err
}

// Authorize authenticates and checks exact action/resource scope against current
// durable authority in one read transaction. Workspace is the selected resource
// binding, not a caller assertion of identity. No wildcard matching is implied.
func (a *Authority) Authorize(token, id, workspace, action, resource string) (Session, error) {
	if a == nil || workspace == "" || a.binding.Workspace != workspace || action == "" || resource == "" {
		return Session{}, ErrDenied
	}
	var result Session
	err := a.transaction(false, false, func(s *state) error {
		r, err := authenticate(s, token, id, time.Now())
		if err != nil {
			return err
		}
		if !subset([]string{action}, r.Scope.Actions) || !subset([]string{resource}, r.Scope.Resources) {
			return ErrDenied
		}
		result = r.Session
		return nil
	})
	return result, err
}

// Revoke requires the owner credential; all descendants become invalid immediately.
func (a *Authority) Revoke(owner, id string) error {
	return a.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		r, ok := s.Sessions[id]
		if !ok {
			return ErrDenied
		}
		r.Revoked = true
		s.Sessions[id] = r
		return nil
	})
}

// decodeAuthorityState shares the exact supported schema interpreter with bounded readers.
func decodeAuthorityState(data []byte, binding Binding, version int) (state, error) {
	var s state
	if json.Unmarshal(data, &s) != nil || (s.Version != version && !((version >= 6 && version <= 12) && s.Version >= version && s.Version <= 19)) || s.Binding != binding || s.OwnerHash == "" || s.Sessions == nil || (s.Version >= 2 && (s.Tasks == nil || s.Attempts == nil)) || (s.Version >= 3 && (s.Leases == nil || s.Operations == nil)) || (s.Version >= 4 && (s.Revisions == nil || s.Grants == nil || s.Evidence == nil)) || (s.Version >= 5 && (s.ReviewGrants == nil || s.Reviews == nil)) || (s.Version >= 6 && (s.PublicationPolicies == nil || s.PublicationDecisions == nil || s.PublicationConsumed == nil)) {
		return state{}, ErrStorage
	}
	if e := validateAuthorityMaps(&s); e != nil {
		return state{}, e
	}
	return s, nil
}
func validateAuthorityMaps(s *state) error {
	if s.Version < 19 && (s.NativeExternalPolicies != nil || s.NativeExternalHistory != nil || s.NativeExternalOperations != nil) {
		return ErrStorage
	}
	if s.Version >= 19 && (s.NativeExternalPolicies == nil || s.NativeExternalHistory == nil || s.NativeExternalOperations == nil) {
		return ErrStorage
	}

	if s.Version < 18 && (s.NativeIntegrationPolicies != nil || s.NativeIntegrationHistory != nil || s.NativeIntegrationOperations != nil) {
		return ErrStorage
	}
	if s.Version >= 18 && (s.NativeIntegrationPolicies == nil || s.NativeIntegrationHistory == nil || s.NativeIntegrationOperations == nil) {
		return ErrStorage
	}

	if s.Version < 7 && (s.SwarmPolicies != nil || s.Dispatches != nil || s.SwarmOperations != nil) {
		return ErrStorage
	}
	if s.Version >= 7 && (s.SwarmPolicies == nil || s.Dispatches == nil || s.SwarmOperations == nil) {
		return ErrStorage
	}
	if s.Version < 8 && (s.DispatchIntents != nil || s.DispatchIntentOperations != nil) {
		return ErrStorage
	}
	if s.Version >= 8 && (s.DispatchIntents == nil || s.DispatchIntentOperations == nil) {
		return ErrStorage
	}
	if s.Version < 9 && (s.LifecyclePolicyHistory != nil || s.LifecyclePolicies != nil || s.LifecycleObservations != nil || s.LifecycleCancellations != nil || s.LifecycleOperations != nil) {
		return ErrStorage
	}
	if s.Version >= 9 && (s.LifecyclePolicyHistory == nil || s.LifecyclePolicies == nil || s.LifecycleObservations == nil || s.LifecycleCancellations == nil || s.LifecycleOperations == nil) {
		return ErrStorage
	}
	if s.Version < 10 && (s.RecoveryPolicies != nil || s.RecoveryPolicyHistory != nil || s.Recoveries != nil || s.RecoveryOperations != nil) {
		return ErrStorage
	}
	if s.Version >= 10 && (s.RecoveryPolicies == nil || s.RecoveryPolicyHistory == nil || s.Recoveries == nil || s.RecoveryOperations == nil) {
		return ErrStorage
	}
	if s.Version < 12 {
		for _, op := range s.Operations {
			if op.CheckpointVerification != nil {
				return ErrDenied
			}
		}
	}
	if s.Version < 12 && (s.CheckpointPolicies != nil || s.CheckpointPolicyHistory != nil || s.Checkpoints != nil || s.CheckpointOperations != nil) {
		return ErrDenied
	}
	if s.Version >= 12 && (s.CheckpointPolicies == nil || s.CheckpointPolicyHistory == nil || s.Checkpoints == nil || s.CheckpointOperations == nil) {
		return ErrDenied
	}

	if s.Version < 17 && (s.ModelRoutingPolicies != nil || s.ModelRoutingHistory != nil || s.ModelRoutingSelections != nil) {
		return ErrDenied
	}
	if s.Version >= 17 && (s.ModelRoutingPolicies == nil || s.ModelRoutingHistory == nil || s.ModelRoutingSelections == nil) {
		return ErrDenied
	}
	if s.Version < 16 && (s.ControllerPolicies != nil || s.ControllerPolicyHistory != nil || s.ControllerInvocations != nil || s.ControllerHistory != nil || s.ControllerOperations != nil) {
		return ErrStorage
	}
	if s.Version >= 16 && (s.ControllerPolicies == nil || s.ControllerPolicyHistory == nil || s.ControllerInvocations == nil || s.ControllerHistory == nil || s.ControllerOperations == nil) {
		return ErrStorage
	}
	if s.Version < 15 && (s.NativePolicies != nil || s.NativePolicyHistory != nil || s.NativePolicyDecisions != nil || s.NativePolicyApprovals != nil || s.NativePolicyConsumptions != nil || s.NativePolicyOutcomes != nil) {
		return ErrDenied
	}
	if s.Version >= 15 && (s.NativePolicies == nil || s.NativePolicyHistory == nil || s.NativePolicyDecisions == nil || s.NativePolicyApprovals == nil || s.NativePolicyConsumptions == nil || s.NativePolicyOutcomes == nil) {
		return ErrDenied
	}
	if s.Version < 14 && (s.TaskTerminals != nil || s.NativeAcceptanceOperations != nil) {
		return ErrDenied
	}
	if s.Version >= 14 && (s.TaskTerminals == nil || s.NativeAcceptanceOperations == nil) {
		return ErrDenied
	}
	if s.Version < 13 && (s.BudgetPolicies != nil || s.BudgetPolicyHistory != nil || s.BudgetAllocations != nil || s.BudgetOperations != nil) {
		return ErrStorage
	}
	if s.Version >= 13 && (s.BudgetPolicies == nil || s.BudgetPolicyHistory == nil || s.BudgetAllocations == nil || s.BudgetOperations == nil) {
		return ErrStorage
	}
	if s.Version < 11 && (s.PreflightPolicies != nil || s.PreflightPolicyHistory != nil || s.CapacityOverrides != nil || s.PreflightOperations != nil || s.PreflightRetryBudgets != nil) {
		return ErrStorage
	}
	if s.Version >= 11 && (s.PreflightPolicies == nil || s.PreflightPolicyHistory == nil || s.CapacityOverrides == nil || s.PreflightOperations == nil || s.PreflightRetryBudgets == nil) {
		return ErrStorage
	}
	return nil
}
