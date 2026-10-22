// Package catalog projects the .dacli role/skill roster into ONE browsable
// markdown catalog for humans (docs/ROSTER.md, or the repo wiki). The source of
// truth stays in .dacli/ — the catalog is a generated, one-way read view: you
// change a role or skill by editing its file (via PR), never by editing the
// catalog. This mirrors the ghmirror doctrine: local markdown is canonical,
// the projection is deletable and regenerable.
//
// The wiki publish is disclosure-gated exactly like `github push`: a PUBLIC
// repo makes the projection world-readable, so it needs recorded per-project
// consent. It is best-effort — a wiki failure never fails the docs write, and
// nothing here requires a live network call to be unit-tested.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/skills"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "catalog", Brief: "Generate a browsable role/skill catalog to docs/ROSTER.md (--publish-wiki mirrors it to the repo wiki, disclosure-gated)", Mutates: true, Usage: "dacli catalog [--project slug] [--out path] [--publish-wiki]", Run: cmdCatalog},
}

// defaultOut is the versioned, reliable catalog: committed with the repo so the
// read view travels with the source of truth it is generated from.
const defaultOut = "docs/ROSTER.md"

// resolveOut turns the --out flag into an absolute destination under cwd. An
// empty flag falls back to defaultOut, and a relative path resolves against the
// caller's cwd — NOT the workspace root — so an agent running in an isolated
// worktree writes the catalog into its own tree.
//
// The destination must stay under cwd. An absolute --out used to be honored
// verbatim, which made this an arbitrary-file write for anyone who could run
// the command: `--out ~/.claude/CLAUDE.md` overwrote whatever the operator's
// uid could reach, and `--out ../../x` escaped just as well (2026-08-06 audit).
// Escapes are refused rather than clamped, so a caller who meant a path outside
// the tree is told, not silently redirected.
func resolveOut(cwd, out string) (string, error) {
	if out == "" {
		out = defaultOut
	}
	abs := out
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, out)
	}
	abs = filepath.Clean(abs)
	// Containment is decided with filepath.Rel on the paths as given, not on
	// symlink-resolved ones: resolving only one side misjudges a symlinked cwd
	// (macOS /var -> /private/var) as an escape, and resolving both cannot work
	// for a destination that does not exist yet. A relative path that needs ".."
	// to reach the destination is outside the tree — including the sibling
	// directory that merely shares the root's textual prefix.
	rel, err := filepath.Rel(cwd, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", clikit.Refusedf("--out %s resolves outside this directory (%s) — the catalog is written into the tree you run it from; pass a path under it", out, cwd)
	}
	return abs, nil
}

// wikiPage is the wiki file the roster publishes to. GitHub serves
// `Roster.md` at the wiki path `/Roster`.
const wikiPage = "Roster.md"

// roleRow / skillRow are the flat, render-ready projections of a role/skill.
// Splitting the pure render (renderCatalog) from the workspace reads keeps the
// markdown deterministically testable without a git repo or a live gh.
type roleRow struct {
	Name, Version, Grant, Kind, Runtime, Model, Profile, Purpose, LastChanged string
	Skills                                                                    []string
}

type skillRow struct {
	Name, Version, Purpose, LastChanged string
	EstTokens                           int
}

