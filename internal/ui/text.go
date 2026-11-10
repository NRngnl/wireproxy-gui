package ui

import (
	"fmt"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

var usageGuideParagraphs = []string{
	"Add a WireGuard or Tailscale profile, then choose a SOCKS5 host and port for that profile.",
	"Each connected, connecting, or disconnecting profile must use a unique SOCKS5 bind address, such as 127.0.0.1:1080 and 127.0.0.1:1081.",
	"Connect starts the embedded backend and SOCKS5 listener for the selected profile. WireGuard profiles use the embedded WireGuard engine. Tailscale profiles use an embedded tsnet node. The app does not launch the wireproxy, tailscale, or tailscaled command-line tools. Connect All starts every saved profile. Runtime status is shown in the selected profile log.",
	"Tailscale auth is not the same as WireGuard config. Paste an auth key and click Login or Connect to register this app as a Tailscale device; leave Auth key empty for a browser sign-in URL in the profile log. If Tailscale requires device approval, the profile log will ask you to approve the device in the admin console and the app will detect approval automatically. After authentication succeeds, the app removes the saved auth key, marks the profile authenticated, and keeps the auth field locked until you click Logout. Logout removes this profile's stored Tailscale state and unlocks auth. Auth key, control URL, hostname, backend, and SOCKS5 bind fields are locked while that profile is connected, connecting, or disconnecting. Exit-node mode, selected exit node, and LAN access can be changed while connected; click Save to apply them without reconnecting.",
	"Exit-node choices are not live-updating. Connect the Tailscale profile, click Refresh to load available exit-node devices from that tailnet, and click Refresh again after tailnet devices or approvals change. You can also type a node ID, hostname, or Tailscale IP manually. Automatic exit asks Tailscale to choose an available exit node.",
	"The profile log follows the newest line by default. Scrolling up pauses following so you can read earlier output; scrolling back to the bottom follows new lines again.",
	"The sidebar and tray use both text and color for connection status: gray when disconnected, orange while connecting or disconnecting, green when connected, and red for an error. Each tray submenu also shows the SOCKS5 bind address, backend detail, and Connect or Disconnect actions.",
	"Disconnect a connected or connecting profile, and wait for it to finish disconnecting, before changing its backend, tunnel configuration, or SOCKS5 bind address. Profile name and startup preference can be changed while connected.",
	"Import and Export open the operating system's native file dialog and use the selected filesystem path. Export writes profiles as JSON. Import accepts exported JSON bundles or WireGuard .conf files. Tailscale node state and the local authenticated marker are not exported, so imported Tailscale profiles require Login again.",
	"Closing the window hides it when tray support is available. Use Quit from the tray menu to stop all profiles, wait briefly for them to close, and exit.",
}

type displayError string

func (e displayError) Error() string {
	return string(e)
}

func backendKindLabel(kind profile.BackendKind) string {
	if (profile.Profile{Kind: kind}).IsTailscale() {
		return tr("Tailscale")
	}
	return tr("WireGuard")
}

func trayStatusConnected(status string) bool {
	return status == "running"
}

func trayStatusActive(status string) bool {
	switch status {
	case "starting", "running", "stopping":
		return true
	default:
		return false
	}
}

func trayStatusText(status string) string {
	switch status {
	case "running":
		return tr("connected")
	case "starting":
		return tr("connecting")
	case "stopping":
		return tr("disconnecting")
	case "error":
		return tr("error")
	default:
		return tr("disconnected")
	}
}

func statusTitleText(status string) string {
	switch status {
	case "running":
		return tr("Connected")
	case "starting":
		return tr("Connecting")
	case "stopping":
		return tr("Disconnecting")
	case "error":
		return tr("Error")
	default:
		return tr("Disconnected")
	}
}

func statusSummaryText(p profile.Profile, status string) string {
	return tr("{{.Status}} · SOCKS5 {{.Address}}", map[string]any{
		"Address": p.BindAddress(),
		"Status":  statusTitleText(status),
	})
}

func profileMetaText(p profile.Profile) string {
	return tr("{{.Backend}} · SOCKS5 {{.Address}}", map[string]any{
		"Backend": backendKindLabel(p.Kind),
		"Address": p.BindAddress(),
	})
}

func profilesSummaryText(profiles []profile.Profile, status func(string) string) string {
	if len(profiles) == 0 {
		return tr("No profiles")
	}
	connected := 0
	for _, item := range profiles {
		if trayStatusConnected(status(item.ID)) {
			connected++
		}
	}
	template := "{{.Profiles}} profiles · {{.Connected}} connected"
	if len(profiles) == 1 {
		template = "{{.Profiles}} profile · {{.Connected}} connected"
	}
	return tr(template, map[string]any{
		"Profiles":  len(profiles),
		"Connected": connected,
	})
}

func wireGuardAddressText(p profile.Profile) string {
	address := p.WireGuardAddress()
	if strings.TrimSpace(address) == "" {
		return tr("not configured")
	}
	return address
}

func profileNetworkDetailText(p profile.Profile) string {
	if p.IsTailscale() {
		hostname := p.TailscaleConfig.Hostname
		if hostname == "" {
			hostname = p.Name
		}
		return tr("Tailscale node: {{.Name}}", map[string]any{"Name": hostname})
	}
	return tr("WireGuard IP: {{.Address}}", map[string]any{"Address": wireGuardAddressText(p)})
}

func operationErrorTitle(operation application.Operation) string {
	switch operation {
	case application.OperationConnect:
		return "Connect profile"
	case application.OperationConnectAll:
		return "Connect All"
	case application.OperationLogin:
		return "Login to Tailscale"
	case application.OperationSave:
		return "Save profile"
	default:
		return ""
	}
}

func profileLogText(entries []application.LogEntry) string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Message) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s", entry.At.Format("15:04:05"), localizedLogMessage(entry.Message)))
	}
	return strings.Join(lines, "\n")
}

func displayErrorText(title string, err error) string {
	titleText := tr(title)
	message := trimErrorTitlePrefix(titleText, localizedErrorText(err))
	return tr("{{.Title}}: {{.Message}}", map[string]any{
		"Title":   titleText,
		"Message": message,
	})
}

func trimErrorTitlePrefix(title, message string) string {
	prefix := title + ": "
	if len(message) >= len(prefix) && strings.EqualFold(message[:len(prefix)], prefix) {
		return message[len(prefix):]
	}
	return message
}

func usageGuideText() string {
	paragraphs := make([]string, 0, len(usageGuideParagraphs))
	for _, paragraph := range usageGuideParagraphs {
		paragraphs = append(paragraphs, tr(paragraph))
	}
	return strings.Join(paragraphs, "\n\n")
}

func localizedLogMessage(message string) string {
	return localizedText(message)
}

func safeFileName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "profile"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "profile"
	}
	return out
}

func exitNodeOptionLabel(node connection.ExitNode) string {
	label := strings.TrimSpace(node.Name)
	if label == "" {
		label = strings.TrimSpace(node.ID)
	}
	if label == "" && len(node.TailscaleIPs) > 0 {
		label = node.TailscaleIPs[0]
	}
	return label
}
