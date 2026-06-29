//go:build windows

package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	windowsScriptName   = "cc-connect-daemon.ps1"
	windowsLauncherName = "cc-connect-daemon.vbs"
)

var runPowerShell = func(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", strictPowerShell(script))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func strictPowerShell(script string) string {
	return "$ErrorActionPreference = 'Stop'\n" + script
}

type schtasksManager struct {
	serviceName string
}

func newPlatformManager(serviceName string) (Manager, error) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return nil, fmt.Errorf("powershell.exe not found: Windows Task Scheduler management requires PowerShell")
	}
	return &schtasksManager{serviceName: mustNormalizeServiceName(serviceName)}, nil
}

func (*schtasksManager) Platform() string { return "schtasks" }

func (m *schtasksManager) Install(cfg Config) error {
	cfg.ServiceName = m.normalizedServiceName()
	if err := os.MkdirAll(DefaultDataDir(), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	scriptPath := windowsTaskScriptPath(cfg.ServiceName)
	launcherPath := windowsTaskLauncherPath(cfg.ServiceName)
	// 0644 has weak semantics on Windows; the file ACL is what matters.
	// We still write 0600 so the file's POSIX bits do not advertise read
	// access, and rely on the user's own profile ACLs for primary defense
	// (the script lives under %USERPROFILE%\.cc-connect by default).
	// WriteFile only applies perm on create, so Chmod the existing file
	// after writing to harden reinstalls of pre-existing 0644 scripts.
	if err := os.WriteFile(scriptPath, []byte(buildWindowsTaskScript(cfg)), 0600); err != nil {
		return fmt.Errorf("write task script: %w", err)
	}
	if err := os.Chmod(scriptPath, 0600); err != nil {
		return fmt.Errorf("chmod task script: %w", err)
	}
	if err := os.WriteFile(launcherPath, []byte(buildWindowsTaskLauncher(cfg)), 0600); err != nil {
		return fmt.Errorf("write task launcher: %w", err)
	}
	if err := os.Chmod(launcherPath, 0600); err != nil {
		return fmt.Errorf("chmod task launcher: %w", err)
	}

	if err := stopWindowsTask(cfg.ServiceName); err != nil {
		slog.Warn("schtasks: stop existing task failed", "error", err)
	}
	if err := stopWindowsWrapperProcesses(cfg.ServiceName); err != nil {
		slog.Warn("schtasks: stop existing wrapper processes failed", "error", err)
	}
	if err := stopWindowsChildProcess(cfg.ServiceName); err != nil {
		slog.Warn("schtasks: stop existing child process failed", "error", err)
	}
	if err := deleteWindowsTask(cfg.ServiceName); err != nil {
		if windowsTaskMatchesAction(launcherPath, cfg.ServiceName) {
			if err := m.Start(); err != nil {
				return fmt.Errorf("start existing task: %w", err)
			}
			return nil
		}
		return err
	}

	if err := createWindowsTask(launcherPath, cfg.ServiceName); err != nil {
		return err
	}

	if err := m.Start(); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	return nil
}

func (m *schtasksManager) Uninstall() error {
	if err := stopWindowsTask(m.normalizedServiceName()); err != nil {
		slog.Warn("schtasks: stop task failed", "error", err)
	}
	if err := stopWindowsWrapperProcesses(m.normalizedServiceName()); err != nil {
		slog.Warn("schtasks: stop wrapper processes failed", "error", err)
	}
	if err := stopWindowsChildProcess(m.normalizedServiceName()); err != nil {
		slog.Warn("schtasks: stop child process failed", "error", err)
	}
	if err := deleteWindowsTask(m.normalizedServiceName()); err != nil {
		return err
	}
	if err := os.Remove(windowsTaskScriptPath(m.normalizedServiceName())); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove task script: %w", err)
	}
	if err := os.Remove(windowsTaskLauncherPath(m.normalizedServiceName())); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove task launcher: %w", err)
	}
	return nil
}

func (m *schtasksManager) Start() error {
	return startWindowsTask(m.normalizedServiceName())
}

func (m *schtasksManager) Stop() error {
	if err := stopWindowsTask(m.normalizedServiceName()); err != nil {
		return err
	}
	if err := stopWindowsWrapperProcesses(m.normalizedServiceName()); err != nil {
		return err
	}
	if err := stopWindowsChildProcess(m.normalizedServiceName()); err != nil {
		return err
	}
	return nil
}

