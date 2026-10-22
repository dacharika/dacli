// Package mdstore reads and writes the markdown-with-frontmatter files that
// make up a workspace.
//
// Two invariants this package must never violate:
//
//  1. Round-tripping preserves unknown frontmatter keys and full-line
//     comments. A file written by a newer dacli, a third-party tool, or a
//     human must survive being read and rewritten by this build with nothing
//     dropped.
//
//  2. Writes are atomic and power-loss durable. Everything goes to a temp file
//     in the same directory, whose data is synced before rename; the containing
//     directory is synced after rename. Atomic rename alone prevents readers
//     from seeing a torn file, but does not guarantee the rename survives a
//     machine crash. Half a task file is worse than none, and an acknowledged
//     task or event that vanishes after power loss is worse than an I/O error.
//
// The frontmatter dialect is deliberately narrow — top-level `key: value`
// scalars, inline lists `[a, b]`, inline maps `{k: v}`, and indented blocks
// preserved verbatim. The format spec only uses those; a full YAML parser is
// a dependency and an attack surface this module does not need.
package mdstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Entry is one frontmatter line (or block). Key == "" means a full-line
// comment or blank line, preserved verbatim in Raw.
type Entry struct {
	Key   string
	Value string // scalar value as written (may carry a trailing comment)
	Block string // for `key:` followed by indented lines: those lines, verbatim
	Raw   string // for Key == "": the exact line
}

// Front is ordered frontmatter. Order is preserved so rewrites produce
// minimal diffs — these files live in git and get reviewed by humans.
type Front struct {
	entries []Entry
}

// Get returns the cleaned scalar value: surrounding quotes removed, trailing
// ` # comment` stripped. Returns false for absent keys and block entries.
func (f *Front) Get(k string) (string, bool) {
	for _, e := range f.entries {
		if e.Key == k && e.Block == "" {
			return clean(e.Value), true
		}
	}
	return "", false
}

// GetBlock returns the raw indented block under a key, if any.
func (f *Front) GetBlock(k string) (string, bool) {
	for _, e := range f.entries {
		if e.Key == k && e.Block != "" {
			return e.Block, true
		}
	}
	return "", false
}

// Set adds or replaces a scalar value, preserving position on replace. The
// value is stored render-ready: quoteScalar wraps and escapes anything that
// would not round-trip (a newline that would split the file or orphan a
// keyless line, an inline " #" read as a comment, surrounding whitespace),
// mirroring how a value read from a file is already render-ready. Without this
// a free-text flag value containing a newline could make a task file
// unparseable and the task silently invisible (dacli 170).
func (f *Front) Set(k, v string) {
	rv := quoteScalar(v)
	for i, e := range f.entries {
		if e.Key == k {
			f.entries[i] = Entry{Key: k, Value: rv}
			return
		}
	}
	f.entries = append(f.entries, Entry{Key: k, Value: rv})
}

// SetBlock adds or replaces an indented-block value (e.g. the github:
// mapping). Block lines must arrive already indented; they render verbatim.
func (f *Front) SetBlock(k, block string) {
	for i, e := range f.entries {
		if e.Key == k {
			f.entries[i] = Entry{Key: k, Block: block}
			return
		}
	}
	f.entries = append(f.entries, Entry{Key: k, Block: block})
}

// Delete removes a keyed entry, if present.
func (f *Front) Delete(k string) {
	for i, e := range f.entries {
		if e.Key == k {
			f.entries = append(f.entries[:i], f.entries[i+1:]...)
			return
		}
	}
}

// Keys returns the keyed entries in order.
func (f *Front) Keys() []string {
	var out []string
	for _, e := range f.entries {
		if e.Key != "" {
			out = append(out, e.Key)
		}
	}
	return out
}

// GetText returns a key's human-readable text: a scalar's cleaned value, or
// a literal/folded block's dedented content joined with newlines.
func (f *Front) GetText(k string) (string, bool) {
	for _, e := range f.entries {
		if e.Key != k {
			continue
		}
		if e.Block == "" {
			return clean(e.Value), true
		}
		var lines []string
		for _, l := range strings.Split(e.Block, "\n") {
			lines = append(lines, strings.TrimSpace(l))
		}
		return strings.TrimSpace(strings.Join(lines, "\n")), true
	}
	return "", false
}

