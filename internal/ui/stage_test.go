package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/wmarquardt/hunk/internal/diff"
	"github.com/wmarquardt/hunk/internal/git"
	"github.com/wmarquardt/hunk/internal/theme"
)

func TestMarksBookkeeping(t *testing.T) {
	m := marks{}

	m.set(0, 1, true)
	m.set(0, 2, true)
	m.set(3, 0, true)

	if !m.has(0, 1) || !m.has(3, 0) {
		t.Error("marks did not stick")
	}
	if m.has(0, 5) || m.has(9, 0) {
		t.Error("marks reports hunks that were never marked")
	}
	if hunks, files := m.total(); hunks != 3 || files != 2 {
		t.Errorf("total() = %d hunks in %d files, want 3 in 2", hunks, files)
	}

	m.set(0, 1, false)
	m.set(0, 2, false)
	if _, ok := m[0]; ok {
		t.Error("a file with no marks left should be dropped, not kept empty")
	}
	if hunks, files := m.total(); hunks != 1 || files != 1 {
		t.Errorf("total() = %d hunks in %d files, want 1 in 1", hunks, files)
	}
}

func TestFileState(t *testing.T) {
	f := diff.File{Hunks: make([]diff.Hunk, 3)}
	binary := diff.File{IsBinary: true}

	m := marks{}
	if got := m.state(0, f); got != fileUnmarked {
		t.Errorf("no marks: got %v, want unmarked", got)
	}

	m.set(0, 0, true)
	if got := m.state(0, f); got != filePartial {
		t.Errorf("one of three marked: got %v, want partial", got)
	}

	m.set(0, 1, true)
	m.set(0, 2, true)
	if got := m.state(0, f); got != fileAllMarked {
		t.Errorf("all marked: got %v, want fully marked", got)
	}

	m.set(1, wholeFile, true)
	if got := m.state(1, binary); got != fileAllMarked {
		t.Errorf("a marked hunkless file: got %v, want fully marked", got)
	}
}

// gitModel builds a model over a throwaway repo with the given working-tree
// edits already applied.
func gitModel(t *testing.T, committed, edited map[string]string) (*Model, *git.Repo) {
	return gitModelOpts(t, committed, edited, Options{})
}

// gitModelOpts is gitModel with explicit startup options.
func gitModelOpts(t *testing.T, committed, edited map[string]string, opts Options) (*Model, *git.Repo) {
	t.Helper()
	if !git.Available() {
		t.Skip("git is not on PATH")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "hunk test")
	for name, content := range committed {
		write(name, content)
	}
	run("add", "-A")
	run("commit", "-qm", "baseline")

	for name, content := range edited {
		write(name, content)
	}

	repo := &git.Repo{Dir: dir}
	text, err := GitSource(repo, false, diff.DefaultContext)
	if err != nil {
		t.Fatal(err)
	}
	files, err := diff.ParseString(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}

	m := NewGit(repo, files, theme.Default(), opts)
	t.Cleanup(func() { m.watch.Close() }) // stop the follow goroutine started by NewGit
	u, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	return u.(*Model), repo
}

func gitOut(t *testing.T, r *git.Repo, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line ")
		b.WriteString(strconvItoa(i))
		b.WriteString("\n")
	}
	return b.String()
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func replaceLine(content string, line int, to string) string {
	parts := strings.Split(content, "\n")
	parts[line-1] = to
	return strings.Join(parts, "\n")
}

func TestGitSourceIncludesUntrackedFiles(t *testing.T) {
	m, _ := gitModel(t,
		map[string]string{"tracked.txt": lines(10)},
		map[string]string{"tracked.txt": replaceLine(lines(10), 3, "CHANGED"), "brand-new.txt": "hello\n"},
	)

	var paths []string
	for _, f := range m.files {
		paths = append(paths, f.Path())
	}
	if !strings.Contains(strings.Join(paths, " "), "brand-new.txt") {
		t.Errorf("untracked file missing from review: %v", paths)
	}
	for _, f := range m.files {
		if f.Path() == "brand-new.txt" && !f.IsNew {
			t.Error("untracked file should be shown as a new file")
		}
	}
}

