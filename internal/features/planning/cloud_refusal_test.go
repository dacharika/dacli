package planning

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
	"github.com/mlnomadpy/dacli/internal/deviceidentity"
)

func TestCloudErrorMapsInboundIdentityConflictsToPolicyRefusal(t *testing.T) {
	causes := []struct {
		name string
		err  error
	}{
		{name: "collision", err: cloudsync.ErrInboundCollision},
		{name: "untrusted history", err: cloudsync.ErrUntrustedHistory},
		{name: "below replay floor", err: cloudsync.ErrReplay},
	}
	for _, cause := range causes {
		for _, wrapped := range []bool{false, true} {
			name := cause.name + "/direct"
			input := cause.err
			if wrapped {
				name = cause.name + "/wrapped"
				input = fmt.Errorf("credential Bearer must-not-leak: %w", cause.err)
			}
			t.Run(name, func(t *testing.T) {
				got := cloudError(input)
				if code := clikit.ExitCode(got); code != 3 {
					t.Fatalf("cloudError(%v) exit = %d, want policy refusal 3", input, code)
				}
				if strings.Contains(got.Error(), "must-not-leak") || strings.Contains(got.Error(), "Bearer") {
					t.Fatalf("cloudError leaked wrapped input: %v", got)
				}
			})
		}
	}
}

func TestCloudErrorKeepsOfflineStorageAndUnexpectedFailuresRetryable(t *testing.T) {
	causes := []struct {
		name string
		err  error
	}{
		{name: "transport offline", err: cloudsync.ErrTransportOffline},
		{name: "storage failure", err: os.ErrPermission},
		{name: "unexpected", err: errors.New("provider response contains must-not-leak")},
	}
	for _, cause := range causes {
		t.Run(cause.name, func(t *testing.T) {
			got := cloudError(cause.err)
			if code := clikit.ExitCode(got); code != 1 {
				t.Fatalf("cloudError(%v) exit = %d, want retryable failure 1", cause.err, code)
			}
			if strings.Contains(got.Error(), "must-not-leak") {
				t.Fatalf("cloudError leaked underlying failure: %v", got)
			}
		})
	}
}

func TestCloudErrorPreservesTypedDeviceIdentityExitMapping(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retryable bool
		want      int
	}{
		{name: "permanent refusal", retryable: false, want: 3},
		{name: "transient failure", retryable: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &deviceidentity.Error{Code: "invalid_grant", Retryable: tc.retryable, Next: "credential must-not-leak"}
			got := cloudError(input)
			if code := clikit.ExitCode(got); code != tc.want {
				t.Fatalf("cloudError(device identity error) exit = %d, want %d", code, tc.want)
			}
			if strings.Contains(got.Error(), "must-not-leak") {
				t.Fatalf("cloudError leaked device identity guidance: %v", got)
			}
		})
	}
}
