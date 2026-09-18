package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// runSearch drives the / prompt: open, type the query, press enter.
func runSearch(t *testing.T, m *Model, query string) {
	t.Helper()
	m.handleKey(keyPress("/"))
	if !m.searchInput {
		t.Fatal("/ did not open the search prompt")
	}
	for _, r := range query {
		m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestMatchTextSmartcase(t *testing.T) {
	if !matchText("Hello World", "world") {
		t.Error("lowercase query should match case-insensitively")
	}
	if matchText("Hello World", "World!") {
		t.Error("query with uppercase should match exactly (no match here)")
	}
	if !matchText("Hello World", "World") {
		t.Error("uppercase query should match the exact case")
	}
	if matchText("anything", "") {
		t.Error("empty query should never match")
	}
}

const searchDiff = "diff --git a/x b/x\n--- a/x\n+++ b/x\n" +
	"@@ -1,1 +1,4 @@\n keep\n+alpha needle\n+beta\n+gamma needle\n"

func TestSearchJumpsAndRepeats(t *testing.T) {
	m := newTestModel(t, searchDiff)
	screen(t, m, 120, 30)

	runSearch(t, m, "needle")
	if m.search != "needle" || m.searchInput {
		t.Fatalf("after enter: search=%q input=%v", m.search, m.searchInput)
	}
	first := m.cur
	if !strings.Contains(m.rowSearchText(first), "needle") {
		t.Fatalf("cursor did not land on a match: %q", m.rowSearchText(first))
	}

	m.command("n") // next match
	second := m.cur
	if second == first || !strings.Contains(m.rowSearchText(second), "needle") {
		t.Fatalf("n did not advance to the next match: %d -> %d", first, second)
	}

	m.command("n") // wraps back to the first
	if m.cur != first {
		t.Errorf("n did not wrap around: got %d, want %d", m.cur, first)
	}

	m.command("N") // previous match
	if m.cur != second {
		t.Errorf("N did not go back: got %d, want %d", m.cur, second)
	}
}

func TestSearchNoMatchKeepsCursor(t *testing.T) {
	m := newTestModel(t, searchDiff)
	screen(t, m, 120, 30)
	m.moveTo(2)
	before := m.cur

	runSearch(t, m, "zzzznope")
	if m.cur != before {
		t.Errorf("a failed search moved the cursor: %d -> %d", before, m.cur)
	}
	if !strings.Contains(m.msg, "no match") {
		t.Errorf("status = %q, want a no-match message", m.msg)
	}
}

func TestEscClearsSearchSoNReturnsToHunkNav(t *testing.T) {
	m := newTestModel(t, searchDiff)
	screen(t, m, 120, 30)
	runSearch(t, m, "needle")

	m.command("esc")
	if m.search != "" {
		t.Fatalf("esc did not clear the search: %q", m.search)
	}
	// With no active search, n is next-hunk again.
	m.moveTo(0)
	m.command("n")
	if m.cur == 0 {
		t.Error("with search cleared, n should move to the next hunk")
	}
}

func focusCommitsPanel(t *testing.T, m *Model) {
	t.Helper()
	m.command("ctrl+w")
	if m.panelFocus() != focusCommits {
		t.Fatalf("ctrl+w did not focus the commits panel: got %v", m.panelFocus())
	}
}

func TestCommitSearchMatchesSubject(t *testing.T) {
	m, _ := logModel(t)
	screen(t, m, 140, 24)
	focusCommitsPanel(t, m)

	runSearch(t, m, "two")
	if m.commitSearch != "two" || m.search != "" {
		t.Fatalf("commit search clobbered the other query: commitSearch=%q search=%q", m.commitSearch, m.search)
	}
	if m.commitIdx != 1 || m.commits[m.commitIdx].Subject != "add two to a" {
		t.Fatalf("subject search landed on commit %d %q, want 'add two to a'", m.commitIdx, m.commits[m.commitIdx].Subject)
	}
}

func TestCommitSearchMatchesAuthorAndWraps(t *testing.T) {
	m, _ := logModel(t)
	screen(t, m, 140, 24)
	focusCommitsPanel(t, m)

	runSearch(t, m, "hunk test")
	if m.commitIdx != 0 {
		t.Fatalf("author search started at commit %d, want 0 (inclusive)", m.commitIdx)
	}

	m.command("n")
	if m.commitIdx != 1 {
		t.Fatalf("n did not advance to the next author match: got %d", m.commitIdx)
	}
	m.command("n")
	if m.commitIdx != 2 {
		t.Fatalf("n did not advance to the last author match: got %d", m.commitIdx)
	}
	m.command("n")
	if m.commitIdx != 0 {
		t.Fatalf("n did not wrap around: got %d, want 0", m.commitIdx)
	}
	m.command("N")
	if m.commitIdx != 2 {
		t.Fatalf("N did not wrap backwards: got %d, want 2", m.commitIdx)
	}
}

func TestCommitSearchSmartcase(t *testing.T) {
	m, _ := logModel(t)
	screen(t, m, 140, 24)
	focusCommitsPanel(t, m)

	runSearch(t, m, "Two")
	if m.commitIdx != 0 {
		t.Errorf("uppercase query moved the cursor: landed on %d", m.commitIdx)
	}
	if !strings.Contains(m.msg, "no match") {
		t.Errorf("status = %q, want a no-match message", m.msg)
	}

	runSearch(t, m, "two")
	if m.commitIdx != 1 {
		t.Fatalf("lowercase query did not match the subject: got %d", m.commitIdx)
	}
}

func TestCommitSearchNoMatchKeepsCommit(t *testing.T) {
	m, _ := logModel(t)
	screen(t, m, 140, 24)
	focusCommitsPanel(t, m)
	m.loadCommit(1)
	before := m.commitIdx

	runSearch(t, m, "zzzznope")
	if m.commitIdx != before {
		t.Errorf("a failed search moved the commit: %d -> %d", before, m.commitIdx)
	}
	if !strings.Contains(m.msg, "no match") {
		t.Errorf("status = %q, want a no-match message", m.msg)
	}
}

func TestCommitAndDiffSearchStayIndependent(t *testing.T) {
	m, _ := logModel(t)
	screen(t, m, 140, 24)

	runSearch(t, m, "hello")
	if m.search != "hello" || m.commitSearch != "" {
		t.Fatalf("diff search leaked: search=%q commitSearch=%q", m.search, m.commitSearch)
	}
	diffAt := m.cur

	focusCommitsPanel(t, m)
	runSearch(t, m, "two")
	if m.commitSearch != "two" || m.search != "hello" {
		t.Fatalf("commit search clobbered the diff query: search=%q commitSearch=%q", m.search, m.commitSearch)
	}
	if m.commitIdx != 1 {
		t.Fatalf("commit search landed on %d, want 1", m.commitIdx)
	}

	m.focus = focusDiff
	if !strings.Contains(m.rowSearchText(diffAt), "hello") && m.search != "hello" {
		t.Fatalf("diff search was lost after a commit search")
	}
	m.command("n")
	if m.search != "hello" {
		t.Fatalf("n on the diff panel used the commit query: search=%q", m.search)
	}
}

func TestNOnCommitsWithoutQueryDoesNotSearchDiff(t *testing.T) {
	m, _ := logModel(t)
	screen(t, m, 140, 24)
	runSearch(t, m, "hello")
	diffAt := m.cur
	focusCommitsPanel(t, m)
	commitAt := m.commitIdx

	m.command("n")
	if m.cur != diffAt {
		t.Errorf("n on commits with an empty query moved the diff cursor: %d -> %d", diffAt, m.cur)
	}
	if m.commitIdx != commitAt {
		t.Errorf("n on commits with an empty query moved the commit: %d -> %d", commitAt, m.commitIdx)
	}
}

func TestSearchEnterUsesPanelCapturedAtSlash(t *testing.T) {
	m, _ := logModel(t)
	screen(t, m, 140, 24)
	focusCommitsPanel(t, m)

	m.handleKey(keyPress("/"))
	if m.searchPanel != focusCommits {
		t.Fatalf("slash captured panel %v, want commits", m.searchPanel)
	}
	for _, r := range "two" {
		m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	// A resize during typing can change panelFocus(); Enter must still use
	// the panel from when / was pressed.
	m.focus = focusDiff
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.commitSearch != "two" || m.search != "" {
		t.Fatalf("enter followed live focus: commitSearch=%q search=%q", m.commitSearch, m.search)
	}
	if m.commitIdx != 1 {
		t.Fatalf("commit search landed on %d, want 1", m.commitIdx)
	}
}

func TestSearchWrapInclusiveAndWraps(t *testing.T) {
	hits := map[int]bool{1: true, 3: true}
	match := func(i int) bool { return hits[i] }
	if got := searchWrap(4, 1, 1, false, match); got != 3 {
		t.Errorf("next from 1 exclusive = %d, want 3", got)
	}
	if got := searchWrap(4, 3, 1, false, match); got != 1 {
		t.Errorf("wrap from 3 = %d, want 1", got)
	}
	if got := searchWrap(4, 1, 1, true, match); got != 1 {
		t.Errorf("inclusive from a hit = %d, want 1", got)
	}
	if got := searchWrap(4, 0, 1, true, func(int) bool { return false }); got != -1 {
		t.Errorf("no match = %d, want -1", got)
	}
}
