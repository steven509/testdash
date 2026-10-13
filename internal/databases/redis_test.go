package databases

import (
	"slices"
	"testing"
)

func TestRedisCommandsFromMonitor(t *testing.T) {
	q, ok := redisCommand(`1760313600.123456 [0 172.18.0.5:52342] "SET" "cart:42" "{\"items\":[1]}" "EX" "60"`)
	if !ok || q.Kind != "SET" || q.Connection != "172.18.0.5:52342" || q.At.UnixMicro() != 1760313600123456 ||
		!slices.Equal(q.Args, []string{"SET", "cart:42", `{"items":[1]}`, "EX", "60"}) || q.Noise() {
		t.Errorf("a command: %+v", q)
	}
	if q.Text != `SET cart:42 "{\"items\":[1]}" EX 60` {
		t.Errorf("as redis-cli takes it: %s", q.Text)
	}
	q, _ = redisCommand(`1760313600.2 [3 lua] "get" "a b\n\x01"`)
	if q.Kind != "GET" || q.Connection != "lua db 3" || q.Args[1] != "a b\n\x01" || q.Text != `get "a b\n\x01"` {
		t.Errorf("escapes, a script's call and another database: %+v %s", q, q.Text)
	}
	if q, _ := redisCommand(`1760313600.3 [0 172.18.0.5:52342] "PING"`); !q.Noise() {
		t.Error("PING is chatter")
	}
	if _, ok := redisCommand("OK"); ok {
		t.Error("MONITOR's OK is not a command")
	}
	if got := requirePass([]string{"redis-server", "--requirepass", "s3cret"}); got != "s3cret" {
		t.Errorf("the password from the command line: %q", got)
	}
}
