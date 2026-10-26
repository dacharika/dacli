package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/features/nativegovernance"
)

func TestNativeReaderAddIsAdvertisedAsExplicitOwnerMutation(t *testing.T) {
	cmd, args := dispatch([]string{"native", "reader", "add", "--help"})
	if cmd == nil || cmd.Path != "native reader add" || !cmd.Mutates {
		t.Fatalf("reader add missing mutation capability: %+v", cmd)
	}
	if !nativegovernance.AuthenticatesExplicitly(cmd.Path) {
		t.Fatal("reader add is not classified as explicit owner authentication")
	}
	var out bytes.Buffer
	ctx := &Ctx{Stdout: &out, Stderr: &out}
	if err := invoke(ctx, cmd, args); err != nil {
		t.Fatalf("reader help failed: %v", err)
	}
	for _, text := range []string{"--workspace", "--project", "--task", "--connection", "--credential", "--expires"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("reader help omitted %s: %s", text, out.String())
		}
	}
	found := false
	for _, command := range capabilityManifest().Commands {
		if command.Path == "native reader add" {
			found = true
			if !command.Mutates || !strings.Contains(command.Usage, "--workspace") {
				t.Fatalf("capability manifest has incomplete reader command: %+v", command)
			}
		}
	}
	if !found {
		t.Fatal("capability manifest omitted native reader add")
	}
}
