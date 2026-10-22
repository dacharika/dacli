// Package native exposes only explicit session-authorized governance services.
package native

import (
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"path/filepath"
)

var ErrDenied = nativeconfig.ErrDenied

type Connection = nativeconfig.Connection

func Decode(b []byte, value any) error { return nativeconfig.Decode(b, value) }
func ReadPrivate(path string, limit int64) ([]byte, error) {
	return nativeconfig.ReadPrivate(path, limit)
}
func ReadCredential(path string) (string, error) { return nativeconfig.ReadCredential(path) }
func ResolveWorkspace(path, id string) (*workspace.Workspace, error) {
	return nativeconfig.ResolveWorkspace(path, id)
}

type Server struct {
	path          string
	connection    Connection
	verification  *application.NativeVerificationJobs
	review        *application.NativeReviewProfile
	orchestration *application.NativeOrchestrationProfile
}

func Open(path string) (*Server, error) {
	b, e := ReadPrivate(path, 16384)
	if e != nil {
		return nil, ErrDenied
	}
	var c Connection
	if Decode(b, &c) != nil || c.SchemaVersion != 1 || c.WorkspaceID == "" || c.InstallationID == "" || c.SessionID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.CredentialFile) {
		return nil, ErrDenied
	}
	s := &Server{path: path, connection: c}
	if _, _, _, _, e = s.authenticate(); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *Server) authenticate() (*workspace.Workspace, *sessions.Authority, string, sessions.Session, error) {
	deny := func() (*workspace.Workspace, *sessions.Authority, string, sessions.Session, error) {
		return nil, nil, "", sessions.Session{}, ErrDenied
	}
	b, e := ReadPrivate(s.path, 16384)
	var c Connection
	if e != nil || Decode(b, &c) != nil || c != s.connection {
		return deny()
	}
	w, e := ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if e != nil {
		return deny()
	}
	admission, e := application.NewLeaseAdmission(w)
	if e != nil {
		return deny()
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: c.WorkspaceID, Installation: c.InstallationID}, admission)
	if e != nil {
		return deny()
	}
	token, e := ReadCredential(c.CredentialFile)
	if e != nil {
		return deny()
	}
	session, e := a.Authenticate(token, c.SessionID)
	if e != nil {
		return deny()
	}
	if s.orchestration != nil {
		if _, e := s.orchestration.Authenticate(); e != nil {
			return deny()
		}
	}
	if s.review != nil {
		if _, e := s.review.Authenticate(); e != nil {
			return deny()
		}
	}
	return w, a, token, session, nil
}

// OpenWithRunner enables only the owner's explicitly selected private profile.
func OpenWithRunner(path, runnerPath string) (*Server, error) {
	return OpenWithProfiles(path, runnerPath, "")
}
func OpenWithProfiles(path, runnerPath, reviewPath string) (*Server, error) {
	if runnerPath != "" && reviewPath != "" {
		return nil, ErrDenied
	}
	s, e := Open(path)
	if e != nil {
		return nil, e
	}
	if reviewPath != "" {
		s.review, e = application.OpenNativeReviewProfile(path, reviewPath)
		if e != nil {
			return nil, ErrDenied
		}
		return s, nil
	}
	if runnerPath == "" {
		return s, nil
	}
	selected, e := application.OpenNativeVerificationProfile(path, runnerPath)
	if e != nil {
		return nil, ErrDenied
	}
	s.verification, e = application.NewNativeVerificationJobs(selected)
	if e != nil {
		return nil, ErrDenied
	}
	return s, nil
}
func (s *Server) Close() error {
	if s.verification != nil {
		return s.verification.Close()
	}
	return nil
}

// OpenWithOrchestration selects a distinct owner-pinned controller, bridge or
// worker handshake profile. It does not enroll or configure authority.
func OpenWithOrchestration(path, profile string) (*Server, error) {
	s, e := Open(path)
	if e != nil {
		return nil, e
	}
	s.orchestration, e = application.OpenNativeOrchestrationProfile(path, profile)
	if e != nil {
		return nil, ErrDenied
	}
	return s, nil
}
