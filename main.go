// testdash: pick tests, run them, and follow the results and every service the run starts.
//
//	testdash [--task TASK] [--config FILE] [--show-test-output] [--all-containers] [TASK...] [--tests PATTERN...] [ARG...] [-- COMMAND...]
//	testdash --settings
//	testdash --version
//
// In a Gradle build it lists every test task's tests (JUnit discovery, no build change) and runs the ones you
// pick. Given a task path or Gradle's --tests filters, as gradlew takes them, it runs those tests at once. Other
// ARGs are added to the Gradle command. Everything after `--` replaces the command. The containers the run
// starts are found through Docker and get their own views, WireMock with its requests and stubs. See README.md.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/steven509/testdash/internal/catalog"
	"github.com/steven509/testdash/internal/noise"
	"github.com/steven509/testdash/internal/settings"
	"github.com/steven509/testdash/internal/ui"
)

const configName = ".testdash.json"

// version is set for a release's binaries (-ldflags "-X main.version=v1.2.3"); go install stamps its own.
var version string

const usage = `usage: testdash [--task TASK] [--config FILE] [--show-test-output] [--all-containers]
                [TASK...] [--tests PATTERN...] [ARG...] [-- COMMAND...]
       testdash --settings
       testdash --version

Pick tests, run them, and follow the results and every service the run starts.

  testdash                                  pick from the build's tests
  testdash :app:test --tests '*OrderTest'   run these at once, as gradlew would

options:
  --task TASK         the Gradle test task to show first, e.g. :app:blackBoxTest
  --tests PATTERN     run the tests Gradle's filter matches, at once; may be given more than once
  --config FILE       config file (default: the nearest .testdash.json)
  --show-test-output  keep test stdout in Gradle's output; by default the service views show it
  --all-containers    follow every running container, not only those the run starts
  --settings          change testdash's settings: icons, colours, mouse, log defaults
  --version           print testdash's version

A task path (:app:test) runs that task at once. Arguments testdash does not know are passed to Gradle;
everything after -- replaces the command.
`

type options struct {
	task           string
	config         string
	showTestOutput bool
	allContainers  bool
	settings       bool
	tests          []string
	extra          []string
	explicit       []string
}

