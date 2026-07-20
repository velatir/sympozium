package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// --- edit_file native tool (runs in the agent container) ---
//
// Applies an ordered, all-or-nothing batch of exact-string replacements. Each
// old_string must match the file's current bytes exactly once (unless
// replace_all is set); zero or ambiguous matches fail loudly with line
// numbers. Matching against current bytes means a stale or bogus old_string
// is a no-op error, never a silent clobber.

// Tool name constant.
const ToolEditFile = "edit_file"

// editOp is a single replacement request.
type editOp struct {
	OldString  string
	NewString  string
	ReplaceAll bool
}

// appliedEdit records what an editOp did, for rendering the result diff.
type appliedEdit struct {
	line       int // 1-based line where the (first) replacement landed
	count      int // number of occurrences replaced
	oldString  string
	newString  string
	replaceAll bool
}

// editFileTool applies an ordered batch of replacements to one file,
// sequentially and all-or-nothing: every edit must apply cleanly or the file is
// left untouched. A single edit is just a one-element batch.
func editFileTool(ctx context.Context, args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "Error: 'path' is required"
	}
	raw, ok := args["edits"].([]any)
	if !ok || len(raw) == 0 {
		return "Error: 'edits' must be a non-empty array of {old_string, new_string} objects"
	}

	edits := make([]editOp, 0, len(raw))
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return fmt.Sprintf("Error: edits[%d] is not an object", i)
		}
		oldStr, _ := m["old_string"].(string)
		newStr, _ := m["new_string"].(string)
		replaceAll, _ := m["replace_all"].(bool)
		edits = append(edits, editOp{OldString: oldStr, NewString: newStr, ReplaceAll: replaceAll})
	}
	return runEdits(ctx, path, edits)
}

// runEdits reads the file, applies the edits in memory (enforcing the unique
// match per edit), and atomically writes the result back. On any error the file
// on disk is unchanged.
func runEdits(ctx context.Context, path string, edits []editOp) string {
	if len(edits) == 0 {
		return "Error: no edits provided"
	}

	// The file must already exist — edit_file replaces text, so a missing file
	// is an error rather than a create.
	clean, err := resolvePath(path, writableRoots)
	if err != nil {
		return "Error: " + err.Error()
	}

	data, err := os.ReadFile(clean)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err)
	}
	// Preserve the file's real mode across the atomic rewrite (CreateTemp is 0600).
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(clean); statErr == nil {
		mode = info.Mode().Perm()
	}

	// The unique-match invariant is enforced per-edit inside applyEdits.
	out, applied, err := applyEdits(string(data), edits)
	if err != nil {
		return "Error: " + err.Error()
	}

	// Honour cancellation right before the write so a delegate/run timeout does
	// not leave us mid-rewrite. With the atomic rename below, the worst case of
	// racing here is a stray temp file, never a half-written target.
	if err := ctx.Err(); err != nil {
		return fmt.Sprintf("Error: edit cancelled before write: %v", err)
	}

	if err := atomicWrite(clean, []byte(out), mode); err != nil {
		return fmt.Sprintf("Error writing file: %v", err)
	}

	log.Printf("Edited file %s (%d edit(s), %d -> %d bytes)", clean, len(edits), len(data), len(out))
	detailedLog.LogAgent("edit_file", map[string]any{"path": clean, "edits": len(edits)})

	header := fmt.Sprintf("Edited %s (%s)", clean, pluralize(len(applied), "change"))
	return renderEditResult(header, applied)
}