func cmdCatalog(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("out", "publish-wiki", "project"); err != nil {
		return err
	}

	// Refuse rather than publish an empty roster over the real one: the catalog
	// is a generated view, and a view that silently becomes blank is worse than
	// no view at all (dacli 208).
	roles, err := collectRoles(w)
	if err != nil {
		return err
	}
	skls := collectSkills(w)
	md := renderCatalog(roles, skls)

	// A relative --out resolves against the CALLER's working directory, not the
	// workspace root: a worktree agent's catalog must land in its own tree, not
	// the shared main checkout that workspace.Find redirects to.
	out, err := resolveOut(ctx.Cwd, f.Get("out"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
		return err
	}
	rel := out
	if r, err := filepath.Rel(ctx.Cwd, out); err == nil && !strings.HasPrefix(r, "..") {
		rel = r
	}
	fmt.Fprintf(ctx.Stdout, "wrote %s — %d roles, %d skills (generated from .dacli/; edit roles/skills via PR, never here)\n", rel, len(roles), len(skls))

	// --publish-wiki is a SEPARATE, best-effort outbound projection. The docs
	// write above has already succeeded and is never rolled back: a wiki
	// failure degrades to a warning, per the acceptance ("a wiki failure must
	// not fail the docs write").
	if f.Bool("publish-wiki") {
		if err := publishWiki(ctx, w, f, md); err != nil {
			// A refusal (disclosure gate) is an answer, not a crash: surface it
			// and still exit 0, because the reliable catalog is already written.
			fmt.Fprintf(ctx.Stderr, "wiki publish skipped: %v\n", err)
		}
	}
	return nil
}

// collectRoles reads every role and annotates it with its version and the
// most-recent commit that touched its file (the "last changed" column).
func collectRoles(w *workspace.Workspace) ([]roleRow, error) {
	// LoadRoles deliberately distinguishes "no roles" from "could not read"
	// (see store/roles.go). Discarding that turned a permission error into an
	// EMPTY roster, which cmdCatalog then wrote over the committed
	// docs/ROSTER.md and reported as success — deleting the read view of the
	// team because a directory was briefly unreadable (dacli 208).
	roles, err := store.LoadRoles(w)
	if err != nil {
		return nil, fmt.Errorf("reading roles: %w", err)
	}
	rows := make([]roleRow, 0, len(roles))
	for _, r := range roles {
		path := w.RolePath(r.Name)
		rows = append(rows, roleRow{
			Name:        r.Name,
			Version:     store.FileVersion(path),
			Grant:       r.Grant,
			Kind:        r.Kind,
			Runtime:     r.Runtime,
			Model:       r.Model,
			Profile:     profileLabel(r),
			Purpose:     r.Summary,
			LastChanged: lastChanged(path),
			Skills:      r.Skills,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func profileLabel(r team.Role) string {
	var parts []string
	if r.Profile.CostTier > 0 {
		parts = append(parts, fmt.Sprintf("tier %d", r.Profile.CostTier))
	}
	if cap := r.TaskCapacity(); cap > 0 {
		parts = append(parts, fmt.Sprintf("≤%g pt", cap))
	}
	if r.Profile.ContextLimit > 0 {
		parts = append(parts, fmt.Sprintf("%dk ctx", r.Profile.ContextLimit/1000))
	}
	return strings.Join(parts, " / ")
}

func collectSkills(w *workspace.Workspace) []skillRow {
	list, _ := skills.LoadSkills(w)
	rows := make([]skillRow, 0, len(list))
	for _, s := range list {
		manifest := skillManifest(s.Dir)
		rows = append(rows, skillRow{
			Name:        s.Name,
			Version:     store.FileVersion(manifest),
			Purpose:     clikit.FirstLine(s.Desc),
			EstTokens:   s.EstTokens,
			LastChanged: lastChanged(manifest),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// skillManifest returns the skill's manifest path (skill.md or the native
// SKILL.md) so version and changelog read the same file the loader parsed. It
// falls back to skill.md so an absent directory still yields a stable path
// (FileVersion/lastChanged degrade to their defaults on a missing file).
func skillManifest(dir string) string {
	for _, name := range []string{"skill.md", "SKILL.md"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, "skill.md")
}

// lastChanged renders the newest commit that touched path as "when · subject",
// or a dash when there is no committed history (untracked, no git). It reuses
// the H1 changelog helper so the catalog and `role show` agree on history.
func lastChanged(path string) string {
	changes, _ := store.FileChangelog(path, 1)
	if len(changes) == 0 {
		return "—"
	}
	c := changes[0]
	return c.When + " · " + c.Subject
}

// renderCatalog is the pure, deterministic markdown projection — no workspace,
// no git, no clock — so it is exhaustively unit-testable. Every cell is escaped
// so a pipe or newline in a purpose can never break the table.
func renderCatalog(roles []roleRow, skls []skillRow) string {
	var b strings.Builder
	b.WriteString("# Team Roster\n\n")
	b.WriteString("_Generated from `.dacli/` by `dacli catalog` — do **not** edit this page. " +
		"It is a one-way read view: to change a role or skill, edit its file under `.dacli/` (via PR), " +
		"then regenerate. Versions and last-changed come from git history._\n\n")
	b.WriteString("Roles describe intended responsibilities; they do not authenticate native sessions or prove reviewer independence. " +
		"Runtime/model columns retain historical or planning metadata. Native authority comes from separately enrolled scoped credentials; " +
		"the coding host owns execution and sandbox settings.\n\n")

	fmt.Fprintf(&b, "## Roles (%d)\n\n", len(roles))
	if len(roles) == 0 {
		b.WriteString("_No roles defined._\n\n")
	} else {
		b.WriteString("| Role | Version | Grant | Kind | Runtime | Model | Tier / capacity / context | Skills | Purpose | Last changed |\n")
		b.WriteString("|------|---------|-------|------|---------|-------|---------------------------|--------|---------|--------------|\n")
		for _, r := range roles {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
				cell(r.Name), cell(r.Version), dash(r.Grant), dash(r.Kind), dash(r.Runtime), dash(r.Model), dash(r.Profile),
				cell(strings.Join(r.Skills, ", ")), dash(r.Purpose), dash(r.LastChanged))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Skills (%d)\n\n", len(skls))
	if len(skls) == 0 {
		b.WriteString("_No skills in the library._\n")
	} else {
		b.WriteString("| Skill | Version | Est. tokens | Purpose | Last changed |\n")
		b.WriteString("|-------|---------|-------------|---------|--------------|\n")
		for _, s := range skls {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				cell(s.Name), cell(s.Version), strconv.Itoa(s.EstTokens), dash(s.Purpose), dash(s.LastChanged))
		}
	}
	return b.String()
}

// cell makes any string safe inside a markdown table cell: pipes would end the
// cell early and newlines would end the row, so both are neutralised.
func cell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}

// dash renders an empty value as an em dash so a table cell is never blank.
func dash(s string) string {
	if c := cell(s); c != "" {
		return c
	}
	return "—"
}

// --- wiki publish (best-effort, disclosure-gated) ---

// publishWiki mirrors the rendered catalog to the repo's wiki, which is itself
// a git repo at <owner/repo>.wiki.git. It honors the SAME disclosure gate as
// `github push`: a PUBLIC repo needs recorded per-project consent, because the
// wiki of a public repo is world-readable. Everything is best-effort — a clone,
// write, or push failure returns an error the caller turns into a warning.
func publishWiki(ctx *clikit.Ctx, w *workspace.Workspace, f *clikit.Flags, md string) error {
	slug := f.Get("project")
	if slug == "" {
		return clikit.Usagef("--publish-wiki needs --project <slug> to know which repo's wiki to write (its linked repo)")
	}
	p, err := store.LoadProject(w, slug)
	if err != nil {
		return err
	}
	repo, _ := p.Doc.Front.Get("github_repo")
	if repo == "" {
		return fmt.Errorf("project %s is not linked to a repo — `dacli github link %s` first", p.Slug, p.Slug)
	}
	// Live-visibility disclosure gate, identical in intent to ghmirror's: a
	// repo flipped public after linking must re-trip it before we publish.
	if err := disclosureGate(w, repo, p); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "dacli-wiki-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	url := "https://github.com/" + repo + ".wiki.git"
	if out, err := git(w, tmp, "clone", "--depth", "1", url, "."); err != nil {
		// A wiki that has never been initialized has no clonable repo; say so
		// plainly rather than leaking git's raw message.
		return fmt.Errorf("clone %s failed (create the first wiki page in the browser once to initialize it; output: %s): %w", url, out, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, wikiPage), []byte(md), 0o644); err != nil {
		return err
	}
	if out, err := git(w, tmp, "add", wikiPage); err != nil {
		return fmt.Errorf("git add (output: %s): %w", out, err)
	}
	// Nothing to commit means the wiki already matches — a success, not a
	// failure, so report it and stop before an empty-commit error. A FAILED
	// status is neither: its empty output must not be read as a clean tree, or
	// a wiki that was never pushed is falsely reported as up to date (219).
	clean, err := wikiClean(git(w, tmp, "status", "--porcelain"))
	if err != nil {
		return err
	}
	if clean {
		fmt.Fprintf(ctx.Stdout, "wiki already up to date at %s/wiki/Roster\n", repoWebBase(repo))
		return nil
	}
	if out, err := git(w, tmp, "commit", "-m", "dacli catalog: update Roster"); err != nil {
		return fmt.Errorf("git commit (output: %s): %w", out, err)
	}
	if out, err := git(w, tmp, "push"); err != nil {
		return fmt.Errorf("git push (output: %s): %w", out, err)
	}
	fmt.Fprintf(ctx.Stdout, "published roster to %s/wiki/Roster\n", repoWebBase(repo))
	return nil
}

func repoWebBase(repo string) string { return "https://github.com/" + repo }

// wikiClean classifies a `git status --porcelain` result from the wiki clone,
// taking the (out, err) pair from git directly. A FAILED status is an error,
// never a silent clean tree: git prints nothing to stdout on failure, so
// treating its empty output as "nothing to commit" would report a never-pushed
// wiki as already up to date and skip the commit/push entirely (219).
func wikiClean(out string, err error) (bool, error) {
	if err != nil {
		return false, fmt.Errorf("git status (output: %s): %w", out, err)
	}
	return strings.TrimSpace(out) == "", nil
}

// git runs a git subcommand in dir with a deadline so a hung network/auth
// prompt cannot block the caller (or the mcp stdio loop). It returns combined
// output for diagnostics.
func git(w *workspace.Workspace, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	operation := "git"
	if len(args) > 0 {
		operation += " " + args[0]
	}
	out, err := runCatalogCommand(ctx, w.Root, dir, operation, "git", full...)
	return strings.TrimSpace(string(out)), err
}

// runCatalogCommand is the single governed process boundary for catalog's git
// and gh calls. Its operation is deliberately a stable verb, not argv: clone
// URLs and commit messages may contain credentials or private project text.
func runCatalogCommand(ctx context.Context, root, dir, operation, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	return commandresult.Run(cmd, commandresult.RunOptions{
		Operation:     operation,
		WorkspaceRoot: root,
		TimedOut: func() bool {
			return ctx.Err() == context.DeadlineExceeded
		},
	})
}

type repoInfo struct {
	NameWithOwner string `json:"nameWithOwner"`
	Visibility    string `json:"visibility"`
}

// repoView probes the LIVE visibility of the given repo via gh, so the
// disclosure gate decides on current reality, not a value cached at link time.
// It queries the TARGET repo explicitly (--repo) rather than whatever the cwd
// remote resolves to: the wiki push targets the project's linked repo, so the
// gate must judge that repo's visibility, not the working directory's (dacli
// 167). An empty repo falls back to the cwd repo.
func repoView(w *workspace.Workspace, repo string) (repoInfo, error) {
	var info repoInfo
	c, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	args := []string{"repo", "view", "--json", "nameWithOwner,visibility"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	out, err := runCatalogCommand(c, w.Root, w.Root, "gh repo view", "gh", args...)
	if err != nil {
		return info, fmt.Errorf("gh repo view failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return info, json.Unmarshal(out, &info)
}

// disclosureGate refuses to publish onto a PUBLIC repo's wiki without recorded
// per-project consent — the same gate `github push` applies, reimplemented here
// because a feature slice may not import another slice (arch_test).
func disclosureGate(w *workspace.Workspace, repo string, p *store.Project) error {
	info, err := repoView(w, repo)
	if err != nil {
		return err
	}
	if strings.EqualFold(info.Visibility, "PUBLIC") {
		confirmed, _ := p.Doc.Front.Get("github_public_confirmed")
		if !consentCoversRepo(confirmed, info.NameWithOwner) {
			return clikit.Refusedf("repo %s is PUBLIC and project %s has no recorded consent for it — publishing the roster to its wiki is a disclosure event; `dacli github link %s --allow-public` first", info.NameWithOwner, p.Slug, p.Slug)
		}
	}
	return nil
}

// consentCoversRepo mirrors ghmirror's scoped-consent rule: consent is stored
// as the exact nameWithOwner, so it never leaks to a different repo the project
// is later relinked to, and a legacy bare-boolean "true" fails closed.
func consentCoversRepo(confirmed, repo string) bool {
	return confirmed != "" && strings.EqualFold(confirmed, repo)
}
