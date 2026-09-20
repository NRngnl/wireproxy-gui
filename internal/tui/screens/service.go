package screens

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NRngnl/wireproxy-gui/internal/svcinstall"
)

// ServicePanel renders the per-user login-service status (from
// internal/svcinstall) and, once status is known, offers a single
// install-or-uninstall action confirmed with the same y/N convention as
// Confirm. It is deliberately dependency-free of any Bubble Tea runtime
// state beyond what's needed to render and to interpret a key press,
// mirroring Confirm's shape.
//
// The panel explicitly states the per-user-only scope in its copy: this
// feature never installs a system-wide service and never requires
// elevated privileges.
type ServicePanel struct {
	// Supported reports whether internal/svcinstall has a working
	// backend on this OS.
	Supported bool

	// Loading is true while the initial Status() call is in flight.
	Loading bool

	// Status holds the last known service status, valid once Loading is
	// false and Err is nil.
	Status svcinstall.Status

	// Err holds the most recent error from Status/Install/Uninstall, if
	// any. LockErr is a special case: it means Install returned
	// svcinstall.ErrLockHeld, and the panel should offer a "press y
	// again to force" retry instead of a plain error message.
	Err     error
	LockErr bool

	// Busy is true while an Install/Uninstall call is in flight (via a
	// tea.Cmd), during which key presses other than the outcome message
	// are ignored.
	Busy bool

	// resolved/confirmedInstall/confirmedUninstall record the outcome
	// once the user has pressed a key to act on the current status.
	resolved bool
}

// NewServicePanel constructs a ServicePanel in its initial loading state.
func NewServicePanel(supported bool) *ServicePanel {
	return &ServicePanel{Supported: supported, Loading: supported}
}

// StatusResultMsg carries the result of an async Status() call back into
// the Bubble Tea Update loop. Gen echoes back the generation the command
// was dispatched for, so a caller can discard a result that arrives
// after the panel was closed and reopened (see model.go's serviceGen).
type StatusResultMsg struct {
	Status svcinstall.Status
	Err    error
	Gen    int
}

// ActionResultMsg carries the result of an async Install()/Uninstall()
// call back into the Bubble Tea Update loop. Installed distinguishes
// which action was performed, for status-message rendering. Gen has the
// same stale-result-discarding purpose as StatusResultMsg.Gen.
type ActionResultMsg struct {
	Installed bool
	Err       error
	Gen       int
}

// LoadStatusCmd returns a tea.Cmd that calls svc.Status(ctx) and reports
// the result as a StatusResultMsg carrying gen.
func LoadStatusCmd(svc svcinstall.Installer, ctx context.Context, gen int) tea.Cmd {
	return func() tea.Msg {
		status, err := svc.Status(ctx)
		return StatusResultMsg{Status: status, Err: err, Gen: gen}
	}
}

// InstallCmd returns a tea.Cmd that calls svc.Install(ctx, opts) and
// reports the result as an ActionResultMsg carrying gen.
func InstallCmd(svc svcinstall.Installer, ctx context.Context, opts svcinstall.Options, gen int) tea.Cmd {
	return func() tea.Msg {
		err := svc.Install(ctx, opts)
		return ActionResultMsg{Installed: true, Err: err, Gen: gen}
	}
}

// UninstallCmd returns a tea.Cmd that calls svc.Uninstall(ctx) and
// reports the result as an ActionResultMsg carrying gen.
func UninstallCmd(svc svcinstall.Installer, ctx context.Context, gen int) tea.Cmd {
	return func() tea.Msg {
		err := svc.Uninstall(ctx)
		return ActionResultMsg{Installed: false, Err: err, Gen: gen}
	}
}

// View renders the service panel: current status, then a single
// action/confirm line appropriate to that status.
func (p *ServicePanel) View() string {
	if !p.Supported {
		return "Start on login is not yet supported on this OS.\n\n" +
			"For system-wide/server deployment, see docs/daemon.md.\n\n" +
			"(esc to close)"
	}

	if p.Loading {
		return "Checking service status\u2026"
	}

	var body string
	if p.Err != nil && !p.LockErr {
		body = fmt.Sprintf("Error: %v\n\n(esc to close)", p.Err)
		return statusHeader(p) + body
	}

	if p.LockErr {
		body = fmt.Sprintf(
			"%v\n\nInstall anyway? (y/N)",
			p.Err,
		)
		return statusHeader(p) + body
	}

	if p.Busy {
		return statusHeader(p) + "Working\u2026"
	}

	if p.Status.Installed {
		body = "Disable and remove the per-user login service? (y/N)"
	} else {
		body = "Installs as a per-user login service. For system-wide/server\n" +
			"deployment, see docs/daemon.md.\n\n" +
			"Enable start on login? (y/N)"
	}
	return statusHeader(p) + body
}

func statusHeader(p *ServicePanel) string {
	return fmt.Sprintf(
		"Installed: %v   Enabled: %v   Running: %v\n%s\n\n",
		p.Status.Installed, p.Status.Enabled, p.Status.Running, statusDetail(p.Status),
	)
}

func statusDetail(s svcinstall.Status) string {
	if s.Detail == "" {
		return ""
	}
	return s.Detail
}

// HandleKey processes a key press against the current panel state and
// reports what action (if any) the caller should now take. install/
// uninstall/forceInstall are mutually exclusive with each other and with
// close; when none are true and close is false, the key was consumed
// without producing an action (e.g. while Loading or Busy).
func (p *ServicePanel) HandleKey(msg tea.KeyMsg) (install, uninstall, forceInstall, closePanel bool) {
	if !p.Supported {
		return false, false, false, true
	}
	// esc always closes the panel, even while a Status/Install/Uninstall
	// call is in flight: the pending tea.Cmd still runs to completion, but
	// its result is discarded (see model.go's serviceGen) since the panel
	// it was fetched for is no longer open.
	if msg.String() == "esc" {
		return false, false, false, true
	}
	if p.Loading || p.Busy {
		return false, false, false, false
	}

	confirmed := msg.String() == "y" || msg.String() == "Y"

	if p.Err != nil && !p.LockErr {
		// A plain (non-lock) error: any key closes.
		return false, false, false, true
	}

	if p.LockErr {
		if confirmed {
			return false, false, true, false
		}
		return false, false, false, true
	}

	if !confirmed {
		return false, false, false, true
	}
	if p.Status.Installed {
		return false, true, false, false
	}
	return true, false, false, false
}
