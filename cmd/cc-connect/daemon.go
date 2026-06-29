package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/YingSuiAI/connect/config"
	"github.com/YingSuiAI/connect/daemon"
)

func runDaemon(args []string) {
	if len(args) == 0 {
		printDaemonUsage()
		os.Exit(1)
	}

	command := args[0]
	serviceName, commandArgs, err := parseDaemonServiceName(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	switch command {
	case "install":
		daemonInstall(serviceName, commandArgs)
	case "uninstall":
		daemonUninstall(serviceName)
	case "start":
		daemonStart(serviceName)
	case "stop":
		daemonStop(serviceName)
	case "restart":
		daemonRestart(serviceName, commandArgs)
	case "status":
		daemonStatus(serviceName)
	case "logs":
		daemonLogs(serviceName, commandArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown daemon command: %s\n\n", command)
		printDaemonUsage()
		os.Exit(1)
	}
}

// ── install ─────────────────────────────────────────────────

func daemonInstall(serviceName string, args []string) {
	cfg, force, err := parseDaemonInstallArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if strings.TrimSpace(cfg.ServiceName) == "" {
		cfg.ServiceName = serviceName
	}

	if err := daemon.Resolve(&cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	configPath := cfg.WorkDir + "/config.toml"
	if _, err := os.Stat(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: config.toml not found in %s\n", cfg.WorkDir)
		fmt.Fprintf(os.Stderr, "  Use --work-dir to specify the config directory or --config to point to the config file\n")
		os.Exit(1)
	}

	mgr, err := daemon.NewManagerForService(cfg.ServiceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	st, _ := mgr.Status()
	if st != nil && st.Installed && !force {
		fmt.Fprintf(os.Stderr, "Service already installed. Use --force to reinstall.\n")
		os.Exit(1)
	}

	if err := mgr.Install(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Install failed: %v\n", err)
		os.Exit(1)
	}

	if err := daemon.SaveMetaForService(cfg.ServiceName, &daemon.Meta{
		ServiceName:   cfg.ServiceName,
		LogFile:       cfg.LogFile,
		LogMaxSize:    cfg.LogMaxSize,
		LogMaxBackups: cfg.LogMaxBackups,
		WorkDir:       cfg.WorkDir,
		BinaryPath:    cfg.BinaryPath,
		InstalledAt:   daemon.NowISO(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save metadata: %v\n", err)
	}

	fmt.Println("direxio-connect daemon installed and started.")
	fmt.Println()
	fmt.Printf("  Service:   %s\n", cfg.ServiceName)
	fmt.Printf("  Platform:  %s\n", mgr.Platform())
	fmt.Printf("  Binary:    %s\n", cfg.BinaryPath)
	fmt.Printf("  WorkDir:   %s\n", cfg.WorkDir)
	fmt.Printf("  Log:       %s\n", cfg.LogFile)
	fmt.Printf("  LogMax:    %d MB\n", cfg.LogMaxSize/1024/1024)
	fmt.Println()
	fmt.Println("Commands:")
	serviceFlag := daemonServiceFlag(cfg.ServiceName)
	fmt.Printf("  direxio-connect daemon status%s    - Check status\n", serviceFlag)
	fmt.Printf("  direxio-connect daemon logs%s -f   - Follow logs\n", serviceFlag)
	fmt.Printf("  direxio-connect daemon restart%s   - Restart\n", serviceFlag)
	fmt.Printf("  direxio-connect daemon stop%s      - Stop\n", serviceFlag)
	fmt.Printf("  direxio-connect daemon uninstall%s - Remove\n", serviceFlag)

	// Check linger for user-mode systemd
	if strings.Contains(mgr.Platform(), "user") {
		enabled, user := daemon.CheckLinger()
		if !enabled {
			fmt.Println()
			fmt.Println("⚠️  Warning: Linger is not enabled for this user.")
			fmt.Println("   direxio-connect will stop when your last login session ends (e.g., SSH disconnect).")
			fmt.Println("   To keep it running persistently, run:")
			fmt.Printf("     sudo loginctl enable-linger %s\n", user)
		}
	}
}

func parseDaemonInstallArgs(args []string) (daemon.Config, bool, error) {
	var cfg daemon.Config
	var force bool

	// Env-based opt-out: CC_DAEMON_NO_CAPTURE_SECRETS=1 / true / yes / on
	// triggers --no-capture-secrets without the CLI flag, for CI / container
	// scenarios where the global env is the right configuration surface.
	if isTruthyEnv(os.Getenv("CC_DAEMON_NO_CAPTURE_SECRETS")) {
		cfg.NoCaptureSecrets = true
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--force":
			force = true
		case arg == "--service-name" || arg == "--name":
			value, next, err := daemonInstallFlagValue(args, i, arg)
			if err != nil {
				return daemon.Config{}, false, err
			}
			cfg.ServiceName = value
			i = next
		case strings.HasPrefix(arg, "--service-name="):
			cfg.ServiceName = strings.TrimPrefix(arg, "--service-name=")
		case strings.HasPrefix(arg, "--name="):
			cfg.ServiceName = strings.TrimPrefix(arg, "--name=")
		case arg == "--no-capture-secrets":
			cfg.NoCaptureSecrets = true
		case arg == "--log-file":
			value, next, err := daemonInstallFlagValue(args, i, "--log-file")
			if err != nil {
				return daemon.Config{}, false, err
			}
			cfg.LogFile = value
			i = next
		case strings.HasPrefix(arg, "--log-file="):
			cfg.LogFile = strings.TrimPrefix(arg, "--log-file=")
		case arg == "--log-max-size":
			value, next, err := daemonInstallFlagValue(args, i, "--log-max-size")
			if err != nil {
				return daemon.Config{}, false, err
			}
			mb, err := strconv.Atoi(value)
			if err != nil {
				return daemon.Config{}, false, fmt.Errorf("invalid value for --log-max-size: %s", value)
			}
			cfg.LogMaxSize = int64(mb) * 1024 * 1024
			i = next
		case strings.HasPrefix(arg, "--log-max-size="):
			value := strings.TrimPrefix(arg, "--log-max-size=")
			mb, err := strconv.Atoi(value)
			if err != nil {
				return daemon.Config{}, false, fmt.Errorf("invalid value for --log-max-size: %s", value)
			}
			cfg.LogMaxSize = int64(mb) * 1024 * 1024
		case arg == "--work-dir":
			value, next, err := daemonInstallFlagValue(args, i, "--work-dir")
			if err != nil {
				return daemon.Config{}, false, err
			}
			cfg.WorkDir = value
			i = next
		case strings.HasPrefix(arg, "--work-dir="):
			cfg.WorkDir = strings.TrimPrefix(arg, "--work-dir=")
		case arg == "--config" || arg == "-config":
			value, next, err := daemonInstallFlagValue(args, i, arg)
			if err != nil {
				return daemon.Config{}, false, err
			}
			cfg.WorkDir = filepath.Dir(value)
			i = next
		case strings.HasPrefix(arg, "--config="):
			cfg.WorkDir = filepath.Dir(strings.TrimPrefix(arg, "--config="))
		case strings.HasPrefix(arg, "-config="):
			cfg.WorkDir = filepath.Dir(strings.TrimPrefix(arg, "-config="))
		default:
			return daemon.Config{}, false, fmt.Errorf("unknown flag: %s", arg)
		}
	}

	return cfg, force, nil
}

func parseDaemonServiceName(args []string) (string, []string, error) {
	serviceName := os.Getenv("DIREXIO_CONNECT_SERVICE_NAME")
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--service-name" || arg == "--name":
			value, next, err := daemonInstallFlagValue(args, i, arg)
			if err != nil {
				return "", nil, err
			}
			serviceName = value
			i = next
		case strings.HasPrefix(arg, "--service-name="):
			serviceName = strings.TrimPrefix(arg, "--service-name=")
		case strings.HasPrefix(arg, "--name="):
			serviceName = strings.TrimPrefix(arg, "--name=")
		default:
			rest = append(rest, arg)
		}
	}
	normalized, err := daemon.NormalizeServiceName(serviceName)
	if err != nil {
		return "", nil, err
	}
	return normalized, rest, nil
}

func daemonServiceFlag(serviceName string) string {
	normalized, err := daemon.NormalizeServiceName(serviceName)
	if err != nil || normalized == daemon.ServiceName {
		return ""
	}
	return " --service-name " + normalized
}

func daemonInstallFlagValue(args []string, index int, flagName string) (string, int, error) {
	next := index + 1
	if next >= len(args) {
		return "", index, fmt.Errorf("missing value for %s", flagName)
	}
	return args[next], next, nil
}

// isTruthyEnv accepts the conventional opt-in values for boolean env vars.
// Anything else, including "0" / "false" / "" / unset, is treated as false.
func isTruthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ── uninstall ───────────────────────────────────────────────

func daemonUninstall(serviceName string) {
	mgr, err := daemon.NewManagerForService(serviceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	st, _ := mgr.Status()
	if st != nil && !st.Installed {
		fmt.Println("Service is not installed.")
		return
	}

	if err := mgr.Uninstall(); err != nil {
		fmt.Fprintf(os.Stderr, "Uninstall failed: %v\n", err)
		os.Exit(1)
	}

	daemon.RemoveMetaForService(serviceName)
	fmt.Println("direxio-connect daemon uninstalled.")
}

// ── start / stop / restart ──────────────────────────────────

func daemonStart(serviceName string) {
	mgr := mustManager(serviceName)
	requireInstalled(mgr)
	if err := mgr.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Start failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("direxio-connect daemon started.")
}

func daemonStop(serviceName string) {
	mgr := mustManager(serviceName)
	requireInstalled(mgr)
	if dataDir, err := daemonDataDirFromMeta(serviceName); err == nil {
		if err := postLocalShutdown(dataDir); err == nil {
			time.Sleep(2 * time.Second)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: graceful daemon shutdown failed, falling back to service stop: %v\n", err)
		}
	}
	if err := mgr.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "Stop failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("direxio-connect daemon stopped.")
}

func daemonDataDirFromMeta(serviceName string) (string, error) {
	meta, err := daemon.LoadMetaForService(serviceName)
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(filepath.Join(meta.WorkDir, "config.toml"))
	if err != nil {
		return "", err
	}
	return cfg.DataDir, nil
}

func postLocalShutdown(dataDir string) error {
	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); err != nil {
		return fmt.Errorf("shutdown socket unavailable: %w", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
		Timeout: 3 * time.Second,
	}

	resp, err := client.Post("http://unix/shutdown", "application/json", bytes.NewReader(nil))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("shutdown returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func daemonRestart(serviceName string, args []string) {
	force := false
	for _, a := range args {
		if a == "--force" {
			force = true
		}
	}

	mgr := mustManager(serviceName)
	requireInstalled(mgr)

	if force {
		if meta, err := daemon.LoadMetaForService(serviceName); err == nil {
			configPath := meta.WorkDir + "/config.toml"
			KillExistingInstance(configPath)
		}
	}

	if err := mgr.Restart(); err != nil {
		fmt.Fprintf(os.Stderr, "Restart failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("direxio-connect daemon restarted.")
}

func requireInstalled(mgr daemon.Manager) {
	st, _ := mgr.Status()
	if st == nil || !st.Installed {
		serviceName := daemon.ServiceName
		if st != nil && st.Service != "" {
			serviceName = st.Service
		}
		fmt.Fprintln(os.Stderr, "Service is not installed. Run first:")
		fmt.Fprintf(os.Stderr, "  direxio-connect daemon install%s --work-dir /path/to/config-dir\n", daemonServiceFlag(serviceName))
		os.Exit(1)
	}
}

// ── status ──────────────────────────────────────────────────

func daemonStatus(serviceName string) {
	mgr := mustManager(serviceName)
	st, err := mgr.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("direxio-connect daemon status")
	fmt.Println()

	if !st.Installed {
		fmt.Println("  Status:    Not installed")
		fmt.Printf("  Service:   %s\n", serviceName)
		fmt.Printf("  Platform:  %s\n", st.Platform)
		fmt.Println()
		fmt.Printf("  Run: direxio-connect daemon install%s\n", daemonServiceFlag(serviceName))
		return
	}

	statusStr := "Stopped"
	if st.Running {
		statusStr = "Running"
	}
	fmt.Printf("  Status:    %s\n", statusStr)
	fmt.Printf("  Service:   %s\n", serviceName)
	fmt.Printf("  Platform:  %s\n", st.Platform)
	if st.PID > 0 {
		fmt.Printf("  PID:       %d\n", st.PID)
	}

	if meta, err := daemon.LoadMetaForService(serviceName); err == nil {
		fmt.Printf("  Log:       %s\n", meta.LogFile)
		fmt.Printf("  WorkDir:   %s\n", meta.WorkDir)
		if t, err := time.Parse(time.RFC3339, meta.InstalledAt); err == nil {
			fmt.Printf("  Installed: %s\n", t.Format("2006-01-02 15:04:05"))
		}
	}
}

// ── logs ────────────────────────────────────────────────────

func daemonLogs(serviceName string, args []string) {
	follow := false
	lines := 100
	logFile := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--follow":
			follow = true
		case "-n":
			i++
			if i < len(args) {
				if n, err := strconv.Atoi(args[i]); err == nil && n > 0 {
					lines = n
				}
			}
		case "--log-file":
			i++
			if i < len(args) {
				logFile = args[i]
			}
		}
	}

	if logFile == "" {
		if meta, err := daemon.LoadMetaForService(serviceName); err == nil {
			logFile = meta.LogFile
		} else {
			logFile = daemon.DefaultLogFileForService(serviceName)
		}
	}

	if _, err := os.Stat(logFile); err != nil {
		fmt.Fprintf(os.Stderr, "Log file not found: %s\n", logFile)
		os.Exit(1)
	}

	if !follow {
		printLastLines(logFile, lines)
		return
	}

	printLastLines(logFile, lines)
	followFile(logFile)
}

func printLastLines(path string, n int) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading log: %v\n", err)
		return
	}

	allLines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	start := 0
	if len(allLines) > n {
		start = len(allLines) - n
	}
	for _, line := range allLines[start:] {
		fmt.Println(line)
	}
}

func followFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	_, _ = f.Seek(0, io.SeekEnd)
	reader := bufio.NewReader(f)

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			fmt.Print(line)
		}
		if err == io.EOF {
			time.Sleep(300 * time.Millisecond)
			reader.Reset(f)
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
	}
}

// ── helpers ─────────────────────────────────────────────────

func mustManager(serviceName string) daemon.Manager {
	mgr, err := daemon.NewManagerForService(serviceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return mgr
}

func printDaemonUsage() {
	fmt.Println(`Usage: direxio-connect daemon <command> [flags]

Commands:
  install     Install and start as system service
  uninstall   Remove system service
  start       Start the service
  stop        Stop the service
  restart     Restart the service
  status      Show service status
  logs        View log output

Install flags:
  --service-name NAME   Named service instance (default: cc-connect; env:
                        DIREXIO_CONNECT_SERVICE_NAME). Use one per Direxio node.
  --config PATH         Path to config.toml (uses its parent as work dir)
  --log-file PATH       Log file path (default: ~/.direxio-connect/logs/direxio-connect.log)
  --log-max-size N      Max log file size in MB (default: 10)
  --work-dir DIR        Directory containing config.toml (default: current dir)
  --force               Overwrite existing installation
  --no-capture-secrets  Do not capture config.toml ${ENV} placeholders into
                        the service file. Also enabled by setting
                        CC_DAEMON_NO_CAPTURE_SECRETS=1 in the environment.

Restart flags:
  --force               Kill existing process before restarting

Logs flags:
  -f, --follow          Follow log output (like tail -f)
  -n N                  Number of lines to show (default: 100)
  --log-file PATH       Custom log file path

Supported platforms:
  Linux (root)     - systemd system service (/etc/systemd/system/)
  Linux (non-root) - systemd user service (~/.config/systemd/user/)
  macOS            - launchd LaunchAgent
  Windows          - Task Scheduler task (schtasks)`)
}
