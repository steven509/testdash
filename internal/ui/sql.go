package ui

import (
	"fmt"
	"strings"

	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/sqlformat"
	"github.com/steven509/testdash/internal/style"
)

// describeSQL is a SQL query's details: laid out over lines and coloured by sql-formatter and chroma, or as the
// database logged it.
func describeSQL(q databases.Query, dialect string, raw bool) string {
	header := style.Bold.Render(cleanText(q.Kind)) +
		style.Dim.Render(fmt.Sprintf("  connection %s  %s", q.Connection, clockMillis(q.At))) + "\n\n"
	text := strings.TrimSpace(q.Text)
	if raw {
		return header + styleLines(cleanBlock(text), style.Cyan)
	}
	if q.Kind == "Query" || q.Kind == "Execute" {
		if formatted, err := sqlformat.Format(text, dialect); err == nil {
			text = formatted
		}
	}
	return header + highlightSQL(cleanBlock(text))
}
