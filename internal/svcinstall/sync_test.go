package svcinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repositoryRoot locates the repo root from this test file's own path,
// mirroring internal/architecture/architecture_test.go's helper. Kept as
// a local copy rather than a shared dependency so internal/svcinstall
// stays free of any dependency on internal/architecture.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate sync test file")
	}
	// this file lives at internal/svcinstall/sync_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// TestSystemdUnitStructurallyMatchesPackaging guards against someone
// editing packaging/systemd/wireproxy-daemon.service (the human-facing
// manual-install reference) or internal/svcinstall/templates/
// wireproxy-daemon.service.tmpl (the embedded, actually-used template)
// without mirroring the change to the other copy.
//
// Tolerated differences:
//   - The packaging copy has one extra leading "# Human-facing manual-
//     install reference..." sync-reminder comment line that the template
//     does not have.
//   - The ExecStart= line's content differs (literal path in the
//     packaging copy vs. {{.DaemonBinaryPath}}/{{.StorePath}} template
//     fields) — only the "ExecStart=" prefix is required to match.
//
// Every other line must be byte-identical between the two files.
func TestSystemdUnitStructurallyMatchesPackaging(t *testing.T) {
	root := repositoryRoot(t)
	packagingLines := readLines(t, filepath.Join(root, "packaging", "systemd", "wireproxy-daemon.service"))
	templateLines := readLines(t, filepath.Join(root, "internal", "svcinstall", "templates", "wireproxy-daemon.service.tmpl"))

	// Strip the packaging copy's known extra leading sync-reminder
	// comment lines (2 lines: "# Human-facing..." and its continuation)
	// so both slices start at the same structural content ("[Unit]").
	packagingLines = skipLeadingComment(packagingLines, "# Human-facing manual-install reference")

	if len(packagingLines) != len(templateLines) {
		t.Fatalf(
			"line count mismatch after stripping the known sync-reminder comment: packaging=%d template=%d\npackaging lines:\n%s\ntemplate lines:\n%s",
			len(packagingLines), len(templateLines),
			strings.Join(packagingLines, "\n"), strings.Join(templateLines, "\n"),
		)
	}

	for i := range packagingLines {
		pLine, tLine := packagingLines[i], templateLines[i]
		if strings.HasPrefix(pLine, "ExecStart=") || strings.HasPrefix(tLine, "ExecStart=") {
			if !strings.HasPrefix(pLine, "ExecStart=") || !strings.HasPrefix(tLine, "ExecStart=") {
				t.Errorf("line %d: expected both files to have an ExecStart= line at this position, got packaging=%q template=%q", i, pLine, tLine)
			}
			continue
		}
		if pLine != tLine {
			t.Errorf("line %d differs between packaging and template copies:\n  packaging: %q\n  template:  %q", i, pLine, tLine)
		}
	}
}

