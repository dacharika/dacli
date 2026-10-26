package dashboard

import (
	"time"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

// OperatorProjection exposes the same canonical dashboard builders to the
// app-owned operator adapter without moving projection logic into the transport.
func OperatorProjection(w *workspace.Workspace, operation, project, task, cursor string, limit int, kind, state, timeRange string) (any, error) {
	switch operation {
	case "tasks.list":
		return buildTasks(w, project)
	case "tasks.get":
		return buildTaskDetail(w, task)
	case "activity.list":
		if limit == 0 {
			limit = eventsDefaultLimit
		}
		filters, err := parseEventFilters(eventFilters{Project: project, Task: task, Cursor: cursor, Limit: limit, Kind: kind, State: state, Range: timeRange})
		if err != nil {
			return nil, err
		}
		return buildEvents(w, filters)
	case "attempts.timeline":
		return buildDeliveryTimeline(w, task)
	case "attention.list":
		return buildOperatorAttention(w, project, time.Now())
	default:
		return nil, nil
	}
}