func TestStagingOneMarkedHunk(t *testing.T) {
	base := lines(60)
	edited := replaceLine(base, 5, "FIRST")
	edited = replaceLine(edited, 30, "SECOND")

	m, repo := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	if len(m.files) != 1 || len(m.files[0].Hunks) != 2 {
		t.Fatalf("expected one file with two hunks, got %d files", len(m.files))
	}

	// Land on the second hunk and mark it.
	m.moveTo(m.view.HunkRows[1])
	m.handleKey(keyPress(" "))
	if hunks, _ := m.marks.total(); hunks != 1 {
		t.Fatalf("space marked %d hunks, want 1", hunks)
	}

	// w stages immediately — no confirmation step.
	m.handleKey(keyPress("w"))
	if !strings.Contains(m.msg, "staged 1 hunk") {
		t.Errorf("stage message = %q", m.msg)
	}

	cached := gitOut(t, repo, "diff", "--cached")
	if !strings.Contains(cached, "SECOND") {
		t.Errorf("marked hunk was not staged:\n%s", cached)
	}
	if strings.Contains(cached, "FIRST") {
		t.Errorf("an unmarked hunk was staged:\n%s", cached)
	}
	if unstaged := gitOut(t, repo, "diff"); !strings.Contains(unstaged, "FIRST") {
		t.Errorf("the unmarked hunk should still be unstaged:\n%s", unstaged)
	}

	// The working tree is untouched: staging only ever writes the index.
	got, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Error("staging modified the working-tree file")
	}
}

func TestStagingReloadsAndClearsMarks(t *testing.T) {
	base := lines(60)
	edited := replaceLine(base, 5, "FIRST")
	edited = replaceLine(edited, 30, "SECOND")

	m, _ := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	m.moveTo(m.view.HunkRows[0])
	m.handleKey(keyPress(" "))
	m.handleKey(keyPress("w"))

	if hunks, _ := m.marks.total(); hunks != 0 {
		t.Errorf("%d marks survived staging", hunks)
	}
	if len(m.files) != 1 || len(m.files[0].Hunks) != 1 {
		t.Errorf("after staging one of two hunks, one should remain; got %d files", len(m.files))
	}
	if !strings.Contains(m.msg, "u to undo") {
		t.Errorf("the result should mention undo, got %q", m.msg)
	}
}

func TestFullyStagedFileStaysWithGreenCheck(t *testing.T) {
	base := lines(20)
	m, _ := gitModel(t,
		map[string]string{"a.txt": base},
		map[string]string{"a.txt": replaceLine(base, 3, "CHANGED")},
	)

	// Approve the whole file, then stage it.
	m.handleKey(keyPress("a"))
	m.handleKey(keyPress("w"))

	// The file has no unstaged changes left, yet it must not vanish.
	if len(m.files) != 1 || m.files[0].Path() != "a.txt" {
		t.Fatalf("fully staged file dropped off the list: %v", m.files)
	}
	if !m.staged["a.txt"] || m.unstaged["a.txt"] {
		t.Fatalf("git state wrong: staged=%v unstaged=%v", m.staged, m.unstaged)
	}
	symbol, style := m.fileGlyph(0, m.files[0], m.st.sidebar)
	if symbol != "✓" {
		t.Errorf("glyph = %q, want a check", symbol)
	}
	if style.GetForeground() != m.st.stagedFg {
		t.Errorf("a fully staged file's check should be green")
	}
}

func TestPartiallyStagedFileShowsGrayCheck(t *testing.T) {
	base := lines(60)
	edited := replaceLine(base, 5, "FIRST")
	edited = replaceLine(edited, 30, "SECOND")

	m, _ := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	// Stage only the first of two hunks.
	m.moveTo(m.view.HunkRows[0])
	m.handleKey(keyPress(" "))
	m.handleKey(keyPress("w"))

	if !m.staged["a.txt"] || !m.unstaged["a.txt"] {
		t.Fatalf("expected both staged and unstaged content: staged=%v unstaged=%v", m.staged, m.unstaged)
	}
	symbol, style := m.fileGlyph(0, m.files[0], m.st.sidebar)
	if symbol != "✓" {
		t.Errorf("glyph = %q, want a check", symbol)
	}
	if style.GetForeground() != m.st.partialFg {
		t.Errorf("a partially staged file's check should be gray, not green")
	}
}

