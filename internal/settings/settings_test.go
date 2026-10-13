package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOlderSettingsNotifyWhenAway(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"theme":"Teal"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil || s.Notify != "away" || s.Theme != "Teal" {
		t.Errorf("a file from before the setting notifies while away: %+v %v", s, err)
	}
}
