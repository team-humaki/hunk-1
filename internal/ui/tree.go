package ui

import (
	"slices"
	"strings"

	"github.com/wmarquardt/hunk/internal/diff"
)

// treeLine is one row of the sidebar's directory tree.
type treeLine struct {
	prefix string // the connectors drawn before the name: "│ " or "  " per ancestor, then "├─" or "└─"
	name   string // the last path segment
	path   string // the full path; for a directory, the key it collapses under
	depth  int
	file   int // index into the files; -1 for a directory
	// lo and hi are the [lo, hi) range of files a directory holds. The files are
	// in tree order, so every descendant sits in one contiguous run.
	lo, hi int
}

// comparePaths orders paths the way a tree walks them: at each level,
// directories (paths that keep going) before files (paths that end), then
// alphabetically. That keeps every subtree in one run and puts loose files
// after the directory spine, the way a file explorer does.
func comparePaths(a, b string) int {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		aLeaf, bLeaf := i == len(as)-1, i == len(bs)-1
		if aLeaf != bLeaf {
			if aLeaf {
				return 1
			}
			return -1
		}
		if c := strings.Compare(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return 0
}

// sortFiles puts files in tree order, so a file's index is also its position in
// the sidebar and ] / [ walk the files in the order they are drawn.
func sortFiles(files []diff.File) {
	slices.SortStableFunc(files, func(a, b diff.File) int { return comparePaths(a.Path(), b.Path()) })
}

// buildTree lays sorted files out as a tree: each directory on its own line, its
// children right below it, two columns per level. A directory in collapsed keeps
// its own line but hides everything under it.
//
// ponytail: rebuilt from the files on every render and click rather than cached,
// so filters, reloads and commit changes can never leave it stale. O(files) per
// frame; cache it on the model if a huge diff ever makes the sidebar lag.
func buildTree(files []diff.File, collapsed map[string]bool) []treeLine {
	segs := make([][]string, len(files))
	for i, f := range files {
		segs[i] = strings.Split(f.Path(), "/")
	}

	// underDir reports whether file j lives inside the directory dir.
	underDir := func(j int, dir []string) bool {
		return len(segs[j]) > len(dir) && slices.Equal(segs[j][:len(dir)], dir)
	}

	var out []treeLine
	var last []bool // last[d]: the most recent node at depth d is its parent's last child
	hidden := 0     // files below this index sit inside a collapsed directory
	emit := func(depth int, name string, file, lo, hi, next int, parent []string) {
		if lo < hidden {
			return
		}
		path := strings.Join(append(slices.Clip(parent), name), "/")
		if file < 0 && collapsed[path] {
			hidden = hi
		}
		isLast := next >= len(files) || !underDir(next, parent)
		last = append(last[:depth], isLast)
		var b strings.Builder
		for _, l := range last[:depth] {
			if l {
				b.WriteString("  ")
			} else {
				b.WriteString("│ ")
			}
		}
		if isLast {
			b.WriteString("└─")
		} else {
			b.WriteString("├─")
		}
		out = append(out, treeLine{prefix: b.String(), name: name, path: path, depth: depth, file: file, lo: lo, hi: hi})
	}

	for i, s := range segs {
		// Directories the previous file already opened are not drawn again.
		shared := 0
		if i > 0 {
			prev := segs[i-1]
			for shared < len(s)-1 && shared < len(prev)-1 && s[shared] == prev[shared] {
				shared++
			}
		}
		for d := shared; d < len(s)-1; d++ {
			hi := i + 1
			for hi < len(files) && underDir(hi, s[:d+1]) {
				hi++
			}
			emit(d, s[d], -1, i, hi, hi, s[:d])
		}
		emit(len(s)-1, s[len(s)-1], i, i, i+1, i+1, s[:len(s)-1])
	}
	return out
}

// treeIndexOf is the tree line that draws file, or the collapsed directory
// hiding it, or 0 when it is not there.
func treeIndexOf(tree []treeLine, file int) int {
	at := 0
	for i, l := range tree {
		if l.file == file {
			return i
		}
		if l.file < 0 && l.lo <= file && file < l.hi {
			at = i
		}
	}
	return at
}

// dirIndexOf is the line of the directory at path, or -1.
func dirIndexOf(tree []treeLine, path string) int {
	return slices.IndexFunc(tree, func(l treeLine) bool { return l.file < 0 && l.path == path })
}
