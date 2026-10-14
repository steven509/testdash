package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseArgs(t *testing.T) {
	opts, err := parseArgs([]string{"--task", ":app:it", "--info", "--show-test-output", "--config=x.json", "-x", "check",
		"--tests", "*OrderTest", "--tests=com.a.B.c", "--", "npm", "test", "--watch"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.task != ":app:it" || opts.config != "x.json" || !opts.showTestOutput || opts.allContainers {
		t.Errorf("%+v", opts)
	}
	if !reflect.DeepEqual(opts.extra, []string{"--info", "-x", "check"}) {
		t.Errorf("extra %q", opts.extra)
	}
	if !reflect.DeepEqual(opts.tests, []string{"*OrderTest", "com.a.B.c"}) {
		t.Errorf("tests %q", opts.tests)
	}
	if !reflect.DeepEqual(opts.explicit, []string{"npm", "test", "--watch"}) {
		t.Errorf("explicit %q", opts.explicit)
	}
	soak, err := parseArgs([]string{":app:blackBoxTest", "--tests", "*HeapSoakTest", "-Dbb.soak=true",
		"-Dbb.soak.queries=/Users/me/export"})
	if err != nil || !reflect.DeepEqual(soak.tests, []string{"*HeapSoakTest"}) ||
		!reflect.DeepEqual(soak.extra, []string{":app:blackBoxTest", "-Dbb.soak=true", "-Dbb.soak.queries=/Users/me/export"}) {
		t.Errorf("system properties go to Gradle as gradlew takes them: %+v %v", soak, err)
	}
	if _, err := parseArgs([]string{"--task"}); err == nil {
		t.Error("--task needs a value")
	}
	if _, err := parseArgs([]string{"--version"}); !errors.Is(err, errVersion) {
		t.Error("--version asks for the version")
	}
	if _, err := parseArgs([]string{"-h"}); !errors.Is(err, errHelp) {
		t.Error("-h asks for help")
	}
}

func TestCommandOf(t *testing.T) {
	if got := commandOf("./gradlew  :app:test --info"); !reflect.DeepEqual(got, []string{"./gradlew", ":app:test", "--info"}) {
		t.Errorf("a string command splits on spaces: %q", got)
	}
	if got := commandOf([]any{"npm", "run", "test:e2e"}); !reflect.DeepEqual(got, []string{"npm", "run", "test:e2e"}) {
		t.Errorf("a list command stays as it is: %q", got)
	}
}

func TestKeycastMode(t *testing.T) {
	for value, want := range map[string]string{"": "", "1": "all", "all": "all", "shortcuts": "shortcuts"} {
		if got := keycastMode(value); got != want {
			t.Errorf("TESTDASH_KEYCAST=%s is %q, want %q", value, got, want)
		}
	}
}
