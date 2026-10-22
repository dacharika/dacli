package sessions

import (
	"crypto/subtle"
	"time"
)

const (
	MinNativeCompatibleSchema = 6
	MaxNativeCompatibleSchema = 19
)

// NativeCompatibility is a bounded, read-only classification of one exact
// actor credential against one authority binding. It deliberately returns no
// session fields, credential digest, or file paths.
type NativeCompatibility struct {
	Status          string
	AuthoritySchema int
}

// InspectNativeCompatibility reads one authority snapshot through the same
// bounded lock, mode and schema checks used by authority transactions. It never
// creates a session or migrates state.
func (a *Authority) InspectNativeCompatibility(token, id string) NativeCompatibility {
	unknown := NativeCompatibility{Status: "unknown"}
	if a == nil || id == "" || len(token) != 64 {
		return unknown
	}
	result := unknown
	err := a.transaction(false, false, func(s *state) error {
		actor, ok := s.Sessions[id]
		if !ok || subtle.ConstantTimeCompare([]byte(actor.Hash), []byte(hash(token))) != 1 {
			result.Status = "missing_enrollment"
			return nil
		}
		seen := map[string]bool{}
		for {
			if actor.ID == "" || seen[actor.ID] {
				result.Status = "profile_schema_mismatch"
				return nil
			}
			seen[actor.ID] = true
			if actor.Revoked {
				result.Status = "revoked_actor"
				return nil
			}
			if !time.Now().Before(actor.ExpiresAt) {
				result.Status = "expired_enrollment"
				return nil
			}
			if actor.Parent == "" {
				result = NativeCompatibility{Status: "authorized", AuthoritySchema: s.Version}
				return nil
			}
			actor, ok = s.Sessions[actor.Parent]
			if !ok {
				result.Status = "profile_schema_mismatch"
				return nil
			}
		}
	})
	if err == nil {
		return result
	}
	// The shared decoder refuses unsupported schemas and mismatched bindings
	// before it can authenticate a credential. Keep those classifications
	// unknown rather than reading unauthenticated header fields.
	return unknown
}
