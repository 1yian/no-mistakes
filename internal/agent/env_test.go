package agent

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestGitSafeEnvAppliesRunOverlayBeforeGateEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient-token")
	env := gitSafeEnvWithOverlay("/work/dir", runenv.Overlay{
		Set:   map[string]string{"GH_CONFIG_DIR": "/profiles/personal"},
		Unset: []string{"GH_TOKEN"},
	})
	resolved := resolveAgentEnv(env)

	if _, ok := resolved["GH_TOKEN"]; ok {
		t.Fatal("GH_TOKEN remained in agent environment")
	}
	if got := resolved["GH_CONFIG_DIR"]; got != "/profiles/personal" {
		t.Fatalf("GH_CONFIG_DIR = %q, want /profiles/personal", got)
	}
	if got := resolved[GateRoleEnvVar]; got != "1" {
		t.Fatalf("%s = %q, want 1", GateRoleEnvVar, got)
	}
	if got := resolved[CompactAdviserDisableEnvVar]; got != "1" {
		t.Fatalf("%s = %q, want 1", CompactAdviserDisableEnvVar, got)
	}
}

func resolveAgentEnv(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	return m
}

func TestGitSafeEnv_DisablesInteractiveGit(t *testing.T) {
	t.Setenv("GIT_EDITOR", "vim")

	resolved := resolveAgentEnv(gitSafeEnv("/work/dir"))

	if resolved["GIT_EDITOR"] != "true" {
		t.Errorf("GIT_EDITOR = %q, want \"true\"", resolved["GIT_EDITOR"])
	}
	if resolved["GIT_SEQUENCE_EDITOR"] != "true" {
		t.Errorf("GIT_SEQUENCE_EDITOR = %q, want \"true\"", resolved["GIT_SEQUENCE_EDITOR"])
	}
	if resolved["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want \"0\"", resolved["GIT_TERMINAL_PROMPT"])
	}
}

func TestGitSafeEnv_CouplesPWDToWorkdir(t *testing.T) {
	t.Setenv("PWD", "/somewhere/else")

	resolved := resolveAgentEnv(gitSafeEnv("/work/dir"))

	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		if resolved["PWD"] != "/somewhere/else" {
			t.Errorf("PWD = %q, want ambient PWD on %s", resolved["PWD"], runtime.GOOS)
		}
		return
	}

	if resolved["PWD"] != "/work/dir" {
		t.Errorf("PWD = %q, want \"/work/dir\"", resolved["PWD"])
	}
}

func TestGitSafeEnv_StampsGateRoleMarker(t *testing.T) {
	resolved := resolveAgentEnv(gitSafeEnv("/work/dir"))
	if resolved[GateRoleEnvVar] != "1" {
		t.Errorf("%s = %q, want \"1\"", GateRoleEnvVar, resolved[GateRoleEnvVar])
	}
	if resolved[CompactAdviserDisableEnvVar] != "1" {
		t.Errorf("%s = %q, want \"1\"", CompactAdviserDisableEnvVar, resolved[CompactAdviserDisableEnvVar])
	}
}

func TestGitSafeEnvIsObservedBySpawnedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentEnvProbe$")
	cmd.Env = gitSafeEnv(t.TempDir(), []string{
		"NM_HOME=/isolated/eval",
		GateRoleEnvVar + "=0",
		CompactAdviserDisableEnvVar + "=0",
		"NM_TEST_ENV_PROBE=1",
	})
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run environment probe: %v", err)
	}
	if got := string(output); !strings.HasPrefix(got, "/isolated/eval|1|1") {
		t.Fatalf("spawned environment = %q, want isolated home, gate marker, and compact-adviser disable", got)
	}
}

