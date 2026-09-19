package ui

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wmarquardt/hunk/internal/diff"
	"github.com/wmarquardt/hunk/internal/git"
)

// errSkippedStuckUndo means the top undo no longer applies (the index was
// mutated outside hunk) and was dropped so older entries stay reachable.
var errSkippedStuckUndo = errors.New("skipped stuck undo")

// wholeFile is the hunk index used to mark a file that has no hunks to choose
// between — a binary file, which git can only stage whole.
const wholeFile = -1

// marks is the set of hunks the user has picked, keyed by file index.
type marks map[int]map[int]bool

func (m marks) has(file, hunk int) bool { return m[file][hunk] }

func (m marks) set(file, hunk int, on bool) {
	if !on {
		delete(m[file], hunk)
		if len(m[file]) == 0 {
			delete(m, file)
		}
		return
	}
	if m[file] == nil {
		m[file] = map[int]bool{}
	}
	m[file][hunk] = true
}

func (m marks) inFile(file int) int { return len(m[file]) }

func (m marks) total() (hunks, files int) {
	for _, h := range m {
		hunks += len(h)
		files++
	}
	return hunks, files
}

// fileState describes how much of a file is marked, for the sidebar.
type fileState int

const (
	fileUnmarked fileState = iota
	filePartial
	fileAllMarked
)

func (m marks) state(file int, f diff.File) fileState {
	n := m.inFile(file)
	switch {
	case n == 0:
		return fileUnmarked
	case len(f.Hunks) == 0 || n >= len(f.Hunks):
		return fileAllMarked
	default:
		return filePartial
	}
}

func (s fileState) symbol() string {
	switch s {
	case fileAllMarked:
		return "●"
	case filePartial:
		return "◐"
	default:
		return "·"
	}
}

// GitSource reads the working tree: unstaged changes plus untracked files,
// rendered as one unified diff so review mode has nothing special about it.
// ignoreWS drops whitespace-only changes from the tracked diffs.
func GitSource(r *git.Repo, ignoreWS bool, context int) (string, error) {
	text, err := r.Diff(ignoreWS, context)
	if err != nil {
		return "", err
	}

	untracked, err := r.Untracked()
	if err != nil {
		return "", err
	}
	slices.Sort(untracked)

	var b strings.Builder
	b.WriteString(text)

	included := map[string]bool{}
	for _, path := range untracked {
		content, err := os.ReadFile(filepath.Join(r.Dir, path))
		if err != nil {
			// A file that vanished between listing and reading is not an error
			// worth aborting a review over.
			continue
		}
		included[path] = true
		b.WriteString(diff.NewFilePatch(path, content))
	}

	// A fully staged file has no unstaged changes, so it would drop off the
	// list. Keep it on screen by including its staged diff, so staging never
	// makes a file vanish — it just gets a check beside it.
	unstaged, err := r.UnstagedPaths()
	if err != nil {
		return "", err
	}
	for _, path := range unstaged {
		included[path] = true
	}
	staged, err := r.StagedPaths()
	if err != nil {
		return "", err
	}
	var stagedOnly []string
	for _, path := range staged {
		if !included[path] {
			stagedOnly = append(stagedOnly, path)
		}
	}
	stagedDiff, err := r.StagedDiff(stagedOnly, ignoreWS, context)
	if err != nil {
		return "", err
	}
	b.WriteString(stagedDiff)

	return b.String(), nil
}

// stageMarked writes the marked hunks to the index in one git call, then a
// second one for files that can only be staged whole.
//
// Only the index is written: no working-tree file is created, modified, or
// deleted, so an unwanted stage is undone with "git restore --staged".
func (m *Model) stageMarked() (string, error) {
	var patch strings.Builder
	var wholeFiles []string

	for fileIdx, hunks := range m.marks {
		f := m.files[fileIdx]

		if hunks[wholeFile] {
			wholeFiles = append(wholeFiles, f.Path())
			continue
		}

		patch.WriteString(f.Patch(slices.Sorted(maps.Keys(hunks))))
	}
	slices.Sort(wholeFiles)

	if err := m.repo.ApplyCached(patch.String()); err != nil {
		return "", err
	}
	if err := m.repo.StageFiles(wholeFiles); err != nil {
		return "", err
	}

	// Push this stage so "u" can reverse it without forgetting earlier ones.
	m.undoStack = append(m.undoStack, stageRecord{patch: patch.String(), whole: wholeFiles})

	hunks, files := m.marks.total()
	return fmt.Sprintf("staged %s in %s", plural(hunks, "hunk"), plural(files, "file")), nil
}

// stageRecord is one stageMarked call: a hunk patch and/or whole files.
type stageRecord struct {
	patch string
	whole []string
}

// unstageLast reverses the most recent stage: the hunk patch comes back out of
// the index, and any whole files staged alongside it are removed too. Earlier
// stages stay on the stack.
//
// Each half of the record is cleared as soon as it succeeds, then written back
// onto the stack. If UnapplyCached lands but UnstageFiles hits an index.lock
// race, the next undo retries only the files — not the already-reversed patch,
// which would fail permanently and block everything beneath.
//
// If the reverse fails for any other reason (another terminal reset the index,
// a hook rewrote it), the top entry is dropped so it cannot strand the rest of
// the stack. The next "u" then reaches the older undo.
func (m *Model) unstageLast() error {
	i := len(m.undoStack) - 1
	rec, err := consumeUnstage(m.undoStack[i], m.repo.UnapplyCached, m.repo.UnstageFiles)
	m.undoStack, err = applyUnstageResult(m.undoStack, rec, err)
	return err
}

