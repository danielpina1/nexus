package vault

import (
	"fmt"
	"strings"
)

// Line-based diff (Myers), unified diff rendering and a diff3-style
// three-way merge. Used by History (diffs between versions) and by
// conflict handling (merging a stale write with the newer version).

type editKind int8

const (
	editEqual editKind = iota
	editDelete
	editInsert
)

type edit struct {
	kind editKind
	a, b int // line index in a (equal/delete) and b (equal/insert)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines returns the shortest edit script from a to b.
func diffLines(a, b []string) []edit {
	n, m := len(a), len(b)
	// Trim common prefix/suffix: cheap and makes the O(ND) part small.
	pre := 0
	for pre < n && pre < m && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < n-pre && suf < m-pre && a[n-1-suf] == b[m-1-suf] {
		suf++
	}
	var out []edit
	for i := 0; i < pre; i++ {
		out = append(out, edit{editEqual, i, i})
	}
	mid := myers(a[pre:n-suf], b[pre:m-suf])
	for _, e := range mid {
		e.a += pre
		e.b += pre
		out = append(out, e)
	}
	for i := 0; i < suf; i++ {
		out = append(out, edit{editEqual, n - suf + i, m - suf + i})
	}
	return out
}

func myers(a, b []string) []edit {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	max := n + m
	off := max
	v := make([]int, 2*max+2)
	var trace [][]int
	found := false
	for d := 0; d <= max && !found; d++ {
		cp := make([]int, len(v))
		copy(cp, v)
		trace = append(trace, cp)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	// Backtrack.
	var rev []edit
	x, y := n, m
	for d := len(trace) - 1; d >= 0 && (x > 0 || y > 0); d-- {
		vv := trace[d]
		k := x - y
		var pk int
		if k == -d || (k != d && vv[off+k-1] < vv[off+k+1]) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := vv[off+pk]
		py := px - pk
		for x > px && y > py {
			x--
			y--
			rev = append(rev, edit{editEqual, x, y})
		}
		if d > 0 {
			if x == px {
				y--
				rev = append(rev, edit{editInsert, x, y})
			} else {
				x--
				rev = append(rev, edit{editDelete, x, y})
			}
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, edit{editEqual, x, y})
	}
	out := make([]edit, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out
}

// UnifiedDiff renders a unified diff (3 lines of context) between two texts.
// It returns "" when they are equal.
func UnifiedDiff(fromName, toName, a, b string) string {
	if a == b {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	edits := diffLines(al, bl)
	const ctx = 3
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", fromName, toName)
	i := 0
	for i < len(edits) {
		// Find the next change.
		for i < len(edits) && edits[i].kind == editEqual {
			i++
		}
		if i >= len(edits) {
			break
		}
		start := max(0, i-ctx)
		end := i
		// Extend the hunk while changes are within 2*ctx lines.
		for end < len(edits) {
			if edits[end].kind != editEqual {
				end++
				continue
			}
			run := end
			for run < len(edits) && edits[run].kind == editEqual {
				run++
			}
			if run >= len(edits) || run-end > 2*ctx {
				end = min(len(edits), end+ctx)
				break
			}
			end = run
		}
		hunk := edits[start:end]
		aStart, bStart, aLen, bLen := -1, -1, 0, 0
		for _, e := range hunk {
			switch e.kind {
			case editEqual:
				if aStart < 0 {
					aStart = e.a
				}
				if bStart < 0 {
					bStart = e.b
				}
				aLen++
				bLen++
			case editDelete:
				if aStart < 0 {
					aStart = e.a
				}
				aLen++
			case editInsert:
				if bStart < 0 {
					bStart = e.b
				}
				bLen++
			}
		}
		if aStart < 0 {
			aStart = hunkPos(edits, start, true)
		}
		if bStart < 0 {
			bStart = hunkPos(edits, start, false)
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", rangeStr(aStart, aLen), rangeStr(bStart, bLen))
		for _, e := range hunk {
			var line string
			switch e.kind {
			case editEqual:
				line = " " + al[e.a]
			case editDelete:
				line = "-" + al[e.a]
			case editInsert:
				line = "+" + bl[e.b]
			}
			sb.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				sb.WriteString("\n\\ No newline at end of file\n")
			}
		}
		i = end
	}
	return sb.String()
}

// hunkPos finds the line position for a hunk with no lines on one side.
func hunkPos(edits []edit, at int, sideA bool) int {
	for j := at; j < len(edits); j++ {
		if sideA && edits[j].kind != editInsert {
			return edits[j].a
		}
		if !sideA && edits[j].kind != editDelete {
			return edits[j].b
		}
	}
	for j := at - 1; j >= 0; j-- {
		if sideA && edits[j].kind != editInsert {
			return edits[j].a + 1
		}
		if !sideA && edits[j].kind != editDelete {
			return edits[j].b + 1
		}
	}
	return 0
}

func rangeStr(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if n == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}

// matchMap maps each line of a kept in b to its index in b (-1 = deleted).
func matchMap(a, b []string) []int {
	m := make([]int, len(a))
	for i := range m {
		m[i] = -1
	}
	for _, e := range diffLines(a, b) {
		if e.kind == editEqual {
			m[e.a] = e.b
		}
	}
	return m
}

// Merge3 merges ours and theirs, both derived from base (line based, like
// diff3). ok is false when both sides changed the same region differently;
// the result then contains git-style conflict markers.
func Merge3(base, ours, theirs string) (merged string, ok bool) {
	if ours == theirs {
		return ours, true
	}
	if base == ours {
		return theirs, true
	}
	if base == theirs {
		return ours, true
	}
	b, o, t := splitLines(base), splitLines(ours), splitLines(theirs)
	mo, mt := matchMap(b, o), matchMap(b, t)
	var sb strings.Builder
	ok = true
	i, j, k := 0, 0, 0
	emit := func(lines []string) {
		for _, l := range lines {
			sb.WriteString(l)
		}
	}
	resolve := func(bs, os, ts []string) {
		switch {
		case equalLines(os, bs):
			emit(ts)
		case equalLines(ts, bs):
			emit(os)
		case equalLines(os, ts):
			emit(os)
		default:
			ok = false
			sb.WriteString("<<<<<<< current\n")
			emitNL(&sb, os)
			sb.WriteString("=======\n")
			emitNL(&sb, ts)
			sb.WriteString(">>>>>>> incoming\n")
		}
	}
	for {
		ii := -1
		for x := i; x < len(b); x++ {
			if mo[x] >= j && mt[x] >= k {
				ii = x
				break
			}
		}
		if ii < 0 {
			resolve(b[i:], o[j:], t[k:])
			break
		}
		if ii == i && mo[ii] == j && mt[ii] == k {
			sb.WriteString(b[i])
			i, j, k = i+1, j+1, k+1
			continue
		}
		resolve(b[i:ii], o[j:mo[ii]], t[k:mt[ii]])
		i, j, k = ii, mo[ii], mt[ii]
	}
	return sb.String(), ok
}

func emitNL(sb *strings.Builder, lines []string) {
	for _, l := range lines {
		sb.WriteString(l)
		if !strings.HasSuffix(l, "\n") {
			sb.WriteString("\n")
		}
	}
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