// GetList parses an inline list value: `[a, b, "c d"]`.
func (f *Front) GetList(k string) []string {
	v, ok := f.Get(k)
	if !ok || !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil
	}
	return splitTop(v[1 : len(v)-1])
}

// SetList encodes v as an inline list and stores it under k — the exact
// inverse of GetList: any element containing a comma, bracket, brace, quote,
// or leading/trailing whitespace is quoted so it round-trips losslessly
// through splitTop/clean.
func (f *Front) SetList(k string, v []string) {
	quoted := make([]string, len(v))
	for i, elem := range v {
		quoted[i] = quoteListElem(elem)
	}
	f.Set(k, "["+strings.Join(quoted, ", ")+"]")
}

// quoteListElem wraps an inline-list element in double quotes when it
// contains a character splitTop/clean treat as significant (a comma would
// otherwise re-split the element into extra list entries; brackets, braces,
// or a `#` would otherwise be misread as structure or a comment), or when
// leading/trailing whitespace would otherwise be trimmed on read-back. Any
// backslash or embedded double quote is backslash-escaped so an element that
// carries both quote characters plus a comma still decodes to exactly one
// list entry, byte-for-byte (see clean's unescapeDouble counterpart).
func quoteListElem(s string) string {
	if !strings.ContainsAny(s, ",[]{}#\"'") && s == strings.TrimSpace(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '"' || c == '\\' {
			b.WriteByte('\\')
			b.WriteByte(c)
		} else {
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// GetMap parses an inline map value: `{a: 1, b: two}`.
func (f *Front) GetMap(k string) map[string]string {
	v, ok := f.Get(k)
	if !ok || !strings.HasPrefix(v, "{") || !strings.HasSuffix(v, "}") {
		return nil
	}
	out := map[string]string{}
	for _, part := range splitTop(v[1 : len(v)-1]) {
		if i := strings.Index(part, ":"); i >= 0 {
			out[strings.TrimSpace(part[:i])] = strings.TrimSpace(part[i+1:])
		}
	}
	return out
}

// clean strips a trailing ` # comment` (outside quotes) and surrounding
// quotes, unescaping a double-quoted value's backslash escapes (the inverse
// of quoteListElem).
func clean(v string) string {
	v = strings.TrimSpace(v)
	inQ := byte(0)
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case inQ == '"' && c == '\\' && i+1 < len(v):
			i++
		case inQ != 0:
			if c == inQ {
				inQ = 0
			}
		case c == '"' || c == '\'':
			inQ = c
		case c == '#' && i > 0 && (v[i-1] == ' ' || v[i-1] == '\t'):
			v = strings.TrimSpace(v[:i])
			i = len(v)
		}
	}
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		q := v[0]
		v = v[1 : len(v)-1]
		if q == '"' {
			v = unescapeDouble(v)
		}
	}
	return v
}

