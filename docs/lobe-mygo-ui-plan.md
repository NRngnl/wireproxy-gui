# Synthesis: wireproxy-gui on lobe-mygo

Base: (`docs/lobe-mygo-ui-design.md`). Judge scored this design 26/30 against 16/30 for the alternative. The parent's independent read picked the same base.

## Grafts from the alternative design
1. Keep the existing two-method `profileFileDialog` seam for file effects.
2. Move the pure text helpers (status, meta, error titles, filenames) into `text.go` unchanged, with their existing tests, before any restyling.

Rejected from the alternative design: the model/controller/bridge pipeline, dropping per-profile log offsets, JSON-only import filter (`.conf` must stay), keeping the cgo dock helper, and the Fyne-first refactor phase.

## Corrections applied to the base
1. Dock: use `mygo.App.Dock.Hide()/Show()` and `App.ActivationPolicy()`. Delete `internal/ui/dock_darwin.go` and `dock_other.go` at cutover.
2. Linux: MyGo loads GTK3/GDK3 plus optional Ayatana AppIndicator through purego, not GTK4. A successful `NewTray` does not prove a panel shows the icon, so the window and Quit stay reachable without a tray.
3. Dialogs: native Open/Save with `.conf` and JSON filters. Cancellation, overwrite and 0600 export permissions get checked on real builds.
4. Version policy: lobe-mygo has no tagged release. Pinning its newest pseudo-version needs an owner-approved exception to AGENTS.md "Dependency versions", recorded there. Blocker until approved.

## Phases (from the base, one PR each)
Phase 0 proves MyGo viability: `CGO_ENABLED=0` builds for linux, darwin and windows, a tray, a dialog and a WASM preview render. Then feature slices, each with the design-skill loop (critique, audit, screenshots at 390 and 1280 px, light and dark, both densities, polish, `impeccable detect`). The last phase cuts over, removes Fyne and the cgo helper, and runs the full gates.
