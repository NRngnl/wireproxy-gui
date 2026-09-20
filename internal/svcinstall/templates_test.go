package svcinstall

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixedRenderInput is the deterministic input used for golden-file
// comparisons. Its values are arbitrary but fixed so re-running the
// tests always reproduces byte-identical output.
func fixedRenderInput() RenderInput {
	return RenderInput{
		DaemonBinaryPath: "/opt/wireproxy/wireproxy-daemon",
		StorePath:        "/home/testuser/.config/wireproxy-gui/profiles.json",
		HomeDir:          "/home/testuser",
	}
}

func goldenPath(name string) string {
	return filepath.Join("testdata", name)
}

func TestRenderSystemdUnit_Golden(t *testing.T) {
	got, err := RenderSystemdUnit(fixedRenderInput())
	if err != nil {
		t.Fatalf("RenderSystemdUnit: %v", err)
	}
	want, err := os.ReadFile(goldenPath("wireproxy-daemon.service.golden"))
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if !bytesEqual(got, want) {
		t.Fatalf("rendered systemd unit does not match golden file.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderLaunchdPlist_Golden(t *testing.T) {
	got, err := RenderLaunchdPlist(fixedRenderInput())
	if err != nil {
		t.Fatalf("RenderLaunchdPlist: %v", err)
	}
	want, err := os.ReadFile(goldenPath("com.github.nrngnl.wireproxy-daemon.plist.golden"))
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if !bytesEqual(got, want) {
		t.Fatalf("rendered launchd plist does not match golden file.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func bytesEqual(a, b []byte) bool {
	return string(a) == string(b)
}

// TestRenderedPlist_IsWellFormedXML verifies the rendered plist is
// well-formed XML using the standard library, so this check runs on
// every platform without depending on macOS's plutil.
func TestRenderedPlist_IsWellFormedXML(t *testing.T) {
	got, err := RenderLaunchdPlist(fixedRenderInput())
	if err != nil {
		t.Fatalf("RenderLaunchdPlist: %v", err)
	}

	// encoding/xml's Decoder does not natively validate a DOCTYPE
	// against an external DTD (nor should a unit test require network
	// access to fetch Apple's DTD), so strip the DOCTYPE line before
	// decoding and just confirm the remaining document is well-formed
	// XML with balanced, correctly nested tags.
	stripped := stripDoctype(string(got))

	dec := xml.NewDecoder(strings.NewReader(stripped))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("rendered plist is not well-formed XML: %v", err)
		}
	}
}

func stripDoctype(s string) string {
	start := strings.Index(s, "<!DOCTYPE")
	if start == -1 {
		return s
	}
	end := strings.Index(s[start:], ">")
	if end == -1 {
		return s
	}
	return s[:start] + s[start+end+1:]
}

// TestRenderedPlist_PlutilLint optionally re-validates the rendered
// plist with macOS's plutil, when available. It is not the primary
// validity check (that is TestRenderedPlist_IsWellFormedXML, which runs
// everywhere) so the suite does not hard-depend on macOS tooling.
func TestRenderedPlist_PlutilLint(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is only available on darwin")
	}
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not found on PATH")
	}

	got, err := RenderLaunchdPlist(fixedRenderInput())
	if err != nil {
		t.Fatalf("RenderLaunchdPlist: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "rendered.plist")
	if err := os.WriteFile(path, got, 0o644); err != nil {
		t.Fatalf("writing rendered plist for plutil: %v", err)
	}

	out, err := exec.Command("plutil", "-lint", path).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil -lint failed: %v\n%s", err, out)
	}
}

// TestRender_SpecialCharactersInFields documents the current v1
// behavior for RenderInput fields containing characters that could be
// meaningful to the surrounding syntax (e.g. a space or a double-quote
// inside StorePath). text/template performs no escaping of its own, so
// the systemd template's ExecStart line double-quotes StorePath, which
// keeps spaces from splitting the argument. A literal double-quote
// inside StorePath is a known, documented edge case left unguarded in
// v1 (store paths containing a double-quote are exceedingly unlikely
// in practice); this test asserts the space case renders successfully
// and captures the quote-character caveat rather than silently
// "fixing" it with more machinery than v1 needs.
func TestRender_SpecialCharactersInFields(t *testing.T) {
	in := RenderInput{
		DaemonBinaryPath: "/opt/wireproxy/wireproxy-daemon",
		StorePath:        "/home/test user/.config/wireproxy-gui/profiles.json",
		HomeDir:          "/home/test user",
	}

	unit, err := RenderSystemdUnit(in)
	if err != nil {
		t.Fatalf("RenderSystemdUnit with space in StorePath: %v", err)
	}
	if !strings.Contains(string(unit), `--store "/home/test user/.config/wireproxy-gui/profiles.json"`) {
		t.Errorf("expected double-quoted --store argument to preserve the space, got:\n%s", unit)
	}

	plist, err := RenderLaunchdPlist(in)
	if err != nil {
		t.Fatalf("RenderLaunchdPlist with space in StorePath: %v", err)
	}
	// launchd's ProgramArguments is an array of separate strings, so a
	// space inside StorePath needs no quoting at all here.
	if !strings.Contains(string(plist), "<string>/home/test user/.config/wireproxy-gui/profiles.json</string>") {
		t.Errorf("expected plist array element to preserve the space unquoted, got:\n%s", plist)
	}
}
