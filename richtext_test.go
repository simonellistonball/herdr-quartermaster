package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func richText(text string, w int) string {
	return strings.Join(texts(richLines(text, "  ", "", w)), "\n")
}

func TestRichLinesMarkdown(t *testing.T) {
	got := richText("Intro line.\n\n## Acceptance\n- first\n- second item that is long enough to wrap onto another line\n  - nested\n\nAfter.", 40)
	want := `  Intro line.

  Acceptance
  • first

  • second item that is long enough to
    wrap onto another line
    ◦ nested

  After.`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRichLinesTightListAndNumbers(t *testing.T) {
	got := richText("Steps:\n1. one\n2. two\n10. ten", 80)
	want := "  Steps\n   1. one\n   2. two\n  10. ten"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRichLinesInlineEnumeration(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Verified: (1) logs clean; (2) 429 after 30; and (3) only 3 mails.",
			"  Verified:\n  (1) logs clean\n  (2) 429 after 30\n  (3) only 3 mails."},
		{"QUESTION: which? Options: 1) you run it 2) I prepare it",
			"  QUESTION: which? Options:\n  1) you run it\n  2) I prepare it"},
		{"(a) first (b) second", "  (a) first\n  (b) second"},
		{"RUN: 1. Policy edit. 2. Check client.", "  RUN:\n  1. Policy edit.\n  2. Check client."},
		// Not an enumeration: a lone marker, a call, versions, bare letters.
		{"step (1) only, f(1) f(2), v1.7.27, see a) b)", "  step (1) only, f(1) f(2), v1.7.27, see a) b)"},
		{"count 2) then 3)", "  count 2) then 3)"},
	} {
		if got := richText(tc.in, 80); got != tc.want {
			t.Errorf("%q:\ngot\n%s\nwant\n%s", tc.in, got, tc.want)
		}
	}
}

func TestRichLinesNestedEnumerationInItem(t *testing.T) {
	got := richText("- pick: (a) this (b) that", 80)
	want := "  • pick:\n    (a) this\n    (b) that"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRichLinesCodeAndTasks(t *testing.T) {
	got := richText("```\nkubectl get pods -A\n```\n- [ ] todo\n- [x] done\n**bold** text", 80)
	want := "    kubectl get pods -A\n  ☐ todo\n  ☑ done\n\n  bold text"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRichLinesFitsWidth(t *testing.T) {
	text := "Intro (1) " + strings.Repeat("word ", 30) + "(2) https://example.com/" + strings.Repeat("x", 80) +
		"\n```\n" + strings.Repeat("c", 90) + "\n```\n## " + strings.Repeat("Heading ", 10)
	for _, w := range []int{12, 20, 33, 80} {
		for _, l := range richLines(text, "  ", "", w) {
			if n := utf8.RuneCountInString(l.text); n > w {
				t.Errorf("width %d: %d-rune line %q", w, n, l.text)
			}
		}
	}
}
