// Read operations take an explicit validated binding; storage remains unchanged.
package application

import (
	"errors"
	"strings"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

type ProjectStatus struct {
	Slug                string `json:"slug"`
	Title               string `json:"title"`
	Open                int    `json:"open"`
	Active              int    `json:"active"`
	Blocked             int    `json:"blocked"`
	Done                int    `json:"done"`
	ImplementedUnlanded int    `json:"implemented_unlanded"`
}
type WorkspaceStatus struct {
	Schema        string          `json:"schema"`
	ProjectFilter string          `json:"project_filter,omitempty"`
	Projects      []ProjectStatus `json:"projects"`
	PendingEvents int             `json:"pending_events"`
}

func Status(r RequestContext, project string) (WorkspaceStatus, error) {
	if err := r.validate(); err != nil {
		return WorkspaceStatus{}, err
	}
	if r.resource != "workspace:"+r.workspace.ID && (project == "" || r.resource != "project:"+project) {
		return WorkspaceStatus{}, clikit.Refusedf("read scope does not cover workspace status request")
	}
	w := &r.workspace
	narrow := r.resource != "workspace:"+w.ID
	var ps []*store.Project
	if narrow {
		p, err := store.LoadProject(w, project)
		if err != nil {
			return WorkspaceStatus{}, clikit.Refusedf("read scope does not cover workspace status request")
		}
		ps = []*store.Project{p}
	} else {
		if project != "" {
			if _, err := store.LoadProject(w, project); err != nil {
				return WorkspaceStatus{}, err
			}
		}
		var err error
		ps, err = store.ListProjects(w)
		if err != nil {
			return WorkspaceStatus{}, err
		}
	}

	var statuses []ProjectStatus
	projectTaskIDs := map[string]bool{}
	for _, p := range ps {
		if project != "" && p.Slug != project {
			continue
		}
		counts := map[model.Status]int{}
		implemented := 0
		ts, err := store.ListTasks(w, p.Slug, "")
		if err != nil {
			if narrow {
				return WorkspaceStatus{}, clikit.Refusedf("read scope does not cover workspace status request")
			}
			return WorkspaceStatus{}, err
		}
		for _, t := range ts {
			if p.Slug == project {
				projectTaskIDs[t.ID] = true
				projectTaskIDs[strings.TrimPrefix(t.ID, "t-")] = true
			}
			counts[t.Status]++
			if t.CompletionState() == "implemented-unlanded" {
				implemented++
			}
		}
		statuses = append(statuses, ProjectStatus{Slug: p.Slug, Title: p.Title, Open: counts[model.StatusOpen], Active: counts[model.StatusActive], Blocked: counts[model.StatusBlocked], Done: counts[model.StatusDone], ImplementedUnlanded: implemented})
	}

	pending, holes, err := eventlog.ListReport(w, eventlog.Query{Pending: true})
	if err != nil {
		if narrow {
			return WorkspaceStatus{}, clikit.Refusedf("read scope does not cover workspace status request")
		}
		return WorkspaceStatus{}, err
	}
	if len(holes) != 0 {
		return WorkspaceStatus{}, errors.New("workspace status unavailable: incomplete event log")
	}
	pendingCount := len(pending)
	if r.resource == "project:"+project && project != "" {
		pendingCount = 0
		for _, event := range pending {
			if projectTaskIDs[event.About] || projectTaskIDs[strings.TrimPrefix(event.About, "t-")] {
				pendingCount++
			}
		}
	}
	return WorkspaceStatus{Schema: "workspace-status/v1", ProjectFilter: project, Projects: statuses, PendingEvents: pendingCount}, nil
}
func ListTasks(r RequestContext, project string, status model.Status) ([]*store.Task, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if r.resource != "workspace:"+r.workspace.ID && (project == "" || r.resource != "project:"+project) {
		return nil, clikit.Refusedf("read scope does not cover task list request")
	}
	tasks, err := store.ListTasks(&r.workspace, project, status)
	if err != nil && r.resource != "workspace:"+r.workspace.ID {
		return nil, clikit.Refusedf("read scope does not cover task list request")
	}
	return tasks, err
}
func GetTask(r RequestContext, ref string) (*store.Task, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	switch {
	case r.resource == "workspace:"+r.workspace.ID:
		return store.FindTask(&r.workspace, ref)
	case strings.HasPrefix(r.resource, "project:"):
		project := strings.TrimPrefix(r.resource, "project:")
		tasks, err := store.ListTasks(&r.workspace, project, "")
		if err != nil {
			return nil, clikit.Refusedf("read scope does not cover task request")
		}
		t, err := store.NewTaskIndex(tasks).Find(ref)
		if err != nil || t.Project != project {
			return nil, clikit.Refusedf("read scope does not cover task request")
		}
		return t, nil
	case strings.HasPrefix(r.resource, "task:"):
		if ref != strings.TrimPrefix(r.resource, "task:") {
			return nil, clikit.Refusedf("read scope does not cover task request")
		}
		t, err := store.FindTask(&r.workspace, ref)
		if err != nil || t.ID != ref || r.resource != "task:"+t.ID {
			return nil, clikit.Refusedf("read scope does not cover task request")
		}
		return t, nil
	default:
		return nil, clikit.Refusedf("read scope does not cover task request")
	}
}
