package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// editList wraps replacement objects as the []any the tool expects.
func editList(es ...map[string]any) []any {
	a := make([]any, len(es))
	for i, e := range es {
		a[i] = e
	}
	return a
}

// oneEdit is the common single-replacement case: a one-element edits array.
func oneEdit(oldStr, newStr string) []any {
	return editList(map[string]any{"old_string": oldStr, "new_string": newStr})
}

func TestEditFileTool_Basic(t *testing.T) {
	path := writeReadFileFixture(t, "hello\nworld\n")

	got := editFileTool(context.Background(), map[string]any{
		"path": path, "edits": oneEdit("world", "there"),
	})
	if strings.HasPrefix(got, "Error") {
		t.Fatalf("edit failed: %s", got)
	}
	if !strings.Contains(got, "- world") || !strings.Contains(got, "+ there") {
		t.Fatalf("result missing diff hunk: %q", got)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello\nthere\n" {
		t.Fatalf("file = %q, want %q", data, "hello\nthere\n")
	}
}

func TestEditFileTool_AmbiguousMatch(t *testing.T) {
	path := writeReadFileFixture(t, "apple\nbanana\napple\ncherry\napple\n")

	got := editFileTool(context.Background(), map[string]any{
		"path": path, "edits": oneEdit("apple", "APPLE"),
	})
	if !strings.Contains(got, "matches 3 times") || !strings.Contains(got, "lines 1, 3, 5") {
		t.Fatalf("want ambiguous-match error naming lines 1, 3, 5, got: %q", got)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "APPLE") {
		t.Fatalf("file was modified despite ambiguous match: %q", data)
	}
}