// unescapeDouble reverses quoteListElem's backslash-escaping of `"` and `\`
// inside a double-quoted element.
func unescapeDouble(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '"', '\\':
				i++
				b.WriteByte(s[i])
				continue
			case 'n':
				i++
				b.WriteByte('\n')
				continue
			case 'r':
				i++
				b.WriteByte('\r')
				continue
			case 't':
				i++
				b.WriteByte('\t')
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// quoteScalar renders v as a frontmatter scalar that round-trips through clean.
// A value is emitted bare when it is safe; otherwise it is double-quoted with
// ", \, and CR/LF/TAB escaped. The triggers are exactly the cases clean would
// otherwise mis-handle: surrounding whitespace (trimmed on read), an inline
// " #" (read as a comment), an embedded newline (which would break the
// one-key-per-line frontmatter grammar), or a leading quote/YAML indicator.
func quoteScalar(s string) string {
	needs := s == "" ||
		s != strings.TrimSpace(s) ||
		strings.ContainsAny(s, "\r\n\t\"'#") ||
		strings.HasPrefix(s, "|") || strings.HasPrefix(s, ">") ||
		strings.HasPrefix(s, "&") || strings.HasPrefix(s, "*") ||
		strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{")
	if !needs {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// splitTop splits on commas not nested inside quotes/brackets/braces.
func splitTop(s string) []string {
	var out []string
	depth := 0
	inQ := byte(0)
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQ == '"' && c == '\\' && i+1 < len(s):
			i++
		case inQ != 0:
			if c == inQ {
				inQ = 0
			}
		case c == '"' || c == '\'':
			inQ = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			if p := strings.TrimSpace(s[start:i]); p != "" {
				out = append(out, clean(p))
			}
			start = i + 1
		}
	}
	if p := strings.TrimSpace(s[start:]); p != "" {
		out = append(out, clean(p))
	}
	return out
}

// Section is a markdown heading and the content under it, up to the next
// heading at any level. Level 0 with empty Title is body text before the
// first heading.
type Section struct {
	Level   int
	Title   string
	Content string
}

// Doc is a parsed markdown file.
type Doc struct {
	Front    Front
	Sections []Section
	// SourcePath is populated by ReadFile for provenance consumers. It is not
	// rendered or serialized as part of the document contents.
	SourcePath string `json:"-"`
}

// Section returns the first section with the given title, case-insensitively.
func (d *Doc) Section(title string) (Section, bool) {
	for _, s := range d.Sections {
		if strings.EqualFold(s.Title, title) {
			return s, true
		}
	}
	return Section{}, false
}

// SetSection replaces the content of the named section, or appends a new
// level-2 section if absent.
func (d *Doc) SetSection(title, content string) {
	for i, s := range d.Sections {
		if strings.EqualFold(s.Title, title) {
			d.Sections[i].Content = content
			return
		}
	}
	d.Sections = append(d.Sections, Section{Level: 2, Title: title, Content: content})
}

// Parse reads a document. Malformed frontmatter is an error, never a silent
// partial parse — a file half-understood is a file about to be corrupted on
// rewrite.
func Parse(raw string) (*Doc, error) {
	d := &Doc{}
	// Normalize CRLF to LF before any structural parsing. Git for Windows
	// checks files out with CRLF by default (core.autocrlf=true), which would
	// make the `---\n` frontmatter probe below fail and silently yield an
	// empty Front — every id/owner/status read blank, with no error (dacli
	// 169). A .gitattributes pins these files to LF in the repo; this makes
	// the parser robust regardless of how a file reached disk.
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	body := raw

	if strings.HasPrefix(raw, "---\n") {
		rest := raw[4:]
		end := strings.Index(rest, "\n---\n")
		var fm string
		switch {
		// An immediately-closing block (`---\n---\n`) is EMPTY frontmatter, not
		// a malformed one. Render emits exactly this to disambiguate a body that
		// opens with a `---` rule, so Parse must accept it or the round trip
		// breaks (dacli 204).
		case strings.HasPrefix(rest, "---\n"):
			fm, body = "", rest[4:]
		case rest == "---":
			fm, body = "", ""
		case end >= 0:
			fm, body = rest[:end], rest[end+5:]
		case strings.HasSuffix(rest, "\n---"):
			fm, body = rest[:len(rest)-4], ""
		default:
			return nil, fmt.Errorf("mdstore: unterminated frontmatter")
		}
		if err := parseFront(&d.Front, fm); err != nil {
			return nil, err
		}
	}

	d.Sections = parseSections(body)
	return d, nil
}

func parseFront(f *Front, fm string) error {
	lines := strings.Split(fm, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Blank lines and full-line comments are preserved verbatim.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			f.entries = append(f.entries, Entry{Raw: line})
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			return fmt.Errorf("mdstore: unexpected indented frontmatter line %q", line)
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			return fmt.Errorf("mdstore: frontmatter line without key: %q", line)
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])

		// A bare `key:` (or one whose value is only a comment), or a YAML
		// literal/folded indicator (| |- > >-), followed by indented lines
		// is a block, preserved verbatim. The indicator case exists because
		// real native skills write `description: |` — found when the first
		// real library import parsed losslessly and read as nothing.
		isIndicator := val == "|" || val == "|-" || val == ">" || val == ">-"
		if val == "" || isIndicator || strings.HasPrefix(val, "#") {
			var block []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || strings.TrimSpace(lines[i+1]) == "") {
				// Trailing blank lines belong to whatever follows, not the block.
				if strings.TrimSpace(lines[i+1]) == "" {
					allBlank := true
					for j := i + 1; j < len(lines); j++ {
						if strings.TrimSpace(lines[j]) != "" {
							allBlank = strings.HasPrefix(lines[j], " ") || strings.HasPrefix(lines[j], "\t")
							break
						}
					}
					if !allBlank {
						break
					}
				}
				i++
				block = append(block, lines[i])
			}
			if len(block) > 0 {
				f.entries = append(f.entries, Entry{Key: key, Value: val, Block: strings.Join(block, "\n")})
				continue
			}
		}
		f.entries = append(f.entries, Entry{Key: key, Value: val})
	}
	return nil
}

