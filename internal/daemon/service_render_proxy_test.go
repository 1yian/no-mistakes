package daemon

import (
	"reflect"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/paths"
)

func TestSystemdUnitProxyEnvRoundTripsThroughRenderer(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	in := [][2]string{
		{"HTTPS_PROXY", "http://user:p%40ss@127.0.0.1:7897"},
		{"NO_PROXY", "localhost,127.0.0.1"},
		{"http_proxy", "http://lower:1/"},
	}

	unit := renderSystemdUnitWithProxyEnv("/usr/local/bin/no-mistakes", p, "/home/u", in)

	got := systemdUnitProxyEnv([]byte(unit))
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("systemdUnitProxyEnv() = %v, want %v (HOME/PATH must be skipped, %% undone)", got, in)
	}
	if reRendered := renderSystemdUnitWithProxyEnv("/usr/local/bin/no-mistakes", p, "/home/u", got); reRendered != unit {
		t.Fatalf("re-rendering parsed proxy env did not reproduce the unit:\nwant:\n%s\ngot:\n%s", unit, reRendered)
	}
}

func TestLaunchAgentProxyEnvRoundTripsThroughRenderer(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	in := [][2]string{
		{"HTTPS_PROXY", "http://user:p&ss@127.0.0.1:7897"},
		{"NO_PROXY", "localhost,127.0.0.1"},
		{"http_proxy", "http://lower:1/"},
	}

	plist := renderLaunchAgentWithProxyEnv("/usr/local/bin/no-mistakes", p, "/home/u", in)

	got := launchAgentProxyEnv([]byte(plist))
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("launchAgentProxyEnv() = %v, want %v (HOME/PATH must be skipped)", got, in)
	}
	if reRendered := renderLaunchAgentWithProxyEnv("/usr/local/bin/no-mistakes", p, "/home/u", got); reRendered != plist {
		t.Fatalf("re-rendering parsed proxy env did not reproduce the plist:\nwant:\n%s\ngot:\n%s", plist, reRendered)
	}
}

func TestServiceProxyEnvParsersReturnNilForNoProxyDefinition(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	unit := renderSystemdUnitWithProxyEnv("/usr/local/bin/no-mistakes", p, "/home/u", nil)
	if got := systemdUnitProxyEnv([]byte(unit)); len(got) != 0 {
		t.Fatalf("systemdUnitProxyEnv(no-proxy unit) = %v, want empty", got)
	}
	plist := renderLaunchAgentWithProxyEnv("/usr/local/bin/no-mistakes", p, "/home/u", nil)
	if got := launchAgentProxyEnv([]byte(plist)); len(got) != 0 {
		t.Fatalf("launchAgentProxyEnv(no-proxy plist) = %v, want empty", got)
	}
	if got := systemdUnitProxyEnv(nil); len(got) != 0 {
		t.Fatalf("systemdUnitProxyEnv(nil) = %v, want empty", got)
	}
	if got := launchAgentProxyEnv([]byte("not a plist")); len(got) != 0 {
		t.Fatalf("launchAgentProxyEnv(garbage) = %v, want empty", got)
	}
}