func TestMarkedHunkShowsGreenRail(t *testing.T) {
	base := lines(30)
	edited := replaceLine(base, 5, "FIRST")
	edited = replaceLine(edited, 25, "SECOND")

	m, _ := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	// Mark the first hunk, then move the cursor onto the second one.
	m.moveTo(m.view.HunkRows[0] + 1)
	m.handleKey(keyPress(" "))
	m.moveTo(m.view.HunkRows[1] + 1)

	// A body row inside the marked (now non-current) hunk gets the green bar.
	row := m.view.HunkRows[0] + 1
	if row == m.cur {
		t.Fatal("test setup put the cursor on the row under test")
	}
	hlo, hhi := m.currentHunkSpan()
	if got := m.railMark(row, hlo, hhi); got != m.st.railMarked.Render("▌") {
		t.Errorf("marked hunk row rail = %q, want the green marked bar", got)
	}
}

func TestMarkedCurrentHunkStaysGreenUnderCursor(t *testing.T) {
	base := lines(30)
	m, _ := gitModel(t,
		map[string]string{"a.txt": base},
		map[string]string{"a.txt": replaceLine(base, 5, "CHANGED")},
	)

	// Mark the hunk and leave the cursor on it, so it is marked AND current.
	m.moveTo(m.view.HunkRows[0] + 1)
	m.handleKey(keyPress(" "))

	hlo, hhi := m.currentHunkSpan()
	// The cursor line of a marked hunk must read green, not the cursor or the
	// current-hunk accent — marked is what the user needs to see.
	if got := m.railMark(m.cur, hlo, hhi); got != m.st.railMarked.Render("▌") {
		t.Errorf("cursor row of a marked hunk = %q, want the green marked bar", got)
	}
	// A non-cursor row of the same hunk is green too.
	if got := m.railMark(hlo, hlo, hhi); got != m.st.railMarked.Render("▌") {
		t.Errorf("marked+current row = %q, want the green marked bar", got)
	}
}

func TestOptionsSetStartingState(t *testing.T) {
	m := New(nil, theme.Default(), Options{Unified: true, NoSidebar: true, IgnoreWS: true, Context: 7, ShowWS: true})
	if m.wantSplit {
		t.Error("Unified should open in unified, not split")
	}
	if m.wantSidebar {
		t.Error("NoSidebar should open with the sidebar hidden")
	}
	if !m.ignoreWS {
		t.Error("IgnoreWS should open with whitespace ignored")
	}
	if m.context != 7 {
		t.Errorf("Context = %d, want 7", m.context)
	}
	if !m.showWS {
		t.Error("ShowWS should open with whitespace visible")
	}
	// A zero Context falls back to the default rather than showing no context.
	if d := New(nil, theme.Default(), Options{}); d.context != diff.DefaultContext {
		t.Errorf("default context = %d, want %d", d.context, diff.DefaultContext)
	}
}

func TestContextKeysRediff(t *testing.T) {
	// A change in the middle of a long file, so context lines are plentiful.
	base := lines(40)
	m, _ := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": replaceLine(base, 20, "CHANGED")})

	if m.context != diff.DefaultContext {
		t.Fatalf("start context = %d, want %d", m.context, diff.DefaultContext)
	}
	wide := len(m.view.Rows)

	m.handleKey(keyPress("-"))
	if m.context != diff.DefaultContext-1 {
		t.Fatalf("- gave context %d", m.context)
	}
	if len(m.view.Rows) >= wide {
		t.Errorf("less context should mean fewer rows: %d -> %d", wide, len(m.view.Rows))
	}
	if !strings.Contains(m.msg, "context:") {
		t.Errorf("status = %q", m.msg)
	}

	m.handleKey(keyPress("+"))
	m.handleKey(keyPress("+"))
	if m.context != diff.DefaultContext+1 {
		t.Errorf("+ gave context %d, want %d", m.context, diff.DefaultContext+1)
	}
}

func TestNoFollowOpensPaused(t *testing.T) {
	base := "alpha\nbeta\n"
	m, _ := gitModelOpts(t, map[string]string{"a.txt": base},
		map[string]string{"a.txt": "alpha\nBETA\n"}, Options{NoFollow: true})
	if m.live {
		t.Error("NoFollow should open with live-follow paused")
	}
	if m.watch == nil {
		t.Error("the watcher should still start so f can resume it")
	}
}

