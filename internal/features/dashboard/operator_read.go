package dashboard

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	operatorv1 "github.com/mlnomadpy/dacli/contracts/operator/v1"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var ErrOperatorNotFound = errors.New("operator resource not found")

type OperatorProjectionResult struct {
	Data any
	Page *operatorv1.Page
}

// OperatorProjection exposes the same canonical dashboard builders to the
// app-owned operator adapter without moving projection logic into the transport.
func OperatorProjection(w *workspace.Workspace, operation, project, task, cursor string, limit int, kind, state, timeRange string) (OperatorProjectionResult, error) {
	switch operation {
	case "tasks.list":
		projection, err := operatorProjectionResult(buildTasks(w, project))
		if err != nil || task == "" {
			return projection, err
		}
		rows := projection.Data.(tasksResponse)
		selected := rows.Tasks[:0]
		for _, row := range rows.Tasks {
			if row.ID == task {
				selected = append(selected, row)
			}
		}
		rows.Tasks = selected
		projection.Data = rows
		return pageOperatorTasks(projection, limit), nil
	case "tasks.get":
		projection, err := operatorProjectionResult(buildTaskDetail(w, task))
		if err != nil {
			return projection, err
		}
		detail := projection.Data.(taskDetailResponse)
		projectTasks, err := store.ListTasks(w, project, "")
		if err != nil {
			return OperatorProjectionResult{}, err
		}
		projectTaskIDs := make(map[string]struct{}, len(projectTasks))
		for _, projectTask := range projectTasks {
			projectTaskIDs[projectTask.ID] = struct{}{}
		}
		for i, dependency := range detail.Task.Deps {
			if dependency.ID == "" {
				continue
			}
			if _, inProject := projectTaskIDs[dependency.ID]; !inProject {
				// Keep the canonical edge reference and type, but report the
				// dependency metadata as unavailable in this project scope.
				detail.Task.Deps[i].ID = ""
				detail.Task.Deps[i].Title = ""
				detail.Task.Deps[i].Status = ""
				detail.Task.Deps[i].Resolved = false
			}
		}
		projection.Data = detail
		return projection, nil
	case "activity.list":
		if limit == 0 {
			limit = eventsDefaultLimit
		}
		filters, err := parseEventFilters(eventFilters{Project: project, Task: task, Cursor: cursor, Limit: limit, Kind: kind, State: state, Range: timeRange})
		if err != nil {
			return OperatorProjectionResult{}, err
		}
		projection, err := operatorProjectionResult(buildEvents(w, filters))
		if err != nil {
			return projection, err
		}
		events := projection.Data.(eventsResponse)
		projection.Page = &operatorv1.Page{Limit: events.Limit, NextCursor: events.NextCursor, Truncated: events.Truncated, Partial: events.Partial}
		return projection, nil
	case "attempts.timeline":
		projection, err := operatorProjectionResult(buildDeliveryTimeline(w, task))
		return pageOperatorTimeline(projection, limit), err
	case "attention.list":
		projection, err := operatorProjectionResult(buildOperatorAttention(w, project, time.Now()))
		if err != nil {
			return projection, err
		}
		response := projection.Data.(operatorAttentionResponse)
		selected := response.Alerts[:0]
		for _, alert := range response.Alerts {
			if task == "" || alert.Affected.Task == task {
				selected = append(selected, alert)
			}
		}
		response.Alerts = selected
		projection.Data = response
		return pageOperatorAttention(projection, limit), nil
	default:
		return OperatorProjectionResult{}, fmt.Errorf("unsupported operator projection")
	}
}

func operatorProjectionResult[T any](value T, err error) (OperatorProjectionResult, error) {
	if err != nil {
		var api apiError
		if errors.As(err, &api) && api.status == http.StatusNotFound {
			return OperatorProjectionResult{}, ErrOperatorNotFound
		}
		return OperatorProjectionResult{}, err
	}
	return OperatorProjectionResult{Data: value}, nil
}

func pageOperatorTasks(result OperatorProjectionResult, limit int) OperatorProjectionResult {
	if limit <= 0 {
		limit = 50
	}
	data := result.Data.(tasksResponse)
	page := operatorv1.Page{Limit: limit}
	if len(data.Tasks) > limit {
		data.Tasks = data.Tasks[:limit]
		page.Truncated, page.Partial = true, true
	}
	result.Data, result.Page = data, &page
	return result
}

func pageOperatorTimeline(result OperatorProjectionResult, limit int) OperatorProjectionResult {
	if result.Data == nil {
		return result
	}
	if limit <= 0 {
		limit = 50
	}
	data := result.Data.(deliveryTimelineResponse)
	page := operatorv1.Page{Limit: limit}
	if len(data.Attempts) > limit {
		data.Attempts = data.Attempts[:limit]
		page.Truncated, page.Partial = true, true
	}
	result.Data, result.Page = data, &page
	return result
}

func pageOperatorAttention(result OperatorProjectionResult, limit int) OperatorProjectionResult {
	if limit <= 0 {
		limit = 50
	}
	data := result.Data.(operatorAttentionResponse)
	page := operatorv1.Page{Limit: limit}
	if len(data.Alerts) > limit {
		data.Alerts = data.Alerts[:limit]
		page.Truncated, page.Partial = true, true
	}
	result.Data, result.Page = data, &page
	return result
}
