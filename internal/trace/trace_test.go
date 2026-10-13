package trace

import "testing"

func TestFromHeaders(t *testing.T) {
	headers := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	for name, c := range map[string]struct {
		headers map[string]string
		want    Context
	}{
		"traceparent": {map[string]string{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
			Context{"4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"}},
		"b3 single": {map[string]string{"b3": "80f198ee56343ba864fe8b2a57d3eff7-e457b5a2e4d86bd1-1"},
			Context{"80f198ee56343ba864fe8b2a57d3eff7", "e457b5a2e4d86bd1"}},
		"b3 64-bit": {map[string]string{"X-B3-TraceId": "a3ce929d0e0e4736", "X-B3-SpanId": "00f067aa0ba902b7"},
			Context{"0000000000000000a3ce929d0e0e4736", "00f067aa0ba902b7"}},
	} {
		if got, ok := FromHeaders(headers(c.headers)); !ok || got != c.want {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if _, ok := FromHeaders(headers(map[string]string{"traceparent": "00-" + "00000000000000000000000000000000" +
		"-00f067aa0ba902b7-01"})); ok {
		t.Error("an all-zero trace id is invalid")
	}
}

func TestFromText(t *testing.T) {
	for text, want := range map[string]Context{
		"2026-10-13 08:00:00.123  INFO 1 --- [shop] [exec-1] [4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7] " +
			"c.e.Shop : placed": {"4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"},
		"INFO [shop,4bf92f3577b34da6a3ce929d0e0e4736,00f067aa0ba902b7] 1 --- placed": {
			"4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"},
		"SELECT 1 /*traceparent='00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'*/": {
			"4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"},
		`{"message":"placed","traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7"}`: {
			"4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"},
		`{"trace.id": "4bf92f3577b34da6a3ce929d0e0e4736"}`: {"4bf92f3577b34da6a3ce929d0e0e4736", ""},
	} {
		if got, ok := FromText(text); !ok || got != want {
			t.Errorf("%s: %+v", text, got)
		}
	}
	for _, text := range []string{"a uuid 123e4567e89b12d3a456426614174000 is not a trace", "INFO [main] started"} {
		if got, ok := FromText(text); ok {
			t.Errorf("%s: %+v", text, got)
		}
	}
}
