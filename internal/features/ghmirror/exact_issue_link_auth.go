package ghmirror

// AuthenticatesExplicitly reports commands whose mutating path authenticates
// through this feature's explicit native owner profile instead of DACLI_AGENT.
// The CLI uses this narrow classification for both its grant gate and its
// generic MCP local-only boundary; the native tool has a separate catalog.
func AuthenticatesExplicitly(path string) bool {
	return path == "github issue link"
}
