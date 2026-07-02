package core

import (
	"context"
	"testing"
)

type doctorTestAgent struct {
	name string
}

func (a doctorTestAgent) Name() string { return a.name }
func (a doctorTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	return nil, nil
}
func (a doctorTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a doctorTestAgent) Stop() error { return nil }

func TestCheckDependenciesOmitsSQLiteForCodex(t *testing.T) {
	results := checkDependencies(doctorTestAgent{name: "codex"})
	for _, result := range results {
		if result.Name == "SQLite3" {
			t.Fatalf("checkDependencies included SQLite3 for codex: %#v", result)
		}
	}
}

func TestCheckDependenciesIncludesSQLiteForCursorAndOpenCode(t *testing.T) {
	for _, name := range []string{"cursor", "opencode"} {
		t.Run(name, func(t *testing.T) {
			results := checkDependencies(doctorTestAgent{name: name})
			if !hasDoctorResult(results, "SQLite3") {
				t.Fatalf("checkDependencies(%q) did not include SQLite3: %#v", name, results)
			}
		})
	}
}

func TestCheckRuntimePathsUsesConfiguredDataDir(t *testing.T) {
	dataDir := t.TempDir()

	results := checkRuntimePaths(dataDir)
	for _, result := range results {
		if result.Name == "Data Directory" {
			if result.Status != DoctorPass {
				t.Fatalf("Data Directory status = %v, want pass; detail=%q", result.Status, result.Detail)
			}
			if result.Detail != dataDir {
				t.Fatalf("Data Directory detail = %q, want %q", result.Detail, dataDir)
			}
			return
		}
	}
	t.Fatalf("Data Directory result not found: %#v", results)
}

func hasDoctorResult(results []DoctorCheckResult, name string) bool {
	for _, result := range results {
		if result.Name == name {
			return true
		}
	}
	return false
}
