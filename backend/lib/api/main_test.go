package api

import (
	"os"
	"testing"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// TestMain swaps the default CLI clients for fakes: tests that reach quota or
// model queries without installing their own clients must never run a real
// (and possibly installed, signed-out or slow) CLI. Tests needing specific
// answers still override with agentwrapper.SetClients; restoring (nil) now
// brings back these fakes instead of the real clients.
func TestMain(m *testing.M) {
	agentwrapper.SetDefaultClients(map[string]types.CLIClient{
		"agy":      agentwrapper.NewFakeClient(),
		"claude":   agentwrapper.NewFakeClient(),
		"opencode": agentwrapper.NewFakeClient(),
		"simplest": agentwrapper.NewFakeClient(),
	})
	os.Exit(m.Run())
}