type config struct {
	Task           string        `json:"task"`
	Command        any           `json:"command"`
	ShowTestOutput bool          `json:"showTestOutput"`
	Logs           []ui.ExtraLog `json:"logs"`
	Noise          []noise.Rule  `json:"noise"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	opts, err := parseArgs(argv)
	if errors.Is(err, errHelp) {
		fmt.Print(usage)
		return 0
	}
	if errors.Is(err, errVersion) {
		fmt.Println("testdash", versionString())
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\ntestdash: error: %s\n", strings.SplitN(usage, "\n", 2)[0], err)
		return 2
	}
	prefs, err := settings.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testdash: cannot read %s, using the defaults: %s\n", settings.Path(), err)
	}
	if opts.settings {
		return runSettings(prefs)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdash:", err)
		return 1
	}
	configPath := opts.config
	if configPath == "" {
		configPath = findConfig(cwd)
	}
	var cfg config
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err == nil {
			err = json.Unmarshal(data, &cfg)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "testdash: cannot read %s: %s\n", configPath, err)
			return 1
		}
	}
	var command []string
	workdir := cwd
	switch {
	case len(opts.explicit) > 0:
		command = opts.explicit
	case cfg.Command != nil:
		command = commandOf(cfg.Command)
		if opts.config == "" { // a discovered config sits in its repo
			workdir = filepath.Dir(configPath)
		}
	}
	buildRoot := catalog.BuildRoot(workdir)
	if command == nil && buildRoot != "" {
		workdir = buildRoot
	}
	runDir := filepath.Join(os.TempDir(), "testdash", filepath.Base(workdir)+"-"+randomHex(3))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "testdash:", err)
		return 1
	}
	task := opts.task
	if task == "" {
		task = cfg.Task
	}
	options := ui.Options{
		Workdir:              workdir,
		BuildRoot:            buildRoot,
		Command:              command,
		ExtraArgs:            opts.extra,
		Tests:                opts.tests,
		Task:                 task,
		ShowTestOutput:       opts.showTestOutput || cfg.ShowTestOutput || prefs.ShowTestOutput,
		IncludeAllContainers: opts.allContainers || prefs.AllContainers,
		RunDir:               runDir,
		ExtraLogs:            cfg.Logs,
		Noise:                cfg.Noise,
		ConfigPath:           configFor(configPath, buildRoot, workdir),
		Keycast:              keycastMode(os.Getenv("TESTDASH_KEYCAST")),
	}

	ctx, cancel := context.WithCancel(context.Background())
	model := ui.New(ctx, options, prefs)
	program := tea.NewProgram(model)
	model.SetSend(program.Send)
	// A closed terminal hangs up: stop the run with it, rather than leave Gradle running in its own session.
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	go func() {
		if _, ok := <-hangup; ok {
			program.Kill()
		}
	}()
	_, err = program.Run()
	signal.Stop(hangup)
	close(hangup)
	cancel()
	model.Shutdown()
	if errors.Is(err, tea.ErrProgramKilled) {
		return 1
	}
	resetKeyboard()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdash:", err)
		return 1
	}
	return 0
}

// runSettings shows the settings on their own screen.
func runSettings(prefs settings.Settings) int {
	if _, err := tea.NewProgram(ui.NewSettingsApp(prefs)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "testdash:", err)
		return 1
	}
	resetKeyboard()
	return 0
}

var (
	errHelp    = errors.New("help")
	errVersion = errors.New("version")
)

// versionString is the release's version, else the module version go install stamps (v1.2.3), else for a build
// from a clone, the commit it was built from.
func versionString() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value[:min(12, len(setting.Value))]
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	switch {
	case revision == "":
		return "devel"
	case dirty:
		return "devel " + revision + " with changes"
	}
	return "devel " + revision
}

// parseArgs reads testdash's own options; anything else before `--` is for Gradle, and everything after it is
// the command.
func parseArgs(argv []string) (options, error) {
	var opts options
	for i, arg := range argv {
		if arg == "--" {
			opts.explicit = argv[i+1:]
			argv = argv[:i]
			break
		}
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		name, value, hasValue := strings.Cut(arg, "=")
		takeValue := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(argv) {
				return "", fmt.Errorf("argument %s: expected one argument", name)
			}
			i++
			return argv[i], nil
		}
		var err error
		switch name {
		case "-h", "--help":
			return opts, errHelp
		case "--version":
			return opts, errVersion
		case "--task":
			opts.task, err = takeValue()
		case "--config":
			opts.config, err = takeValue()
		case "--show-test-output":
			opts.showTestOutput = true
		case "--all-containers":
			opts.allContainers = true
		case "--settings":
			opts.settings = true
		case "--tests":
			var pattern string
			pattern, err = takeValue()
			opts.tests = append(opts.tests, pattern)
		default:
			opts.extra = append(opts.extra, arg)
		}
		if err != nil {
			return opts, err
		}
	}
	return opts, nil
}

func commandOf(value any) []string {
	switch command := value.(type) {
	case string:
		return strings.Fields(command)
	case []any:
		var parts []string
		for _, part := range command {
			parts = append(parts, fmt.Sprint(part))
		}
		return parts
	}
	return nil
}

// findConfig is the nearest .testdash.json from start up to the git root.
func findConfig(start string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, configName)); err == nil && info.Mode().IsRegular() {
			return filepath.Join(dir, configName)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return ""
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

// resetKeyboard turns the kitty keyboard protocol off once testdash has left the alternate screen. Bubble Tea
// switches it on and back off before leaving that screen; terminals that keep one protocol setting for both
// screens (Orca's among them) can stay switched, and then the shell gets keys as codes ("13u" for Enter).
func resetKeyboard() {
	if term.IsTerminal(os.Stdout.Fd()) {
		fmt.Print(ansi.KittyKeyboard(0, 1))
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// keycastMode is what TESTDASH_KEYCAST asks to show: only the shortcuts, or any other value every key.
func keycastMode(value string) string {
	if value == "" || value == "shortcuts" {
		return value
	}
	return "all"
}

// configFor is where a build's noise rules are saved: its .testdash.json, or one to make at its root.
func configFor(found, buildRoot, workdir string) string {
	switch {
	case found != "":
		return found
	case buildRoot != "":
		return filepath.Join(buildRoot, configName)
	}
	return filepath.Join(workdir, configName)
}
