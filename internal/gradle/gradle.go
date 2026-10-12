// Package gradle hooks testdash into Gradle without changing the build: a JUnit Platform listener for runs, and
// JUnit discovery for listing a build's tests.
//
// The Java side (junit/*.java, embedded in the binary) is compiled once, against JUnit Platform jars from the
// local Gradle cache, and cached under ~/.cache/testdash. Gradle gets it through the init script in junit/.
package gradle

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/steven509/testdash/internal/catalog"
)

//go:embed junit
var junit embed.FS

const (
	listenerClass = "testdash.TestdashListener"
	// Bump when the way the jar is built changes, so cached jars are rebuilt.
	buildVersion = "1"
)

var apiModules = []string{"junit-platform-launcher", "junit-platform-engine", "junit-platform-commons"}

// ErrUnavailable says why the listener cannot be built.
type ErrUnavailable struct {
	Reason string
}

func (e *ErrUnavailable) Error() string {
	return e.Reason
}

func unavailable(format string, args ...any) error {
	return &ErrUnavailable{Reason: fmt.Sprintf(format, args...)}
}

func GradleCache() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gradle", "caches", "modules-2", "files-2.1")
}

func IsGradle(command []string) bool {
	if len(command) == 0 {
		return false
	}
	switch filepath.Base(command[0]) {
	case "gradlew", "gradle", "gradlew.bat":
		return true
	}
	return false
}

// DiscoverCommand lists every JUnit Platform Test task's tests into outputDir, one <task>.json each, running none.
func DiscoverCommand(gradlew, outputDir string) ([]string, error) {
	jar, err := ListenerJar()
	if err != nil {
		return nil, err
	}
	script, err := initScript()
	if err != nil {
		return nil, err
	}
	return []string{gradlew, "-I", script, "-Ptestdash.discover=" + outputDir, "-Ptestdash.listenerJar=" + jar,
		"testdashDiscover", "--continue", "--quiet"}, nil
}

// WithListener is the Gradle command with the init script and its properties added after the executable.
func WithListener(command []string, runDir string, quietTestOutput bool) ([]string, error) {
	jar, err := ListenerJar()
	if err != nil {
		return nil, err
	}
	script, err := initScript()
	if err != nil {
		return nil, err
	}
	extra := []string{"-I", script, "-Ptestdash.dir=" + runDir, "-Ptestdash.listenerJar=" + jar}
	if quietTestOutput {
		extra = append(extra, "-Ptestdash.quietTestOutput=true")
	}
	return append(append(append([]string{}, command[:1]...), extra...), command[1:]...), nil
}

func javaSources() ([]string, [][]byte) {
	entries, _ := fs.Glob(junit, "junit/*.java")
	sort.Strings(entries)
	var contents [][]byte
	for _, name := range entries {
		data, _ := junit.ReadFile(name)
		contents = append(contents, data)
	}
	return entries, contents
}

// initScript writes the embedded init script to the cache, where Gradle can read it, named by its content.
func initScript() (string, error) {
	data, err := junit.ReadFile("junit/testdash.init.gradle")
	if err != nil {
		return "", err
	}
	return cached("testdash-%s.init.gradle", data, func(path string) error {
		return writeAtomically(path, data)
	})
}

// ListenerJar is the listener and discovery classes, built once per version of their sources.
func ListenerJar() (string, error) {
	_, contents := javaSources()
	source := append(bytes.Join(contents, nil), buildVersion...)
	return cached("listener-%s.jar", source, buildListener)
}

// cached returns the cache file named after the sources' hash, building it first when it is missing.
func cached(pattern string, source []byte, build func(path string) error) (string, error) {
	sum := sha1.Sum(source)
	path := filepath.Join(catalog.CacheDir(), fmt.Sprintf(pattern, hex.EncodeToString(sum[:])[:12]))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := build(path); err != nil {
		return "", err
	}
	return path, nil
}

