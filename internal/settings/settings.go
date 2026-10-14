// Package settings keeps the user's preferences in their home directory, in
// $XDG_CONFIG_HOME/testdash/settings.json, else ~/.config/testdash/settings.json.
package settings

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/steven509/testdash/internal/noise"
)

type Settings struct {
	NerdFont       bool   `json:"nerdFont"`       // Nerd Font glyphs for icons and carets
	IconSpacing    int    `json:"iconSpacing"`    // spaces after a Nerd Font icon: 1, or 2 for fonts that draw them wide
	Theme          string `json:"theme"`          // the accent, one of style.Themes
	Mouse          bool   `json:"mouse"`          // testdash takes the mouse; off leaves selecting text to the terminal
	FullLogLines   bool   `json:"fullLogLines"`   // logs start with lines as they were logged, not simplified
	LogTime        bool   `json:"logTime"`        // logs start with the time column
	WrapLogs       bool   `json:"wrapLogs"`       // logs start wrapping long lines
	ShowTestOutput bool   `json:"showTestOutput"` // test stdout stays in Gradle's output
	AllContainers  bool   `json:"allContainers"`  // follow every container, not only the run's
	// ActivityWindow is how many seconds of a run that goes on the activity shows, the last of them; once it is
	// over, it shows all of it.
	ActivityWindow int `json:"activityWindow"`
	// Profile samples the applications while tests run: CPU and memory, and a JVM's heap, collections and threads.
	Profile bool `json:"profile"`
	// Notify asks the terminal for a desktop notification when a run ends: away (while the terminal is in the
	// background), always or off.
	Notify string `json:"notify"`
	// HiddenServices are the kinds of service testdash leaves alone, such as redis: not listed, not followed, and
	// nothing switched on in them.
	HiddenServices []string `json:"hiddenServices,omitempty"`
	// Noise are the noise rules for every build.
	Noise []noise.Rule `json:"noise,omitempty"`
}

func Defaults() Settings {
	return Settings{Theme: "Violet", Mouse: true, IconSpacing: 1, Notify: "away", ActivityWindow: 30}
}

// Window is how much of a run that goes on the activity shows.
func (s Settings) Window() time.Duration {
	if s.ActivityWindow <= 0 {
		return 30 * time.Second
	}
	return time.Duration(s.ActivityWindow) * time.Second
}

// Hidden is whether testdash leaves a kind of service alone.
func (s Settings) Hidden(kind string) bool {
	return slices.Contains(s.HiddenServices, kind)
}

// Hide hides or shows kinds of service.
func (s *Settings) Hide(kinds []string, hide bool) {
	var kept []string
	for _, kind := range s.HiddenServices {
		if !slices.Contains(kinds, kind) {
			kept = append(kept, kind)
		}
	}
	if hide {
		kept = append(kept, kinds...)
	}
	s.HiddenServices = kept
}

// Path is where the settings live.
func Path() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "testdash", "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "testdash", "settings.json")
}

// Load reads the settings, the defaults standing in for any that are missing; a file that cannot be read is
// an error, a missing one is not.
func Load() (Settings, error) {
	s := Defaults()
	data, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Defaults(), err
	}
	return s, nil
}

// Save writes the settings, replacing the file in one step.
func (s Settings) Save() error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "settings.*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
