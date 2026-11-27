package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Bead text (descriptions, notes) is markdown-ish prose written by workers:
// numbered steps, bullets, "## Acceptance" headings, and very often a run of
// steps packed into one line as "(1) … (2) …" or "Options: 1) … 2) …".
// richLines lays it out for the detail pane so steps and paragraphs stand
// apart: lists get hanging indents, headings lose their #s, inline
// enumerations go one per line, and a list whose items wrap gets a blank line
// between them.

type blockKind int

const (
	blockPara blockKind = iota
	blockHeading
	blockItem
	blockCode
	blockBlank
)

type textBlock struct {
	kind   blockKind
	level  int    // list nesting, items only
	marker string // as shown, items only
	text   string
}

var (
	headingRE = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)[\s#]*$`)
	// A line that is only a short label ("Acceptance:") heads what follows.
	labelRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9 /&()-]{0,40}:$`)
	itemRE  = regexp.MustCompile(`^(\s*)([-*+•]|\d{1,3}[.)]|\(\d{1,3}\)|\([a-z]\))\s+(.*)$`)
	fenceRE = regexp.MustCompile("^\\s*(```|~~~)")
	boldRE  = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	// Inline enumeration markers: "(1)", "(a)", "1)", "1." each preceded by
	// the start or a space and followed by a space.
	enumRE = regexp.MustCompile(`(?:^|\s)(\((\d{1,2})\)|\(([a-z])\)|(\d{1,2})\)|(\d{1,2})\.)\s`)
)

// parseBlocks splits s into blocks, one per source line, with indented lines
// under a list item folded into it and fenced code kept verbatim.
func parseBlocks(s string) []textBlock {
	var out []textBlock
	inCode := false
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(strings.ReplaceAll(raw, "\t", "    "), " ")
		if fenceRE.MatchString(line) {
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, textBlock{kind: blockCode, text: line})
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			out = append(out, textBlock{kind: blockBlank})
			continue
		case headingRE.MatchString(line):
			out = append(out, textBlock{kind: blockHeading, text: plain(headingRE.FindStringSubmatch(line)[1])})
			continue
		case labelRE.MatchString(trimmed):
			out = append(out, textBlock{kind: blockHeading, text: plain(strings.TrimSuffix(trimmed, ":"))})
			continue
		}
		// "(a) … (b) …" on one line is a run of items, not one item.
		if lead, items := splitEnum(trimmed); lead == "" && len(items) > 0 && line == trimmed {
			out = append(out, textBlock{kind: blockPara, text: plain(trimmed)})
			continue
		}
		if m := itemRE.FindStringSubmatch(line); m != nil {
			out = append(out, textBlock{kind: blockItem, level: min(len(m[1])/2, 3), marker: m[2], text: plain(m[3])})
			continue
		}
		// An indented line under an item continues it.
		if n := len(out); n > 0 && out[n-1].kind == blockItem && line != trimmed {
			out[n-1].text += " " + plain(trimmed)
			continue
		}
		out = append(out, textBlock{kind: blockPara, text: plain(trimmed)})
	}
	return expandEnums(out)
}

// plain drops markdown bold markers, which the pane can't show inline.
func plain(s string) string { return boldRE.ReplaceAllString(s, "$1") }

// expandEnums turns each inline enumeration into list items: a paragraph's
// become items under it, an item's become its children.
func expandEnums(in []textBlock) []textBlock {
	var out []textBlock
	for _, b := range in {
		out = append(out, expandBlock(b)...)
	}
	return out
}

func expandBlock(b textBlock) []textBlock {
	if b.kind != blockPara && b.kind != blockItem {
		return []textBlock{b}
	}
	lead, items := splitEnum(b.text)
	if len(items) == 0 {
		return []textBlock{b}
	}
	level := 0
	if b.kind == blockItem {
		level = b.level + 1
	}
	var out []textBlock
	if lead != "" || b.kind == blockItem {
		b.text = lead
		out = append(out, b)
	}
	for _, it := range items {
		it.level = level
		if level < 3 {
			out = append(out, expandBlock(it)...)
		} else {
			out = append(out, it)
		}
	}
	return out
}

type enumMark struct {
	style      int // which capture group matched
	ord        int
	start, end int // marker span, end just past the space after it
}

// splitEnum finds the earliest run of two or more markers of one style
// counting up from 1 or a, and splits s at them: the lead before the first,
// then one item per marker. A string with no such run comes back whole.
func splitEnum(s string) (string, []textBlock) {
	var marks []enumMark
	for _, m := range enumRE.FindAllStringSubmatchIndex(s, -1) {
		for g := 2; g <= 5; g++ {
			if m[2*g] < 0 {
				continue
			}
			v := s[m[2*g]:m[2*g+1]]
			ord, err := strconv.Atoi(v)
			if err != nil {
				ord = int(v[0]-'a') + 1
			}
			marks = append(marks, enumMark{style: g, ord: ord, start: m[2], end: m[1]})
		}
	}
	var best []enumMark
	for i, first := range marks {
		if first.ord != 1 {
			continue
		}
		chain := []enumMark{first}
		for _, m := range marks[i+1:] {
			if m.style == first.style && m.ord == chain[len(chain)-1].ord+1 {
				chain = append(chain, m)
			}
		}
		if len(chain) >= 2 {
			best = chain
			break
		}
	}
	if best == nil {
		return s, nil
	}
	var items []textBlock
	for i, m := range best {
		end := len(s)
		if i+1 < len(best) {
			end = best[i+1].start
		}
		marker := strings.TrimSpace(s[m.start:m.end])
		items = append(items, textBlock{kind: blockItem, marker: marker, text: trimJoiner(s[m.end:end])})
	}
	return strings.TrimSpace(s[:best[0].start]), items
}

