package svcinstall

import (
	"bytes"
	"embed"
	"text/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// RenderInput holds the values substituted into the embedded systemd
// unit / launchd plist templates. It is a rendering-specific type, kept
// separate from Options: rendering needs fields (HomeDir) that Options
// does not expose to callers.
type RenderInput struct {
	// DaemonBinaryPath is the absolute path to the wireproxy-daemon
	// binary, substituted into the ExecStart / ProgramArguments entry.
	DaemonBinaryPath string
	// StorePath is the profile store path passed to the daemon via
	// --store.
	StorePath string
	// HomeDir is the invoking user's home directory, substituted into
	// the launchd plist's StandardOutPath/StandardErrorPath.
	HomeDir string
}

// RenderSystemdUnit renders the embedded systemd --user unit template
// with in, returning the rendered unit file bytes.
func RenderSystemdUnit(in RenderInput) ([]byte, error) {
	return render("templates/wireproxy-daemon.service.tmpl", in)
}

// RenderLaunchdPlist renders the embedded launchd LaunchAgent plist
// template with in, returning the rendered plist bytes.
func RenderLaunchdPlist(in RenderInput) ([]byte, error) {
	return render("templates/com.github.nrngnl.wireproxy-daemon.plist.tmpl", in)
}

func render(name string, in RenderInput) ([]byte, error) {
	tmpl, err := template.New(name).ParseFS(templatesFS, name)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	// ParseFS names the template after the base filename, not the full
	// path, so execute by that base name.
	base := name
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			base = name[i+1:]
			break
		}
	}
	if err := tmpl.ExecuteTemplate(&buf, base, in); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
