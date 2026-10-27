package application

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	providerv1 "github.com/mlnomadpy/dacli/contracts/provider/v1"
)

const ProviderReadinessNotice = "Local observations only; no identity, dispatch authority, provider qualification, subscription, API billing, user selection, or execution architecture is established."

// ProviderProbeResult contains only bounded stdout and process outcome facts.
// Callers must discard it after parsing; it must never enter a report or log.
type ProviderProbeResult struct {
	Stdout    []byte
	ExitCode  int
	TimedOut  bool
	Truncated bool
}

// ProviderProbeRunner executes only the fixed diagnostics selected by this
// package. The CLI adapter owns process deadlines and byte limits.
type ProviderProbeRunner interface {
	RunProviderProbe(context.Context, Provider, string, []string) ProviderProbeResult
}

type ProviderReadinessInputs struct {
	Discovery  DiscoveryInputs
	AuthStatus bool
	ObservedAt time.Time
}

var providerVersionPattern = regexp.MustCompile(`(?:^|[^0-9])v?([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)`)

// ProviderReadiness performs bounded, documented version probes and optional
// documented authentication-status checks. It never starts an interactive
// provider session or infers identity, billing, subscription, or qualification.
func ProviderReadiness(ctx context.Context, inputs ProviderReadinessInputs, runner ProviderProbeRunner) providerv1.ReadinessReport {
	observedAt := inputs.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	results := DiscoverProvider(inputs.Discovery)
	report := providerv1.ReadinessReport{
		SchemaVersion:       providerv1.Version,
		Schema:              providerv1.Schema,
		ObservedAt:          observedAt.UTC(),
		AuthStatusRequested: inputs.AuthStatus,
		Providers:           make([]providerv1.ProviderReport, 0, len(results)),
		Notice:              ProviderReadinessNotice,
	}
	for _, discovered := range results {
		providerReport := providerv1.ProviderReport{
			Provider:       string(discovered.Provider),
			InstallState:   discovered.State,
			CandidateCount: len(discovered.Candidates),
			InstallSource:  discovered.Source,
			VersionState:   "unknown",
			AuthState:      "unknown",
			Reason:         discovered.Reason,
		}
		if discovered.State == "ambiguous" {
			providerReport.VersionState = "ambiguous"
		}
		if discovered.State != "installed" || len(discovered.Candidates) != 1 {
			report.Providers = append(report.Providers, providerReport)
			continue
		}
		candidate := discovered.Candidates[0]
		if runner == nil {
			providerReport.Reason = "probe_failed"
			report.Providers = append(report.Providers, providerReport)
			continue
		}
		versionProbe := runner.RunProviderProbe(ctx, candidate.Provider, candidate.Path, []string{"--version"})
		providerReport.VersionState, providerReport.Version = classifyVersion(versionProbe)
		if providerReport.VersionState != "known" {
			providerReport.Reason = probeReason(versionProbe)
		}
		if !inputs.AuthStatus {
			providerReport.AuthState = "unknown"
			if providerReport.Reason == "ok" {
				providerReport.Reason = "diagnostic_not_requested"
			}
			report.Providers = append(report.Providers, providerReport)
			continue
		}
		if candidate.Provider == ProviderAntigravity {
			providerReport.AuthState = "unknown"
			providerReport.Reason = "auth_diagnostic_unsupported"
			report.Providers = append(report.Providers, providerReport)
			continue
		}
		args := []string{"login", "status"}
		if candidate.Provider == ProviderClaudeCode {
			args = []string{"auth", "status"}
		}
		authProbe := runner.RunProviderProbe(ctx, candidate.Provider, candidate.Path, args)
		providerReport.AuthState = classifyAuth(candidate.Provider, authProbe)
		if providerReport.AuthState == "unknown" {
			providerReport.Reason = probeReason(authProbe)
		} else if providerReport.Reason == "ok" || providerReport.Reason == "diagnostic_not_requested" {
			providerReport.Reason = "ok"
		}
		report.Providers = append(report.Providers, providerReport)
	}
	return report
}

func classifyVersion(result ProviderProbeResult) (string, string) {
	if result.TimedOut || result.Truncated || result.ExitCode != 0 || len(result.Stdout) == 0 {
		return "unknown", ""
	}
	match := providerVersionPattern.FindSubmatch(result.Stdout)
	if len(match) != 2 || len(match[1]) > 64 {
		return "unknown", ""
	}
	return "known", strings.TrimSpace(string(match[1]))
}

func classifyAuth(provider Provider, result ProviderProbeResult) string {
	if result.TimedOut || result.Truncated {
		return "unknown"
	}
	switch provider {
	case ProviderCodex:
		if result.ExitCode == 0 {
			return "authenticated"
		}
		// Codex documents exit zero as credentials-present, but does not
		// document every nonzero status as a definitive logged-out result.
		return "unknown"
	case ProviderClaudeCode:
		if len(result.Stdout) == 0 {
			return "unknown"
		}
		var status struct {
			AuthMethod string `json:"authMethod"`
		}
		if json.Unmarshal(result.Stdout, &status) != nil || !validClaudeAuthMethod(status.AuthMethod) {
			return "unknown"
		}
		switch result.ExitCode {
		case 0:
			if status.AuthMethod != "none" {
				return "authenticated"
			}
		case 1:
			if status.AuthMethod == "none" {
				return "unauthenticated"
			}
		}
		return "unknown"
	default:
		return "unknown"
	}
}

func validClaudeAuthMethod(value string) bool {
	switch value {
	case "none", "claude.ai", "oauth_token", "api_key", "api_key_helper", "third_party":
		return true
	default:
		return false
	}
}

func probeReason(result ProviderProbeResult) string {
	if result.TimedOut {
		return "probe_timeout"
	}
	if result.Truncated {
		return "invalid_probe_output"
	}
	if result.ExitCode != 0 {
		return "probe_failed"
	}
	return "invalid_probe_output"
}
