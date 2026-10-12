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

func TestParseJournalAndNearMisses(t *testing.T) {
	events, ok := ParseJournal([]byte(`{
  "requests" : [ {
    "id" : "e2",
    "request" : { "url" : "/payments/p1/refunds", "method" : "POST", "loggedDate" : 1760283951100 },
    "responseDefinition" : { "status" : 404 },
    "wasMatched" : false
  }, {
    "id" : "e1",
    "request" : { "url" : "/payments", "method" : "POST", "loggedDate" : 1760283951006 },
    "response" : { "status" : 201 },
    "wasMatched" : true
  } ],
  "meta" : { "total" : 2 }
}`))
	if !ok || len(events) != 2 || events[0].ID != "e2" || events[1].Status != 201 {
		t.Fatalf("a journal reads newest first: %+v", events)
	}
	misses, ok := ParseNearMisses([]byte(`{"nearMisses":[{"request":{"url":"/payments/p1/refunds","method":"POST",` +
		`"loggedDate":1760283951100},"stubMapping":{"name":"Refund a payment"},"matchResult":{"distance":0.12}}]}`))
	if !ok || len(misses) != 1 || misses[0].Distance != 0.12 || misses[0].LoggedDate != "1760283951100" ||
		StubLabel(misses[0].StubMapping) != "Refund a payment" {
		t.Fatalf("%+v", misses)
	}
	if _, ok := ParseJournal([]byte("<html>not WireMock</html>")); ok {
		t.Error("a reply that is not a journal is refused")
	}
}
