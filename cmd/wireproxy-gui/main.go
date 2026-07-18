package main

import (
	"path/filepath"

	"github.com/NRngnl/wireproxy-gui/internal/application"
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
	runtime := runner.New(wireproxy.NewRunner(), tailscale.NewRunner())
	core := application.New(profilejson.NewRepository(storePath), profilejson.Codec{}, runtime)
	loadErr := core.Load()
	ui.Run(core, loadErr)
}
