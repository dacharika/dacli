package reviewing

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

const reviewEventOrigin = "independent-review-result/v1"

func cmdReviewRecord(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("task", "result"); err != nil {
		return err
	}
	if f.Get("task") == "" || f.Get("result") == "" {
		return clikit.Usagef("usage: dacli review record --task <ref> --result <JSON>")
	}
	if id.Grant != model.GrantRO {
		return clikit.Refusedf("independent review results require a read-only reviewer identity; current grant is %s", id.Grant)
	}
	task, err := store.FindTask(w, f.Get("task"))
	if err != nil {
		return err
	}
	var result store.IndependentReviewResult
	if err := json.Unmarshal([]byte(f.Get("result")), &result); err != nil {
		return clikit.Usagef("--result is not valid %s JSON: %v", store.ReviewResultSchema, err)
	}
	role, ok := store.LoadRole(w, id.Role)
	if !ok {
		return clikit.Refusedf("reviewer role %s is not declared", id.Role)
	}
	if result.ReviewerID != id.ID || result.ReviewerRole != id.Role || result.Runtime != role.Runtime || result.Model != role.ModelID() || result.Grant != string(id.Grant) {
		return clikit.Refusedf("review result identity/runtime/model/grant does not match the acting reviewer")
	}
	if err := result.Validate(); err != nil {
		return clikit.Refusedf("invalid structured review result: %v", err)
	}
	branch := taskBranch(task)
	commit, err := gitx.Run(w.Root, "rev-parse", "--verify", branch)
	if err != nil {
		return clikit.Refusedf("cannot observe reviewed branch %s: %v", branch, err)
	}
	tree, err := gitx.Run(w.Root, "rev-parse", "--verify", strings.TrimSpace(commit)+"^{tree}")
	if err != nil {
		return clikit.Refusedf("cannot observe reviewed tree for %s: %v", branch, err)
	}
	if result.CommitSHA != strings.TrimSpace(commit) || result.TreeSHA != strings.TrimSpace(tree) {
		return clikit.Refusedf("stale review output: result binds %s/%s but %s is %s/%s", result.CommitSHA, result.TreeSHA, branch, strings.TrimSpace(commit), strings.TrimSpace(tree))
	}
	raw, _ := json.Marshal(result)
	ev, err := eventlog.Append(w, id.ID, model.EventReview, task.ID, reviewEventOrigin, string(raw))
	if err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "recorded review %s for %s at %s/%s\n", ev.ID, task.ID, result.CommitSHA, result.TreeSHA)
	return nil
}

func cmdReviewProjection(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("task"); err != nil {
		return err
	}
	if f.Get("task") == "" {
		return clikit.Usagef("usage: dacli review projection --task <ref> [--json]")
	}
	task, err := store.FindTask(w, f.Get("task"))
	if err != nil {
		return err
	}
	events, err := eventlog.List(w, eventlog.Query{About: task.ID, Kinds: []model.EventKind{model.EventReview}, Limit: 1})
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return store.ErrNotFound{Ref: "structured review for " + task.ID}
	}
	var result store.IndependentReviewResult
	if err := json.Unmarshal([]byte(events[0].Body), &result); err != nil {
		return fmt.Errorf("decode recorded review %s: %w", events[0].ID, err)
	}
	projection, err := result.PublicProjection()
	if err != nil {
		return fmt.Errorf("project recorded review %s: %w", events[0].ID, err)
	}
	enc := json.NewEncoder(ctx.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(projection)
}

var Commands = []clikit.Command{
	{Path: "review record", Brief: "Record an identity-bound independent review verdict", Usage: "dacli review record --task <ref> --result <JSON>", Run: cmdReviewRecord},
	{Path: "review projection", Brief: "Render public-safe structured review projection", JSON: true, Usage: "dacli review projection --task <ref> [--json]", Run: cmdReviewProjection},
}

func taskBranch(t *store.Task) string { return fmt.Sprintf("dacli/%03d-%s", t.Seq, t.Slug) }
