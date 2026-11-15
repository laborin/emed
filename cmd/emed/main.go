package main

import (
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/laborin/emed/internal/config"
	"github.com/laborin/emed/internal/logging"
	"github.com/laborin/emed/internal/theme"
	"github.com/laborin/emed/internal/ui"
	"github.com/rivo/tview"
)

func main() {
	os.Setenv("COLORTERM", "truecolor")

	if pprofPort := os.Getenv("EMED_PPROF_PORT"); pprofPort != "" {
		go func() {
			addr := "localhost:" + pprofPort
			http.ListenAndServe(addr, nil)
		}()
	}

	var workingDir string
	var startupFile string
	var singleFileMode bool

	opts, err := parseCLIArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
		printUsage(os.Stderr)
		os.Exit(2)
	}

	if opts.showHelp {
		printUsage(os.Stdout)
		return
	}
	if opts.showVersion {
		fmt.Printf("%s %s\n", config.AppName, config.Version())
		return
	}

	pathArg := opts.pathArg
	if pathArg != "" {
		info, err := os.Stat(pathArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Cannot access '%s': %v\n", pathArg, err)
			os.Exit(1)
		}

		if info.IsDir() {
			workingDir, err = filepath.Abs(pathArg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: Cannot resolve path '%s': %v\n", pathArg, err)
				os.Exit(1)
			}
		} else {
			absPath, err := filepath.Abs(pathArg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: Cannot resolve path '%s': %v\n", pathArg, err)
				os.Exit(1)
			}
			startupFile = absPath
			singleFileMode = true
			workingDir = filepath.Dir(absPath)
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: Cannot determine working directory: %v\n", err)
				os.Exit(1)
			}
			workingDir = homeDir
		} else {
			workingDir = cwd
		}
	}

	if err := os.Chdir(workingDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot change to directory '%s': %v\n", workingDir, err)
		os.Exit(1)
	}

	logger := logging.GetLogger()
	defer logger.Close()
	logging.Info("Emed started in directory: %s", workingDir)

	userConfig := config.LoadUserConfig()
	if opts.transparent {
		userConfig.UseTerminalBackground = true
	}
	if opts.opaque {
		userConfig.UseTerminalBackground = false
	}
	theme.Init(userConfig.UseTerminalBackground)
	logging.Info("terminal background mode: %t", userConfig.UseTerminalBackground)

	app := tview.NewApplication()
	layout := ui.NewLayout(app, ui.LayoutOptions{
		SingleFileMode: singleFileMode,
		StartupFile:    startupFile,
	})

	if opts.launchClaude && !singleFileMode {
		layout.SetLaunchClaudeOnStart(true)
	} else if opts.launchClaude && singleFileMode {
		logging.Info("ignoring --claude in single file mode")
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		layout.Stop()
		restoreTerminalState(app)
		os.Exit(0)
	}()

	defer func() {
		if r := recover(); r != nil {
			layout.Stop()
			restoreTerminalState(app)
			fmt.Printf("Application error: %v\n", r)
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			fmt.Printf("Stack trace:\n%s\n", buf[:n])
		}
	}()

	enablePaste := shouldEnableTerminalPaste()
	logging.Info("terminal paste capture enabled: %t", enablePaste)

	if err := app.SetRoot(layout.GetRoot(), true).EnableMouse(true).EnablePaste(enablePaste).Run(); err != nil {
		layout.Stop()
		restoreTerminalState(app)
		panic(err)
	}

	layout.Stop()
	restoreTerminalState(app)
}

func restoreTerminalState(app *tview.Application) {
	if app != nil {
		app.Stop()
	}
	fmt.Print(config.TerminalDisableMouse)
	fmt.Print(config.TerminalExitAltScreen)
	fmt.Print(config.TerminalShowCursor)
	fmt.Print(config.TerminalResetColors)
}

func shouldEnableTerminalPaste() bool {
	if override, ok := parseBoolEnv("EMED_ENABLE_PASTE"); ok {
		return override
	}
	if disable, ok := parseBoolEnv("EMED_DISABLE_PASTE"); ok {
		return !disable
	}
	return true
}

func parseBoolEnv(name string) (bool, bool) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch value {
	case "":
		return false, false
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

type cliOptions struct {
	launchClaude bool
	transparent  bool
	opaque       bool
	showHelp     bool
	showVersion  bool
	pathArg      string
}

func parseCLIArgs(args []string) (cliOptions, error) {
	var opts cliOptions

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-c", "--claude":
			opts.launchClaude = true
		case "-t", "--transparent":
			opts.transparent = true
		case "-o", "--opaque":
			opts.opaque = true
		case "-h", "--help":
			opts.showHelp = true
		case "-v", "--version":
			opts.showVersion = true
		case "--":
			remaining := args[i+1:]
			if len(remaining) == 0 {
				return opts, nil
			}
			if len(remaining) > 1 {
				return opts, fmt.Errorf("expected at most one path argument, got %d", len(remaining))
			}
			if opts.pathArg != "" {
				return opts, fmt.Errorf("expected a single path argument")
			}
			opts.pathArg = remaining[0]
			return opts, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown option: %s", arg)
			}
			if opts.pathArg != "" {
				return opts, fmt.Errorf("expected a single path argument")
			}
			opts.pathArg = arg
		}
	}

	return opts, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "%s %s\n", config.AppName, config.Version())
	fmt.Fprintf(w, "Usage: emed [options] [path]\n\n")
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  -c, --claude       Launch Claude Code on startup")
	fmt.Fprintln(w, "  -t, --transparent  Use the terminal background (default)")
	fmt.Fprintln(w, "  -o, --opaque       Use fixed background colors")
	fmt.Fprintln(w, "  -h, --help         Show this help message")
	fmt.Fprintln(w, "  -v, --version      Show version")
}
