package gradle

import (
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
