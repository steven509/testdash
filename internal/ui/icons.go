package ui

import "strings"

// iconSet is every glyph testdash draws that a Nerd Font draws better. The Unicode set works in any font.
// Every icon is followed by a space where it is drawn, which a Nerd Font glyph wider than its cell can use.
type iconSet struct {
	caretClosed, caretOpen                                   string
	unpicked, picked, partly                                 string // a row's selection mark
	passed, failed, aborted, skipped, pending, idle, flaky   string
	logo, search, gear, warning, clock, check                string
	app, http, kafka, database, output, log, command, server string
}

var unicodeIcons = iconSet{
	caretClosed: "▸", caretOpen: "▾",
	unpicked: "○", picked: "●", partly: "◐",
	passed: "✔", failed: "✘", aborted: "⚠", skipped: "↷", pending: "○", idle: "·", flaky: "≈",
	logo: "◆", search: "⌕", gear: "⚙", warning: "⚠", clock: "◷", check: "✔",
	app: "★", http: "⇄", kafka: "≋", database: "▦", output: "≡", log: "≡", command: "$", server: "■",
}

// nerdIcons are glyphs a Nerd Font draws within about one cell, so they sit at the size of the text: the
// Octicons chevrons, x, dot and arrows, and Codicons where an Octicon is a full-size square that spills into the
// next cell (the star, the triangle, the database, the beaker).
var nerdIcons = iconSet{
	caretClosed: "\uf460", caretOpen: "\uf47c", // oct-chevron_right, oct-chevron_down
	unpicked: "\uf10c", // fa-circle_o: one family, so empty, half and full are the same size
	partly:   "\uf042", // fa-adjust
	picked:   "\uf111", // fa-circle
	passed:   "\ueab2", // cod-check
	failed:   "\uf467", // oct-x
	aborted:  "\uea6c", // cod-warning
	skipped:  "\uead6", // cod-debug_step_over
	pending:  "\uf4c3", // oct-dot
	idle:     "·",
	flaky:    "≈",
	logo:     "\uea79", // cod-beaker
	search:   "\uea6d", // cod-search
	gear:     "\ueaf8", // cod-gear
	warning:  "\uea6c", // cod-warning
	clock:    "\ueb7c", // cod-watch
	check:    "\ueab2", // cod-check
	app:      "\ueb59", // cod-star_full
	http:     "\uf443", // oct-arrow_switch
	kafka:    "\ueaad", // cod-broadcast
	database: "\ueace", // cod-database
	output:   "\ueb9d", // cod-output
	log:      "\ueb9d", // cod-output
	command:  "\uea85", // cod-terminal
	server:   "\ueb50", // cod-server
}

// ic is the set in use, which the settings choose.
var ic = unicodeIcons

// useIcons picks the icons: Unicode, or Nerd Font glyphs followed by spacing spaces wherever they are drawn.
func useIcons(nerdFont bool, spacing int) {
	if !nerdFont {
		ic = unicodeIcons
		return
	}
	pad := strings.Repeat(" ", max(0, min(spacing, 2)-1))
	ic = nerdIcons
	for _, glyph := range []*string{&ic.caretClosed, &ic.caretOpen, &ic.unpicked, &ic.picked, &ic.partly, &ic.passed, &ic.failed, &ic.aborted, &ic.skipped,
		&ic.pending, &ic.idle, &ic.logo, &ic.search, &ic.gear, &ic.warning, &ic.clock, &ic.check, &ic.app, &ic.http,
		&ic.kafka, &ic.database, &ic.output, &ic.log, &ic.command, &ic.server} {
		*glyph += pad
	}
}

// kindIcon is the icon for a service of a kind.
func kindIcon(kind string) string {
	switch kind {
	case "app":
		return ic.app
	case "wiremock", "mockserver":
		return ic.http
	case "kafka", "rabbitmq":
		return ic.kafka
	case "mariadb", "mysql", "mongodb", "postgres", "redis", "elasticsearch", "opensearch":
		return ic.database
	case "file":
		return ic.log
	case "command":
		return ic.command
	}
	return ic.server
}
