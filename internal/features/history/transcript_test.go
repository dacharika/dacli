package history

import (
	"strings"
	"testing"
)

const streamJSONFixture = `{"type":"system","subtype":"init","tools":["Read"]}
{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the file."},{"type":"tool_use","name":"Read","input":{"path":"x.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}
{"type":"result","subtype":"success","usage":{"input_tokens":1200,"output_tokens":345},"num_turns":2,"total_cost_usd":0.0421}
`

func TestRenderTranscriptToRendersRawStreamJSON(t *testing.T) {
	var out strings.Builder
	renderTranscriptTo(&out, []byte(streamJSONFixture))
	got := out.String()
	for _, want := range []string{"Looking at the file.", "[tool: Read]", "Done."} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered transcript missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"type":`) {
		t.Errorf("rendered transcript leaked raw JSON:\n%s", got)
	}
}