// parseSections splits on ATX headings, ignoring headings inside fenced code
// blocks — task bodies contain code, and a `# comment` inside a fence is not
// a heading.
func parseSections(body string) []Section {
	if body == "" {
		return nil
	}
	var out []Section
	cur := Section{Level: 0}
	var buf []string
	inFence := false

	flush := func() {
		// Every stored line is newline-terminated, so Content round-trips
		// byte-exactly: blank lines are real "" entries, and the file's final
		// newline is restored by the terminator on the last line.
		if len(buf) > 0 {
			cur.Content = strings.Join(buf, "\n") + "\n"
		} else {
			cur.Content = ""
		}
		if cur.Level != 0 || cur.Title != "" || strings.TrimSpace(cur.Content) != "" {
			out = append(out, cur)
		}
		buf = nil
	}

	lines := strings.Split(body, "\n")
	// The trailing "" from a final newline is an artifact of Split, not a
	// blank line; dropping it (and re-adding "\n" per line above) normalizes
	// files to newline-terminated, which is the only shape we ever write.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence {
			if lvl, title, ok := heading(line); ok {
				flush()
				cur = Section{Level: lvl, Title: title}
				continue
			}
		}
		buf = append(buf, line)
	}
	flush()
	return out
}

func heading(line string) (int, string, bool) {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 || i >= len(line) || line[i] != ' ' {
		return 0, "", false
	}
	return i, strings.TrimSpace(line[i+1:]), true
}

// Render serializes a Doc: frontmatter entries in order (unknown keys and
// comments verbatim), then sections.
// bodyOpensWithRule reports whether the rendered body would begin with a `---`
// line, which Parse would otherwise read as a frontmatter opener.
func bodyOpensWithRule(d *Doc) bool {
	for _, s := range d.Sections {
		if s.Level > 0 {
			return false // a heading comes first; unambiguous
		}
		if c := strings.TrimLeft(s.Content, "\n"); c != "" {
			return strings.HasPrefix(c, "---")
		}
	}
	return false
}

