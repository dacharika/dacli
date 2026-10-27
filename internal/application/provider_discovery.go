package application

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Provider names are stable identifiers, not executable launch permissions.
type Provider string

const (
	ProviderCodex          Provider = "codex"
	ProviderClaudeCode     Provider = "claude-code"
	ProviderAntigravity    Provider = "antigravity"
	maxProviderPathBytes            = 32 * 1024
	maxProviderPathEntries          = 128
)

// DiscoveryInputs are captured explicitly by the CLI. Application code never
// reads ambient environment state.
type DiscoveryInputs struct {
	ExplicitPaths map[Provider]string
	Path          string
	Home          string
	LocalAppData  string
	GOOS          string
	WorkingDir    string
}

type ProviderCandidate struct {
	Provider Provider
	Path     string
	Source   string
}

type ProviderDiscovery struct {
	Provider   Provider
	Candidates []ProviderCandidate
	State      string
	Reason     string
	Source     string
}

// NormalizeProvider accepts documented CLI names plus stable product aliases.
// Aliases are never executed; they only identify one provider family.
func NormalizeProvider(value string) (Provider, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "codex", "codex-cli":
		return ProviderCodex, true
	case "claude", "claude-code":
		return ProviderClaudeCode, true
	case "agy", "antigravity":
		return ProviderAntigravity, true
	default:
		return "", false
	}
}

// DiscoverProvider resolves every supported provider in deterministic order.
// It examines only file metadata and never reads provider config or credentials.
func DiscoverProvider(inputs DiscoveryInputs) []ProviderDiscovery {
	if inputs.GOOS == "" {
		inputs.GOOS = runtime.GOOS
	}
	providers := []Provider{ProviderCodex, ProviderClaudeCode, ProviderAntigravity}
	out := make([]ProviderDiscovery, 0, len(providers))
	for _, provider := range providers {
		out = append(out, discoverOne(provider, inputs))
	}
	return out
}

func discoverOne(provider Provider, inputs DiscoveryInputs) ProviderDiscovery {
	result := ProviderDiscovery{Provider: provider, State: "missing", Reason: "not_found"}
	command := map[Provider]string{
		ProviderCodex:       "codex",
		ProviderClaudeCode:  "claude",
		ProviderAntigravity: "agy",
	}[provider]
	if explicit := inputs.ExplicitPaths[provider]; explicit != "" {
		if !filepath.IsAbs(explicit) {
			result.State, result.Reason = "denied", "invalid_selection"
			return result
		}
		candidate, state := inspectCandidate(provider, explicit, "explicit_config")
		if state != "" {
			result.State, result.Reason = state, candidateReason(state)
			return result
		}
		result.Candidates = []ProviderCandidate{candidate}
		result.State, result.Source = "installed", candidate.Source
		result.Reason = "ok"
		return result
	}

	if len(inputs.Path) > maxProviderPathBytes {
		result.State, result.Reason = "denied", "candidate_limit_exceeded"
		return result
	}
	pathDirs, overflow := splitPath(inputs.Path, inputs.GOOS, inputs.WorkingDir)
	if overflow {
		result.State, result.Reason = "denied", "candidate_limit_exceeded"
		return result
	}
	for _, dir := range pathDirs {
		candidatePath := filepath.Join(dir, executableName(command, inputs.GOOS))
		candidate, state := inspectCandidate(provider, candidatePath, "path")
		if state == "denied" {
			result.State, result.Reason = "denied", "not_executable"
			return result
		}
		if state == "" {
			result.Candidates = appendUniqueCandidate(result.Candidates, candidate)
		}
	}
	if len(result.Candidates) == 1 {
		result.State, result.Source, result.Reason = "installed", "path", "ok"
		return result
	}
	if len(result.Candidates) > 1 {
		result.State, result.Reason = "ambiguous", "multiple_candidates"
		return result
	}

	if location, ok := documentedLocation(provider, inputs); ok {
		candidate, state := inspectCandidate(provider, location, "documented_location")
		if state == "denied" {
			result.State, result.Reason = "denied", "not_executable"
			return result
		}
		if state == "" {
			result.Candidates = []ProviderCandidate{candidate}
			result.State, result.Source, result.Reason = "installed", candidate.Source, "ok"
		}
		return result
	}
	if provider == ProviderAntigravity && !supportedPlatform(inputs.GOOS) {
		result.State, result.Reason = "unsupported", "not_found"
	}
	return result
}

func inspectCandidate(provider Provider, path, source string) (ProviderCandidate, string) {
	candidate := ProviderCandidate{Provider: provider, Path: path, Source: source}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsPermission(err) {
			return candidate, "denied"
		}
		return candidate, "missing"
	}
	if !info.Mode().IsRegular() {
		return candidate, "missing"
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return candidate, "denied"
	}
	return candidate, ""
}

func candidateReason(state string) string {
	if state == "denied" {
		return "not_executable"
	}
	if state == "missing" {
		return "not_found"
	}
	return "invalid_selection"
}

func appendUniqueCandidate(candidates []ProviderCandidate, candidate ProviderCandidate) []ProviderCandidate {
	resolved, err := filepath.EvalSymlinks(candidate.Path)
	if err == nil {
		candidate.Path = resolved
	}
	for _, existing := range candidates {
		left, leftErr := filepath.EvalSymlinks(existing.Path)
		if leftErr != nil {
			left = existing.Path
		}
		if filepath.Clean(left) == filepath.Clean(candidate.Path) {
			return candidates
		}
	}
	return append(candidates, candidate)
}

func splitPath(value, goos, workingDir string) ([]string, bool) {
	separator := string(os.PathListSeparator)
	if goos == "windows" {
		separator = ";"
	}
	parts := strings.Split(value, separator)
	var out []string
	seen := map[string]bool{}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		clean := filepath.Clean(part)
		if !filepath.IsAbs(clean) {
			if workingDir == "" || !filepath.IsAbs(workingDir) {
				continue
			}
			clean = filepath.Join(workingDir, clean)
		}
		if !seen[clean] {
			if len(out) >= maxProviderPathEntries {
				return nil, true
			}
			out = append(out, clean)
			seen[clean] = true
		}
	}
	return out, false
}

func executableName(command, goos string) string {
	if goos == "windows" {
		return command + ".exe"
	}
	return command
}

func documentedLocation(provider Provider, inputs DiscoveryInputs) (string, bool) {
	switch provider {
	case ProviderAntigravity:
		switch inputs.GOOS {
		case "darwin", "linux":
			if inputs.Home != "" {
				return filepath.Join(inputs.Home, ".local", "bin", "agy"), true
			}
		case "windows":
			if inputs.LocalAppData != "" {
				return filepath.Join(inputs.LocalAppData, "agy", "bin", "agy.exe"), true
			}
		}
	}
	return "", false
}

func supportedPlatform(goos string) bool {
	return goos == "darwin" || goos == "linux" || goos == "windows"
}
