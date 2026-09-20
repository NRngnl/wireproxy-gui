package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/lock"
	"github.com/NRngnl/wireproxy-gui/internal/profilejson"
	"github.com/NRngnl/wireproxy-gui/internal/runner"
	"github.com/NRngnl/wireproxy-gui/internal/tailscale"
	"github.com/NRngnl/wireproxy-gui/internal/ui"
	"github.com/NRngnl/wireproxy-gui/internal/wireproxy"
)

func main() {
	storePath, err := profilejson.DefaultStorePath()
	if err != nil {
		storePath = filepath.Join(".", "profiles.json")
	}

	if held, err := lock.Probe(storePath + ".lock"); err == nil && held {
		fmt.Fprintln(os.Stderr, "wireproxy-gui: a wireproxy-daemon appears to be managing this profile store; changes made here may be overwritten until the daemon is stopped")
	}

	runtime := runner.New(wireproxy.NewRunner(), tailscale.NewRunner())
	core := application.New(profilejson.NewRepository(storePath), profilejson.Codec{}, runtime)
	loadErr := core.Load()
	ui.Run(core, loadErr)
}
