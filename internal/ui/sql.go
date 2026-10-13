package ui

import (
	"fmt"
	"strings"

	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/sqlformat"
	"github.com/steven509/testdash/internal/style"
)

// describeSQL is a SQL query's details: laid out over lines and coloured by sql-formatter and chroma, or as the
// database logged it, with a prepared statement's parameters under it.
func describeSQL(q databases.Query, kind string, raw bool) string {
	timing := fmt.Sprintf("  connection %s  %s", q.Connection, clockMillis(q.At))
	if q.Millis != "" {
		timing += "  " + q.Millis + " ms"
	}
	header := style.Bold.Render(cleanText(q.Kind)) + style.Dim.Render(timing) + "\n\n"
	text, params := strings.TrimSpace(q.Text), ""
	if q.Params != "" {
		params = "\n\n" + style.Dim.Render("parameters") + "\n" + styleLines(cleanBlock(q.Params), style.Muted)
	}
	if raw {
		return header + styleLines(cleanBlock(text), style.Cyan) + params
	}
	if q.Kind == "Query" || q.Kind == "Execute" {
		if formatted, err := sqlformat.Format(text, sqlDialects[kind]); err == nil {
			text = formatted
		}
	}
	return header + highlightSQL(cleanBlock(text), kind) + params
}

// sqlDialects are sql-formatter's names for the databases testdash reads.
var sqlDialects = map[string]string{"mariadb": "mariadb", "mysql": "mysql", "postgres": "postgresql"}
