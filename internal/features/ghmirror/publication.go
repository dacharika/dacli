package ghmirror

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/publication"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func findingMarker(w *workspace.Workspace, noteID string) string {
	return fmt.Sprintf("<!-- dacli-finding:%s ws:%s -->", noteID, w.ID)
}

// findingAboutTask reports whether a finding note names this task in its `about`
// field. The `about` value is one or more `[[ref]]` wikilinks (store.CreateNote
// wraps the --about value in brackets); we match each ref EXACTLY against the
// task — never as a loose substring. A substring test would cross-match: a
// finding about task `1007` (or a ULID that happens to contain the digits)
// would look like it belonged to task `007`, mirroring another task's finding
// onto the wrong issue.
func findingAboutTask(n *mdstore.Doc, t *store.Task) bool {
	about, _ := n.Front.Get("about")
	for _, ref := range aboutRefs(about) {
		if taskMatchesRef(t, ref) {
			return true
		}
	}
	return false
}

// aboutRefs extracts the reference tokens a note's `about` field names. The
// field is stored as one or more `[[ref]]` wikilinks; we return each bracketed
// target, falling back to the whole trimmed string when no wikilink is present.
func aboutRefs(about string) []string {
	var refs []string
	rest := about
	for {
		i := strings.Index(rest, "[[")
		if i < 0 {
			break
		}
		rest = rest[i+2:]
		j := strings.Index(rest, "]]")
		if j < 0 {
			break
		}
		if ref := strings.TrimSpace(rest[:j]); ref != "" {
			refs = append(refs, ref)
		}
		rest = rest[j+2:]
	}
	if len(refs) == 0 {
		if s := strings.TrimSpace(about); s != "" {
			refs = append(refs, s)
		}
	}
	return refs
}

// taskMatchesRef reports whether ref names this task EXACTLY — the ULID (with or
// without the `t-` prefix), the slug, the bare or zero-padded sequence, or the
// `NNN-slug` form. It mirrors store.matchesRef (unexported there) so the mirror
// resolves a finding's target the same way the rest of dacli resolves a task
// ref, and never by a loose substring that cross-matches a sibling task.
func taskMatchesRef(t *store.Task, ref string) bool {
	if ref == "" {
		return false
	}
	if t.ID == ref || t.Slug == ref || strings.TrimPrefix(t.ID, "t-") == ref {
		return true
	}
	if strconv.Itoa(t.Seq) == ref {
		return true
	}
	seq3 := fmt.Sprintf("%03d", t.Seq)
	return seq3 == ref || seq3+"-"+t.Slug == ref
}

// findingText collects the note's rendered body — the same rule the brief and PR
// assemblers use: content runs from the level-1 title to the next heading, so we
// concatenate every section's content.
func findingText(n *mdstore.Doc) string {
	var b strings.Builder
	for _, s := range n.Sections {
		b.WriteString(s.Content)
	}
	return strings.TrimSpace(b.String())
}

// findingComment renders the comment body a finding becomes: the marker leads
// (for idempotency), the severity is surfaced, then the finding text and a
// backlink to the dacli note.
func findingComment(mk, severity, id, text string) string {
	var b strings.Builder
	b.WriteString(mk + "\n\n")
	if severity != "" {
		b.WriteString("**" + severity + "** ")
	}
	b.WriteString(text + "\n\n")
	b.WriteString("_Filed as dacli finding " + id + "; the workspace is the source of truth._\n")
	return b.String()
}

func findingDocIDs(n *mdstore.Doc) []string {
	id, _ := n.Front.Get("id")
	aliases, _ := n.Front.Get("mirror_aliases")
	ids := []string{id}
	for _, alias := range strings.Split(aliases, ",") {
		if alias = strings.TrimSpace(alias); alias != "" && alias != id {
			ids = append(ids, alias)
		}
	}
	return ids
}

// commentsHaveMarker reports whether any existing comment already carries the
// marker — the idempotency check that stops a re-push from re-posting a finding.
func commentsHaveMarker(comments []string, mk string) bool {
	for _, c := range comments {
		if strings.Contains(c, mk) {
			return true
		}
	}
	return false
}

// issueComments fetches the bodies of an issue's existing comments so the mirror
// can skip a finding it already posted (idempotency by marker substring). It
// returns an error on a fetch or parse failure so the caller can tell "the issue
// has no comments" (safe to post) from "we could not read the comments" (must
// NOT post) — dacli 220: an unparseable list returned as an empty slice made
// mirrorFindings believe nothing had been posted yet and re-post every finding
// on the next push, so a transient gh/JSON hiccup duplicated every comment.
func issueComments(w *workspace.Workspace, repo string, num int) ([]string, error) {
	out, err := ghRepo(w, repo, "issue", "view", strconv.Itoa(num), "--json", "comments")
	if err != nil {
		return nil, err
	}
	var v struct {
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("parse issue %d comments: %w", num, err)
	}
	bodies := make([]string, 0, len(v.Comments))
	for _, c := range v.Comments {
		bodies = append(bodies, c.Body)
	}
	return bodies, nil
}

// mirrorFindings posts each finding note about this task as a comment on the
// mirrored issue, idempotently (a finding whose marker is already present is
// skipped), and returns the count actually posted. A read or post failure is a
// partial apply and therefore fails the push. Existing comments are fetched once
// per task so N findings cost one extra read, not N.
//
// notes is the project's finding notes, read ONCE by the caller before the task
// loop (dacli 245). Reading them here made the mirror O(tasks × notes) —
// 579,551,265 ns/op and 341 MB per push at 238 tasks, versus 2,433,990 ns/op
// and 1.4 MB hoisted. Take the slice; never re-read it per task.
func mirrorFindings(w *workspace.Workspace, repo string, num int, t *store.Task, notes []*mdstore.Doc) (int, error) {
	todo, err := findingsToPost(w, repo, num, t, notes)
	if err != nil {
		// If we cannot read the existing comments we cannot tell which findings
		// are already posted; posting anyway would duplicate every one (dacli 220).
		// Fail loudly; the next push retries once the read succeeds.
		return 0, fmt.Errorf("read finding comments for issue #%d: %w", num, err)
	}
	posted := 0
	for _, n := range todo {
		id, _ := n.Front.Get("id")
		body := findingCommentBody(w, n)
		commentOut, err := ghRepo(w, repo, "issue", "comment", strconv.Itoa(num), "--body", body)
		if err != nil {
			return posted, fmt.Errorf("post finding %s on issue #%d: %w (%s)", id, num, err, commentOut)
		}
		posted++
	}
	return posted, nil
}

func findingCommentBody(w *workspace.Workspace, n *mdstore.Doc) string {
	id, _ := n.Front.Get("id")
	markers := make([]string, 0, len(findingDocIDs(n)))
	for _, sourceID := range findingDocIDs(n) {
		markers = append(markers, findingMarker(w, sourceID))
	}
	sev, _ := n.Front.Get("severity")
	return findingComment(strings.Join(markers, "\n"), sev, id, findingText(n))
}

