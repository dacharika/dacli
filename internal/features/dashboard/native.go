package dashboard

import (
	"errors"
	"net/url"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type nativeDashboardReader interface {
	Read(string, string) (sessions.DashboardSnapshot, error)
}
type deniedNativeReader struct{}

func (deniedNativeReader) Read(string, string) (sessions.DashboardSnapshot, error) {
	return sessions.DashboardSnapshot{}, nativeconfig.ErrDenied
}
func openNativeReader(path string, w *workspace.Workspace) nativeDashboardReader {
	if path == "" {
		return nil
	}
	p, e := application.OpenNativeDashboard(path, w)
	if e != nil {
		return deniedNativeReader{}
	}
	return p
}
func nativeSnapshot(reader nativeDashboardReader, project, task string) sessions.DashboardSnapshot {
	empty := sessions.DashboardSnapshot{Schema: "native-dashboard/v1", State: "unavailable", Project: project, ObservedAt: time.Now().UTC(), Records: []sessions.DashboardRecord{}, Attention: []sessions.DashboardAttention{}, Services: map[string]string{}, Spend: sessions.DashboardSpend{State: "unavailable", Provenance: "unavailable", Enforcement: "unavailable", Unit: "owner-estimated tokens"}}
	if reader == nil {
		return empty
	}
	result, e := reader.Read(project, task)
	if e == nil {
		return result
	}
	empty.State = "error"
	if errors.Is(e, sessions.ErrDenied) || errors.Is(e, nativeconfig.ErrDenied) {
		empty.State = "permission"
	}
	return empty
}
func nativeProjects(w *workspace.Workspace, reader nativeDashboardReader, project string) ([]sessions.DashboardSnapshot, error) {
	out := []sessions.DashboardSnapshot{}
	if project != "" {
		return append(out, nativeSnapshot(reader, project, "")), nil
	}
	projects, e := store.ListProjects(w)
	if e != nil {
		return nil, e
	}
	for _, p := range projects {
		out = append(out, nativeSnapshot(reader, p.Slug, ""))
	}
	return out, nil
}
func buildNativeTaskDetail(w *workspace.Workspace, reader nativeDashboardReader, ref string) (taskDetailResponse, error) {
	out, e := buildTaskDetail(w, ref)
	if e == nil {
		snapshot := nativeSnapshot(reader, out.Task.Project, out.Task.ID)
		out.Task.Native = &snapshot
	}
	return out, e
}
func buildNativeAttention(w *workspace.Workspace, reader nativeDashboardReader, project string) (operatorAttentionResponse, error) {
	out, e := buildOperatorAttention(w, project, time.Now())
	if e != nil {
		return out, e
	}
	out.Native, e = nativeProjects(w, reader, project)
	if e != nil {
		return out, e
	}
	nativeFreshness := map[string]string{}
	for _, snapshot := range out.Native {
		for _, item := range snapshot.Attention {
			severity := "medium"
			if item.Category == "blocked" {
				severity = "high"
			}
			link := "#/work?project=" + url.QueryEscape(snapshot.Project) + "&task=" + url.QueryEscape(item.TaskID)
			alert := attentionCandidate("native_"+item.Category, severity, attentionAffected{Project: snapshot.Project, Task: item.TaskID}, item.ObservedAt, false, item.Summary, "Inspect exact native record; dashboard grants no execution or release rights.", link, attentionEvidence{Kind: "native-governance", ID: item.ID, URL: link, ObservedAt: item.ObservedAt.UTC().Format(time.RFC3339), Confidence: "historical"})
			alert.Freshness = item.Freshness
			nativeFreshness[alert.ID] = item.Freshness
			out.Alerts = append(out.Alerts, alert)
		}
	}
	out.Alerts = collapseAttentionCandidates(out.Alerts, time.Now())
	for i := range out.Alerts {
		if value, ok := nativeFreshness[out.Alerts[i].ID]; ok {
			out.Alerts[i].Freshness = value
		}
	}
	return out, nil
}
func buildNativeBurn(w *workspace.Workspace, reader nativeDashboardReader) (burnResponse, error) {
	out, e := buildBurnResponse(w)
	if e == nil {
		out.Native, e = nativeProjects(w, reader, "")
	}
	return out, e
}