func buildListener(jar string) error {
	classpath, err := junitAPIJars()
	if err != nil {
		return err
	}
	javac, err := Javac()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "testdash-listener")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	names, contents := javaSources()
	var sources []string
	for i, name := range names {
		path := filepath.Join(tmp, "src", filepath.Base(name))
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, contents[i], 0o644); err != nil {
			return err
		}
		sources = append(sources, path)
	}
	classes := filepath.Join(tmp, "classes")
	args := append([]string{"--release", "11", "-nowarn", "-implicit:none", "-d", classes, "-cp",
		strings.Join(classpath, string(os.PathListSeparator))}, sources...)
	if output, err := exec.Command(javac, args...).CombinedOutput(); err != nil {
		return unavailable("javac failed:\n%s", strings.TrimSpace(string(output)))
	}
	return WriteJar(jar, classes, map[string]string{
		"META-INF/services/org.junit.platform.launcher.TestExecutionListener": listenerClass + "\n",
	})
}

// WriteJar zips the classes under dir, plus extra files, into jar, replacing it in one step.
func WriteJar(jar, dir string, extra map[string]string) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".class") {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		return err
	}
	for name, content := range extra {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return writeAtomically(jar, buf.Bytes())
}

// writeAtomically writes through a temporary file, so two builds at once never leave half a file.
func writeAtomically(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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

// Javac finds the JDK's compiler: JAVA_HOME's, else the one on PATH.
func Javac() (string, error) {
	if home := os.Getenv("JAVA_HOME"); home != "" {
		javac := filepath.Join(home, "bin", "javac")
		if _, err := os.Stat(javac); err == nil {
			return javac, nil
		}
	}
	javac, err := exec.LookPath("javac")
	if err != nil {
		return "", unavailable("no javac found (set JAVA_HOME or put a JDK on PATH)")
	}
	return javac, nil
}

// junitAPIJars finds launcher, engine and commons jars of one JUnit Platform version in the Gradle cache.
func junitAPIJars() ([]string, error) {
	cache := GradleCache()
	var versions map[string]bool
	for _, module := range apiModules {
		found := map[string]bool{}
		dirs, _ := os.ReadDir(filepath.Join(cache, "org.junit.platform", module))
		for _, d := range dirs {
			if d.IsDir() && (versions == nil || versions[d.Name()]) {
				found[d.Name()] = true
			}
		}
		versions = found
	}
	if len(versions) == 0 {
		return nil, unavailable("no JUnit Platform jars in %s; run any JUnit 5 build with Gradle once first", cache)
	}
	var version string
	for v := range versions {
		if version == "" || CompareVersions(v, version) > 0 {
			version = v
		}
	}
	var jars []string
	for _, module := range apiModules {
		candidates, _ := filepath.Glob(filepath.Join(cache, "org.junit.platform", module, version, "*",
			module+"-"+version+".jar"))
		if len(candidates) == 0 {
			return nil, unavailable("%s %s has no jar in the Gradle cache", module, version)
		}
		jars = append(jars, candidates[0])
	}
	guardians, _ := filepath.Glob(filepath.Join(cache, "org.apiguardian", "apiguardian-api", "*", "*", "apiguardian-api-*.jar"))
	for _, jar := range guardians {
		if !strings.HasSuffix(jar, "-sources.jar") && !strings.HasSuffix(jar, "-javadoc.jar") {
			jars = append(jars, jar)
			break
		}
	}
	return jars, nil
}

// CompareVersions orders versions by their numeric parts, a non-numeric part (a qualifier) below any number.
func CompareVersions(a, b string) int {
	pa, pb := versionKey(a), versionKey(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		if i >= len(pa) {
			return -1
		}
		if i >= len(pb) {
			return 1
		}
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionKey(version string) []int {
	var key []int
	for _, part := range strings.Split(strings.ReplaceAll(version, "-", "."), ".") {
		n, err := strconv.Atoi(part)
		if err != nil || strings.TrimLeft(part, "0123456789") != "" {
			n = -1
		}
		key = append(key, n)
	}
	return key
}

// IsUnavailable reports whether err says the listener cannot be built, as opposed to an I/O error.
func IsUnavailable(err error) bool {
	var target *ErrUnavailable
	return errors.As(err, &target)
}
