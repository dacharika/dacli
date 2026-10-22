package main

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/cloud/internal/config"
)

func TestRunArgsUsesExplicitAndDefaultConfigPaths(t *testing.T) {
	sentinel := errors.New("stop after config wiring")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"default":  {want: "cloud/config.development.json"},
		"explicit": {args: []string{"--config", "operator.json"}, want: "operator.json"},
	} {
		t.Run(name, func(t *testing.T) {
			var got string
			err := runArgs(tc.args, func(path string, _ config.LookupEnv) (config.Config, error) {
				got = path
				return config.Config{}, sentinel
			})
			if !errors.Is(err, sentinel) || got != tc.want {
				t.Fatalf("runArgs path=%q err=%v, want path=%q sentinel", got, err, tc.want)
			}
		})
	}
}

func TestRunArgsRejectsInvalidArgumentsBeforeLoadingConfig(t *testing.T) {
	sentinel := errors.New("loader should not run")
	for name, args := range map[string][]string{
		"unknown flag": {"--unknown"},
		"positional":   {"extra"},
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			err := runArgs(args, func(string, config.LookupEnv) (config.Config, error) {
				called = true
				return config.Config{}, sentinel
			})
			if err == nil || called {
				t.Fatalf("invalid args err=%v loader-called=%v", err, called)
			}
			if name == "unknown flag" && !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("unknown flag error = %v", err)
			}
		})
	}
}

func TestRunArgsPropagatesCompositionStartupRefusal(t *testing.T) {
	var loadedPath string
	err := runArgs(nil, func(path string, _ config.LookupEnv) (config.Config, error) {
		loadedPath = path
		// The process can start only with an explicit hosted composition. This
		// fixture intentionally omits it, so startup stops before any database
		// connection or external service is attempted.
		return config.Config{Mode: "development", ListenAddress: "127.0.0.1:0",
			PublicBaseURL: &url.URL{Scheme: "http", Host: "localhost:8080"}}, nil
	})
	if loadedPath != "cloud/config.development.json" {
		t.Fatalf("default config path = %q", loadedPath)
	}
	if err == nil || err.Error() != "hosted composition requires explicit hosted configuration" {
		t.Fatalf("startup composition error = %v", err)
	}
}
