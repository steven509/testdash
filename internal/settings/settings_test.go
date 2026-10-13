package settings

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestHide(t *testing.T) {
	s := Defaults()
	s.Hide([]string{"mariadb", "mysql"}, true)
	s.Hide([]string{"redis"}, true)
	if !s.Hidden("mysql") || !s.Hidden("redis") || s.Hidden("kafka") {
		t.Errorf("hidden: %v", s.HiddenServices)
	}
	s.Hide([]string{"mariadb", "mysql"}, false)
	if !slices.Equal(s.HiddenServices, []string{"redis"}) {
		t.Errorf("showing again takes them out: %v", s.HiddenServices)
	}
}

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
