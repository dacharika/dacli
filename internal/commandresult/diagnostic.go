package commandresult

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

const diagnosticTailBytes = 4096

// Diagnostic is the stable, bounded account of an external command failure.
// It deliberately excludes argv and environment: both commonly carry tokens.
type Diagnostic struct {
	Kind       string `json:"kind"`
	Operation  string `json:"operation"`
	Executable string `json:"executable"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Signal     string `json:"signal,omitempty"`
	Timeout    bool   `json:"timeout"`
	StdoutTail string `json:"stdout_tail,omitempty"`
	StderrTail string `json:"stderr_tail,omitempty"`
	CwdScope   string `json:"cwd_scope"`
	Retryable  bool   `json:"retryable"`
	NextAction string `json:"next_action"`
}

// ExternalError retains the process error as its cause while carrying facts
// callers can inspect without parsing presentation text.
type ExternalError struct {
	Diagnostic Diagnostic
	cause      error
}

func (e *ExternalError) Error() string {
	detail := actionableLine(e.Diagnostic.StderrTail)
	if detail == "" {
		detail = actionableLine(e.Diagnostic.StdoutTail)
	}
	if detail == "" {
		detail = "external command failed"
	}
	state := "command failure"
	if e.Diagnostic.Timeout {
		state = "timeout"
	} else if e.Diagnostic.Signal != "" {
		state = "signal " + e.Diagnostic.Signal
	} else if e.Diagnostic.ExitCode != nil {
		state = fmt.Sprintf("exit %d", *e.Diagnostic.ExitCode)
	}
	return fmt.Sprintf("%s: %s (%s; next: %s)", e.Diagnostic.Operation, detail, state, e.Diagnostic.NextAction)
}

func (e *ExternalError) Unwrap() error { return e.cause }

// AsDiagnostic returns a copy of the typed diagnostic retained anywhere in an
// error chain. Wrapping with %w therefore preserves both the cause and facts.
func AsDiagnostic(err error) (Diagnostic, bool) {
	var external *ExternalError
	if !errors.As(err, &external) {
		return Diagnostic{}, false
	}
	return external.Diagnostic, true
}

// RunOptions describes only safe command metadata. Operation should be a
// verb-level label ("git status"), never an unsanitized command line.
type RunOptions struct {
	Operation     string
	WorkspaceRoot string
	TimedOut      func() bool
}

// Run executes cmd and turns a failure into an ExternalError. Successful output
// remains compatible with CombinedOutput callers; failed output is bounded and
// sanitized because legacy callers may interpolate it. stdout and stderr stay
// distinct in the typed failure.
func Run(cmd *exec.Cmd, opts RunOptions) ([]byte, error) {
	stdout, stderr, err := output(cmd)
	out := append(append([]byte(nil), stdout...), stderr...)
	if err == nil {
		return out, nil
	}
	timedOut := opts.TimedOut != nil && opts.TimedOut()
	// Legacy wrappers often interpolate the returned output alongside err. Make
	// that compatibility surface obey the same bounded disclosure policy too;
	// otherwise a typed safe error could sit next to a raw leaked token.
	safeOut := boundedTail(redactCommand(string(out), opts.WorkspaceRoot, cmd))
	return []byte(safeOut), NewExternalError(cmd, opts, stdout, stderr, err, timedOut)
}

// Output is the typed-diagnostic equivalent of exec.Cmd.Output: successful
// callers receive stdout alone, while a failure retains separately bounded
// and redacted stdout/stderr facts. Probe wrappers use this instead of Run
// because stderr is evidence, never part of the value they parse (issue #876).
func Output(cmd *exec.Cmd, opts RunOptions) ([]byte, error) {
	stdout, stderr, err := output(cmd)
	if err == nil {
		return stdout, nil
	}
	timedOut := opts.TimedOut != nil && opts.TimedOut()
	return nil, NewExternalError(cmd, opts, stdout, stderr, err, timedOut)
}

func output(cmd *exec.Cmd) (stdoutBytes, stderrBytes []byte, err error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// NewExternalError classifies a failed command that was executed by a wrapper
// with custom stream handling.
func NewExternalError(cmd *exec.Cmd, opts RunOptions, stdout, stderr []byte, cause error, timedOut bool) error {
	executable := redactCommand(filepath.Base(cmd.Path), opts.WorkspaceRoot, cmd)
	operation := strings.TrimSpace(redactCommand(opts.Operation, opts.WorkspaceRoot, cmd))
	if operation == "" {
		operation = executable
	}
	d := Diagnostic{
		Kind:       "command_failure",
		Operation:  operation,
		Executable: executable,
		Timeout:    timedOut,
		StdoutTail: boundedTail(redactCommand(string(stdout), opts.WorkspaceRoot, cmd)),
		StderrTail: boundedTail(redactCommand(string(stderr), opts.WorkspaceRoot, cmd)),
		CwdScope:   redactCommand(cwdScope(cmd.Dir, opts.WorkspaceRoot), opts.WorkspaceRoot, cmd),
	}
	if timedOut {
		d.Kind, d.Retryable = "timeout", true
		d.NextAction = "check the external service or child process, then retry"
	} else {
		var exit interface{ ExitCode() int }
		if errors.As(cause, &exit) {
			code := exit.ExitCode()
			d.ExitCode = &code
			var processExit *exec.ExitError
			if errors.As(cause, &processExit) {
				status, ok := processExit.Sys().(syscall.WaitStatus)
				if ok && status.Signaled() {
					d.Kind = "signal"
					d.Signal = strings.ToUpper(status.Signal().String())
					d.Retryable = true
					d.NextAction = "inspect why the process was terminated, then retry if safe"
				}
			}
		}
	}
	if d.NextAction == "" {
		combined := strings.ToLower(d.StdoutTail + "\n" + d.StderrTail)
		switch {
		case isMissingExecutable(cause):
			d.Kind = "missing_executable"
			d.NextAction = "install the executable or correct PATH"
		case strings.Contains(combined, "index.lock") || strings.Contains(combined, "another git process"):
			d.Kind, d.Retryable = "contention", true
			d.NextAction = "wait for the other process; remove a stale lock only after confirming no process owns it"
		case strings.Contains(combined, "authentication") || strings.Contains(combined, "not logged") || strings.Contains(combined, "bad credentials"):
			d.Kind = "authentication"
			d.NextAction = "authenticate the executable, then retry"
		default:
			d.NextAction = "inspect the retained stderr/stdout tail and correct the command condition"
		}
	}
	return &ExternalError{Diagnostic: d, cause: cause}
}

type recordedExitError struct{ code int }

func (e recordedExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e recordedExitError) ExitCode() int { return e.code }

// NewRecordedExitError reconstructs a typed failure from a durable exit marker
// after the original process is no longer available to inspect. Its cause still
// exposes ExitCode(), while the shared diagnostic policy classifies and redacts
// the retained output exactly like a live command failure.
func NewRecordedExitError(cmd *exec.Cmd, opts RunOptions, stdout, stderr []byte, exitCode int) error {
	return NewExternalError(cmd, opts, stdout, stderr, recordedExitError{code: exitCode}, false)
}

func isMissingExecutable(err error) bool {
	var execErr *exec.Error
	var pathErr *os.PathError
	return errors.As(err, &execErr) || (errors.As(err, &pathErr) && errors.Is(pathErr, os.ErrNotExist))
}

func boundedTail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= diagnosticTailBytes {
		return s
	}
	return "[truncated] " + s[len(s)-diagnosticTailBytes:]
}

func actionableLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func cwdScope(cwd, root string) string {
	if cwd == "" || root == "" {
		return "<undisclosed>"
	}
	absCWD, cwdErr := filepath.Abs(cwd)
	absRoot, rootErr := filepath.Abs(root)
	if cwdErr != nil || rootErr != nil {
		return "<undisclosed>"
	}
	rel, err := filepath.Rel(absRoot, absCWD)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "<outside-workspace>"
	}
	if rel == "." {
		return "."
	}
	return "<workspace>/" + filepath.ToSlash(rel)
}

var (
	helperSecret    = regexp.MustCompile(`(?im)^([\t ]*(?:password|passwd|token|access_token|oauth_token)[\t ]*=[\t ]*)[^\r\n]*`)
	unixPathPattern = regexp.MustCompile(`(^|[ =(:])(/[[:alnum:]_.~@%+,:=-]+(?:/[[:alnum:]_.~@%+,:=-]+)*)`)
	urlSecret       = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
	tokenPattern    = regexp.MustCompile(`(?i)\b(?:gh[pousr]_[A-Za-z0-9_]{8,}|github_pat_[A-Za-z0-9_]{8,}|bearer\s+[A-Za-z0-9._~+/=-]{8,})\b`)
)

// Redact applies the shared disclosure policy used by subprocess wrappers.
// Secret-valued environment variables, recognizable tokens, URL credentials,
// and absolute paths outside workspaceRoot never survive it.
func Redact(value, workspaceRoot string) string {
	return redactEnvironment(value, workspaceRoot, os.Environ())
}

func redactCommand(value, workspaceRoot string, cmd *exec.Cmd) string {
	// Protect both inherited parent secrets and the child's effective environment.
	// Values are used only for replacement; none are retained in diagnostics.
	return redactEnvironment(value, workspaceRoot, append(os.Environ(), cmd.Environ()...))
}

func redactEnvironment(value, workspaceRoot string, environment []string) string {
	secrets := []string{}
	seen := map[string]bool{}
	for _, entry := range environment {
		name, secret, ok := strings.Cut(entry, "=")
		if !ok || len(secret) < 4 || !secretName(name) {
			continue
		}
		// Streaming callers may sanitize complete lines independently. Protect
		// nonempty CR/LF components too, so splitting a known multiline secret
		// does not turn its fragments into unrecognized diagnostic text.
		values := append([]string{secret}, strings.FieldsFunc(secret, func(r rune) bool { return r == '\n' || r == '\r' })...)
		for _, value := range values {
			if !seen[value] {
				secrets = append(secrets, value)
				seen[value] = true
			}
		}
	}
	// Longest matches win at the same position. Matching scans the original
	// input once, so a shorter parent value cannot corrupt a child match and
	// emitted redaction markers are never processed as secret input.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	// Discover all spans against the original text. Neither structural
	// classification nor known-secret matching may erase the other's syntax.
	spans := structuralSpans(value, workspaceRoot)
	for i := 0; i < len(value); i++ {
		for _, secret := range secrets {
			if strings.HasPrefix(value[i:], secret) {
				spans = append(spans, disclosureSpan{i, i + len(secret), "<redacted>"})
				break
			}
		}
	}
	return renderDisclosure(value, spans)
}

type disclosureSpan struct {
	start, end  int
	replacement string
}

func structuralSpans(value, workspaceRoot string) []disclosureSpan {
	var spans []disclosureSpan
	for _, match := range helperSecret.FindAllStringSubmatchIndex(value, -1) {
		spans = append(spans, disclosureSpan{match[3], match[1], "<redacted>"})
	}
	for _, match := range tokenPattern.FindAllStringIndex(value, -1) {
		spans = append(spans, disclosureSpan{match[0], match[1], "<redacted>"})
	}
	for _, match := range urlSecret.FindAllStringSubmatchIndex(value, -1) {
		spans = append(spans, disclosureSpan{match[3], match[1] - 1, "<redacted>"})
	}
	if runtime.GOOS != "windows" {
		spans = append(spans, quotedPathSpans(value, workspaceRoot)...)
		for _, match := range unixPathPattern.FindAllStringSubmatchIndex(value, -1) {
			start, end := match[4], match[5]
			spans = append(spans, disclosureSpan{start, end, disclosedPath(value[start:end], workspaceRoot)})
		}
	}
	return spans
}

// Quoted paths can contain whitespace that the unquoted path matcher cannot
// distinguish from ordinary prose. An unterminated quote ends at the newline.
func quotedPathSpans(value, workspaceRoot string) []disclosureSpan {
	var spans []disclosureSpan
	for i := 0; i < len(value); i++ {
		quote := value[i]
		if (quote != '\'' && quote != '"') || i+1 >= len(value) || value[i+1] != '/' {
			continue
		}
		end := i + 1
		for end < len(value) && value[end] != quote && value[end] != '\n' && value[end] != '\r' {
			end++
		}
		spans = append(spans, disclosureSpan{i + 1, end, disclosedPath(value[i+1:end], workspaceRoot)})
		i = end - 1
		if end < len(value) && value[end] == quote {
			i = end
		}
	}
	return spans
}

func renderDisclosure(value string, spans []disclosureSpan) string {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	var out strings.Builder
	cursor := 0
	for i := 0; i < len(spans); {
		span := spans[i]
		i++
		// Overlap means both protected structures must be hidden as a union.
		// A known larger secret cannot leave fragments around a URL/path span.
		for i < len(spans) && spans[i].start < span.end {
			if spans[i].end > span.end {
				span.end = spans[i].end
			}
			span.replacement = "<redacted>"
			i++
		}
		out.WriteString(value[cursor:span.start])
		out.WriteString(span.replacement)
		cursor = span.end
	}
	out.WriteString(value[cursor:])
	return out.String()
}

// SanitizeTail is the compatibility boundary for wrappers that must manage
// their own streaming pipes. It applies the same redaction and 4 KiB bound as
// Run before legacy callers can interpolate retained output (issue #876).
func SanitizeTail(value, workspaceRoot string) string {
	return boundedTail(Redact(value, workspaceRoot))
}

// SanitizeCommandTail applies the same bounded disclosure policy as Run to
// wrappers with custom stream handling, including the effective child secrets.
// It is pure: it does not execute cmd or manufacture a process error.
func SanitizeCommandTail(cmd *exec.Cmd, value, workspaceRoot string) string {
	return boundedTail(redactCommand(value, workspaceRoot, cmd))
}

func secretName(name string) bool {
	name = strings.ToUpper(name)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "CREDENTIAL", "AUTHORIZATION"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func disclosedPath(path, root string) string {
	if root == "" {
		return "<outside-workspace>"
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "<outside-workspace>"
	}
	rel, err := filepath.Rel(absRoot, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "<outside-workspace>"
	}
	if rel == "." {
		return "<workspace>"
	}
	return "<workspace>/" + filepath.ToSlash(rel)
}