// applyEdits applies edits sequentially to src in memory. Edit i sees the
// result of edits 0..i-1 (so a batch may deliberately chain, e.g. a→b then
// b→c). It returns the rewritten source plus a record of what each edit did, or
// an error (leaving the caller to discard the buffer). Correctness rests on the
// uniqueness invariant: each edit must match exactly once — unless replace_all
// is set — so an ambiguous match after an earlier edit fails loudly rather than
// landing somewhere unintended. The error strings are the model's only feedback
// channel, so they name line numbers and say what to do next.
func applyEdits(src string, edits []editOp) (string, []appliedEdit, error) {
	applied := make([]appliedEdit, 0, len(edits))

	// Prefix errors with "edits[i]: " only for multi-edit batches; a
	// one-element batch gets a clean message.
	prefix := func(i int) string {
		if len(edits) == 1 {
			return ""
		}
		return fmt.Sprintf("edits[%d]: ", i)
	}

	for i, e := range edits {
		if e.OldString == "" {
			return "", nil, fmt.Errorf("%sold_string is required", prefix(i))
		}
		if e.OldString == e.NewString {
			return "", nil, fmt.Errorf("%sold_string and new_string are identical", prefix(i))
		}

		n := strings.Count(src, e.OldString)
		switch {
		case n == 0:
			if i == 0 {
				return "", nil, fmt.Errorf("%sold_string not found in the file", prefix(i))
			}
			return "", nil, fmt.Errorf("%sold_string not found (an earlier edit may have changed it)", prefix(i))
		case n > 1 && !e.ReplaceAll:
			return "", nil, fmt.Errorf(
				"%sold_string matches %d times at lines %s — add surrounding context to make it unique, or set replace_all",
				prefix(i), n, formatLines(lineHits(src, e.OldString)))
		}

		if e.ReplaceAll {
			line := lineOf(src, strings.Index(src, e.OldString))
			src = strings.ReplaceAll(src, e.OldString, e.NewString)
			applied = append(applied, appliedEdit{line: line, count: n, oldString: e.OldString, newString: e.NewString, replaceAll: true})
			continue
		}

		idx := strings.Index(src, e.OldString)
		line := lineOf(src, idx)
		src = src[:idx] + e.NewString + src[idx+len(e.OldString):]
		applied = append(applied, appliedEdit{line: line, count: 1, oldString: e.OldString, newString: e.NewString})
	}

	return src, applied, nil
}

// atomicWrite writes data to a temp file in the same directory, fsyncs it,
// restores the original file mode, then renames it over path. The rename is
// atomic on POSIX filesystems, so a reader never sees a partial file and a
// crash mid-write leaves the original intact.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".edit-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Best-effort cleanup; a no-op once the rename below succeeds.
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// os.CreateTemp makes the file 0600; restore the target's real mode.
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// renderEditResult returns a compact diff the model can use to verify its own
// edit — a header plus a -/+ hunk per applied edit — rather than a bare "ok".
func renderEditResult(header string, applied []appliedEdit) string {
	var sb strings.Builder
	sb.WriteString(header)
	for _, a := range applied {
		if a.replaceAll {
			sb.WriteString(fmt.Sprintf("\n\n@ line %d (%s):\n", a.line, pluralize(a.count, "occurrence")))
		} else {
			sb.WriteString(fmt.Sprintf("\n\n@ line %d:\n", a.line))
		}
		sb.WriteString(diffBlock("-", a.oldString))
		sb.WriteString(diffBlock("+", a.newString))
	}
	return sb.String()
}

// diffBlock renders s as prefixed diff lines, capping very large blocks so the
// tool result stays small.
func diffBlock(prefix, s string) string {
	const max = 20
	lines := strings.Split(s, "\n")
	var sb strings.Builder
	for i, ln := range lines {
		if i == max {
			sb.WriteString(fmt.Sprintf("%s ... (%d more lines)\n", prefix, len(lines)-max))
			break
		}
		sb.WriteString(prefix + " " + ln + "\n")
	}
	return sb.String()
}

// lineOf returns the 1-based line number of the byte offset idx in src.
func lineOf(src string, idx int) int {
	if idx < 0 {
		return 0
	}
	return 1 + strings.Count(src[:idx], "\n")
}

// lineHits returns the 1-based line numbers where sub starts in src.
func lineHits(src, sub string) []int {
	if sub == "" {
		return nil
	}
	var lines []int
	for off := 0; ; {
		rel := strings.Index(src[off:], sub)
		if rel < 0 {
			break
		}
		pos := off + rel
		lines = append(lines, lineOf(src, pos))
		off = pos + len(sub)
	}
	return lines
}

// formatLines renders line numbers as "12, 40, 88".
func formatLines(lines []int) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = fmt.Sprintf("%d", l)
	}
	return strings.Join(parts, ", ")
}

// pluralize renders "1 change" / "2 changes".
func pluralize(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
