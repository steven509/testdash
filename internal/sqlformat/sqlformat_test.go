package sqlformat

import (
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	got, err := Format("INSERT INTO customers (email, name) VALUES ('a@b.c', 'Ann') ON DUPLICATE KEY UPDATE "+
		"name = VALUES(name)", "mariadb")
	want := "INSERT INTO\n  customers (email, name)\nVALUES\n  ('a@b.c', 'Ann')\nON DUPLICATE KEY UPDATE\n" +
		"  name = VALUES(name)"
	if err != nil || got != want {
		t.Errorf("MariaDB's own clauses are laid out:\n%s\n%v", got, err)
	}
	if got, _ := Format("select id from orders where at between ? and ?", "mariadb"); !strings.Contains(got,
		"at between ? and ?") || !strings.HasPrefix(got, "select\n") {
		t.Errorf("the case of words is kept, and BETWEEN's AND stays with it:\n%s", got)
	}
	if _, err := Format("this is not sql ((", "mariadb"); err == nil {
		t.Error("what sql-formatter cannot parse is an error")
	}
	if _, err := Format("SELECT '"+strings.Repeat("x", maxLength)+"'", "mariadb"); err != errTooLong {
		t.Errorf("a very long query is not formatted: %v", err)
	}
}
