package deviceauth

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/deviceidentity"
)

func TestSelectedLogoutUsesExactSelectorWithoutReadingMetadata(t *testing.T) {
	var got [3]string
	called := false
	opener := func(provider, tenant, device string) (*deviceidentity.Client, error) {
		called = true
		got = [3]string{provider, tenant, device}
		return nil, nil
	}
	client, err := selectedLogoutWith([]string{"--provider", "https://id.example", "--tenant", "tenant-a", "--device", "device-a"}, opener)
	if err != nil || client != nil || !called {
		t.Fatalf("selectedLogoutWith = (%v, %v), called=%v", client, err, called)
	}
	if want := [3]string{"https://id.example", "tenant-a", "device-a"}; got != want {
		t.Fatalf("logout selector = %v, want %v", got, want)
	}
}

func TestPublicIdentityJSONRedactsCredentialsAndPreservesErrorCodes(t *testing.T) {
	raw, err := json.Marshal(public(deviceidentity.Metadata{Provider: "https://id.example", TenantID: "tenant-a", AccountID: "account-a", DeviceID: "device-a", Scopes: []string{"device sync"}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"access-secret-marker", "refresh-secret-marker", "private_key", "access_token", "credential_path"} {
		if strings.Contains(string(raw), marker) {
			t.Fatalf("public identity JSON contains %q: %s", marker, raw)
		}
	}
	if got := clikit.ExitCode(typed(&deviceidentity.Error{Code: "offline", Retryable: true, Next: "retry"})); got != 1 {
		t.Fatalf("retryable offline error exit = %d, want 1", got)
	}
	if got := clikit.ExitCode(typed(&deviceidentity.Error{Code: "denied", Retryable: false, Next: "login again"})); got != 3 {
		t.Fatalf("policy refusal exit = %d, want 3", got)
	}
}

func TestSelectedLogoutRejectsIncompleteSelectorBeforeOpeningStore(t *testing.T) {
	called := false
	_, err := selectedLogoutWith([]string{"--provider", "https://id.example", "--tenant", "tenant-a"}, func(string, string, string) (*deviceidentity.Client, error) {
		called = true
		return nil, nil
	})
	if err == nil || called {
		t.Fatalf("incomplete selector err=%v, opener called=%v", err, called)
	}
}
