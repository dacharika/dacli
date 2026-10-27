package acceptance

import (
	"strings"
	"testing"
)

func TestNativeBatchLandingInvalidPresentNoLegacyFallback(t *testing.T) {
	w, task := landedFixture(t)
	task.Doc.Front.Set("native_landing.current", "sha256:"+strings.Repeat("a", 64))
	old := runLandingGH
	calls := 0
	runLandingGH = func(string, ...string) (string, error) {
		calls++
		return `[{"state":"MERGED","baseRefName":"main"}]`, nil
	}
	t.Cleanup(func() { runLandingGH = old })
	if state, _ := checkLanded(w, task, "main"); state != landingUnknown || calls != 0 {
		t.Fatalf("invalid native receipt fell through to loose MERGED evidence: state=%v calls=%d", state, calls)
	}
}