// TestLaunchdPlistStructurallyMatchesPackaging is the plist analog of
// TestSystemdUnitStructurallyMatchesPackaging.
//
// Tolerated differences:
//   - The packaging copy has one extra XML-comment line (plus its
//     continuation line) after the DOCTYPE that the template does not
//     have.
//   - The ProgramArguments array line's content differs (one literal
//     <string> in the packaging copy vs. three templated <string>
//     elements for binary+flag+path in the template) — only the
//     surrounding <key>ProgramArguments</key> key line is required to
//     match exactly; the following <array>...</array> line's content is
//     exempted.
//   - The StandardOutPath/StandardErrorPath <string> content differs
//     (__HOME__ literal placeholder vs. {{.HomeDir}} template field) —
//     only the <key>StandardOutPath</key>/<key>StandardErrorPath</key>
//     key lines are required to match exactly.
func TestLaunchdPlistStructurallyMatchesPackaging(t *testing.T) {
	root := repositoryRoot(t)
	packagingLines := readLines(t, filepath.Join(root, "packaging", "launchd", "com.github.nrngnl.wireproxy-daemon.plist"))
	templateLines := readLines(t, filepath.Join(root, "internal", "svcinstall", "templates", "com.github.nrngnl.wireproxy-daemon.plist.tmpl"))

	packagingLines = removeXMLComment(t, packagingLines)

	if len(packagingLines) != len(templateLines) {
		t.Fatalf(
			"line count mismatch after stripping the known sync-reminder XML comment: packaging=%d template=%d\npackaging lines:\n%s\ntemplate lines:\n%s",
			len(packagingLines), len(templateLines),
			strings.Join(packagingLines, "\n"), strings.Join(templateLines, "\n"),
		)
	}

	// Lines whose CONTENT is allowed to differ (identified by a
	// preceding <key> line for the entries that involve a value), plus
	// the array line that immediately follows <key>ProgramArguments</key>.
	tolerateNextArrayDiff := false
	for i := range packagingLines {
		pLine, tLine := packagingLines[i], templateLines[i]

		if tolerateNextArrayDiff {
			tolerateNextArrayDiff = false
			if !strings.Contains(pLine, "<array>") || !strings.Contains(tLine, "<array>") {
				t.Errorf("line %d: expected the ProgramArguments array line in both files, got packaging=%q template=%q", i, pLine, tLine)
			}
			continue
		}

		switch {
		case strings.Contains(pLine, "<key>ProgramArguments</key>"):
			if pLine != tLine {
				t.Errorf("line %d: ProgramArguments key line should match exactly: packaging=%q template=%q", i, pLine, tLine)
			}
			tolerateNextArrayDiff = true
			continue
		case strings.Contains(pLine, "<key>StandardOutPath</key>") || strings.Contains(pLine, "<key>StandardErrorPath</key>"):
			// The <key>...</key> portion must match; the trailing
			// <string>...</string> value is allowed to differ
			// (__HOME__ literal vs {{.HomeDir}} template field).
			pKey := lineKeyPrefix(pLine)
			tKey := lineKeyPrefix(tLine)
			if pKey == "" || pKey != tKey {
				t.Errorf("line %d: StandardOutPath/StandardErrorPath key portion should match: packaging=%q template=%q", i, pLine, tLine)
			}
			continue
		}

		if pLine != tLine {
			t.Errorf("line %d differs between packaging and template copies:\n  packaging: %q\n  template:  %q", i, pLine, tLine)
		}
	}
}

// skipLeadingComment removes leading lines from lines that form a
// comment block starting with the given prefix (the sync-reminder
// comment added to the packaging/ reference copies), returning the
// remaining lines unchanged.
func skipLeadingComment(lines []string, prefix string) []string {
	if len(lines) > 0 && strings.HasPrefix(lines[0], prefix) {
		// The sync-reminder comment is exactly two lines: the prefix
		// line and its continuation line starting with "#".
		i := 1
		for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			i++
		}
		return lines[i:]
	}
	return lines
}

// removeXMLComment removes the "<!-- ... -->" sync-reminder comment
// block (which may span two lines in this repo's formatting) that was
// added to the packaging/ plist reference copy, right after the DOCTYPE
// line and before <plist version="1.0">.
func removeXMLComment(t *testing.T, lines []string) []string {
	t.Helper()
	out := make([]string, 0, len(lines))
	inComment := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inComment && strings.HasPrefix(trimmed, "<!--") {
			inComment = true
			if strings.Contains(trimmed, "-->") {
				inComment = false
			}
			continue
		}
		if inComment {
			if strings.Contains(trimmed, "-->") {
				inComment = false
			}
			continue
		}
		out = append(out, line)
	}
	return out
}

// lineKeyPrefix extracts the "<key>...</key>" portion of a plist line
// that also contains a trailing <string>...</string> value, e.g.
// "  <key>StandardOutPath</key><string>...</string>" ->
// "  <key>StandardOutPath</key>".
func lineKeyPrefix(line string) string {
	idx := strings.Index(line, "</key>")
	if idx == -1 {
		return ""
	}
	return line[:idx+len("</key>")]
}
