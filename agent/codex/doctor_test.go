package codex

import "testing"

func TestAgentDoctorInfoUsesConfiguredCommand(t *testing.T) {
	agent := &Agent{cmd: "C:/Users/example/AppData/Local/OpenAI/Codex/bin/hash/codex.exe"}

	if got := agent.CLIBinaryName(); got != agent.cmd {
		t.Fatalf("CLIBinaryName() = %q, want %q", got, agent.cmd)
	}
	if got := agent.CLIDisplayName(); got != "Codex" {
		t.Fatalf("CLIDisplayName() = %q, want Codex", got)
	}
}