// findingsToPost is the SHARED decision that names which finding notes about
// task t are NOT yet posted as a comment on issue num — the notes a push would
// comment. Both the real mirror (mirrorFindings, which posts them) and the
// --dry-run preview (which prints them) run this one function, so the preview
// can never drift from what a real push would comment (task 294). An error means
// the existing comments could not be read (dacli 220): the caller treats that as
// "post nothing" so a transient read failure never duplicates every comment.
func findingsToPost(w *workspace.Workspace, repo string, num int, t *store.Task, notes []*mdstore.Doc) ([]*mdstore.Doc, error) {
	if num == 0 || len(notes) == 0 {
		return nil, nil
	}
	var about []*mdstore.Doc
	for _, n := range notes {
		if findingAboutTask(n, t) && findingText(n) != "" {
			about = append(about, n)
		}
	}
	if len(about) == 0 {
		return nil, nil
	}
	existing, err := issueComments(w, repo, num)
	if err != nil {
		return nil, err
	}
	var todo []*mdstore.Doc
	for _, n := range about {
		posted := false
		for _, id := range findingDocIDs(n) {
			if commentsHaveMarker(existing, findingMarker(w, id)) {
				posted = true
				break
			}
		}
		if !posted {
			todo = append(todo, n)
		}
	}
	return todo, nil
}

// canonicalFindingDocs applies the same semantic grouping to task comments.
// These docs are read-only projection inputs, so the representative may carry
// the merged evidence without changing any local note on disk.
func canonicalFindingDocs(notes []*mdstore.Doc) []*mdstore.Doc {
	files := make([]noteFile, 0, len(notes))
	for _, doc := range notes {
		id, _ := doc.Front.Get("id")
		title := ""
		for _, section := range doc.Sections {
			if section.Level == 1 {
				title = section.Title
				break
			}
		}
		files = append(files, noteFile{doc: doc, id: id, title: title})
	}
	groups := canonicalNoteFiles(files)
	out := make([]*mdstore.Doc, 0, len(groups))
	for _, group := range groups {
		group.doc.Front.Set("mirror_aliases", strings.Join(group.aliases, ","))
		if len(group.evidence) > 1 {
			group.doc.Sections = append(group.doc.Sections, mdstore.Section{Content: "\n---\n\n" + strings.Join(group.evidence[1:], "\n\n---\n\n") + "\n"})
		}
		out = append(out, group.doc)
	}
	return out
}

// --- status labels (G1 residual) ---

// statusLabel is the per-status label a mirrored issue carries, tracking the
// task's status folder (status:open | status:active | status:blocked |
// status:done).
func statusLabel(s model.Status) string { return "status:" + string(s) }

// otherStatusLabels are the status labels a mirrored issue must NOT carry given
// its current status — the stale labels to strip so a task that changed folders
// doesn't accumulate a second status: label.
func otherStatusLabels(s model.Status) []string {
	var out []string
	for _, o := range model.AllStatuses {
		if o != s {
			out = append(out, statusLabel(o))
		}
	}
	return out
}

// ensureLabel creates a label if missing, with a stable color. Best-effort:
// --force turns an "already exists" into a harmless update (also re-applying the
// canonical color) instead of an error, so a repeated push never fails on label
// creation and the label set stays visually consistent across pushes.
func ensureLabel(w *workspace.Workspace, repo, name string) {
	_, _ = ghRepo(w, repo, "label", "create", name, "--color", labelColor(name), "--force")
}

// labelColor returns a stable GitHub label color (6 hex digits, no leading #)
// for any label dacli emits. Colors are fixed per category so a pre-created set
// is visually consistent and a re-push (label create --force) never randomizes
// them: type: labels share a hue, severities run hot→cool by seriousness, and
// area: labels share one neutral tint.
func labelColor(name string) string {
	switch name {
	case "finding":
		return "d73a4a" // red — a discovered problem
	case "decision":
		return "0075ca" // blue — a recorded choice
	case "type:finding":
		return "5319e7" // purple family (type:)
	case "type:task":
		return "8a63d2"
	case "type:decision":
		return "6f42c1"
	case "severity:major":
		return "b60205" // dark red — most serious
	case "severity:moderate":
		return "d93f0b" // orange
	case "severity:minor":
		return "fbca04" // yellow
	case "severity:unspecified":
		return "cccccc" // gray — no severity carried
	}
	switch {
	case strings.HasPrefix(name, "status:"):
		return "0e8a16" // green family — lifecycle
	case strings.HasPrefix(name, "area:"):
		return "bfd4f2" // light blue — code slice
	}
	return "ededed"
}

// baseLabels is the full STATIC label set every push pre-creates once up front,
// so no issue-create ever races a not-yet-created label (the ensureLabel
// flakiness the G6 spec targets). area: labels are dynamic (derived per note or
// task) and ensured just-in-time before the issue that carries them.
func baseLabels() []string {
	labels := []string{
		"finding", "decision",
		"type:finding", "type:task", "type:decision",
		"severity:major", "severity:moderate", "severity:minor", "severity:unspecified",
	}
	for _, s := range model.AllStatuses {
		labels = append(labels, statusLabel(s))
	}
	return labels
}

// precreateLabels creates the base label set (plus any dynamic extras, e.g. the
// area: label a task project derives) with stable colors ONCE, before any
// issue-create references them — so under a flaky network a missing label never
// fails a push mid-loop. Best-effort per label; a create that later carries the
// label still fails loudly if the label genuinely could not be made.
func precreateLabels(w *workspace.Workspace, repo string, extra ...string) {
	// One list, then create only what is missing. This used to fire an
	// unconditional `label create --force` per label — 13 base labels plus the
	// extras — at the top of EVERY push, so a repo whose labels were created
	// on the first push paid ~13 network round-trips on every push after it to
	// recreate them identically. A repo already set up now costs one call.
	//
	// A failed or unparseable list degrades to the old behavior rather than
	// skipping creation: an unknown label set must not mean "assume they all
	// exist", because a missing label makes every later --add-label fail.
	existing, err := listLabels(w, repo)
	for _, name := range append(baseLabels(), extra...) {
		if name == "" {
			continue
		}
		if err == nil && existing[name] {
			continue
		}
		ensureLabel(w, repo, name)
	}
}