// trimJoiner drops what tied an item to the next one: "foo; and" is "foo".
func trimJoiner(s string) string {
	s = strings.TrimSpace(s)
	for {
		t := strings.TrimRight(s, ";, ")
		for _, w := range []string{" and", " or", " then"} {
			t = strings.TrimSuffix(t, w)
		}
		if t == s {
			return s
		}
		s = t
	}
}

// richLines lays text out in width w, every line led by indent and drawn in
// style (headings add bold).
func richLines(text, indent, style string, w int) []styledLine {
	blocks := parseBlocks(strings.TrimSpace(text))
	var out []styledLine
	blank := func() {
		if len(out) > 0 && out[len(out)-1].text != "" {
			out = append(out, styledLine{})
		}
	}
	avail := max(w-utf8.RuneCountInString(indent), 1)
	for i := 0; i < len(blocks); i++ {
		b := blocks[i]
		switch b.kind {
		case blockBlank:
			blank()
		case blockHeading:
			blank()
			for _, l := range wrap(b.text, avail) {
				out = append(out, styledLine{indent + l, style + ansiBold})
			}
		case blockCode:
			out = append(out, styledLine{indent + "  " + cut(b.text, avail-2), ansiDim})
		case blockPara:
			if i > 0 && blocks[i-1].kind == blockItem {
				blank()
			}
			for _, l := range wrap(b.text, avail) {
				out = append(out, styledLine{indent + l, style})
			}
		case blockItem:
			j := i
			for j < len(blocks) && blocks[j].kind == blockItem {
				j++
			}
			lines, loose := itemLines(blocks[i:j], indent, style, avail)
			if loose && (i == 0 || blocks[i-1].kind != blockHeading) {
				blank()
			}
			out = append(out, lines...)
			i = j - 1
		}
	}
	for len(out) > 0 && out[len(out)-1].text == "" {
		out = out[:len(out)-1]
	}
	return out
}

// itemLines lays out one run of items. A list any of whose items wraps is
// loose: a blank line goes before it and between its top-level items.
func itemLines(items []textBlock, indent, style string, avail int) ([]styledLine, bool) {
	top := items[0].level
	markW := map[int]int{}
	for _, it := range items {
		top = min(top, it.level)
		markW[it.level] = max(markW[it.level], utf8.RuneCountInString(shownMarker(it)))
	}
	type laid struct {
		top   bool
		lines []string
	}
	var all []laid
	loose := false
	for _, it := range items {
		mark := shownMarker(it)
		if box, rest, ok := strings.Cut(it.text, "] "); ok && (box == "[ " || box == "[x" || box == "[X") {
			mark, it.text = "☐", rest
			if box != "[ " {
				mark = "☑"
			}
		}
		pad := strings.Repeat(" ", markW[it.level]-utf8.RuneCountInString(mark))
		lead := strings.Repeat("  ", it.level-top)
		if it.marker[0] >= '0' && it.marker[0] <= '9' {
			mark = pad + mark // numbers line up on the right
		} else {
			mark += pad
		}
		hang := lead + strings.Repeat(" ", markW[it.level]+1)
		if avail-utf8.RuneCountInString(hang) < 6 {
			// Too narrow to hang: the marker leads, the text runs under it.
			lead, hang = "", ""
		}
		var lines []string
		textW := max(avail-utf8.RuneCountInString(hang), 1)
		for k, l := range wrap(it.text, textW) {
			if k == 0 && hang == "" {
				lines = append(lines, cut(mark, avail))
				lines = append(lines, l)
				continue
			}
			if k == 0 {
				lines = append(lines, strings.TrimRight(lead+mark+" "+l, " "))
			} else {
				lines = append(lines, hang+l)
			}
		}
		loose = loose || len(lines) > 1
		all = append(all, laid{it.level == top, lines})
	}
	var out []styledLine
	for k, it := range all {
		if loose && k > 0 && it.top {
			out = append(out, styledLine{})
		}
		for _, l := range it.lines {
			out = append(out, styledLine{indent + l, style})
		}
	}
	return out, loose
}

// shownMarker is how an item's marker is drawn: bullets as •/◦, task boxes
// as ☐/☑, numbers and letters as written.
func shownMarker(it textBlock) string {
	switch it.marker {
	case "-", "*", "+", "•":
		if it.level > 0 {
			return "◦"
		}
		return "•"
	}
	return it.marker
}

// cut shortens s to n runes, marking the cut.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > max(n, 1) {
		return string(r[:max(n, 1)-1]) + "…"
	}
	return s
}
