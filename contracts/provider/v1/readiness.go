// Package providerv1 defines the local, read-only provider readiness report.
// It describes observations only; it carries no identity or execution authority.
package providerv1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const (
	Version = 1
	Schema  = "provider-readiness/v1"
)

// Config contains only explicit executable selections. It must never contain
// credentials, provider settings, tokens, account identifiers, or key material.
type Config struct {
	SchemaVersion   int    `json:"schema_version"`
	CodexPath       string `json:"codex_path,omitempty"`
	ClaudeCodePath  string `json:"claude_code_path,omitempty"`
	AntigravityPath string `json:"antigravity_path,omitempty"`
}

// ProviderReport records bounded local discovery and optional status probes.
// Executable paths and raw provider output are intentionally omitted.
type ProviderReport struct {
	Provider       string `json:"provider"`
	InstallState   string `json:"install_state"`
	CandidateCount int    `json:"candidate_count"`
	InstallSource  string `json:"install_source,omitempty"`
	VersionState   string `json:"version_state"`
	Version        string `json:"version,omitempty"`
	AuthState      string `json:"auth_state"`
	Reason         string `json:"reason"`
}

// ReadinessReport is a local observation, not provider qualification.
type ReadinessReport struct {
	SchemaVersion       int              `json:"schema_version"`
	Schema              string           `json:"schema"`
	ObservedAt          time.Time        `json:"observed_at"`
	AuthStatusRequested bool             `json:"auth_status_requested"`
	Providers           []ProviderReport `json:"providers"`
	Notice              string           `json:"notice"`
}

// DecodeConfig rejects unknown fields, trailing data, unsupported versions,
// relative executable paths, and oversized input.
func DecodeConfig(raw []byte, maxBytes int) (Config, error) {
	var config Config
	if maxBytes <= 0 || len(raw) > maxBytes {
		return config, errors.New("provider config size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, errors.New("provider config is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("provider config has trailing data")
	}
	if config.SchemaVersion != Version || !safePath(config.CodexPath) || !safePath(config.ClaudeCodePath) || !safePath(config.AntigravityPath) || !absoluteOrEmpty(config.CodexPath) || !absoluteOrEmpty(config.ClaudeCodePath) || !absoluteOrEmpty(config.AntigravityPath) {
		return Config{}, errors.New("provider config is invalid")
	}
	return config, nil
}

func safePath(value string) bool {
	return value == "" || (len(value) <= 4096 && !strings.ContainsAny(value, "\x00\r\n"))
}

func absoluteOrEmpty(value string) bool {
	return value == "" || filepath.IsAbs(value)
}

// DecodeReport checks the closed report envelope and the fixed provider set.
func DecodeReport(raw []byte, maxBytes int) (ReadinessReport, error) {
	var report ReadinessReport
	if maxBytes <= 0 || len(raw) > maxBytes {
		return report, errors.New("provider report size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return ReadinessReport{}, errors.New("provider report is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ReadinessReport{}, errors.New("provider report has trailing data")
	}
	if report.SchemaVersion != Version || report.Schema != Schema || report.ObservedAt.IsZero() || len(report.Notice) == 0 || len(report.Notice) > 512 || len(report.Providers) != 3 {
		return ReadinessReport{}, errors.New("provider report is invalid")
	}
	seen := map[string]bool{}
	for _, provider := range report.Providers {
		if !validProvider(provider) || seen[provider.Provider] {
			return ReadinessReport{}, errors.New("provider report is invalid")
		}
		seen[provider.Provider] = true
	}
	for _, name := range []string{"codex", "claude-code", "antigravity"} {
		if !seen[name] {
			return ReadinessReport{}, errors.New("provider report is invalid")
		}
	}
	return report, nil
}

func validProvider(p ProviderReport) bool {
	provider := p.Provider == "codex" || p.Provider == "claude-code" || p.Provider == "antigravity"
	install := p.InstallState == "installed" || p.InstallState == "missing" || p.InstallState == "ambiguous" || p.InstallState == "denied" || p.InstallState == "unsupported"
	version := p.VersionState == "known" || p.VersionState == "unknown" || p.VersionState == "ambiguous" || p.VersionState == "unsupported"
	auth := p.AuthState == "authenticated" || p.AuthState == "unauthenticated" || p.AuthState == "unknown" || p.AuthState == "unsupported"
	reason := p.Reason == "ok" || p.Reason == "not_found" || p.Reason == "multiple_candidates" || p.Reason == "invalid_selection" || p.Reason == "not_executable" || p.Reason == "candidate_limit_exceeded" || p.Reason == "probe_failed" || p.Reason == "probe_timeout" || p.Reason == "invalid_probe_output" || p.Reason == "diagnostic_not_requested" || p.Reason == "auth_diagnostic_unsupported" || p.Reason == "auth_status_unknown"
	source := p.InstallSource == "" || p.InstallSource == "explicit_config" || p.InstallSource == "path" || p.InstallSource == "documented_location"
	return provider && install && version && auth && reason && source && p.CandidateCount >= 0 && p.CandidateCount <= 128 && len(p.Version) <= 64 && (p.VersionState == "known" && p.Version != "" || p.VersionState != "known" && p.Version == "")
}