func TestIgnoreWhitespaceHidesWhitespaceOnlyChange(t *testing.T) {
	base := "alpha\nbeta\ngamma\n"
	edited := "alpha\n    beta\ngamma\n" // only indentation changed

	m, _ := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	if len(m.files) != 1 {
		t.Fatalf("a whitespace change should show by default, got %d files", len(m.files))
	}

	m.handleKey(keyPress("i"))
	if !m.ignoreWS {
		t.Fatal("i did not turn on ignore-whitespace")
	}
	if len(m.files) != 0 {
		t.Errorf("whitespace-only change still shown with ignore-ws on: %v", m.files)
	}
	if !strings.Contains(m.msg, "ignoring whitespace") {
		t.Errorf("status = %q", m.msg)
	}

	m.handleKey(keyPress("i"))
	if len(m.files) != 1 {
		t.Errorf("turning ignore-ws back off should restore the change, got %d files", len(m.files))
	}
}

func TestUndoUnstagesTheLastStage(t *testing.T) {
	base := lines(60)
	edited := replaceLine(base, 5, "FIRST")
	edited = replaceLine(edited, 30, "SECOND")

	m, repo := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	// Stage the first hunk, then undo it.
	m.moveTo(m.view.HunkRows[0])
	m.handleKey(keyPress(" "))
	m.handleKey(keyPress("w"))
	if cached := gitOut(t, repo, "diff", "--cached"); !strings.Contains(cached, "FIRST") {
		t.Fatalf("w did not stage the hunk:\n%s", cached)
	}
	if !strings.Contains(m.msg, "u to undo") {
		t.Errorf("stage message should mention undo, got %q", m.msg)
	}

	m.handleKey(keyPress("u"))
	if cached := gitOut(t, repo, "diff", "--cached"); strings.TrimSpace(cached) != "" {
		t.Errorf("undo left something staged:\n%s", cached)
	}
	if !strings.Contains(m.msg, "undone") {
		t.Errorf("undo message = %q", m.msg)
	}
	// Both hunks are unstaged again, so the file shows both.
	if len(m.files) != 1 || len(m.files[0].Hunks) != 2 {
		t.Errorf("after undo the file should have both hunks back; got %v", m.files)
	}

	// A second undo has nothing to reverse.
	m.handleKey(keyPress("u"))
	if !strings.Contains(m.msg, "nothing to undo") {
		t.Errorf("second undo message = %q", m.msg)
	}
}

