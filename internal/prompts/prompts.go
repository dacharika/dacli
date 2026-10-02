// Package prompts is the registry for every multi-sentence piece of agent-
// facing prose dacli emits: native-host workflow guidance, brief
// headers, MCP tool descriptions.
//
// The doctrine is the same one shortcuts already follow:
// PROMPTS ARE DATA, NOT CODE. A prompt buried in an Fprintf chain cannot be
// audited, diffed in a PR, or improved without recompiling — and for this
// tool the prompts are load-bearing artifacts. So the defaults live as
// template files embedded at build time (still files in the repo: reviewable,
// blame-able), and a workspace may override any of them by placing a file of
// the same name in .dacli/prompts/.
//
// The boundary, stated so it doesn't erode: one-line refusal/usage messages
// STAY in code. They are the exit-code contract's surface, tested by string,
// and versioned with the behavior they describe. Prose blocks move here;
// contract lines do not.
package prompts

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"

	"github.com/mlnomadpy/dacli/internal/mdstore"
)

// Schema is the provider-neutral autonomous-delivery contract understood by
// this binary. Overrides name it explicitly so old prose cannot silently run
// after lifecycle semantics change (issue #707).
const (
	Schema  = "dacli-prompt/v1"
	Version = "native-governance/v1"
)

const declarationPrefix = "<!-- dacli-prompt schema: dacli-prompt/v1 base: "

// Resolved describes the template selected for an invocation. Content omits
// the declaration line: metadata is executable compatibility data, not prose
// worth charging every agent to reread.
type Resolved struct {
	Content    string
	Schema     string
	Version    string
	Hash       string
	Overridden bool
}

func parseDeclared(name, raw string, overridden bool) (Resolved, error) {
	line, content, ok := strings.Cut(raw, "\n")
	if !ok || !strings.HasPrefix(line, declarationPrefix) || !strings.HasSuffix(line, " -->") {
		return Resolved{}, fmt.Errorf("prompt %s: missing declaration %q", name, declarationPrefix+Version+" -->")
	}
	version := strings.TrimSuffix(strings.TrimPrefix(line, declarationPrefix), " -->")
	if version != Version {
		return Resolved{}, fmt.Errorf("prompt %s: incompatible base version %q (binary requires %q)", name, version, Version)
	}
	sum := sha256.Sum256([]byte(content))
	return Resolved{Content: content, Schema: Schema, Version: version, Hash: hex.EncodeToString(sum[:]), Overridden: overridden}, nil
}

//go:embed tpl
var embedded embed.FS

// tplCache memoizes compiled templates keyed by (name, raw content). Template
// text is immutable once resolved — an embedded default never changes at
// runtime, and a workspace override changes only if the file on disk changes,
// which changes its content and thus the cache key. Repeat renders of the same
// prompt (e.g. a brief header emitted per task) reuse the parsed tree.
var (
	tplMu    sync.RWMutex
	tplCache = map[string]*template.Template{}
)

func compiled(name, content string) (*template.Template, error) {
	key := name + "\x00" + content
	tplMu.RLock()
	t, ok := tplCache[key]
	tplMu.RUnlock()
	if ok {
		return t, nil
	}
	t, err := template.New(name).Parse(content)
	if err != nil {
		return nil, err
	}
	tplMu.Lock()
	tplCache[key] = t
	tplMu.Unlock()
	return t, nil
}

// Render resolves a prompt by name (workspace override first, embedded
// default second) and executes it as a text/template against data.
func Render(overrideDir, name string, data any) (string, error) {
	content, _, err := Resolve(overrideDir, name)
	if err != nil {
		return "", err
	}
	t, err := compiled(name, content)
	if err != nil {
		return "", fmt.Errorf("prompt %s: %w", name, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("prompt %s: %w", name, err)
	}
	return b.String(), nil
}

// Resolve returns a prompt's raw template and whether a workspace override
// supplied it.
func Resolve(overrideDir, name string) (content string, overridden bool, err error) {
	r, err := ResolveContract(overrideDir, name)
	return r.Content, r.Overridden, err
}

// ResolveContract resolves and validates one versioned prompt template.
func ResolveContract(overrideDir, name string) (Resolved, error) {
	if overrideDir != "" {
		if raw, err := os.ReadFile(filepath.Join(overrideDir, name+".md")); err == nil {
			return parseDeclared(name, string(raw), true)
		}
	}
	raw, err := embedded.ReadFile("tpl/" + name + ".md")
	if err != nil {
		return Resolved{}, fmt.Errorf("no such prompt %q", name)
	}
	return parseDeclared(name, string(raw), false)
}

// Names lists the embedded registry, sorted.
func Names() []string {
	entries, _ := embedded.ReadDir("tpl")
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(out)
	return out
}

// mcpDescs is the parsed mcp_tools.md, built exactly once: the file is
// embedded and immutable at runtime, so parsing the whole doc on every tool
// registration (N tools → N full parses) is pure waste.
var (
	mcpDescOnce sync.Once
	mcpDescMap  map[string]string
)

func mcpDescs() map[string]string {
	mcpDescOnce.Do(func() {
		raw, err := embedded.ReadFile("tpl/mcp_tools.md")
		if err != nil {
			panic("prompts: mcp_tools.md missing from embed")
		}
		resolved, err := parseDeclared("mcp_tools", string(raw), false)
		if err != nil {
			panic("prompts: mcp_tools.md declaration: " + err.Error())
		}
		doc, err := mdstore.Parse(resolved.Content)
		if err != nil {
			panic("prompts: mcp_tools.md unparseable: " + err.Error())
		}
		// Section() matched case-insensitively; key by lower-cased title so
		// MCPDesc keeps that behavior.
		mcpDescMap = make(map[string]string, len(doc.Sections))
		for _, s := range doc.Sections {
			mcpDescMap[strings.ToLower(s.Title)] = strings.TrimSpace(s.Content)
		}
	})
	return mcpDescMap
}

// MCPDesc returns one MCP tool's description from the sectioned registry
// file mcp_tools.md — one file so the entire agent-facing tool documentation
// is auditable in a single review. Missing sections panic at init, guarded
// by a test that walks every registered tool.
func MCPDesc(tool string) string {
	desc, ok := mcpDescs()[strings.ToLower(tool)]
	if !ok || desc == "" {
		panic("prompts: no description for MCP tool " + tool)
	}
	return desc
}
