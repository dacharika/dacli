package dashboard

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type fixtureNativeReader struct {
	result        sessions.DashboardSnapshot
	err           error
	project, task string
}

func (f *fixtureNativeReader) Read(project, task string) (sessions.DashboardSnapshot, error) {
	f.project = project
	f.task = task
	return f.result, f.err
}
func TestNativeDashboardHTTPSelectionStatesAndSpend(t *testing.T) {
	w := dashboardEnv(t)
	reader := &fixtureNativeReader{result: sessions.DashboardSnapshot{Schema: "native-dashboard/v1", State: "ready", Project: "core", Records: []sessions.DashboardRecord{{Kind: "evidence", ID: "exact-evidence", AttemptID: "exact-attempt", RevisionID: "exact-revision", ReviewerID: "independent-reviewer", TaskID: "002", Freshness: "stale"}}, Attention: []sessions.DashboardAttention{{ID: "lease-id", TaskID: "002", Category: "blocked", Summary: "Retained protection", Freshness: "stale", ObservedAt: time.Now()}}, Spend: sessions.DashboardSpend{State: "unavailable", Provenance: "unavailable"}}}
	for _, endpoint := range []string{"/api/task?ref=002", "/api/attention?project=core", "/api/burn"} {
		rw := httptest.NewRecorder()
		newHandlerNative(w, reader).ServeHTTP(rw, httptest.NewRequest("GET", "http://127.0.0.1"+endpoint, nil))
		if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"native"`) {
			t.Fatal("native HTTP projection not wired", endpoint, rw.Code, rw.Body.String())
		}
		var decoded any
		if e := json.Unmarshal(rw.Body.Bytes(), &decoded); e != nil {
			t.Fatal(e)
		}
		compact, e := json.Marshal(decoded)
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(compact), `"provider_tokens":null`) || strings.Contains(string(compact), `"provider_tokens":0`) {
			t.Fatal("unknown spend represented as zero", endpoint)
		}
	}
	out, e := buildNativeAttention(w, reader, "core")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, a := range out.Alerts {
		if a.Code == "native_blocked" {
			found = a.Freshness == "stale" && a.Rank > 0 && len(a.Evidence) > 0 && strings.HasPrefix(a.Evidence[0].URL, "#/work?")
		}
	}
	if !found {
		t.Fatal("native attention lost stale freshness or rank")
	}
	for _, tc := range []struct {
		err   error
		state string
	}{{sessions.ErrDenied, "permission"}, {errors.New("private secret error"), "error"}} {
		reader.err = tc.err
		s := nativeSnapshot(reader, "core", "")
		if s.State != tc.state || len(s.Records) != 0 {
			t.Fatal("native error state leaked records", s)
		}
	}
	if s := nativeSnapshot(nil, "core", ""); s.State != "unavailable" || s.Spend.ProviderTokens != nil {
		t.Fatal("unconfigured reader invented snapshot", s)
	}
}
