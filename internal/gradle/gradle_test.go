package gradle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGradleCacheFollowsGradleUserHome(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	t.Setenv("GRADLE_USER_HOME", "")
	if got := GradleCache(); got != filepath.FromSlash("/home/me/.gradle/caches/modules-2/files-2.1") {
		t.Errorf("without GRADLE_USER_HOME the cache is in ~/.gradle: %s", got)
	}
	t.Setenv("GRADLE_USER_HOME", "/ci/gradle")
	if got := GradleCache(); got != filepath.FromSlash("/ci/gradle/caches/modules-2/files-2.1") {
		t.Errorf("GRADLE_USER_HOME moves the cache: %s", got)
	}
}

func TestJarsAreReadableByEveryone(t *testing.T) {
	classes := t.TempDir()
	if err := os.WriteFile(filepath.Join(classes, "A.class"), []byte{0xCA, 0xFE}, 0o644); err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(t.TempDir(), "a.jar")
	if err := WriteJar(jar, classes, nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(jar); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("a jar copied into a container is read by the container's user, so others can read it: %v %v",
			info.Mode(), err)
	}
}
