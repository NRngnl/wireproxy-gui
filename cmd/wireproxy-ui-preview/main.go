//go:build js

package main

import (
	"log/slog"
	"net/url"
	"os"
	"syscall/js"

	"github.com/NRngnl/wireproxy-gui/internal/ui"
)

func main() {
	query, err := url.ParseQuery(js.Global().Get("location").Get("search").String()[1:])
	if err != nil {
		slog.Error("preview", "error", err)
		os.Exit(1)
	}
	core := newFixture(query.Get("fixture"))
	err = ui.Preview(core, ui.PreviewOptions{Dark: query.Get("theme") == "dark", Compact: query.Get("density") == "compact", RailOpen: query.Get("view") == "rail", Help: query.Get("help") == "1"})
	if err != nil {
		slog.Error("preview", "error", err)
		os.Exit(1)
	}
}
