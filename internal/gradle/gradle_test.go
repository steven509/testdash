package gradle

import (
	"os"
	"path/filepath"
	"strings"
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
	t.Setenv("GRADLE_RO_DEP_CACHE", "/ci/shared")
	if got := GradleCaches(); len(got) != 2 || got[1] != filepath.FromSlash("/ci/shared/modules-2/files-2.1") {
		t.Errorf("jars are found in the read-only dependency cache too: %v", got)
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

func TestBuildProperties(t *testing.T) {
	args := []string{"-Dbb.soak=true", "--info", "-P", "env=ci", "--system-prop", "a=b", "-Pz=1", "--offline",
		"--project-prop=y=2"}
	want := []string{"-Dbb.soak=true", "-P", "env=ci", "--system-prop", "a=b", "-Pz=1", "--project-prop=y=2"}
	if got := BuildProperties(args); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the build's properties among its arguments: %q", got)
	}
}