// listLabels returns the repo's current label names as a set.
func listLabels(w *workspace.Workspace, repo string) (map[string]bool, error) {
	out, err := ghRepo(w, repo, "label", "list", "--limit", strconv.Itoa(ghLabelListLimit), "--json", "name")
	if err != nil {
		return nil, fmt.Errorf("gh label list: %w (%s)", err, out)
	}
	var labels []ghLabel
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		return nil, fmt.Errorf("parse label list: %w", err)
	}
	// A hit cap means the tail is unknown, so treat the whole set as unknown
	// and fall back to unconditional creation. Reading a partial page as the
	// complete set is the milestone-pagination bug (dacli 266) in a new place.
	if len(labels) >= ghLabelListLimit {
		return nil, fmt.Errorf("label list hit the %d cap — cannot tell which labels exist", ghLabelListLimit)
	}
	set := make(map[string]bool, len(labels))
	for _, l := range labels {
		set[l.Name] = true
	}
	return set, nil
}

// otherSeverityLabels are the severity labels an issue must NOT carry given its
// current severity — the stale ones to strip so a finding issue first filed when
// its note had no severity (published as severity:unspecified on the public
// repo) is corrected on the next push instead of accumulating two severity
// labels. Mirrors otherStatusLabels for the status family.
func otherSeverityLabels(sevLabel string) []string {
	var out []string
	for _, s := range []string{"severity:major", "severity:moderate", "severity:minor", "severity:unspecified"} {
		if s != sevLabel {
			out = append(out, s)
		}
	}
	return out
}

// areaSlice derives a best-effort code-slice name from the first `internal/<...>`
// path mentioned in text: the LAST directory segment names the package
// (internal/features/ghmirror → ghmirror, internal/store → store). Trailing
// filename segments (containing a dot, e.g. ghmirror.go) are dropped so the area
// is the package, not the file. Returns "" when no internal path is present, so
// the caller skips the area label cleanly.
func areaSlice(text string) string {
	m := internalPathRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	last := ""
	for _, seg := range strings.Split(m[1], "/") {
		if seg == "" || strings.Contains(seg, ".") {
			continue // skip empties and filename-ish segments
		}
		last = seg
	}
	return sanitizeSlice(last)
}

// internalPathRe captures the path following an `internal/` mention. The colon
// and whitespace that end a `file.go:44` reference are not in the class, so a
// trailing line number is naturally excluded.
var internalPathRe = regexp.MustCompile(`internal/([A-Za-z0-9_./-]+)`)

