package screens

import (
	"fmt"
	"net"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// LocalForwardsAction is what the panel asks its owner to do after a key press.
type LocalForwardsAction int

const (
	LocalForwardsNone LocalForwardsAction = iota
	LocalForwardsClose
	LocalForwardsAdd
	LocalForwardsUpdate
	LocalForwardsRemove
	LocalForwardsRetry
)

const exposedListenWarning = "Anyone who can reach this computer can use this forward."

// LocalForwardsPanel lists a profile's local forwards and edits them in place.
type LocalForwardsPanel struct {
	ProfileID string
	Rules     []profile.LocalForward
	States    []connection.LocalForwardState
	Running   bool
	Err       string

	cursor  int
	form    *huh.Form
	confirm *Confirm
	editing *profile.LocalForwardKey

	protocol string
	listen   string
	target   string
}

// NewLocalForwardsPanel creates the panel for one profile.
func NewLocalForwardsPanel(profileID string) *LocalForwardsPanel {
	return &LocalForwardsPanel{ProfileID: profileID}
}

// Selected returns the rule under the cursor.
func (p *LocalForwardsPanel) Selected() (profile.LocalForward, bool) {
	if p.cursor < 0 || p.cursor >= len(p.Rules) {
		return profile.LocalForward{}, false
	}
	return p.Rules[p.cursor], true
}

// Form returns the active add/edit form, if any.
func (p *LocalForwardsPanel) Form() *huh.Form { return p.form }

// Editing returns the key of the rule being edited; nil while adding.
func (p *LocalForwardsPanel) Editing() *profile.LocalForwardKey { return p.editing }

// Rule assembles the form fields into a normalized rule.
func (p *LocalForwardsPanel) Rule() profile.LocalForward {
	rule := profile.LocalForward{Protocol: profile.PortForwardProtocol(p.protocol), ListenAddr: p.listen, TargetAddr: p.target}
	rule.Normalize()
	return rule
}

// SetRules refreshes the rule list and keeps the cursor in range.
func (p *LocalForwardsPanel) SetRules(rules []profile.LocalForward) {
	p.Rules = rules
	p.cursor = max(0, min(p.cursor, len(rules)-1))
}

// OpenForm starts the add form, or the edit form for the selected rule when edit is true.
func (p *LocalForwardsPanel) OpenForm(edit bool) tea.Cmd {
	p.Err, p.editing = "", nil
	p.protocol, p.listen, p.target = string(profile.PortForwardTCP), "127.0.0.1:", ""
	if rule, ok := p.Selected(); edit && ok {
		key := rule.Key()
		p.editing = &key
		p.protocol, p.listen, p.target = string(rule.Protocol), rule.ListenAddr, rule.TargetAddr
	}
	p.buildForm()
	return p.form.Init()
}

func (p *LocalForwardsPanel) buildForm() {
	p.form = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Protocol").
			Options(huh.NewOption("TCP", string(profile.PortForwardTCP)), huh.NewOption("UDP", string(profile.PortForwardUDP))).
			Value(&p.protocol),
		huh.NewInput().Title("Listen address (IP:port, or a port for 127.0.0.1)").
			Description(exposedListenWarning+" Keep 127.0.0.1 unless you need more.").
			Value(&p.listen),
		huh.NewInput().Title("Target (host:port)").Value(&p.target),
	))
}

// HandleKey routes a key press and reports the action for the owner to perform.
func (p *LocalForwardsPanel) HandleKey(msg tea.KeyMsg) (LocalForwardsAction, tea.Cmd) {
	switch {
	case p.confirm != nil:
		resolved, confirmed := p.confirm.HandleKey(msg)
		if resolved {
			p.confirm = nil
			if confirmed {
				return LocalForwardsRemove, nil
			}
		}
		return LocalForwardsNone, nil
	case p.form != nil:
		return p.handleFormKey(msg)
	}
	return p.handleListKey(msg)
}

func (p *LocalForwardsPanel) handleListKey(msg tea.KeyMsg) (LocalForwardsAction, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return LocalForwardsClose, nil
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = min(len(p.Rules)-1, p.cursor+1)
	case "a":
		return LocalForwardsNone, p.OpenForm(false)
	case "e":
		return LocalForwardsNone, p.OpenForm(true)
	case "d", "x":
		if rule, ok := p.Selected(); ok {
			p.confirm = NewConfirm(fmt.Sprintf("Remove %s %s?%s", strings.ToUpper(string(rule.Protocol)), rule.ListenAddr, p.removeNote()))
		}
	case "r":
		return LocalForwardsRetry, nil
	}
	return LocalForwardsNone, nil
}

func (p *LocalForwardsPanel) removeNote() string {
	if p.Running {
		return " This closes connections using this port."
	}
	return ""
}

func (p *LocalForwardsPanel) handleFormKey(msg tea.KeyMsg) (LocalForwardsAction, tea.Cmd) {
	if msg.String() == "esc" {
		p.form = nil
		return LocalForwardsNone, nil
	}
	model, cmd := p.form.Update(msg)
	if form, ok := model.(*huh.Form); ok {
		p.form = form
	}
	switch p.form.State {
	case huh.StateCompleted:
		return p.submit(), nil
	case huh.StateAborted:
		p.form = nil
	}
	return LocalForwardsNone, cmd
}

func (p *LocalForwardsPanel) submit() LocalForwardsAction {
	rule := p.Rule()
	err := rule.Validate()
	if err != nil {
		p.Err = err.Error()
		p.listen, p.target = rule.ListenAddr, rule.TargetAddr
		p.buildForm()
		return LocalForwardsNone
	}
	p.Err = ""
	if p.editing != nil {
		return LocalForwardsUpdate
	}
	return LocalForwardsAdd
}

// CloseForm drops the add/edit form after a successful save.
func (p *LocalForwardsPanel) CloseForm() { p.form, p.editing = nil, nil }

func (p *LocalForwardsPanel) status(rule profile.LocalForward) string {
	if !p.Running {
		return "saved, not connected"
	}
	for _, s := range p.States {
		if s.Key == rule.Key() {
			if s.Status == connection.LocalForwardError {
				return "error: " + s.Err
			}
			return "active"
		}
	}
	return "starting"
}

// View renders the panel.
func (p *LocalForwardsPanel) View() string {
	var b strings.Builder
	switch {
	case p.form != nil:
		b.WriteString(p.form.View())
	case p.confirm != nil:
		b.WriteString(p.confirm.View())
	default:
		p.listView(&b)
	}
	if p.Err != "" {
		b.WriteString("\n" + p.Err)
	}
	return b.String()
}

func (p *LocalForwardsPanel) listView(b *strings.Builder) {
	if len(p.Rules) == 0 {
		b.WriteString("No local forwards yet. Like ssh -L: TCP 127.0.0.1:2222 to remote-pc:22.\n")
	}
	for i, rule := range p.Rules {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		warn := ""
		host, _, err := net.SplitHostPort(rule.ListenAddr)
		if err == nil && !isLoopback(host) {
			warn = " (reachable beyond this computer)"
		}
		fmt.Fprintf(b, "%s%s %s -> %s  [%s]%s\n", cursor, strings.ToUpper(string(rule.Protocol)), rule.ListenAddr, rule.TargetAddr, p.status(rule), warn)
	}
	b.WriteString("\na add  e edit  d remove  r retry  esc back")
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
