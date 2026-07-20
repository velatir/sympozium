package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Roots the native file tools may touch. read_file gets the wider set; every
// tool that writes stays within writableRoots.
var (
	readableRoots = []string{"/workspace", "/skills", "/tmp", "/ipc"}
	writableRoots = []string{"/workspace", "/tmp"}
)

// resolvePath validates that path is absolute and under one of roots, resolves
// symlinks, and re-checks the result, so a symlink inside a root cannot point
// the caller at a file outside it. The file must exist; the resolved path is
// returned for the caller to operate on — operating on the unresolved path
// would make a rename replace the symlink itself instead of the real file.
// Roots are resolved too, so a mount-point symlink on a root itself (e.g.
// macOS /tmp -> /private/tmp) is not mistaken for an escape.
func resolvePath(path string, roots []string) (string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("path must be absolute")
	}
	if !underAnyPrefix(clean, roots) {
		return "", fmt.Errorf("access denied — path must be under %s", strings.Join(roots, ", "))
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("cannot access %s: %v", clean, err)
	}
	if !underAnyPrefix(resolved, resolvePrefixes(roots)) {
		return "", fmt.Errorf("access denied — %s resolves outside the allowed roots", clean)
	}
	return resolved, nil
}

// resolveWritableTarget is resolvePath for a file that may not exist yet (the
// write_file case): it resolves the deepest existing ancestor instead — the
// missing suffix cannot contain symlinks — and returns the resolved path to
// create or overwrite. The caller creates any missing directories.
func resolveWritableTarget(path string) (string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("path must be absolute")
	}
	if !underAnyPrefix(clean, writableRoots) {
		return "", fmt.Errorf("access denied — path must be under %s", strings.Join(writableRoots, ", "))
	}
	base, rest := clean, ""
	for {
		if _, err := os.Lstat(base); err == nil {
			break
		}
		rest = filepath.Join(filepath.Base(base), rest)
		base = filepath.Dir(base)
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("cannot access %s: %v", base, err)
	}
	resolved := filepath.Join(resolvedBase, rest)
	if !underAnyPrefix(resolved, resolvePrefixes(writableRoots)) {
		return "", fmt.Errorf("access denied — %s resolves outside the allowed roots", clean)
	}
	return resolved, nil
}

// resolvePrefixes returns the symlink-resolved form of each prefix, falling back
// to the literal prefix when it cannot be resolved (e.g. it does not exist).
func resolvePrefixes(prefixes []string) []string {
	out := make([]string, len(prefixes))
	for i, p := range prefixes {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			out[i] = rp
		} else {
			out[i] = p
		}
	}
	return out
}

// underAnyPrefix reports whether clean is equal to, or nested under, any of the
// prefixes. It compares path segments so "/tmpfoo" does not count as under
// "/tmp".
func underAnyPrefix(clean string, prefixes []string) bool {
	for _, p := range prefixes {
		if clean == p || strings.HasPrefix(clean, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
