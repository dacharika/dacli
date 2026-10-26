package cli

import "testing"

func TestOperatorServeDeclaresJSONFrameOutput(t *testing.T) {
	cmd, _ := match([]string{"operator", "serve"})
	if cmd == nil || !cmd.JSON {
		t.Fatal("operator serve must declare its JSON-lines output contract")
	}
	if err := refuseUnsupportedJSON(cmd, true); err != nil {
		t.Fatalf("operator serve refused JSON mode: %v", err)
	}
}