func TestEverySupportedAdapterCarriesTheRunOverlayAndGateMarker(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient-must-not-leak")
	t.Setenv(GateRoleEnvVar, "0")
	t.Setenv(CompactAdviserDisableEnvVar, "0")

	overlay := runenv.Overlay{
		Set:   map[string]string{"GH_CONFIG_DIR": "/profiles/personal"},
		Unset: []string{"GH_TOKEN"},
	}

	for _, name := range []types.AgentName{
		types.AgentClaude,
		types.AgentCodex,
		types.AgentGrok,
		types.AgentRovoDev,
		types.AgentOpenCode,
		types.AgentPi,
		types.AgentCopilot,
		types.AgentCursor,
		types.AgentDevin,
		types.AgentAntigravity,
		types.AgentName("acp:some-target"),
	} {
		t.Run(string(name), func(t *testing.T) {
			created, err := NewWithOptions(name, "fake-bin", nil, Options{Environment: overlay})
			if err != nil {
				t.Fatalf("NewWithOptions(%s): %v", name, err)
			}
			scoped, ok := created.(interface {
				gitSafeEnv(dir string, extra ...[]string) []string
				overlay() runenv.Overlay
			})
			if !ok {
				t.Fatalf("%s (%T) does not carry the run-scoped subprocess context", name, created)
			}

			resolved := resolveAgentEnv(scoped.gitSafeEnv("/work/dir"))
			if got := resolved["GH_CONFIG_DIR"]; got != "/profiles/personal" {
				t.Errorf("GH_CONFIG_DIR = %q, want /profiles/personal", got)
			}
			if _, leaked := resolved["GH_TOKEN"]; leaked {
				t.Error("ambient GH_TOKEN survived into the agent environment")
			}
			if got := resolved[GateRoleEnvVar]; got != "1" {
				t.Errorf("%s = %q, want \"1\"", GateRoleEnvVar, got)
			}
			if got := resolved[CompactAdviserDisableEnvVar]; got != "1" {
				t.Errorf("%s = %q, want \"1\"", CompactAdviserDisableEnvVar, got)
			}

			// The managed-server route reaches its child through this overlay;
			// TestStartServerWithPortAppliesForgeEnvironment spawns the process
			// that observes it.
			handed := scoped.overlay()
			if got := handed.Set["GH_CONFIG_DIR"]; got != "/profiles/personal" {
				t.Errorf("overlay handed to the managed-server launcher set GH_CONFIG_DIR=%q", got)
			}
			if len(handed.Unset) != 1 || handed.Unset[0] != "GH_TOKEN" {
				t.Errorf("overlay handed to the managed-server launcher unsets %v, want [GH_TOKEN]", handed.Unset)
			}
		})
	}
}

func TestGitSafeEnvIsTheEmptyOverlayCase(t *testing.T) {
	extra := []string{"NM_HOME=/isolated/eval"}
	plain := gitSafeEnv("/work/dir", extra)
	overlaid := gitSafeEnvWithOverlay("/work/dir", runenv.Overlay{}, extra)

	if !slices.Equal(plain, overlaid) {
		t.Fatalf("gitSafeEnv = %v, want the empty-overlay result %v", plain, overlaid)
	}
	resolved := resolveAgentEnv(plain)
	if resolved[GateRoleEnvVar] != "1" {
		t.Errorf("%s = %q, want \"1\"", GateRoleEnvVar, resolved[GateRoleEnvVar])
	}
	if resolved[CompactAdviserDisableEnvVar] != "1" {
		t.Errorf("%s = %q, want \"1\"", CompactAdviserDisableEnvVar, resolved[CompactAdviserDisableEnvVar])
	}
	if resolved["NM_HOME"] != "/isolated/eval" {
		t.Errorf("NM_HOME = %q, want /isolated/eval", resolved["NM_HOME"])
	}
}

func TestAgentEnvProbe(t *testing.T) {
	if os.Getenv("NM_TEST_ENV_PROBE") != "1" {
		return
	}
	fmt.Printf("%s|%s|%s", os.Getenv("NM_HOME"), os.Getenv(GateRoleEnvVar), os.Getenv(CompactAdviserDisableEnvVar))
}

func TestGitSafeEnv_GateMarkerWinsOverAmbient(t *testing.T) {
	t.Setenv(GateRoleEnvVar, "0")
	resolved := resolveAgentEnv(gitSafeEnv("/work/dir"))
	if resolved[GateRoleEnvVar] != "1" {
		t.Errorf("%s = %q, want \"1\" (managed stamp must win over ambient)", GateRoleEnvVar, resolved[GateRoleEnvVar])
	}
}

func TestGitSafeEnv_CompactAdviserDisableWinsOverOverlay(t *testing.T) {
	t.Setenv(CompactAdviserDisableEnvVar, "0")
	resolved := resolveAgentEnv(gitSafeEnvWithOverlay("/work/dir", runenv.Overlay{
		Set:   map[string]string{CompactAdviserDisableEnvVar: "false"},
		Unset: []string{CompactAdviserDisableEnvVar},
	}, []string{CompactAdviserDisableEnvVar + "=yes"}))
	if resolved[CompactAdviserDisableEnvVar] != "1" {
		t.Errorf("%s = %q, want \"1\" (managed stamp must win over overlay and extra)", CompactAdviserDisableEnvVar, resolved[CompactAdviserDisableEnvVar])
	}
}
