package ui

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/sqlformat"
	"github.com/steven509/testdash/internal/style"
)

var sqlLexer = chroma.Coalesce(lexers.Get("mysql"))

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

// highlightSQL colours a query's parts by what chroma's MySQL lexer reads them as.
func highlightSQL(query string) string {
	iterator, err := sqlLexer.Tokenise(nil, query)
	if err != nil {
		return styleLines(query, style.Cyan)
	}
	tokens := iterator.Tokens()
	var b strings.Builder
	for i, token := range tokens {
		s := sqlStyle(token)
		if token.Value == "." && i > 0 && i+1 < len(tokens) && // the lexer splits a decimal at its point
			tokens[i-1].Type.InSubCategory(chroma.LiteralNumber) && tokens[i+1].Type.InSubCategory(chroma.LiteralNumber) {
			s = style.Yellow
		}
		b.WriteString(styleLines(token.Value, s))
	}
	return b.String()
}

func sqlStyle(token chroma.Token) style.Style {
	t := token.Type
	switch {
	case token.Value == "?":
		return style.PinkText
	case t.InCategory(chroma.Comment):
		return style.Dim
	case t.InCategory(chroma.Keyword):
		return style.AccentBold
	case t == chroma.NameConstant:
		return style.AccentText
	case t == chroma.NameFunction || t == chroma.NameBuiltin:
		return style.TealText
	case t.InSubCategory(chroma.LiteralString):
		return style.Green
	case t.InSubCategory(chroma.LiteralNumber):
		return style.Yellow
	case t.InCategory(chroma.Operator) || t.InCategory(chroma.Punctuation):
		return style.Muted
	}
	return style.Style{}
}
