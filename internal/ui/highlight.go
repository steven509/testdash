package ui

import (
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/steven509/testdash/internal/style"
)

var (
	mysqlLexer    = chroma.Coalesce(lexers.Get("mysql"))
	postgresLexer = chroma.Coalesce(lexers.Get("postgresql"))
	jsonLexer     = chroma.Coalesce(lexers.Get("json"))
	parameter     = regexp.MustCompile(`^(\?|\$\d+)$`)
)

// highlightSQL colours a query's parts by what chroma's lexer for the database reads them as.
func highlightSQL(query, kind string) string {
	lexer := mysqlLexer
	if kind == "postgres" {
		lexer = postgresLexer
	}
	return highlight(lexer, query, func(tokens []chroma.Token, i int) style.Style {
		token := tokens[i]
		t := token.Type
		switch {
		case parameter.MatchString(token.Value):
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
		case t.InSubCategory(chroma.LiteralNumber),
			token.Value == "." && i > 0 && i+1 < len(tokens) && // the lexer splits a decimal at its point
				tokens[i-1].Type.InSubCategory(chroma.LiteralNumber) &&
				tokens[i+1].Type.InSubCategory(chroma.LiteralNumber):
			return style.Yellow
		case t.InCategory(chroma.Operator) || t.InCategory(chroma.Punctuation):
			return style.Muted
		}
		return style.Style{}
	})
}

// highlightJSON colours JSON: keys, strings, numbers, true, false and null.
func highlightJSON(text string) string {
	return highlight(jsonLexer, text, func(tokens []chroma.Token, i int) style.Style {
		t := tokens[i].Type
		switch {
		case t == chroma.NameTag:
			return style.Cyan
		case t.InSubCategory(chroma.LiteralString):
			return style.Green
		case t.InSubCategory(chroma.LiteralNumber):
			return style.Yellow
		case t.InCategory(chroma.Keyword):
			return style.AccentText
		case t.InCategory(chroma.Punctuation):
			return style.Muted
		}
		return style.Style{}
	})
}

// highlight styles each token a lexer reads in text, a line at a time so a wrapped or scrolled line keeps its
// colours.
func highlight(lexer chroma.Lexer, text string, styleOf func(tokens []chroma.Token, i int) style.Style) string {
	iterator, err := lexer.Tokenise(nil, text)
	if err != nil {
		return styleLines(text, style.Cyan)
	}
	tokens := iterator.Tokens()
	var b strings.Builder
	for i, token := range tokens {
		b.WriteString(styleLines(token.Value, styleOf(tokens, i)))
	}
	return b.String()
}
