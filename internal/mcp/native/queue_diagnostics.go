package native

import (
	"context"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type queueDiagnosticsArgs struct {
	version
	Project string `json:"project"`
	Task    string `json:"task,omitempty"`
	Limit   *int   `json:"limit,omitempty"`
}

// queueDiagnostics is the queue tool's explicit early-dispatch boundary. It
// opens the selected operator reader and leaves authentication, exact project
// authorization, canonical reads and revalidation to the application layer.
func queueDiagnostics(ctx context.Context, connectionPath string, expected Connection, args queueDiagnosticsArgs) (sessions.QueueDiagnosticsSnapshot, error) {
	if args.SchemaVersion != 1 || args.Project == "" || (args.Limit != nil && (*args.Limit < 1 || *args.Limit > 100)) {
		return sessions.QueueDiagnosticsSnapshot{}, ErrDenied
	}
	limit := 50
	if args.Limit != nil {
		limit = *args.Limit
	}
	reader, err := application.OpenPinnedQueueDiagnosticsRead(connectionPath, expected)
	if err != nil {
		return sessions.QueueDiagnosticsSnapshot{}, ErrDenied
	}
	return reader.ReadQueueDiagnostics(ctx, args.Project, args.Task, limit)
}