func Render(d *Doc) string {
	var b strings.Builder
	// A document with no frontmatter whose body opens with `---` (a markdown
	// horizontal rule, or a fenced block someone pasted) is ambiguous on
	// re-read: the leading `---` looks exactly like a frontmatter opener, and
	// Parse would then reject the whole file as unterminated. Emit an explicit
	// empty frontmatter block so the body's `---` can never be mistaken for
	// one. Found by FuzzParseNeverPanics, which asserts every rendered document
	// re-parses — an unparseable task file is skipped by every list path, so
	// the task silently disappears (dacli 204).
	emptyFront := len(d.Front.entries) == 0 && bodyOpensWithRule(d)
	if len(d.Front.entries) > 0 || emptyFront {
		b.WriteString("---\n")
		for _, e := range d.Front.entries {
			switch {
			case e.Key == "":
				b.WriteString(e.Raw)
				b.WriteByte('\n')
			case e.Block != "":
				b.WriteString(e.Key)
				b.WriteByte(':')
				if e.Value != "" {
					b.WriteByte(' ')
					b.WriteString(e.Value)
				}
				b.WriteByte('\n')
				b.WriteString(e.Block)
				b.WriteByte('\n')
			default:
				b.WriteString(e.Key)
				b.WriteString(": ")
				b.WriteString(e.Value)
				b.WriteByte('\n')
			}
		}
		b.WriteString("---\n")
	}
	for _, s := range d.Sections {
		if s.Level > 0 {
			b.WriteString(strings.Repeat("#", s.Level))
			b.WriteByte(' ')
			b.WriteString(s.Title)
			b.WriteByte('\n')
		}
		// Content is newline-terminated by construction (parse) or by the
		// writer's convention (SetSection callers end with \n); normalize
		// rather than double-terminate.
		b.WriteString(s.Content)
		if s.Content != "" && !strings.HasSuffix(s.Content, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ReadFile parses the file at path.
func ReadFile(path string) (*Doc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d, err := Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	d.SourcePath = path
	return d, nil
}

// WriteFile renders d to path atomically and durably. It syncs the temporary
// file before renaming it, then syncs the containing directory before
// reporting success. The directory is created if needed.
func WriteFile(path string, d *Doc) error {
	return WriteBytes(path, []byte(Render(d)), 0o600)
}

// WriteBytes atomically and durably replaces path with data. It is also used
// by non-Markdown workspace records that share the store's crash contract.
func WriteBytes(path string, data []byte, mode os.FileMode) error {
	return writeBytes(path, data, mode, osDurableOps{})
}

type syncFile interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
}

type durableOps interface {
	MkdirAll(string, os.FileMode) error
	CreateTemp(string, string) (syncFile, error)
	Chmod(string, os.FileMode) error
	Rename(string, string) error
	Remove(string) error
	Open(string) (syncFile, error)
}

type osDurableOps struct{}

func (osDurableOps) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
func (osDurableOps) CreateTemp(dir, pattern string) (syncFile, error) {
	return os.CreateTemp(dir, pattern)
}
func (osDurableOps) Chmod(path string, mode os.FileMode) error { return os.Chmod(path, mode) }
func (osDurableOps) Rename(old, new string) error              { return os.Rename(old, new) }
func (osDurableOps) Remove(path string) error                  { return os.Remove(path) }
func (osDurableOps) Open(path string) (syncFile, error)        { return os.Open(path) }

func writeBytes(path string, data []byte, mode os.FileMode, ops durableOps) error {
	if err := ops.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := ops.CreateTemp(filepath.Dir(path), ".dacli-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = ops.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := ops.Chmod(name, mode); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = ops.Remove(name)
		return err
	}
	if err := ops.Rename(name, path); err != nil {
		// A rename fault (cross-device link, EACCES, a dir replaced mid-write,
		// index lock) must not orphan the temp file in the object directory —
		// every workspace write funnels through here, so a transient fault
		// would otherwise litter the tree with .dacli-tmp-* files.
		_ = ops.Remove(name)
		return err
	}
	dir, err := ops.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	if err := dir.Close(); err != nil {
		return err
	}
	return nil
}

// Links extracts every [[wikilink]] target from s. Unresolved targets are
// valid: a dangling link marks something worth writing later, not an error.
func Links(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "[[")
		if i < 0 {
			return out
		}
		j := strings.Index(s[i+2:], "]]")
		if j < 0 {
			return out
		}
		target := strings.TrimSpace(s[i+2 : i+2+j])
		if pipe := strings.Index(target, "|"); pipe >= 0 {
			target = strings.TrimSpace(target[:pipe])
		}
		if target != "" {
			out = append(out, target)
		}
		s = s[i+2+j+2:]
	}
}

// Checkbox is a markdown task-list item.
type Checkbox struct {
	Text string
	Done bool
}

// Checkboxes extracts `- [ ]` / `- [x]` items from section content.
func Checkboxes(content string) []Checkbox {
	var out []Checkbox
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "- [ ] "):
			out = append(out, Checkbox{Text: t[6:], Done: false})
		case strings.HasPrefix(t, "- [x] "), strings.HasPrefix(t, "- [X] "):
			out = append(out, Checkbox{Text: t[6:], Done: true})
		}
	}
	return out
}

// RenderCheckboxes produces the markdown for a checkbox list.
func RenderCheckboxes(boxes []Checkbox) string {
	var b strings.Builder
	for _, c := range boxes {
		if c.Done {
			b.WriteString("- [x] ")
		} else {
			b.WriteString("- [ ] ")
		}
		b.WriteString(c.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// Bullets extracts plain `- item` list entries (not checkboxes).
func Bullets(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") && !strings.HasPrefix(t, "- [") {
			out = append(out, t[2:])
		}
	}
	return out
}
