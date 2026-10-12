package databases

import "testing"

func TestMongoQuery(t *testing.T) {
	query, ok := mongoQuery([]byte(`{"op":"query","ns":"shop.items","command":{"find":"items",` +
		`"filter":{"price":{"$gt":2}},"lsid":{"id":1},"$db":"shop"},"millis":3,"client":"10.0.0.1:5000",` +
		`"ts":{"$date":"2026-10-12T15:45:51.006Z"},"planSummary":"COLLSCAN"}`))
	if !ok {
		t.Fatal("not parsed")
	}
	if query.Kind != "find" || query.Target != "shop.items" || query.Millis != "3" || query.Connection != "10.0.0.1:5000" {
		t.Errorf("%+v", query)
	}
	if query.Text != `{"find": "items", "filter": {"price": {"$gt": 2}}}` {
		t.Errorf("text %s", query.Text)
	}
	if query.Noise() {
		t.Error("a MongoDB command is never chatter")
	}
	if _, ok := mongoQuery([]byte(`{"op":"getmore","command":{"getMore":1},"ts":{"$date":"2026-10-12T15:45:51Z"}}`)); ok {
		t.Error("a cursor's next batch is left out")
	}
}

func TestSQLNoiseAndEscapes(t *testing.T) {
	for _, c := range []struct {
		kind, text string
		noise      bool
	}{
		{"Query", "select * from items", false},
		{"Query", "SET autocommit=1", true},
		{"Query", "SELECT @@session.transaction_isolation", true},
		{"Query", "/* ping */ SELECT 1", true},
		{"Query", "select 1 from information_schema.ENGINES WHERE engine='innodb' AND support in ('YES', 'DEFAULT', 'ENABLED')", true},
		{"Connect", "root@localhost on shop", true},
		{"Execute", "insert into items values (1)", false},
	} {
		if got := Chatter(c.kind, "", c.text); got != c.noise {
			t.Errorf("%s %q noise %v", c.kind, c.text, got)
		}
	}
	if got := unescape(`a\nb\tc\\d\0e\q`); got != "a\nb\tc\\d\x00e\\q" {
		t.Errorf("unescape %q", got)
	}
}
