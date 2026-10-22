package insight

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type rosterProblem struct {
	Kind   string
	Detail string
}

var taskRoleName = regexp.MustCompile(`(?i)(^|[-_])(task|issue)[-_]?[0-9]+($|[-_])`)
var taskRoleSummary = regexp.MustCompile(`(?i)\b(?:task|issue)\s*#?[0-9]+\b`)

// rosterProblems keeps execution policy inspectable. A role can parse and
// preflight while still being unroutable, provider-bound, or an expensive
// metadata shell; those defects otherwise tax every future spawn (issue #689).
func rosterProblems(w *workspace.Workspace, roles []team.Role) []rosterProblem {
	var out []rosterProblem
	models := map[string]int{}
	for _, r := range roles {
		var missing []string
		d, err := mdstore.ReadFile(w.RolePath(r.Name))
		if err != nil {
			out = append(out, rosterProblem{"role-metadata", fmt.Sprintf("role %s cannot be read: %v", r.Name, err)})
			continue
		}
		if v, ok := d.Front.Get("version"); !ok || strings.TrimSpace(v) == "" {
			missing = append(missing, "version")
		}
		if r.Kind == "" {
			missing = append(missing, "role_kind")
		}
		if r.Grant == "" {
			missing = append(missing, "grant")
		}
		if r.Runtime == "" {
			missing = append(missing, "runtime")
		}
		if r.ModelID() == "" {
			missing = append(missing, "model_id")
		}
		if r.Profile.CostTier == 0 {
			missing = append(missing, "cost_tier")
		}
		if r.TaskCapacity() == 0 {
			missing = append(missing, "max_task_points")
		}
		if r.Profile.ContextLimit == 0 {
			missing = append(missing, "context_limit")
		}
		if len(r.Profile.CapabilityTags) == 0 {
			missing = append(missing, "capability_tags")
		}
		if len(r.Scope) == 0 {
			missing = append(missing, "scope")
		}
		if len(r.OutOfScope) == 0 {
			missing = append(missing, "out_of_scope")
		}
		if len(r.EscalateTo) == 0 {
			missing = append(missing, "escalate_to")
		}
		if len(r.Skills) == 0 {
			missing = append(missing, "skills")
		}
		if len(missing) > 0 {
			out = append(out, rosterProblem{"role-metadata", fmt.Sprintf("role %s is missing %s", r.Name, strings.Join(missing, ", "))})
		}

		name := strings.ToLower(r.Name)
		for _, provider := range []string{"codex", "claude", "gemini", "copilot", "opus"} {
			if strings.Contains(name, provider) {
				out = append(out, rosterProblem{"provider-specific-role", fmt.Sprintf("role %s encodes provider %s in its responsibility; keep provider choice in runtime/model metadata", r.Name, provider)})
				break
			}
		}
		if taskRoleName.MatchString(name) {
			out = append(out, rosterProblem{"task-specific-role", fmt.Sprintf("role %s is tied to one task/issue; use a reusable responsibility role", r.Name)})
		} else if taskRoleSummary.MatchString(r.Summary) {
			out = append(out, rosterProblem{"task-specific-role", fmt.Sprintf("role %s summary is tied to one task/issue; use a reusable responsibility role", r.Name)})
		}
		if model := strings.TrimSpace(r.ModelID()); model != "" {
			models[model]++
		}
	}

	if len(roles) >= 5 {
		model, count := "", 0
		for candidate, n := range models {
			if n > count || n == count && candidate < model {
				model, count = candidate, n
			}
		}
		if count*100 >= len(roles)*70 {
			out = append(out, rosterProblem{"model-concentration", fmt.Sprintf("model %s is the default for %d/%d roles (>=70%%); route by cost and consequence instead", model, count, len(roles))})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}
