// Package native exposes only explicit session-authorized governance services.
package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var ErrDenied = errors.New("native governance authority denied")

type Connection struct {
	SchemaVersion  int    `json:"schema_version"`
	Workspace      string `json:"workspace"`
	Authority      string `json:"authority"`
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	SessionID      string `json:"session_id"`
	CredentialFile string `json:"credential_file"`
}

// Decode rejects unknown fields and trailing JSON; errors never contain input.
func Decode(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return ErrDenied
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrDenied
	}
	return nil
}
func ReadCredential(path string) (string, error) {
	raw, err := ReadPrivate(path, 4096)
	if err != nil {
		return "", ErrDenied
	}
	token := strings.TrimRight(string(raw), "\r\n")
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 {
		return "", ErrDenied
	}
	return token, nil
}
func ResolveWorkspace(path, id string) (*workspace.Workspace, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrDenied
	}
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, ErrDenied
	}
	w, err := workspace.Find(root)
	if err != nil {
		return nil, ErrDenied
	}
	actual, err := filepath.EvalSymlinks(w.Root)
	if err != nil || actual != root || w.ID == "" || (id != "" && w.ID != id) {
		return nil, ErrDenied
	}
	return w, nil
}

type Server struct {
	path       string
	connection Connection
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
	return w, a, token, session, nil
}
