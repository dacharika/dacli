package sessions

import "fmt"

// TaskClaimsProtection is an owner-only read of native protections for a
// canonical task. It contains no capability or credential material.
type TaskClaimsProtection struct {
	TaskID string   `json:"task_id"`
	Holds  []string `json:"holds"`
}

// WithOwnerTaskClaimsProtection authenticates the owner and holds the existing
// authority lock while fn performs the canonical task CAS. This closes the
// gap where a native reservation could be created after a snapshot-only check.
// The callback is never allowed to mutate this authority; transaction is
// explicitly read-only and revalidates the aggregate before returning.
func (a *Authority) WithOwnerTaskClaimsProtection(owner, taskID string, fn func(TaskClaimsProtection) error) error {
	if taskID == "" || fn == nil {
		return ErrDenied
	}
	return a.transaction(false, false, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		protection := TaskClaimsProtection{TaskID: taskID, Holds: []string{}}
		for _, lease := range s.Leases {
			if lease.TaskID == taskID && !lease.Released {
				protection.Holds = append(protection.Holds, "retained native lease")
			}
		}
		for _, dispatch := range s.Dispatches {
			if dispatch.Reservation.TaskID == taskID && reservationRetained(s, dispatch) {
				protection.Holds = append(protection.Holds, "retained native reservation")
			}
		}
		if len(protection.Holds) > 0 {
			return fmt.Errorf("native task protections remain: %v", protection.Holds)
		}
		return fn(protection)
	})
}