func (m *schtasksManager) Restart() error {
	if err := stopWindowsTask(m.normalizedServiceName()); err != nil {
		slog.Warn("schtasks: stop before restart failed", "error", err)
	}
	return startWindowsTask(m.normalizedServiceName())
}

func (m *schtasksManager) Status() (*Status, error) {
	serviceName := m.normalizedServiceName()
	st := &Status{Platform: "schtasks", Service: serviceName}

	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 1 }
Write-Output $task.State
`, powerShellLiteral(windowsTaskNameForService(serviceName))))
	if err != nil {
		return st, nil
	}
	st.Installed = true

	taskStatus := strings.TrimSpace(out)
	if strings.EqualFold(taskStatus, "Running") {
		st.Running = true
	}
	return st, nil
}

func (m *schtasksManager) normalizedServiceName() string {
	return mustNormalizeServiceName(m.serviceName)
}

func windowsServiceName(serviceNames ...string) string {
	if len(serviceNames) == 0 {
		return ServiceName
	}
	return mustNormalizeServiceName(serviceNames[0])
}

func windowsTaskNameForService(serviceName string) string {
	normalized := mustNormalizeServiceName(serviceName)
	if normalized == ServiceName {
		return ServiceName
	}
	return ServiceName + "-" + normalized
}

func windowsTaskScriptPath(serviceNames ...string) string {
	serviceName := windowsServiceName(serviceNames...)
	scriptName := windowsScriptName
	if serviceName != ServiceName {
		scriptName = "cc-connect-daemon-" + serviceName + ".ps1"
	}
	return filepath.Join(DefaultDataDir(), scriptName)
}

func windowsTaskLauncherPath(serviceNames ...string) string {
	serviceName := windowsServiceName(serviceNames...)
	launcherName := windowsLauncherName
	if serviceName != ServiceName {
		launcherName = "cc-connect-daemon-" + serviceName + ".vbs"
	}
	return filepath.Join(DefaultDataDir(), launcherName)
}

func windowsTaskAction(launcherPath string) string {
	return fmt.Sprintf(`wscript.exe %s`, windowsTaskActionArgs(launcherPath))
}

func windowsTaskActionArgs(launcherPath string) string {
	return fmt.Sprintf(`//B //Nologo "%s"`, launcherPath)
}

func createWindowsTask(launcherPath string, serviceNames ...string) error {
	serviceName := windowsServiceName(serviceNames...)
	out, err := runPowerShell(fmt.Sprintf(`
$action = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument %s
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName %s -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
`, powerShellLiteral(windowsTaskActionArgs(launcherPath)), powerShellLiteral(windowsTaskNameForService(serviceName))))
	if err != nil {
		return fmt.Errorf("register scheduled task: %s (%w)", out, err)
	}
	return nil
}

