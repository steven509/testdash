package wiremock

import "testing"

func TestServeEvent(t *testing.T) {
	event, ok := serveEvent([]byte(`{"id":"e1","request":{"url":"/orders/42?x=1","method":"GET","loggedDate":1760283951006},` +
		`"response":{"status":200},"wasMatched":true,"stubMapping":{"id":"0123456789","request":{"urlPath":"/orders/42","method":"GET"}}}`))
	if !ok {
		t.Fatal("not parsed")
	}
	if event.ID != "e1" || event.Method != "GET" || event.Status != 200 || !event.Matched || event.Stub != "GET /orders/42" {
		t.Errorf("%+v", event)
	}
	if event.At.UnixMilli() != 1760283951006 || event.LoggedDate() != "1760283951006" {
		t.Errorf("at %v %s", event.At, event.LoggedDate())
	}
	unmatched, _ := serveEvent([]byte(`{"id":"e2","request":{"url":"/x","method":"POST"},` +
		`"responseDefinition":{"status":404},"wasMatched":false,"stubMapping":{"name":"default 404"}}`))
	if unmatched.Matched || unmatched.Stub != "" || unmatched.Status != 404 {
		t.Errorf("%+v", unmatched)
	}
}

func TestStubLabel(t *testing.T) {
	for raw, want := range map[string]string{
		`{"name":"orders"}`: "orders",
		`{"request":{"method":"POST","urlPattern":"/a/.*"}}`: "POST /a/.*",
		`{"request":{}}`: "ANY",
		`null`:           "",
	} {
		if got := StubLabel([]byte(raw)); got != want {
			t.Errorf("StubLabel(%s) = %q, want %q", raw, got, want)
		}
	}
}
