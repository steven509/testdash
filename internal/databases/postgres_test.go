package databases

import (
	"strings"
	"testing"
)

func TestPostgresStatementsFromTheLog(t *testing.T) {
	log := `2026-10-13 00:00:00.000 UTC [1] LOG:  database system is ready to accept connections
1760313600.123 [42] LOG:  duration: 0.512 ms  statement: SELECT a,
	b FROM t
1760313600.200 [43] LOG:  duration: 0.050 ms  parse <unnamed>: SELECT * FROM x WHERE id = $1
1760313600.201 [43] LOG:  duration: 0.030 ms  bind <unnamed>: SELECT * FROM x WHERE id = $1
1760313600.201 [43] DETAIL:  parameters: $1 = '42'
1760313600.202 [43] LOG:  duration: 0.020 ms  execute <unnamed>: SELECT * FROM x WHERE id = $1
1760313600.202 [43] DETAIL:  parameters: $1 = '42'
1760313600.300 [44] LOG:  duration: 0.010 ms  statement: SET extra_float_digits = 3
1760313600.400 [44] LOG:  duration: 0.011 ms  execute S_1: COMMIT`
	var p pgParser
	for _, line := range strings.Split(log, "\n") {
		p.line(line)
	}
	p.end()
	queries := p.take()
	var got []string
	for _, q := range queries {
		got = append(got, strings.Join([]string{q.Connection, q.Kind, q.Text, q.Params, q.Millis}, "|"))
	}
	want := []string{"42|Query|SELECT a,\nb FROM t||0.512", "43|Execute|SELECT * FROM x WHERE id = $1|$1 = '42'|0.020",
		"44|Query|SET extra_float_digits = 3||0.010", "44|Execute|COMMIT||0.011"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("statements:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if queries[0].At.UnixMilli() != 1760313600123 || queries[0].Noise() || !queries[2].Noise() {
		t.Errorf("times and chatter: %v %v %v", queries[0].At, queries[0].Noise(), queries[2].Noise())
	}
}