func windowsTaskMatchesAction(launcherPath string, serviceNames ...string) bool {
	serviceName := windowsServiceName(serviceNames...)
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 1 }
$expectedArgs = %s
foreach ($action in $task.Actions) {
	if (($action.Execute -ieq 'wscript.exe') -and ($action.Arguments -eq $expectedArgs)) {
		Write-Output 'true'
		exit 0
	}
}
exit 1
`, powerShellLiteral(windowsTaskNameForService(serviceName)), powerShellLiteral(windowsTaskActionArgs(launcherPath))))
	return err == nil && strings.EqualFold(strings.TrimSpace(out), "true")
}

func buildWindowsTaskLauncher(cfg Config) string {
	var sb strings.Builder
	sb.WriteString("Option Explicit\r\n")
	sb.WriteString("Dim shell, env, exitCode\r\n")
	sb.WriteString("Set shell = CreateObject(\"WScript.Shell\")\r\n")
	sb.WriteString("Set env = shell.Environment(\"PROCESS\")\r\n")
	writeVBScriptEnv(&sb, "CC_LOG_FILE", cfg.LogFile)
	writeVBScriptEnv(&sb, "CC_LOG_MAX_SIZE", strconv.FormatInt(cfg.LogMaxSize, 10))
	writeVBScriptEnv(&sb, "CC_LOG_MAX_BACKUPS", strconv.Itoa(cfg.LogMaxBackups))
	writeVBScriptEnv(&sb, "CC_PID_FILE", cfg.LogFile+".pid")
	if cfg.EnvPATH != "" {
		writeVBScriptEnv(&sb, "PATH", cfg.EnvPATH)
	}
	if len(cfg.EnvExtra) > 0 {
		keys := make([]string, 0, len(cfg.EnvExtra))
		for key := range cfg.EnvExtra {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !isValidEnvName(key) {
				slog.Warn("daemon: windows: dropping invalid env name from EnvExtra",
					"key", key)
				continue
			}
			value := cfg.EnvExtra[key]
			if value == "" {
				continue
			}
			writeVBScriptEnv(&sb, key, value)
		}
	}
	fmt.Fprintf(&sb, "shell.CurrentDirectory = %s\r\n", vbScriptStringLiteral(cfg.WorkDir))
	innerCommand := windowsCommandLineArg(cfg.BinaryPath) + " --force"
	command := `cmd.exe /d /q /s /c "` + innerCommand + `"`
	sb.WriteString("Do\r\n")
	fmt.Fprintf(&sb, "  exitCode = shell.Run(%s, 0, True)\r\n", vbScriptStringLiteral(command))
	sb.WriteString("  If exitCode = 0 Then WScript.Quit 0\r\n")
	sb.WriteString("  WScript.Sleep 10000\r\n")
	sb.WriteString("Loop\r\n")
	return sb.String()
}

func buildWindowsTaskScript(cfg Config) string {
	var sb strings.Builder
	sb.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	writePowerShellEnv(&sb, "CC_LOG_FILE", cfg.LogFile)
	writePowerShellEnv(&sb, "CC_LOG_MAX_SIZE", strconv.FormatInt(cfg.LogMaxSize, 10))
	writePowerShellEnv(&sb, "CC_LOG_MAX_BACKUPS", strconv.Itoa(cfg.LogMaxBackups))
	if cfg.EnvPATH != "" {
		writePowerShellEnv(&sb, "PATH", cfg.EnvPATH)
	}
	if len(cfg.EnvExtra) > 0 {
		keys := make([]string, 0, len(cfg.EnvExtra))
		for key := range cfg.EnvExtra {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !isValidEnvName(key) {
				slog.Warn("daemon: windows: dropping invalid env name from EnvExtra",
					"key", key)
				continue
			}
			value := cfg.EnvExtra[key]
			if value == "" {
				continue
			}
			writePowerShellEnv(&sb, key, value)
		}
	}
	fmt.Fprintf(&sb, "Set-Location -LiteralPath %s\r\n", powerShellLiteral(cfg.WorkDir))
	fmt.Fprintf(&sb, "$binaryPath = %s\r\n", powerShellLiteral(cfg.BinaryPath))
	sb.WriteString("$pidPath = \"$env:CC_LOG_FILE.pid\"\r\n")
	sb.WriteString("while ($true) {\r\n")
	sb.WriteString("  $process = $null\r\n")
	sb.WriteString("  try {\r\n")
	sb.WriteString("    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()\r\n")
	sb.WriteString("    $startInfo.FileName = $binaryPath\r\n")
	sb.WriteString("    $startInfo.Arguments = '--force'\r\n")
	sb.WriteString("    $startInfo.WorkingDirectory = (Get-Location).Path\r\n")
	sb.WriteString("    $startInfo.UseShellExecute = $false\r\n")
	sb.WriteString("    $startInfo.CreateNoWindow = $true\r\n")
	sb.WriteString("    $startInfo.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden\r\n")
	sb.WriteString("    $process = [System.Diagnostics.Process]::new()\r\n")
	sb.WriteString("    $process.StartInfo = $startInfo\r\n")
	sb.WriteString("    if (-not $process.Start()) { exit 1 }\r\n")
	sb.WriteString("    Set-Content -LiteralPath $pidPath -Value ([string]$process.Id) -Encoding ASCII\r\n")
	sb.WriteString("    $process.WaitForExit()\r\n")
	sb.WriteString("    $exitCode = $process.ExitCode\r\n")
	sb.WriteString("  } finally {\r\n")
	sb.WriteString("    Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue\r\n")
	sb.WriteString("    if ($null -ne $process) { $process.Dispose() }\r\n")
	sb.WriteString("  }\r\n")
	sb.WriteString("  if ($exitCode -eq 0) { exit 0 }\r\n")
	sb.WriteString("  Start-Sleep -Seconds 10\r\n")
	sb.WriteString("}\r\n")
	return sb.String()
}

func writePowerShellEnv(sb *strings.Builder, key, value string) {
	fmt.Fprintf(sb, "$env:%s = %s\r\n", key, powerShellLiteral(value))
}

func writeVBScriptEnv(sb *strings.Builder, key, value string) {
	fmt.Fprintf(sb, "env(%s) = %s\r\n", vbScriptStringLiteral(key), vbScriptStringLiteral(value))
}

func powerShellLiteral(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func windowsCommandLineArg(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func vbScriptStringLiteral(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func stopWindowsTask(serviceNames ...string) error {
	serviceName := windowsServiceName(serviceNames...)
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 0 }
if ($task.State -eq 'Running') {
	Stop-ScheduledTask -TaskName %s
}
for ($i = 0; $i -lt 20; $i++) {
	$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
	if ($null -eq $task -or $task.State -ne 'Running') { exit 0 }
	Start-Sleep -Milliseconds 500
}
Write-Error 'scheduled task did not stop within timeout'
exit 1
`, powerShellLiteral(windowsTaskNameForService(serviceName)), powerShellLiteral(windowsTaskNameForService(serviceName)), powerShellLiteral(windowsTaskNameForService(serviceName))))
	if err != nil {
		return fmt.Errorf("stop scheduled task: %s (%w)", out, err)
	}
	return nil
}

