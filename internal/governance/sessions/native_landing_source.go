package sessions

// NativeLandingSource is the exact published worker source joined to its
// owner-attested task binding. It contains no bearer credentials or current
// worker permission.
type NativeLandingSource struct {
	Binding  TaskBinding
	Attempt  Attempt
	Revision AttemptRevision
}

// OwnerReadNativeLandingSource reads a historical source publication using
// explicit owner authentication. It deliberately does not require a live
// worker session or lease: revocation and lease retirement do not erase a
// published source's history.
func (a *Authority) OwnerReadNativeLandingSource(owner, project, task, attempt, revision string) (NativeLandingSource, error) {
	var source NativeLandingSource
	err := a.transaction(false, false, func(s *state) error {
		var err error
		source, err = ownerNativeLandingSource(s, owner, a.binding, project, task, attempt, revision)
		return err
	})
	if err != nil {
		return NativeLandingSource{}, err
	}
	return source, nil
}

// WithOwnerNativeLandingSource holds the authority read lock while the caller
// performs its final task-lock CAS. The callback must not reenter Authority.
func (a *Authority) WithOwnerNativeLandingSource(owner, project, task, attempt, revision string, fn func(NativeLandingSource) error) error {
	if fn == nil {
		return ErrDenied
	}
	return a.transaction(false, false, func(s *state) error {
		source, err := ownerNativeLandingSource(s, owner, a.binding, project, task, attempt, revision)
		if err != nil {
			return err
		}
		return fn(source)
	})
}

func ownerNativeLandingSource(s *state, owner string, binding Binding, project, task, attemptID, revisionID string) (NativeLandingSource, error) {
	if s == nil || len(owner) != 64 || hash(owner) != s.OwnerHash || !validID(project) || !validID(task) || !validID(attemptID) || !validID(revisionID) {
		return NativeLandingSource{}, ErrDenied
	}
	if s.Version < 6 || binding.Workspace == "" || binding.Installation == "" || s.Binding != binding {
		return NativeLandingSource{}, ErrDenied
	}
	b, ok := s.Tasks[task]
	if !ok || b.TaskID != task || b.ProjectID != project || b.Repository == "" {
		return NativeLandingSource{}, ErrDenied
	}
	var at Attempt
	foundAttempt := false
	for _, candidate := range s.Attempts {
		if candidate.ID == attemptID {
			at = candidate
			foundAttempt = true
			break
		}
	}
	if !foundAttempt || at.ProjectID != project || at.Request.TaskID != task || at.Observation.Repository == "" || at.Observation.Repository != b.Repository || at.Observation.Commit == "" {
		return NativeLandingSource{}, ErrDenied
	}
	history := s.Revisions[attemptID]
	if len(history) == 0 {
		return NativeLandingSource{}, ErrDenied
	}
	latest := history[len(history)-1]
	if latest.ID != revisionID || latest.AttemptID != at.ID || latest.SessionID != at.SessionID || latest.Sequence != uint64(len(history)) || latest.Observation.Repository != at.Observation.Repository || latest.Observation.Commit == "" || latest.Observation.Tree == "" {
		return NativeLandingSource{}, ErrDenied
	}
	return NativeLandingSource{Binding: b, Attempt: at, Revision: latest}, nil
}
