// Package nativeconfig reads private native credentials without transport dependencies.
package nativeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"io"
	"path/filepath"
	"strings"
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