func stopWindowsWrapperProcesses(serviceNames ...string) error {
	serviceName := windowsServiceName(serviceNames...)
	scriptPath := windowsTaskScriptPath(serviceName)
	launcherPath := windowsTaskLauncherPath(serviceName)
	out, err := runPowerShell(fmt.Sprintf(`
$paths = @(%s, %s)
$currentPid = $PID
Get-CimInstance Win32_Process | Where-Object {
	$_.ProcessId -ne $currentPid -and
	(($_.Name -ieq 'powershell.exe') -or ($_.Name -ieq 'wscript.exe')) -and
	$null -ne $_.CommandLine -and
	(
		([string]$_.CommandLine).IndexOf($paths[0], [StringComparison]::OrdinalIgnoreCase) -ge 0 -or
		([string]$_.CommandLine).IndexOf($paths[1], [StringComparison]::OrdinalIgnoreCase) -ge 0
	)
} | ForEach-Object {
	Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue
}
exit 0
`, powerShellLiteral(scriptPath), powerShellLiteral(launcherPath)))
	if err != nil {
		return fmt.Errorf("stop scheduled task wrapper processes: %s (%w)", out, err)
	}
	return nil
}

func stopWindowsChildProcess(serviceNames ...string) error {
	serviceName := windowsServiceName(serviceNames...)
	meta, err := LoadMetaForService(serviceName)
	if err != nil || meta == nil || strings.TrimSpace(meta.LogFile) == "" {
		return nil
	}
	pidPath := meta.LogFile + ".pid"
	out, err := runPowerShell(fmt.Sprintf(`
$pidPath = %s
if (-not (Test-Path -LiteralPath $pidPath)) { exit 0 }
$rawPid = (Get-Content -LiteralPath $pidPath -ErrorAction SilentlyContinue | Select-Object -First 1)
$pidText = ([string]$rawPid).Trim()
try {
	$pidValue = [int]$pidText
} catch {
	Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
	exit 0
}
$process = Get-Process -Id $pidValue -ErrorAction SilentlyContinue
if ($null -ne $process) {
	Stop-Process -Id $pidValue -Force -ErrorAction SilentlyContinue
	Wait-Process -Id $pidValue -Timeout 5 -ErrorAction SilentlyContinue
}
Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
exit 0
`, powerShellLiteral(pidPath)))
	if err != nil {
		return fmt.Errorf("stop scheduled task child process: %s (%w)", out, err)
	}
	return nil
}

func startWindowsTask(serviceNames ...string) error {
	serviceName := windowsServiceName(serviceNames...)
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { Write-Error 'scheduled task not found'; exit 1 }
if ($task.State -ne 'Running') { Start-ScheduledTask -TaskName %s }
`, powerShellLiteral(windowsTaskNameForService(serviceName)), powerShellLiteral(windowsTaskNameForService(serviceName))))
	if err != nil {
		return fmt.Errorf("start scheduled task: %s (%w)", out, err)
	}
	return nil
}

func deleteWindowsTask(serviceNames ...string) error {
	serviceName := windowsServiceName(serviceNames...)
	out, err := runPowerShell(fmt.Sprintf(`
$task = Get-ScheduledTask -TaskName %s -ErrorAction SilentlyContinue
if ($null -eq $task) { exit 0 }
Unregister-ScheduledTask -TaskName %s -Confirm:$false
`, powerShellLiteral(windowsTaskNameForService(serviceName)), powerShellLiteral(windowsTaskNameForService(serviceName))))
	if err != nil {
		return fmt.Errorf("delete scheduled task: %s (%w)", out, err)
	}
	return nil
}

// CheckLinger is a no-op on Windows (always returns false).
func CheckLinger() (enabled bool, user string) {
	return false, ""
}
