// Read operations take an explicit validated binding; storage remains unchanged.
package application

import (
	"errors"
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
	w := &r.workspace
	if project != "" {
		if _, err := store.LoadProject(w, project); err != nil {
			return WorkspaceStatus{}, err
		}
	}
	ps, err := store.ListProjects(w)
	if err != nil {
		return WorkspaceStatus{}, err
	}

	var statuses []ProjectStatus
	for _, p := range ps {
		if project != "" && p.Slug != project {
			continue
		}
		counts := map[model.Status]int{}
		implemented := 0
		ts, err := store.ListTasks(w, p.Slug, "")
		if err != nil {
			return WorkspaceStatus{}, err
		}
		for _, t := range ts {
			counts[t.Status]++
			if t.CompletionState() == "implemented-unlanded" {
				implemented++
			}
		}
		statuses = append(statuses, ProjectStatus{Slug: p.Slug, Title: p.Title, Open: counts[model.StatusOpen], Active: counts[model.StatusActive], Blocked: counts[model.StatusBlocked], Done: counts[model.StatusDone], ImplementedUnlanded: implemented})
	}

	pending, holes, err := eventlog.ListReport(w, eventlog.Query{Pending: true})
	if err != nil {
		return WorkspaceStatus{}, err
	}
	if len(holes) != 0 {
		return WorkspaceStatus{}, errors.New("workspace status unavailable: incomplete event log")
	}
	return WorkspaceStatus{Schema: "workspace-status/v1", ProjectFilter: project, Projects: statuses, PendingEvents: len(pending)}, nil
}
func ListTasks(r RequestContext, project string, status model.Status) ([]*store.Task, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	return store.ListTasks(&r.workspace, project, status)
}
func GetTask(r RequestContext, ref string) (*store.Task, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	return store.FindTask(&r.workspace, ref)
}
