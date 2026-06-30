package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YingSuiAI/direxio-connect/core"
)

func TestConfiguredModels_BoundaryConditions(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "first"}}},
			{Models: []core.ModelOption{{Name: "second"}}},
		},
	}

	tests := []struct {
		name      string
		activeIdx int
		wantNil   bool
		wantName  string
	}{
		{name: "negative index", activeIdx: -1, wantNil: true},
		{name: "out of range", activeIdx: 2, wantNil: true},
		{name: "valid index", activeIdx: 1, wantName: "second"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.activeIdx = tt.activeIdx
			got := a.configuredModels()
			if tt.wantNil {
				if got != nil {
					t.Fatalf("configuredModels() = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 || got[0].Name != tt.wantName {
				t.Fatalf("configuredModels() = %v, want %q", got, tt.wantName)
			}
		})
	}
}

func TestGetModel_PrefersActiveProviderModel(t *testing.T) {
	a := &Agent{
		model: "gpt-4.1-mini",
		providers: []core.ProviderConfig{
			{Name: "openai", Model: "gpt-5.4"},
		},
		activeIdx: 0,
	}

	if got := a.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() = %q, want gpt-5.4", got)
	}
}

func TestNormalizeAppServerURL_StdIOIsExplicit(t *testing.T) {
	for _, raw := range []string{"stdio", " stdio "} {
		if got := normalizeAppServerURL(raw); got != "stdio://" {
			t.Fatalf("normalizeAppServerURL(%q) = %q, want stdio://", raw, got)
		}
	}
}

func TestNormalizeAppServerURL_EmptyKeepsWebSocketDefault(t *testing.T) {
	if got := normalizeAppServerURL(""); got != "ws://127.0.0.1:3845" {
		t.Fatalf("normalizeAppServerURL(empty) = %q, want ws://127.0.0.1:3845", got)
	}
}

func TestWorkspaceAgentOptions_PreservesStdIOAppServerURL(t *testing.T) {
	a := &Agent{
		backend:      "app_server",
		appServerURL: normalizeAppServerURL("stdio"),
	}

	opts := a.WorkspaceAgentOptions()
	if got := opts["app_server_url"]; got != "stdio://" {
		t.Fatalf("WorkspaceAgentOptions()[app_server_url] = %#v, want stdio://", got)
	}
}

func TestNew_PrefersDirexioCodexCommandEnv(t *testing.T) {
	t.Setenv("DIREXIO_CODEX_COMMAND", "go")

	agent, err := New(map[string]any{
		"work_dir": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	codexAgent := agent.(*Agent)
	if got := codexAgent.cmd; got != "go" {
		t.Fatalf("New().cmd = %q, want go", got)
	}
}

func TestWindowsLocalAppDataCodexCommand_PicksNewestBinary(t *testing.T) {
	root := filepath.Join(t.TempDir(), "OpenAI", "Codex", "bin")
	oldPath := filepath.Join(root, "old", "codex.exe")
	newPath := filepath.Join(root, "new", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte("old"), 0755); err != nil {
		t.Fatalf("write old codex: %v", err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0755); err != nil {
		t.Fatalf("write new codex: %v", err)
	}
	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}
	t.Setenv("LOCALAPPDATA", filepath.Dir(filepath.Dir(filepath.Dir(root))))

	if got := windowsLocalAppDataCodexCommand(); got != newPath {
		t.Fatalf("windowsLocalAppDataCodexCommand() = %q, want %q", got, newPath)
	}
}

func TestWorkspaceAgentOptions_PreservesResolvedCmd(t *testing.T) {
	a := &Agent{
		mode:         "auto-edit",
		backend:      "exec",
		cmd:          filepath.Join("C:", "Users", "me", "AppData", "Local", "OpenAI", "Codex", "bin", "hash", "codex.exe"),
		cliExtraArgs: []string{"exec"},
	}

	opts := a.WorkspaceAgentOptions()
	if got := opts["cmd"]; got != a.cmd+" exec" {
		t.Fatalf("WorkspaceAgentOptions()[cmd] = %#v, want %q", got, a.cmd+" exec")
	}
}
