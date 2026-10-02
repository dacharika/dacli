package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// buildDacli compiles the real binary once per test run, so fixture children
// can call dacli exactly the way real children do — through the same entry
// path as users. The about-filter bug survived unit tests precisely because
// they bypassed that path.
var dacliBin struct {
	once sync.Once
	path string
	dir  string
	err  error
}

func buildDacli(t *testing.T) string {
	t.Helper()
	dacliBin.once.Do(func() {
		dir, err := os.MkdirTemp("", "dacli-bin-*")
		if err != nil {
			dacliBin.err = err
			return
		}
		dacliBin.dir = dir
		dacliBin.path = filepath.Join(dir, "dacli")
		out, err := exec.Command("go", "build", "-o", dacliBin.path, "../../cmd/dacli").CombinedOutput()
		if err != nil {
			dacliBin.err = err
			dacliBin.path = string(out)
		}
	})
	if dacliBin.err != nil {
		t.Fatalf("building dacli: %v\n%s", dacliBin.err, dacliBin.path)
	}
	return dacliBin.path
}