func TestEditFileTool_ReplaceAll(t *testing.T) {
	path := writeReadFileFixture(t, "apple\nbanana\napple\ncherry\napple\n")

	got := editFileTool(context.Background(), map[string]any{
		"path": path,
		"edits": editList(map[string]any{
			"old_string": "apple", "new_string": "APPLE", "replace_all": true,
		}),
	})
	if strings.HasPrefix(got, "Error") {
		t.Fatalf("replace_all edit failed: %s", got)
	}
	data, _ := os.ReadFile(path)
	if want := "APPLE\nbanana\nAPPLE\ncherry\nAPPLE\n"; string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestEditFileTool_NotFound(t *testing.T) {
	path := writeReadFileFixture(t, "hello\n")

	got := editFileTool(context.Background(), map[string]any{
		"path": path, "edits": oneEdit("missing", "x"),
	})
	if !strings.Contains(got, "not found") {
		t.Fatalf("want not-found error, got: %q", got)
	}
	// A non-matching edit must leave the file untouched.
	data, _ := os.ReadFile(path)
	if string(data) != "hello\n" {
		t.Fatalf("file = %q, want unchanged", data)
	}
}

func TestEditFileTool_IdenticalStrings(t *testing.T) {
	path := writeReadFileFixture(t, "hello\n")

	got := editFileTool(context.Background(), map[string]any{
		"path": path, "edits": oneEdit("hello", "hello"),
	})
	if !strings.Contains(got, "identical") {
		t.Fatalf("want identical-strings error, got: %q", got)
	}
}

func TestEditFileTool_DeleteViaEmptyNewString(t *testing.T) {
	path := writeReadFileFixture(t, "keep DELETE keep")

	got := editFileTool(context.Background(), map[string]any{
		"path": path, "edits": oneEdit(" DELETE", ""),
	})
	if strings.HasPrefix(got, "Error") {
		t.Fatalf("delete edit failed: %s", got)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "keep keep" {
		t.Fatalf("file = %q, want %q", data, "keep keep")
	}
}

func TestEditFileTool_MissingEdits(t *testing.T) {
	path := writeReadFileFixture(t, "hello\n")

	got := editFileTool(context.Background(), map[string]any{"path": path})
	if !strings.Contains(got, "'edits'") {
		t.Fatalf("want missing-edits error, got: %q", got)
	}
}

func TestEditFileTool_AccessDenied(t *testing.T) {
	got := editFileTool(context.Background(), map[string]any{
		"path": "/etc/hosts", "edits": oneEdit("a", "b"),
	})
	if !strings.Contains(got, "access denied") {
		t.Fatalf("want access-denied error, got: %q", got)
	}
}

func TestEditFileTool_PreservesMode(t *testing.T) {
	path := writeReadFileFixture(t, "chmod me\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	got := editFileTool(context.Background(), map[string]any{
		"path": path, "edits": oneEdit("chmod me", "changed"),
	})
	if strings.HasPrefix(got, "Error") {
		t.Fatalf("edit failed: %s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("mode after edit = %o, want 640", perm)
	}
}

func TestEditFileTool_SymlinkEscapeDenied(t *testing.T) {
	outside := filepath.Join(outsideDir(t), "target.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(filepath.Dir(writeReadFileFixture(t, "unused")), "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	got := editFileTool(context.Background(), map[string]any{
		"path": link, "edits": oneEdit("secret", "changed"),
	})
	if !strings.Contains(got, "resolves outside") {
		t.Fatalf("want symlink-escape denial, got: %q", got)
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "secret\n" {
		t.Fatalf("file outside the roots was modified: %q", data)
	}
}

func TestEditFileTool_EditsThroughSymlink(t *testing.T) {
	real := writeReadFileFixture(t, "hello\n")
	link := filepath.Join(filepath.Dir(real), "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	got := editFileTool(context.Background(), map[string]any{
		"path": link, "edits": oneEdit("hello", "hi"),
	})
	if strings.HasPrefix(got, "Error") {
		t.Fatalf("edit through symlink failed: %s", got)
	}
	// The edit must land on the real file, and the symlink must survive —
	// renaming over the link path would replace the link with a regular file.
	data, _ := os.ReadFile(real)
	if string(data) != "hi\n" {
		t.Fatalf("real file = %q, want %q", data, "hi\n")
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced by a regular file (err=%v)", err)
	}
}

// TestEditFileTool_ChangeTwoOfThree is the design's headline case: three
// identical blocks where we want the first and third but not the middle one.
// Context expansion — not an index parameter — keeps each edit unique.
func TestEditFileTool_ChangeTwoOfThree(t *testing.T) {
	path := writeReadFileFixture(t, "apple\nbanana\napple\ncherry\napple\n")

	got := editFileTool(context.Background(), map[string]any{
		"path": path,
		"edits": editList(
			map[string]any{"old_string": "apple\nbanana", "new_string": "APPLE\nbanana"},
			map[string]any{"old_string": "cherry\napple", "new_string": "cherry\nAPPLE"},
		),
	})
	if strings.HasPrefix(got, "Error") {
		t.Fatalf("batch edit failed: %s", got)
	}
	data, _ := os.ReadFile(path)
	// First and last "apple" become APPLE; the middle one (line 3) is untouched.
	if want := "APPLE\nbanana\napple\ncherry\nAPPLE\n"; string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestEditFileTool_Sequential(t *testing.T) {
	// The second edit's old_string only exists after the first edit runs.
	path := writeReadFileFixture(t, "alpha\n")

	got := editFileTool(context.Background(), map[string]any{
		"path": path,
		"edits": editList(
			map[string]any{"old_string": "alpha", "new_string": "beta"},
			map[string]any{"old_string": "beta", "new_string": "gamma"},
		),
	})
	if strings.HasPrefix(got, "Error") {
		t.Fatalf("sequential batch edit failed: %s", got)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "gamma\n" {
		t.Fatalf("file = %q, want %q", data, "gamma\n")
	}
}

func TestEditFileTool_AllOrNothing(t *testing.T) {
	path := writeReadFileFixture(t, "one two three\n")

	got := editFileTool(context.Background(), map[string]any{
		"path": path,
		"edits": editList(
			map[string]any{"old_string": "one", "new_string": "1"},
			map[string]any{"old_string": "nonexistent", "new_string": "x"},
		),
	})
	if !strings.Contains(got, "edits[1]") || !strings.Contains(got, "not found") {
		t.Fatalf("want edits[1] not-found error, got: %q", got)
	}
	// The first edit must not have been persisted.
	data, _ := os.ReadFile(path)
	if string(data) != "one two three\n" {
		t.Fatalf("file = %q, want unchanged (all-or-nothing)", data)
	}
}

// TestApplyEdits_Chaining locks in the sequential semantic: edit 1 may
// deliberately transform text edit 0 just produced.
func TestApplyEdits_Chaining(t *testing.T) {
	out, applied, err := applyEdits("hello world", []editOp{
		{OldString: "hello world", NewString: "hi world"},
		{OldString: "hi", NewString: "hey"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hey world" {
		t.Fatalf("out = %q, want %q", out, "hey world")
	}
	if len(applied) != 2 {
		t.Fatalf("applied = %d edits, want 2", len(applied))
	}
}

func TestApplyEdits_NonOverlapping(t *testing.T) {
	out, applied, err := applyEdits("alpha beta", []editOp{
		{OldString: "alpha", NewString: "ALPHA"},
		{OldString: "beta", NewString: "BETA"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "ALPHA BETA" {
		t.Fatalf("out = %q, want %q", out, "ALPHA BETA")
	}
	if len(applied) != 2 {
		t.Fatalf("applied = %d edits, want 2", len(applied))
	}
}

// TestApplyEdits_SingleEditErrorHasNoIndexPrefix verifies a one-element batch
// gets a clean error message without the "edits[i]:" prefix.
func TestApplyEdits_SingleEditErrorHasNoIndexPrefix(t *testing.T) {
	_, _, err := applyEdits("hello", []editOp{{OldString: "missing", NewString: "x"}})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if strings.HasPrefix(err.Error(), "edits[") {
		t.Fatalf("single-edit error should not carry an index prefix, got: %q", err)
	}
}

// TestEditToolDefs asserts the schema exposes the expected required fields.
func TestEditToolDefs(t *testing.T) {
	defs := defaultTools()
	var edit *ToolDef
	for i := range defs {
		if defs[i].Name == ToolEditFile {
			edit = &defs[i]
			break
		}
	}
	if edit == nil {
		t.Fatalf("%s not found in defaultTools()", ToolEditFile)
	}
	req, _ := edit.Parameters["required"].([]string)
	if strings.Join(req, ",") != "path,edits" {
		t.Fatalf("edit_file required = %v, want [path edits]", req)
	}
}
