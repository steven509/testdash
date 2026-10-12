package jsonx

import "testing"

func TestKeepsKeyOrder(t *testing.T) {
	raw := []byte(`{"z": 1, "a": {"y": [1, 2], "b": "x, y: z"}, "m": null}`)
	if got := Dumps(raw); got != `{"z": 1, "a": {"y": [1, 2], "b": "x, y: z"}, "m": null}` {
		t.Errorf("Dumps = %s", got)
	}
	object, ok := ParseObject(raw)
	if !ok || len(object.Keys) != 3 || object.Keys[0] != "z" || object.Keys[2] != "m" {
		t.Fatalf("keys %v", object.Keys)
	}
	if got := string(object.Without("a").Marshal()); got != `{"z":1,"m":null}` {
		t.Errorf("Without = %s", got)
	}
	if got := Indent([]byte(`{"b":1,"a":[true]}`)); got != "{\n  \"b\": 1,\n  \"a\": [\n    true\n  ]\n}" {
		t.Errorf("Indent = %q", got)
	}
}

func TestTruthy(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "null": false, "false": false, "0": false, `""`: false,
		"[]": false, "{}": false, "0.0": false, "1": true, `"x"`: true, `{"a":1}`: true, "true": true} {
		if got := Truthy([]byte(raw)); got != want {
			t.Errorf("Truthy(%s) = %v", raw, got)
		}
	}
}

func TestPretty(t *testing.T) {
	if got := Pretty(`{"a":1}`); got != "{\n  \"a\": 1\n}" {
		t.Errorf("Pretty JSON = %q", got)
	}
	if got := Pretty("not json"); got != "not json" {
		t.Errorf("Pretty text = %q", got)
	}
}