// Each stage is its own undo entry. Stage twice, undo twice: the first "u"
// reverses only the second stage, the next "u" reverses the first, and a
// third "u" has nothing left.
func TestConsumeUnstageClearsPatchBeforeWholeFilesFail(t *testing.T) {
	rec := stageRecord{patch: "PATCH", whole: []string{"a.bin"}}
	unapplyN, unstageN := 0, 0
	got, err := consumeUnstage(rec,
		func(p string) error {
			unapplyN++
			if p != "PATCH" {
				t.Fatalf("unapply %q", p)
			}
			return nil
		},
		func(_ []string) error {
			unstageN++
			return fmt.Errorf("index.lock")
		},
	)
	if err == nil || err.Error() != "index.lock" {
		t.Fatalf("err = %v, want index.lock", err)
	}
	if got.patch != "" {
		t.Errorf("patch still %q after a successful unapply; retry would reverse it again", got.patch)
	}
	if len(got.whole) != 1 || got.whole[0] != "a.bin" {
		t.Errorf("whole = %v, want the files still pending", got.whole)
	}

	// Retry: unapply must not run again; unstage succeeds and clears the rest.
	got, err = consumeUnstage(got,
		func(string) error {
			t.Fatal("unapply retried after the patch was already reversed")
			return nil
		},
		func(paths []string) error {
			unstageN++
			if len(paths) != 1 || paths[0] != "a.bin" {
				t.Fatalf("unstage %v", paths)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.patch != "" || got.whole != nil {
		t.Errorf("finished record = %+v, want empty", got)
	}
	if unapplyN != 1 || unstageN != 2 {
		t.Errorf("calls unapply=%d unstage=%d, want 1 and 2", unapplyN, unstageN)
	}
}

func TestUndoReversesEachStageInOrder(t *testing.T) {
	base := lines(60)
	edited := replaceLine(base, 5, "FIRST")
	edited = replaceLine(edited, 30, "SECOND")

	m, repo := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	m.moveTo(m.view.HunkRows[0])
	m.handleKey(keyPress(" "))
	m.handleKey(keyPress("w"))
	if cached := gitOut(t, repo, "diff", "--cached"); !strings.Contains(cached, "FIRST") {
		t.Fatalf("first stage missed FIRST:\n%s", cached)
	}

	if len(m.files) != 1 || len(m.files[0].Hunks) != 1 {
		t.Fatalf("after first stage want 1 remaining hunk, got %d files", len(m.files))
	}
	m.moveTo(m.view.HunkRows[0])
	m.handleKey(keyPress(" "))
	m.handleKey(keyPress("w"))
	cached := gitOut(t, repo, "diff", "--cached")
	if !strings.Contains(cached, "FIRST") || !strings.Contains(cached, "SECOND") {
		t.Fatalf("both hunks should be staged:\n%s", cached)
	}

	m.handleKey(keyPress("u"))
	cached = gitOut(t, repo, "diff", "--cached")
	if strings.Contains(cached, "SECOND") {
		t.Errorf("first undo should unstage SECOND only:\n%s", cached)
	}
	if !strings.Contains(cached, "FIRST") {
		t.Errorf("first undo should leave FIRST staged:\n%s", cached)
	}

	m.handleKey(keyPress("u"))
	if cached := gitOut(t, repo, "diff", "--cached"); strings.TrimSpace(cached) != "" {
		t.Errorf("second undo left something staged:\n%s", cached)
	}

	m.handleKey(keyPress("u"))
	if !strings.Contains(m.msg, "nothing to undo") {
		t.Errorf("third undo message = %q", m.msg)
	}
}

func TestStagingNothingMarkedAsksForAMark(t *testing.T) {
	base := lines(20)
	m, repo := gitModel(t,
		map[string]string{"a.txt": base},
		map[string]string{"a.txt": replaceLine(base, 3, "CHANGED")},
	)

	m.handleKey(keyPress("w"))
	if !strings.Contains(m.msg, "nothing marked") {
		t.Errorf("message = %q", m.msg)
	}
	if cached := gitOut(t, repo, "diff", "--cached"); strings.TrimSpace(cached) != "" {
		t.Errorf("nothing was marked but the index changed:\n%s", cached)
	}
}

func TestMarkWholeFileKeys(t *testing.T) {
	base := lines(60)
	edited := replaceLine(base, 5, "FIRST")
	edited = replaceLine(edited, 30, "SECOND")

	m, _ := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	m.handleKey(keyPress("a"))
	if got := m.marks.inFile(0); got != 2 {
		t.Errorf("a marked %d hunks, want both", got)
	}
	if got := m.marks.state(0, m.files[0]); got != fileAllMarked {
		t.Errorf("file state = %v, want fully marked", got)
	}

	m.handleKey(keyPress("d"))
	if hunks, _ := m.marks.total(); hunks != 0 {
		t.Errorf("d left %d marks", hunks)
	}
}

// On a folder in the sidebar, a and d mark and unmark every file under it —
// the same keys as on a file, one level up. Space stays the fold toggle.
func TestMarkKeysOnAFolder(t *testing.T) {
	base := lines(20)
	edited := replaceLine(base, 5, "CHANGED")
	m, _ := gitModel(t,
		map[string]string{"dir/a.txt": base, "dir/b.txt": base, "top.txt": base},
		map[string]string{"dir/a.txt": edited, "dir/b.txt": edited, "top.txt": edited})
	screen(t, m, 140, 20)

	// The tree opens on dir/a.txt; k steps up onto the dir/ row above it.
	m.command("ctrl+w")
	m.command("k")
	if m.treeDir != "dir" {
		t.Fatalf("k selected %q, want the dir/ row", m.treeDir)
	}

	m.command("a")
	if hunks, files := m.marks.total(); hunks != 2 || files != 2 {
		t.Errorf("a on dir/ marked %d hunks in %d files, want 2 in 2", hunks, files)
	}
	if m.marks.inFile(indexOfPath(t, m, "top.txt")) != 0 {
		t.Error("a on dir/ marked a file outside it")
	}

	m.command("d")
	if hunks, _ := m.marks.total(); hunks != 0 {
		t.Errorf("d on dir/ left %d marks, want the folder cleared", hunks)
	}

	// Space on the folder folds it instead of marking anything.
	m.command("space")
	if !m.collapsed["dir"] {
		t.Error("space on dir/ did not fold it")
	}
	if hunks, _ := m.marks.total(); hunks != 0 {
		t.Errorf("space on dir/ marked %d hunks, want none", hunks)
	}
}

func indexOfPath(t *testing.T, m *Model, path string) int {
	t.Helper()
	for i, f := range m.files {
		if f.Path() == path {
			return i
		}
	}
	t.Fatalf("no file %q in %v", path, m.files)
	return -1
}

func TestMarkKeysDoNothingWithoutARepo(t *testing.T) {
	m := newTestModel(t, sample)
	screen(t, m, 140, 20)

	for _, k := range []string{" ", "a", "d", "w", "u"} {
		m.handleKey(keyPress(k))
	}
	if m.marks != nil {
		t.Error("a plain diff should not accumulate marks")
	}
}

func TestStatusBarShowsMarkCount(t *testing.T) {
	base := lines(20)
	m, _ := gitModel(t,
		map[string]string{"a.txt": base},
		map[string]string{"a.txt": replaceLine(base, 3, "CHANGED")},
	)

	m.moveTo(m.view.HunkRows[0])
	m.handleKey(keyPress(" "))

	out := strings.Join(screen(t, m, 140, 20), "\n")
	if !strings.Contains(out, "1 hunk marked") {
		t.Errorf("status bar does not show the mark count:\n%s", out)
	}
}

// space approves a hunk and moves to the next one in the same file, and stops
// at the file's last hunk rather than jumping into the next file.
func TestSpaceAdvancesWithinTheFile(t *testing.T) {
	base := lines(40)
	twoHunks := replaceLine(replaceLine(base, 5, "ONE"), 35, "TWO")
	m, _ := gitModel(t, map[string]string{"a.txt": base, "b.txt": base},
		map[string]string{"a.txt": twoHunks, "b.txt": replaceLine(base, 5, "OTHER")})
	press := pacedSpace(m)

	m.moveTo(m.view.HunkRows[0] + 1)
	press()
	if got := m.cur; got != m.view.HunkRows[1] {
		t.Fatalf("space left the cursor at row %d, want the next hunk at %d", got, m.view.HunkRows[1])
	}

	// The file's last hunk: marking it keeps the cursor in this file.
	press()
	if file := m.view.Rows[m.cur].FileIdx; file != 0 {
		t.Errorf("space on the last hunk jumped to file %d, want to stay on 0", file)
	}

	// Unmarking stays put, so you can see what you took back.
	at := m.cur
	press()
	if m.cur != at {
		t.Errorf("unmarking moved the cursor from %d to %d", at, m.cur)
	}
	if hunks, _ := m.marks.total(); hunks != 1 {
		t.Errorf("%d hunks marked after mark, mark, unmark, want 1", hunks)
	}
}

// A held space bar repeats at machine speed. Because marking moves the cursor
// on, every repeat would land on a fresh hunk and approve the whole file.
func TestHeldSpaceMarksOnlyOneHunk(t *testing.T) {
	base := lines(60)
	edited := replaceLine(replaceLine(replaceLine(base, 5, "A"), 30, "B"), 55, "C")
	m, _ := gitModel(t, map[string]string{"a.txt": base}, map[string]string{"a.txt": edited})

	now := time.Now()
	m.clock = func() time.Time { return now }

	m.moveTo(m.view.HunkRows[0] + 1)
	for i := 0; i < 8; i++ { // one press, eight repeats 30ms apart
		m.handleKey(keyPress(" "))
		now = now.Add(30 * time.Millisecond)
	}
	if hunks, _ := m.marks.total(); hunks != 1 {
		t.Errorf("a held space bar marked %d hunks, want 1", hunks)
	}

	// The same hunk is never blocked: taking the mark back is a decision, not
	// a repeat, however fast it comes.
	m.moveTo(m.view.HunkRows[0] + 1)
	m.handleKey(keyPress(" "))
	if hunks, _ := m.marks.total(); hunks != 0 {
		t.Errorf("%d hunks still marked after unmarking, want 0", hunks)
	}

	// Once the repeat window has passed, the next press marks again.
	now = now.Add(2 * markRepeat)
	m.handleKey(keyPress(" "))
	if hunks, _ := m.marks.total(); hunks != 1 {
		t.Errorf("%d hunks marked after the window passed, want 1", hunks)
	}
}

// pacedSpace presses space with enough time between presses that none of them
// looks like the key repeating.
func pacedSpace(m *Model) func() {
	now := time.Now()
	m.clock = func() time.Time { return now }
	return func() {
		m.handleKey(keyPress(" "))
		now = now.Add(2 * markRepeat)
	}
}

// An untracked file with no content still has to reach the review. It produces
// no hunks, so like a binary file it can only be staged whole — but if the
// patch for it comes back empty the file never appears at all and there is no
// way to stage it from hunk.
func TestEmptyUntrackedFileIsReviewableAndStageable(t *testing.T) {
	m, repo := gitModel(t,
		map[string]string{"tracked.txt": lines(10)},
		map[string]string{"empty.txt": ""},
	)

	idx := -1
	for i, f := range m.files {
		if f.Path() == "empty.txt" {
			idx = i
		}
	}
	if idx < 0 {
		var paths []string
		for _, f := range m.files {
			paths = append(paths, f.Path())
		}
		t.Fatalf("the empty untracked file is missing from the review: %v", paths)
	}
	if !m.files[idx].IsNew {
		t.Error("the empty file should be shown as a new file")
	}
	if len(m.files[idx].Hunks) != 0 {
		t.Errorf("got %d hunks, want 0: an empty file has no lines", len(m.files[idx].Hunks))
	}

	// A file with no hunks is marked whole, the same path binary files take.
	m.marks = marks{}
	m.marks.set(idx, wholeFile, true)
	if _, err := m.stageMarked(); err != nil {
		t.Fatalf("staging the empty file: %v", err)
	}

	cached := gitOut(t, repo, "diff", "--cached", "--name-only")
	if !strings.Contains(cached, "empty.txt") {
		t.Errorf("the empty file did not reach the index:\n%s", cached)
	}

	// Staging writes the index only; the file is still there and still empty.
	got, err := os.ReadFile(filepath.Join(repo.Dir, "empty.txt"))
	if err != nil {
		t.Fatalf("staging removed the working-tree file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("staging wrote %q into the working-tree file", got)
	}
}

// f pauses and resumes live-follow. Resuming has to catch the screen up on
// whatever changed while it was paused, which is why it reloads immediately
// instead of waiting for the next filesystem event.
func TestToggleFollowPausesAndResumes(t *testing.T) {
	base := lines(20)
	m, repo := gitModel(t, map[string]string{"a.txt": base}, nil)

	if !m.live {
		t.Fatal("git review mode should open following the tree")
	}

	if cmd := m.toggleFollow(); cmd != nil {
		t.Error("pausing should not re-arm the watcher")
	}
	if m.live {
		t.Error("live = true after pausing")
	}
	if !strings.Contains(m.msg, "paused") {
		t.Errorf("status = %q, want it to say following is paused", m.msg)
	}

	// Change the tree while paused; the view must not have caught up yet.
	if err := os.WriteFile(filepath.Join(repo.Dir, "a.txt"), []byte(replaceLine(base, 3, "WHILE_PAUSED")), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(m.files) != 0 {
		t.Error("a paused view picked up a change on its own")
	}

	cmd := m.toggleFollow()
	if !m.live {
		t.Error("live = false after resuming")
	}
	if cmd == nil {
		t.Error("resuming did not re-arm the watcher")
	}
	if len(m.files) != 1 {
		t.Fatalf("resuming loaded %d files, want the one that changed", len(m.files))
	}
	var body strings.Builder
	for _, h := range m.files[0].Hunks {
		for _, l := range h.Lines {
			body.WriteString(l.Text)
		}
	}
	if !strings.Contains(body.String(), "WHILE_PAUSED") {
		t.Errorf("resuming did not catch up on the change made while paused:\n%s", body.String())
	}
}

// Without a watcher there is nothing to follow, so the key is inert — a plain
// diff from a pipe must not pretend to be live.
func TestToggleFollowWithoutAWatcher(t *testing.T) {
	m := newTestModel(t, sample)
	if cmd := m.toggleFollow(); cmd != nil {
		t.Error("toggleFollow returned a command without a watcher")
	}
	if m.live {
		t.Error("a plain diff should never be live")
	}
}
