package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/buildinfo"
	"github.com/NRngnl/wireproxy-gui/internal/lock"
	"github.com/NRngnl/wireproxy-gui/internal/profilejson"
	"github.com/NRngnl/wireproxy-gui/internal/runner"
	"github.com/NRngnl/wireproxy-gui/internal/tailscale"
	"github.com/NRngnl/wireproxy-gui/internal/tui"
	"github.com/NRngnl/wireproxy-gui/internal/wireproxy"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("wireproxy-tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	storeFlag := fs.String("store", "", "override the default profile store path")
	versionFlag := fs.Bool("version", false, "print version information and exit")
	noColorFlag := fs.Bool("no-color", false, "disable colored output")
	listFlag := fs.Bool("list", false, "print a table of profiles and exit")
	connectFlag := fs.String("connect", "", "connect to the named profile and exit")
	disconnectFlag := fs.String("disconnect", "", "disconnect the named profile and exit")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *versionFlag {
		fmt.Println(buildinfo.Summary())
		return exitOK
	}

	if *noColorFlag {
		lipgloss.SetColorProfile(termenv.Ascii)
	}

	storePath := *storeFlag
	if storePath == "" {
		defaultPath, err := profilejson.DefaultStorePath()
		if err != nil {
			defaultPath = filepath.Join(".", "profiles.json")
		}
		storePath = defaultPath
	}

	if held, err := lock.Probe(storePath + ".lock"); err == nil && held {
		fmt.Fprintln(os.Stderr, "wireproxy-tui: a wireproxy-gui daemon appears to be managing this profile store; changes made here may be overwritten until the daemon is stopped")
	}

	runtime := runner.New(wireproxy.NewRunner(), tailscale.NewRunner())
	core := application.New(profilejson.NewRepository(storePath), profilejson.Codec{}, runtime)
	loadErr := core.Load()

	switch {
	case *listFlag:
		return runList(core)
	case *connectFlag != "":
		return runConnect(core, *connectFlag)
	case *disconnectFlag != "":
		return runDisconnect(core, *disconnectFlag)
	}

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "wireproxy-tui: stdout is not a terminal; use --list, --connect <name>, --disconnect <name>, or --version for non-interactive use")
		return exitUsage
	}

	if err := tui.Run(core, loadErr); err != nil {
		fmt.Fprintln(os.Stderr, "wireproxy-tui:", err)
		return exitError
	}
	return exitOK
}

func runList(core *application.Service) int {
	profiles := core.Profiles()
	w := os.Stdout
	fmt.Fprintln(w, "NAME\tSTATUS\tBIND ADDRESS\tKIND")
	for _, p := range profiles {
		status := core.Status(p.ID)
		bind := fmt.Sprintf("%s:%d", p.SocksHost, p.SocksPort)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, status, bind, p.Kind)
	}
	return exitOK
}

func findProfileByName(core *application.Service, name string) (string, bool) {
	for _, p := range core.Profiles() {
		if p.Name == name {
			return p.ID, true
		}
	}
	return "", false
}

func runConnect(core *application.Service, name string) int {
	id, ok := findProfileByName(core, name)
	if !ok {
		fmt.Fprintf(os.Stderr, "wireproxy-tui: no profile named %q\n", name)
		return exitError
	}
	if err := core.Connect(id); err != nil {
		fmt.Fprintf(os.Stderr, "wireproxy-tui: connect %q: %v\n", name, err)
		return exitError
	}
	fmt.Printf("connected %q\n", name)
	return exitOK
}

func runDisconnect(core *application.Service, name string) int {
	id, ok := findProfileByName(core, name)
	if !ok {
		fmt.Fprintf(os.Stderr, "wireproxy-tui: no profile named %q\n", name)
		return exitError
	}
	if !core.Disconnect(id) {
		fmt.Fprintf(os.Stderr, "wireproxy-tui: disconnect %q: failed\n", name)
		return exitError
	}
	fmt.Printf("disconnected %q\n", name)
	return exitOK
}