func isIndexLock(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "index.lock")
}

// applyUnstageResult updates the undo stack after one consumeUnstage call.
// A clean success pops the entry. An index.lock keeps the remaining half for
// retry. Any other failure pops the stuck entry so older undos stay reachable.
func applyUnstageResult(stack []stageRecord, rec stageRecord, err error) ([]stageRecord, error) {
	i := len(stack) - 1
	if i < 0 {
		return stack, err
	}
	if err == nil {
		return stack[:i], nil
	}
	if isIndexLock(err) {
		stack[i] = rec
		return stack, err
	}
	return stack[:i], fmt.Errorf("%w: %s", errSkippedStuckUndo, firstLine(err.Error()))
}

// consumeUnstage runs unapply then unstage, clearing each field on success so a
// later retry does not reverse work that already landed. A failed second call
// returns the record with patch already empty.
func consumeUnstage(rec stageRecord, unapply func(string) error, unstage func([]string) error) (stageRecord, error) {
	if rec.patch != "" {
		if err := unapply(rec.patch); err != nil {
			return rec, err
		}
		rec.patch = ""
	}
	if len(rec.whole) > 0 {
		if err := unstage(rec.whole); err != nil {
			return rec, err
		}
		rec.whole = nil
	}
	return rec, nil
}

// markSnapshot captures a file's marks by content rather than by index, so they
// can be re-applied after the diff is rebuilt from a changed working tree.
type markSnapshot struct {
	whole bool            // the whole file was marked (A), so new hunks count too
	hunks map[string]bool // diff.Hunk.Key values that were marked
}

// snapshotMarks records the current marks keyed by file path and hunk content.
// A file with every hunk marked is recorded as whole, so an edit that adds a
// hunk keeps the file fully marked.
func (m *Model) snapshotMarks() map[string]markSnapshot {
	snap := map[string]markSnapshot{}
	for fileIdx, hunks := range m.marks {
		f := m.files[fileIdx]
		s := markSnapshot{hunks: map[string]bool{}}
		for hunkIdx := range hunks {
			if hunkIdx == wholeFile {
				s.whole = true
				continue
			}
			if hunkIdx < len(f.Hunks) {
				s.hunks[f.Hunks[hunkIdx].Key()] = true
			}
		}
		if len(f.Hunks) > 0 && len(s.hunks) == len(f.Hunks) {
			s.whole = true
		}
		snap[f.Path()] = s
	}
	return snap
}

// restoreMarks re-applies a snapshot to the freshly rebuilt files: a hunk whose
// content is unchanged keeps its mark, a whole-file mark re-applies to every
// current hunk, and a hunk that was edited (its key no longer matches) is left
// unmarked. It returns how many marks were dropped by edits, for the status line.
func (m *Model) restoreMarks(snap map[string]markSnapshot) (reset int) {
	m.marks = marks{}
	for idx, f := range m.files {
		s, ok := snap[f.Path()]
		if !ok {
			continue
		}
		if len(f.Hunks) == 0 {
			if s.whole {
				m.marks.set(idx, wholeFile, true)
			}
			continue
		}
		present := map[string]bool{}
		for hi, h := range f.Hunks {
			key := h.Key()
			present[key] = true
			if s.whole || s.hunks[key] {
				m.marks.set(idx, hi, true)
			}
		}
		if !s.whole {
			for key := range s.hunks {
				if !present[key] {
					reset++ // this marked hunk was edited or removed
				}
			}
		}
	}
	return reset
}

// liveReload re-reads the working tree and rebuilds the view while keeping the
// user's place: marks survive by content, the cursor stays on the same hunk,
// and only edited marks are dropped. Unlike reload, it never resets the screen.
func (m *Model) liveReload() {
	where := m.cursorIdentity()
	snap := m.snapshotMarks()

	text, err := GitSource(m.repo, m.ignoreWS, m.context)
	if err != nil {
		m.msg = "reload failed: " + firstLine(err.Error())
		return
	}
	files, err := diff.ParseString(text)
	if err != nil {
		m.msg = "reload failed: " + firstLine(err.Error())
		return
	}

	m.raw = files
	m.applyFilter()
	reset := m.restoreMarks(snap)
	m.rebuildView()
	m.refreshGitState()
	m.restoreCursor(where)

	if reset > 0 {
		m.msg = fmt.Sprintf("working tree changed — %s reset by new edits", plural(reset, "mark"))
	}
}

// reload re-reads the working tree after staging, so what is on screen is what
// is still unstaged.
func (m *Model) reload() error {
	text, err := GitSource(m.repo, m.ignoreWS, m.context)
	if err != nil {
		return err
	}
	files, err := diff.ParseString(text)
	if err != nil {
		return err
	}

	m.raw = files
	m.applyFilter()
	m.marks = marks{}
	m.rebuildView()
	m.cur, m.top = 0, 0
	m.refreshGitState()
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