// sanitizeSlice lowercases a slice name and keeps only label-safe characters, so
// a derived area label is always a valid, stable GitHub label.
func sanitizeSlice(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// areaLabel renders the area: label for a code slice, or "" for an empty slice
// (the signal to skip the label). Shared so findings (slice from the finding
// body) and tasks (slice from the project) produce the identical label form.
func areaLabel(slice string) string {
	s := sanitizeSlice(slice)
	if s == "" {
		return ""
	}
	return "area:" + s
}

// --- milestones (dacli 224) ---

// milestoneTitle is the milestone a project maps to: its human title when set,
// else its slug. One milestone per project, so a mirrored repo's task issues
// group under a planning milestone the way a hand-run project's do.
func milestoneTitle(p *store.Project) string {
	if t := strings.TrimSpace(p.Title); t != "" {
		return t
	}
	return p.Slug
}

// milestoneTitles splits the newline-separated titles a `gh api … --jq
// '.[].title'` milestone list emits into a slice, dropping blank lines.
func milestoneTitles(jqOut string) []string {
	var out []string
	for _, line := range strings.Split(jqOut, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// milestonesHave reports whether title is already among the repo's milestones
// (exact match), so ensureMilestone creates one only when it is genuinely absent.
func milestonesHave(titles []string, title string) bool {
	for _, t := range titles {
		if t == title {
			return true
		}
	}
	return false
}

// ghMilestoneListLimit caps the milestones milestoneExists reads in a single
// call. The REST milestones endpoint paginates at 30 per page by default, so a
// bare list saw only the FIRST page: a repo past 30 milestones never found an
// existing one that had fallen onto a later page, and ensureMilestone re-created
// it on every push, accumulating a duplicate each time (dacli 266). per_page
// tops out at 100 on this endpoint, so 100 is both the page size we request and
// the cap — a list landing exactly on it may have a tail we did not see, the
// same "may be more than this" signal fetchAllIssues guards for issues (205).
const ghMilestoneListLimit = 100

// milestoneExists lists the repo's milestones (open AND closed) and reports
// whether title is among them. Milestones live on the REST endpoint, not the
// issue surface, so this calls `gh api` with the repo in the PATH — not ghRepo's
// `--repo`, which `gh api` does not accept — but still through the stubbable `gh`
// var so the failure-path tests intercept it.
//
// A list that lands exactly on ghMilestoneListLimit WITHOUT the title on it is
// not a trustworthy "absent": the title may sit on an unread page, so this
// errors rather than reporting a false absence — a partial page read as the
// whole repo is exactly what let ensureMilestone duplicate a milestone (266). A
// positive find, by contrast, is definitive at any length.
func milestoneExists(w *workspace.Workspace, repo, title string) (bool, error) {
	out, err := gh(w, "api", fmt.Sprintf("repos/%s/milestones?state=all&per_page=%d", repo, ghMilestoneListLimit), "--jq", ".[].title")
	if err != nil {
		return false, err
	}
	titles := milestoneTitles(out)
	if milestonesHave(titles, title) {
		return true, nil
	}
	if len(titles) >= ghMilestoneListLimit {
		return false, fmt.Errorf("gh milestone list hit the per_page %d cap and %q was not on it — milestones beyond that page cannot be checked, and creating against a partial list would duplicate an existing one; prune milestones before retrying", ghMilestoneListLimit, title)
	}
	return false, nil
}

// ensureMilestone makes the project's milestone exist and returns whether it is
// CONFIRMED present. That confirmation is load-bearing: `gh issue create
// --milestone <title>` hard-fails on an unknown milestone and would abort the
// whole push, so a caller passes --milestone ONLY when this returned true — a
// milestone that could not be confirmed (a gh/network failure, or a create that
// did not land) is skipped, exactly like the best-effort labels, rather than
// poisoning every issue-create in the loop.
//
// gh has no `milestone create` verb, so creation is a POST to the REST
// milestones endpoint; a re-run finds it already present and creates nothing,
// and a create that races another push (a 422 already-exists) still confirms on
// the re-list — so the re-check, not the POST's exit code, is the real gate.
//
// A list that could not be trusted (a gh/network failure, or a hit-cap page on
// which the title was absent) is REFUSED, not created against: creating a
// milestone we merely failed to see is the duplicate this task exists to stop,
// so an unconfirmable existence check returns false and skips --milestone rather
// than POSTing a fresh milestone that may already exist on an unread page (266).
func ensureMilestone(w *workspace.Workspace, repo, title string) bool {
	if title == "" || repo == "" {
		return false
	}
	exists, err := milestoneExists(w, repo, title)
	if err != nil {
		return false
	}
	if exists {
		return true
	}
	_, _ = gh(w, "api", "--method", "POST", "repos/"+repo+"/milestones", "-f", "title="+title)
	exists, err = milestoneExists(w, repo, title)
	if err != nil {
		return false
	}
	return exists
}

// --- decisions → GitHub (G2) ---

// decisionMarker is the recovery key embedded in every mirrored decision issue,
// keyed on the note id AND the workspace id — the same marker-idempotency
// machinery tasks use, but a distinct prefix so a decision issue is never
// adopted as a task mirror (and vice versa).
func decisionMarker(w *workspace.Workspace, noteID string) string {
	return fmt.Sprintf("<!-- dacli-decision:%s ws:%s -->", noteID, w.ID)
}

// noteFile is a note read from disk WITH its on-disk path — ListNotes yields
// docs without paths, but a mirror that writes the issue number back onto the
// note frontmatter needs the exact file. Shared by the decision mirror (G2) and
// the finding-issue mirror (G5).
type noteFile struct {
	path  string
	doc   *mdstore.Doc
	id    string
	title string
	// aliases are semantically equivalent local records folded into this one.
	// Their ids remain recovery keys so a partial push that published any member
	// of the group resumes by adoption instead of creating a second issue.
	aliases  []string
	evidence []string
	members  []*mdstore.Doc
}

// canonicalNoteFiles collapses repeated operational records before either the
// blast-radius plan or a remote write is considered. Title token sets make the
// key insensitive to case, punctuation, word order and light inflection. A
// deliberately stricter threshold than task filing avoids merging records that
// merely share an operational prefix (for example DNS vs authentication).
func canonicalNoteFiles(notes []noteFile) []noteFile {
	var out []noteFile
	for _, n := range notes {
		merged := false
		for i := range out {
			if store.TitleSimilarity(out[i].title, n.title) < 0.65 {
				continue
			}
			if (isDecisionNote(out[i]) || isDecisionNote(n)) && !decisionPayloadEquivalent(out[i].doc, n.doc) {
				continue
			}
			out[i].aliases = append(out[i].aliases, n.id)
			out[i].members = append(out[i].members, n.doc)
			if evidence := noteEvidence(n); evidence != "" && !containsString(out[i].evidence, evidence) {
				out[i].evidence = append(out[i].evidence, evidence)
			}
			merged = true
			break
		}
		if !merged {
			n.aliases = []string{n.id}
			n.evidence = []string{noteEvidence(n)}
			n.members = []*mdstore.Doc{n.doc}
			out = append(out, n)
		}
	}
	return out
}

func isDecisionNote(n noteFile) bool {
	kind, _ := n.doc.Front.Get("note_kind")
	return kind == "decision"
}

func decisionPayloadEquivalent(a, b *mdstore.Doc) bool {
	for _, title := range []string{"Chose", "Rejected", "Because"} {
		as, _ := a.Section(title)
		bs, _ := b.Section(title)
		left := strings.TrimSpace(as.Content)
		right := strings.TrimSpace(bs.Content)
		if strings.EqualFold(strings.Join(strings.Fields(left), " "), strings.Join(strings.Fields(right), " ")) {
			continue
		}
		if left == "" || right == "" || store.TitleSimilarity(left, right) != 1 {
			return false
		}
	}
	return true
}

func noteFileInWindow(n noteFile, refTasks []*store.Task, since time.Time) bool {
	if len(n.members) == 0 {
		return noteInWindow(n.doc, refTasks, since)
	}
	for _, member := range n.members {
		if noteInWindow(member, refTasks, since) {
			return true
		}
	}
	return false
}

func noteEvidence(n noteFile) string {
	return findingText(n.doc)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func noteFileText(n noteFile) string {
	if len(n.evidence) == 0 {
		return findingText(n.doc)
	}
	return strings.TrimSpace(strings.Join(n.evidence, "\n\n---\n\n"))
}

func findNoteMarker(w *workspace.Workspace, idx *markerIndex, n noteFile, mk func(*workspace.Workspace, string) string) int {
	for _, id := range n.aliases {
		if found := idx.find(mk(w, id)); found > 0 {
			return found
		}
	}
	return 0
}

// noteFiles reads a project's notes of one kind with their on-disk paths and
// level-1 titles — the reader both the decision mirror and the finding-issue
// mirror build on, so the two share one traversal and one write-back contract.
func noteFiles(w *workspace.Workspace, project string, kind model.NoteKind) ([]noteFile, error) {
	dir := w.NotesDir(project, kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no notes dir yet is not an error
		}
		return nil, err
	}
	var out []noteFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		d, err := mdstore.ReadFile(path)
		if err != nil {
			continue
		}
		id, _ := d.Front.Get("id")
		title := ""
		for _, s := range d.Sections {
			if s.Level == 1 {
				title = s.Title
				break
			}
		}
		out = append(out, noteFile{path: path, doc: d, id: id, title: title})
	}
	return out, nil
}

// decisionNotes reads the project's decision notes with their on-disk paths.
func decisionNotes(w *workspace.Workspace, project string) ([]noteFile, error) {
	return noteFiles(w, project, model.NoteDecision)
}

// findingNotes reads the project's finding notes with their on-disk paths, so
// the finding-issue mirror (G5) can write the issue number back onto each note.
func findingNotes(w *workspace.Workspace, project string) ([]noteFile, error) {
	return noteFiles(w, project, model.NoteFinding)
}

// decisionBody renders the WHY that is the whole point of mirroring a decision:
// the choice, the rejected alternative, and the because. The marker leads (for
// crash-recovery adoption) and the note id trails (the backlink to the dacli
// decision).
func decisionBody(w *workspace.Workspace, dn noteFile) string {
	var b strings.Builder
	b.WriteString(decisionMarker(w, dn.id) + "\n\n")
	b.WriteString("**Decision:** " + dn.title + "\n\n")
	if s, ok := dn.doc.Section("Chose"); ok && strings.TrimSpace(s.Content) != "" {
		b.WriteString("**Chose:** " + strings.TrimSpace(s.Content) + "\n\n")
	}
	if s, ok := dn.doc.Section("Rejected"); ok && strings.TrimSpace(s.Content) != "" {
		b.WriteString("**Rejected:** " + strings.TrimSpace(s.Content) + "\n\n")
	}
	if s, ok := dn.doc.Section("Because"); ok && strings.TrimSpace(s.Content) != "" {
		b.WriteString("**Because:** " + strings.TrimSpace(s.Content) + "\n\n")
	}
	if len(dn.evidence) > 1 {
		b.WriteString("**Additional evidence:**\n\n" + strings.Join(dn.evidence[1:], "\n\n---\n\n") + "\n\n")
	}
	b.WriteString("_Mirrored from dacli decision " + dn.id + "; the workspace is the source of truth._\n")
	return b.String()
}

// mirrorDecisions projects each decision note to an issue labeled `decision`,
// reusing the marker/searchByMarker/write-back idempotency the task mirror uses:
// frontmatter mapping first, then SEARCH BY MARKER, and only then create — so a
// crash between the remote create and the local write converges by adoption,
// never a duplicate.
func mirrorDecisions(w *workspace.Workspace, repo string, notes []noteFile, refTasks []*store.Task, since time.Time, idx *markerIndex, dry bool, out io.Writer) error {
	if len(notes) == 0 {
		return nil
	}
	// The `decision`/`type:decision` labels are pre-created by precreateLabels at
	// the start of the push, so no create here races a missing label.

	created, adopted, kept, skipped := 0, 0, 0, 0
	for _, dn := range notes {
		// task 298: a windowed push mirrors ONLY the decisions inside the window
		// (about a named task, or created since the cutoff); every other decision
		// is left unpublished rather than riding along on a scoped push.
		if !noteFileInWindow(dn, refTasks, since) {
			skipped++
			continue
		}
		if dn.id == "" {
			// A note with no id cannot be keyed idempotently; skip rather than
			// risk creating a duplicate on every push.
			continue
		}
		num := mappedIssueDoc(dn.doc)
		if num == 0 {
			if found := findNoteMarker(w, idx, dn, decisionMarker); found > 0 {
				num = found
				adopted++
				if dry {
					fmt.Fprintf(out, "would adopt issue #%d by marker for decision %s\n", num, dn.id)
				}
			}
		}
		// A dry-run cannot obtain a real issue number for a would-be-created
		// decision, so it prints the create and leaves the mapping/labels alone.
		if num == 0 {
			if dry {
				fmt.Fprintf(out, "would create issue %q\nexact body:\n%s\n", "decision: "+dn.title, decisionBody(w, dn))
				created++
				continue
			}
			ghout, err := ghRepo(w, repo, "issue", "create",
				"--title", "decision: "+dn.title,
				"--body", decisionBody(w, dn),
				"--label", "decision",
				"--label", "type:decision")
			if err != nil {
				return fmt.Errorf("issue create for decision %s: %w (%s)", dn.id, err, ghout)
			}
			num = trailingInt(ghout)
			if num == 0 {
				return fmt.Errorf("could not parse issue number from gh output %q", ghout)
			}
			// FILE IT CLOSED. A decision is a RECORD of a choice already made,
			// not work anyone can action, and leaving it open put it in the
			// queue reviewers read as "things to do". They accumulate with
			// nothing ever closing them: this repo reached 15 open decision
			// issues crowding out 4 real ones, and clearing them was a manual
			// sweep (dacli 336).
			//
			// Closed on CREATE rather than on every push, deliberately. Closing
			// an existing issue on each push would fight a human who reopened
			// one to discuss it — the mirror publishes records, it does not get
			// to overrule someone reading them.
			//
			// Best-effort: the record is already published, and a close that
			// fails leaves an open issue rather than a lost decision.
			if _, cerr := ghRepo(w, repo, "issue", "close", strconv.Itoa(num), "--reason", "completed"); cerr != nil {
				fmt.Fprintf(out, "note: filed decision issue #%d but could not close it (%v) — close it by hand; it is a record, not open work\n", num, cerr)
			}
			created++
		} else if mappedIssueDoc(dn.doc) != 0 {
			kept++
		}
		if dry {
			// Nothing else to preview for an existing decision issue: its taxonomy
			// re-label is a best-effort cosmetic write outside create/adopt.
			continue
		}
		// G6: keep the decision taxonomy current on adopted/existing issues too
		// (best-effort, idempotent) so a re-push enriches issues filed before G6.
		_, _ = ghRepo(w, repo, "issue", "edit", strconv.Itoa(num), "--add-label", "decision", "--add-label", "type:decision")

		// Write the mapping back after the remote exists, so the failure window
		// leaves an adoptable issue, not a dangling mapping — mirrors the task
		// path, and likewise skipped when unchanged so a re-push rewrites no file.
		if desired := githubBlock(num, repo); mappedBlockChanged(dn.doc, desired) {
			dn.doc.Front.SetBlock("github", desired)
			if err := mdstore.WriteFile(dn.path, dn.doc); err != nil {
				return err
			}
		}
	}
	if dry {
		fmt.Fprintf(out, "dry-run: decisions would create %d, adopt %d, leave %d unchanged, %d out-of-window (of %d)\n",
			created, adopted, kept, skipped, len(notes))
	} else {
		fmt.Fprintf(out, "decisions: %d created, %d adopted-by-marker, %d unchanged, %d out-of-window (of %d)\n",
			created, adopted, kept, skipped, len(notes))
	}
	return nil
}

// --- findings → standalone issues (G5) ---

// findingIssueMarker is the recovery key embedded in every mirrored finding
// ISSUE body, keyed on the note id AND the workspace id. It has a distinct
// prefix from the task (`dacli:`), decision (`dacli-decision:`) and finding
// COMMENT (`dacli-finding:`) markers, so searchByMarker/adoption never crosses
// between the standalone-issue mirror and any other mirror. (A finding issue
// carries this in its body; a finding comment carries findingMarker in a
// comment — never the same location, so the two modes are independently
// idempotent.)
func findingIssueMarker(w *workspace.Workspace, noteID string) string {
	return fmt.Sprintf("<!-- dacli-finding-issue:%s ws:%s -->", noteID, w.ID)
}

// severityLabel maps a finding's severity to its GitHub label. An empty or
// unrecognized severity maps to `severity:unspecified` — an honest, still-valid
// label rather than a silently missing one — so the mapping is total and
// unit-testable without a live gh.
func severityLabel(severity string) string {
	s := strings.ToLower(strings.TrimSpace(severity))
	switch s {
	case "major", "moderate", "minor":
		return "severity:" + s
	default:
		return "severity:unspecified"
	}
}

// findingIssueBody renders the body of a standalone finding issue: the marker
// leads (for crash-recovery adoption), the severity is surfaced, then the
// finding detail and a backlink to the local dacli note (the note id is the
// backlink — the workspace is the source of truth).
func findingIssueBody(w *workspace.Workspace, dn noteFile, severity string) string {
	var b strings.Builder
	b.WriteString(findingIssueMarker(w, dn.id) + "\n\n")
	if s := strings.TrimSpace(severity); s != "" {
		b.WriteString("**Severity:** " + s + "\n\n")
	}
	b.WriteString(noteFileText(dn) + "\n\n")
	b.WriteString("_Filed as dacli finding " + dn.id + "; the workspace is the source of truth._\n")
	return b.String()
}

// mirrorFindingIssues projects each finding note to ONE standalone GitHub issue
// labeled `finding` + `severity:<...>`, reusing the exact marker/searchByMarker/
// write-back idempotency the task and decision mirrors use: frontmatter mapping
// first, then SEARCH BY MARKER, and only then create — so a crash between the
// remote create and the local write converges by adoption on the next push,
// never a duplicate. The issue number is written back onto the finding note.
func mirrorFindingIssues(w *workspace.Workspace, repo string, notes []noteFile, refTasks []*store.Task, since time.Time, idx *markerIndex, dry bool, out io.Writer) error {
	if len(notes) == 0 {
		return nil
	}
	// `finding`, `type:finding` and every `severity:*` label are pre-created by
	// precreateLabels at the start of the push; area: labels are dynamic and
	// ensured just-in-time below, so no create here races a missing label.

	created, adopted, kept, skipped := 0, 0, 0, 0
	for _, dn := range notes {
		// task 298: skip findings outside the window — created before the --since
		// cutoff AND not about a task the explicit refs named. A --since-only push
		// still targets just a recent audit; an explicit-ref push now scopes the
		// standalone finding issues to the named tasks the same way the task mirror
		// is scoped, instead of filing every finding in the project.
		if !noteFileInWindow(dn, refTasks, since) {
			skipped++
			continue
		}
		if dn.id == "" || noteFileText(dn) == "" {
			// A note with no id cannot be keyed idempotently, and an empty
			// finding has no detail to file; skip rather than risk a duplicate.
			continue
		}
		severity, _ := dn.doc.Front.Get("severity")
		sevLabel := severityLabel(severity)
		// G6: a best-effort area: label from the first internal/<...> path named
		// in the finding detail (skipped cleanly when none is present). Ensured
		// just-in-time (it is dynamic, so not in the pre-created static set). A
		// dry-run skips the label create — it is a remote write outside the
		// create/adopt the preview reports.
		area := areaLabel(areaSlice(noteFileText(dn)))
		if area != "" && !dry {
			ensureLabel(w, repo, area)
		}

		num := mappedIssueDoc(dn.doc)
		if num == 0 {
			if found := findNoteMarker(w, idx, dn, findingIssueMarker); found > 0 {
				num = found
				adopted++
				if dry {
					fmt.Fprintf(out, "would adopt issue #%d by marker for finding %s\n", num, dn.id)
				}
			}
		}
		// A dry-run cannot obtain a real issue number for a would-be-created
		// finding issue, so it prints the create and leaves the mapping/labels alone.
		if num == 0 {
			if dry {
				fmt.Fprintf(out, "would create issue %q\nexact body:\n%s\n", dn.title, findingIssueBody(w, dn, severity))
				created++
				continue
			}
			createArgs := []string{"issue", "create",
				"--title", dn.title,
				"--body", findingIssueBody(w, dn, severity),
				"--label", "finding",
				"--label", "type:finding",
				"--label", sevLabel}
			if area != "" {
				createArgs = append(createArgs, "--label", area)
			}
			ghout, err := ghRepo(w, repo, createArgs...)
			if err != nil {
				return fmt.Errorf("issue create for finding %s: %w (%s)", dn.id, err, ghout)
			}
			num = trailingInt(ghout)
			if num == 0 {
				return fmt.Errorf("could not parse issue number from gh output %q", ghout)
			}
			created++
		} else {
			if mappedIssueDoc(dn.doc) != 0 {
				kept++
			}
			if dry {
				// Nothing else to preview for an existing finding issue: the label
				// correction is a best-effort cosmetic write outside create/adopt.
				continue
			}
			// An adopted or already-mapped issue keeps its labels current: add the
			// correct taxonomy AND strip stale severity labels, so a finding issue
			// first filed as severity:unspecified (the public-repo bug) is corrected
			// rather than left carrying two severity labels. Best-effort/idempotent.
			applyFindingLabels(w, repo, num, sevLabel, area)
		}

		// Write the mapping back after the remote exists, so the failure window
		// leaves an adoptable issue, not a dangling mapping — mirrors the task
		// path, and likewise skipped when unchanged so a re-push rewrites no file.
		if desired := githubBlock(num, repo); mappedBlockChanged(dn.doc, desired) {
			dn.doc.Front.SetBlock("github", desired)
			if err := mdstore.WriteFile(dn.path, dn.doc); err != nil {
				return err
			}
		}
	}
	if dry {
		fmt.Fprintf(out, "dry-run: findings-as-issues would create %d, adopt %d, leave %d unchanged, skip %d by --since (of %d); nothing was written\n",
			created, adopted, kept, skipped, len(notes))
	} else {
		fmt.Fprintf(out, "findings-as-issues: %d created, %d adopted-by-marker, %d unchanged, %d skipped-by-since (of %d)\n",
			created, adopted, kept, skipped, len(notes))
	}
	return nil
}

// applyFindingLabels keeps a finding issue's G6 taxonomy current: it adds
// finding, type:finding, the correct severity label and (if any) the area label,
// then strips the OTHER severity labels so exactly one severity: label survives.
// This is what corrects an issue first filed as severity:unspecified — the
// public-repo bug — instead of leaving it carrying two severity labels. All gh
// calls are best-effort (a --remove-label for an absent label errors, ignored).
func applyFindingLabels(w *workspace.Workspace, repo string, num int, sevLabel, area string) {
	if num == 0 {
		return
	}
	args := []string{"issue", "edit", strconv.Itoa(num), "--add-label", "finding", "--add-label", "type:finding", "--add-label", sevLabel}
	if area != "" {
		args = append(args, "--add-label", area)
	}
	_, _ = ghRepo(w, repo, args...)
	for _, stale := range otherSeverityLabels(sevLabel) {
		_, _ = ghRepo(w, repo, "issue", "edit", strconv.Itoa(num), "--remove-label", stale)
	}
}

// marker is the recovery key embedded in every mirrored issue body: a lost
// or corrupted mapping is recoverable by SEARCH rather than by duplication.
func marker(w *workspace.Workspace, t *store.Task) string {
	return fmt.Sprintf("<!-- dacli:%s ws:%s -->", t.ID, w.ID)
}

func mappedIssue(t *store.Task) int { return mappedIssueDoc(t.Doc) }

// mappedIssueDoc reads the mirrored issue number from any doc's `github:` block
// (tasks and decision notes store the mapping the same way), so a doc already
// bound to an issue skips creation on the next push — the local half of the
// idempotency guarantee.
func mappedIssueDoc(d *mdstore.Doc) int {
	block, ok := d.Front.GetBlock("github")
	if !ok {
		return 0
	}
	for _, line := range strings.Split(block, "\n") {
		if k, v, found := strings.Cut(strings.TrimSpace(line), ":"); found && strings.TrimSpace(k) == "issue" {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// githubBlock renders the `github:` frontmatter block that binds a task or note
// to its mirrored issue. One definition so every write-back — and the
// unchanged-check that skips a needless file rewrite — produces the identical
// bytes, and a re-push compares like against like.
func githubBlock(num int, repo string) string {
	return fmt.Sprintf("  issue: %d\n  repo: %s", num, repo)
}

// mappedBlockChanged reports whether the doc's current `github:` block differs
// from the desired one — the guard that lets an idempotent re-push skip a file
// write (and its mtime/git-blame churn) when the issue mapping is already
// current.
func mappedBlockChanged(d *mdstore.Doc, desired string) bool {
	cur, _ := d.Front.GetBlock("github")
	return cur != desired
}

// markerIndex is the strongly-consistent issue list fetched ONCE per push and
// scanned in memory, so marker adoption is not a full `gh issue list` per
// task/decision/finding — the previous behaviour, which cost one list call for
// every unmapped note in the push loop. Built lazily on first lookup and reused
// for the rest of the push.
//
// Adoption is the crash-recovery path: a create that succeeded before its local
// mapping write must be ADOPTED on re-run, never duplicated. It matches the
// marker by exact SUBSTRING over issue bodies from the plain list endpoint —
// deliberately NOT `gh issue list --search`.
//
// `--search` hits GitHub's code/issue search index, which is (a) EVENTUALLY
// CONSISTENT — a just-created issue is not indexed for seconds-to-minutes, so a
// fast retry after a create-then-crash finds nothing and duplicates — and (b)
// TOKENIZED, stripping the angle brackets and colons in the marker so a match
// is not even guaranteed once indexed. The list endpoint reflects a
// just-created issue immediately and we compare bytes, so recovery converges on
// the first retry regardless of index lag. This is what makes the docstring's
// zero-duplicate guarantee hold.
//
// A single snapshot per push is safe: adoption only ever targets issues from a
// PRIOR run — every note created this run writes its mapping back locally before
// the next note is searched — so a mid-push create never needs to be found by a
// later lookup in the same run.
// syncIssueTaxonomy brings ONE issue's labels and milestone to their desired
// state in at most one `gh issue edit`, and in zero calls when they already
// match.
//
// It replaced three unconditional writers (status label, type:/area: labels,
// milestone) that between them issued five gh invocations per mapped issue on
// every push — an add, three separate removes, and the taxonomy edit — with no
// comparison against the issue's current state. On a repo with ~300 mirrored
// tasks that made an idempotent, change-nothing re-push cost roughly 2,100
// network round-trips.
//
// The comparison uses the snapshot the marker index already loaded, so the
// diff is free. When the index could not load (a transient gh failure), the
// snapshot is empty and every issue looks unlabelled: the edit is then issued
// unconditionally, which is exactly the old behavior and still correct, just
// not cheap. Best-effort throughout — a taxonomy write must never fail a push.
func syncIssueTaxonomy(w *workspace.Workspace, repo string, idx *markerIndex, num int, st model.Status, area, milestone string, haveMilestone bool) {
	if num == 0 {
		return
	}
	args := issueTaxonomyArgs(idx, num, st, area, milestone, haveMilestone)
	if len(args) == 0 {
		return // already current: the common case on a re-push
	}
	// The label must exist before it can be attached; only ensure the ones
	// this edit actually adds.
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == "--add-label" {
			ensureLabel(w, repo, args[i+1])
		}
	}
	_, _ = ghRepo(w, repo, append([]string{"issue", "edit", strconv.Itoa(num)}, args...)...)
	idx.forget(num)
}

// syncIssueTaxonomyStrict is used by explicit owner plans, where reporting a
// successful apply after a failed edit would invalidate the plan's receipt.
// Required repository labels were read-verified during whole-plan preflight;
// this path must not create or update shared repository label definitions.
func syncIssueTaxonomyStrict(w *workspace.Workspace, repo string, idx *markerIndex, num int, st model.Status, area, milestone string, haveMilestone bool) error {
	args := issueTaxonomyArgs(idx, num, st, area, milestone, haveMilestone)
	if len(args) == 0 {
		return nil
	}
	if out, err := ghRepo(w, repo, append([]string{"issue", "edit", strconv.Itoa(num)}, args...)...); err != nil {
		return fmt.Errorf("edit planned issue #%d taxonomy: %w (%s)", num, err, out)
	}
	idx.forget(num)
	return nil
}

// issueTaxonomyArgs is shared by apply and dry-run so the preview describes
// the exact label/milestone edit the live path would issue.
func issueTaxonomyArgs(idx *markerIndex, num int, st model.Status, area, milestone string, haveMilestone bool) []string {
	if num == 0 {
		return nil
	}
	want := statusLabel(st)
	have := idx.labelsFor(num)

	var args []string
	add := func(label string) {
		if label != "" && !have[label] {
			args = append(args, "--add-label", label)
		}
	}
	add(want)
	add("type:task")
	add(area)
	for _, stale := range otherStatusLabels(st) {
		if have[stale] {
			args = append(args, "--remove-label", stale)
		}
	}
	if haveMilestone && milestone != "" && idx.milestoneFor(num) != milestone {
		args = append(args, "--milestone", milestone)
	}
	return args
}

type markerIndex struct {
	w         *workspace.Workspace
	repo      string // the linked repo the snapshot is scoped to (dacli 221)
	loaded    bool
	truncated bool
	loadErr   error
	issues    []ghIssue
}

func newMarkerIndex(w *workspace.Workspace, repo string) *markerIndex {
	return &markerIndex{w: w, repo: repo}
}

// load fetches the issue list once. Memoize only a SUCCESSFUL fetch. Setting
// loaded before the call cached a failure as "the repo has no issues", so one
// transient gh error made every later find() miss and the push re-created every
// issue as a duplicate — on the exact path (adoption, when the local mapping is
// gone) that the marker exists to protect. A parse failure is a failure too:
// unparseable output is not an empty repository (dacli 208).
func (m *markerIndex) load() {
	if m.loaded {
		return
	}
	// title is fetched alongside body so title-based adoption (task 275) can
	// match a hand-filed issue that carries no marker in its body.
	if issues, truncated, err := fetchAllIssues(m.w, m.repo, "number,title,body,labels,milestone"); err == nil {
		m.issues = issues
		m.truncated = truncated
		m.loaded = true
		m.loadErr = nil
	} else {
		m.loadErr = err
	}
}

// labelsFor returns the labels the snapshot saw on issue num. An unknown issue
// (freshly created this push, or an index that failed to load) returns an
// empty set, so the caller writes unconditionally rather than skipping a
// needed edit — wrong-but-cheap is not a trade this makes.
func (m *markerIndex) labelsFor(num int) map[string]bool {
	m.load()
	for _, h := range m.issues {
		if h.Number == num {
			return h.labelSet()
		}
	}
	return map[string]bool{}
}

// milestoneFor returns the milestone title the snapshot saw, or "".
func (m *markerIndex) milestoneFor(num int) string {
	m.load()
	for _, h := range m.issues {
		if h.Number == num {
			return h.Milestone.Title
		}
	}
	return ""
}

func (m *markerIndex) issueMilestone(num int) (string, bool) {
	m.load()
	for _, h := range m.issues {
		if h.Number == num {
			return h.Milestone.Title, true
		}
	}
	return "", false
}

func (m *markerIndex) issueLabels(num int) ([]string, bool) {
	m.load()
	for _, h := range m.issues {
		if h.Number == num {
			labels := make([]string, 0, len(h.Labels))
			for _, label := range h.Labels {
				labels = append(labels, label.Name)
			}
			return labels, true
		}
	}
	return nil, false
}

// forget drops an issue from the snapshot after it has been edited, so a later
// read in the same push does not diff against stale labels and skip a real
// change. Dropping (rather than patching) keeps this honest: an unknown issue
// is treated as needing the write.
func (m *markerIndex) forget(num int) {
	for i, h := range m.issues {
		if h.Number == num {
			m.issues = append(m.issues[:i], m.issues[i+1:]...)
			return
		}
	}
}

// find returns the lowest issue number whose body contains the marker, or 0.
// preflight must have completed successfully before a push relies on this
// answer; a failed or partial fetch is never interpreted as no match.
func (m *markerIndex) find(mk string) int {
	matches := m.findAll(mk)
	if len(matches) > 0 {
		return matches[0]
	}
	return 0
}

// findAll exposes every marker collision and sorts it by issue number. The old
// first-match result hid duplicates and made the chosen mapping depend on API
// response order (issue #682).
func (m *markerIndex) findAll(mk string) []int {
	m.load()
	var matches []int
	for _, h := range m.issues {
		if mk != "" && strings.Contains(h.Body, mk) {
			matches = append(matches, h.Number)
		}
	}
	sort.Ints(matches)
	return matches
}

func issueNumbers(nums []int) string {
	parts := make([]string, len(nums))
	for i, num := range nums {
		parts[i] = fmt.Sprintf("#%d", num)
	}
	return strings.Join(parts, ", ")
}

// findByTitle returns the number of the lowest-numbered issue whose title
// EXACTLY equals title, or 0. It is push's SECOND adoption path (task 275): an
// issue an operator filed by hand — titled `NNN: <task title>` but carrying no
// dacli marker — is adopted into the mapping rather than duplicated.
//
// The match is the FULL canonical title, never a prefix, so a coincidental
// `275: ...` cannot cross-adopt. It runs only AFTER the marker search misses, so
// a dacli-mirrored issue (which carries its marker) is always adopted by marker
// first and never reached here. The lowest number wins on the off chance the
// repo already holds two identically-titled issues, so the choice is a
// deterministic tie-break — never a guess — and a re-push converges on the same
// one instead of oscillating.
func (m *markerIndex) findByTitle(title string) int {
	if title == "" {
		return 0
	}
	m.load()
	best := 0
	for _, h := range m.issues {
		if h.Title == title && (best == 0 || h.Number < best) {
			best = h.Number
		}
	}
	return best
}

// preflight loads the index and refuses a push whose issue-list fetch hit
// ghIssueListLimit, BEFORE the first create.
//
// This used to warn at the end of the push instead. The warning was accurate
// and useless: by the time it printed, every issue past the fetched page had
// already been re-created as a duplicate, because none of them was in the
// index to be adopted. Push is the only one of the three cap-readers that
// writes to a live repository, and it was the only one that did not refuse —
// listIssues (pull) and itemSnapshot (project) both stop.
//
// The truncation is knowable here: load runs on the first find(), find() is
// the first idempotency check, and that check precedes the first create. So
// refusing costs no extra fetch — only the decision to make it before the
// writes rather than after them (dacli 205).
//
// A fetch failure is a refusal too. Retrying inside find used to be fail-soft,
// but creation after an untrusted read is exactly how an interrupted push can
// duplicate an issue; callers may retry the whole command once GitHub is
// readable, before any remote mutation has happened (issue #682).
func (m *markerIndex) preflight() error {
	m.load()
	if m.loadErr != nil {
		return fmt.Errorf("refusing github push because the marker index could not be read completely: %w", m.loadErr)
	}
	if m.truncated {
		return fmt.Errorf("gh issue list hit the --limit %d cap while indexing markers — issues beyond that page cannot be checked for an existing marker, and pushing against a partial index would re-create each of them as a duplicate; prune closed issues or raise the limit before retrying", ghIssueListLimit)
	}
	return nil
}

func issueBody(w *workspace.Workspace, t *store.Task) string {
	var b strings.Builder
	b.WriteString(marker(w, t) + "\n\n")
	if s, ok := t.Doc.Section("So that"); ok && strings.TrimSpace(s.Content) != "" {
		b.WriteString("So that " + strings.TrimSpace(s.Content) + "\n\n")
	}
	if s, ok := t.Doc.Section("Acceptance"); ok {
		b.WriteString("### Acceptance\n" + s.Content + "\n")
	}
	if completion := t.CompletionState(); completion != "" {
		b.WriteString("\n### Lifecycle\n")
		fmt.Fprintf(&b, "State: `%s` (implementation is not canonical completion until landing and acceptance).\n", completion)
	}
	if t.IsAggregate() {
		progress, err := store.AggregateProgressFor(w, t)
		b.WriteString("\n### Aggregate progress\n")
		if err != nil {
			b.WriteString("State unavailable: " + err.Error() + "\n")
		} else {
			fmt.Fprintf(&b, "%d/%d required children complete; ready to close: %t.\n", progress.RequiredDone, progress.Required, progress.ReadyToClose)
			for _, child := range progress.Children {
				mark := " "
				if child.Blocker == "" {
					mark = "x"
				}
				fmt.Fprintf(&b, "- [%s] `%s` — %s", mark, child.ID, child.Status)
				if child.Blocker != "" {
					fmt.Fprintf(&b, ": %s", child.Blocker)
				}
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("\n_Mirrored by dacli; the workspace is the source of truth._\n")
	return b.String()
}

func projectedIssueBody(w *workspace.Workspace, t *store.Task, policy publication.Policy) string {
	return policy.Sanitize(issueBody(w, t))
}

func trailingInt(s string) int {
	parts := strings.Split(strings.TrimSpace(s), "/")
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

// originRepo derives `owner/name` from the `origin` remote's URL, with no
// network call. It handles the two forms git writes: SSH (git@host:owner/name)
// and HTTPS (https://host/owner/name), with or without a .git suffix.
//
// It exists so an unlinked project's refusal can name the exact command that
// fixes it. Deliberately NOT an auto-link: binding a project to a repository
// is a consent decision — the disclosure gate exists because pushing to a
// PUBLIC repo publishes the backlog — and inferring it from a remote would
// make that decision silently, on the operator's behalf, at the first push.
// Naming the repo turns a dead end into a copy-paste without taking the
// decision away (task 306).
