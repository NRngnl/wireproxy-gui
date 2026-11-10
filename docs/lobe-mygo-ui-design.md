# A native profile workbench

## Usage first

An operator opens the app, selects a profile, edits its configuration, and connects it. Connection state and activity remain visible without navigating away. Closing the window keeps tunnels running only when a working tray exists. Quit stops the application-owned tunnels and waits up to the existing five-second shutdown deadline.

A maintainer keeps the current application service, persistence format, embedded backends, TUI, and daemon. The replacement lives in `internal/ui`. It does not introduce a webview, HTTP API, JavaScript business layer, or replacement profile store.

Three caller examples define the boundary. These are design snippets, not executable additions. `ui` denotes the project's `internal/ui` adapter, `application` its existing application package, and `myui` MyGo's native UI package.

### Desktop composition

Keep the existing repository, codec, runtime construction, default-store fallback, and advisory daemon-lock warning in `cmd/wireproxy-gui/main.go`. Handle a new startup error from the adapter instead of terminating inside it.

```go
core := application.New(repository, codec, runtime)
loadErr := core.Load()
if err := ui.Run(core, loadErr); err != nil {
    fmt.Fprintln(os.Stderr, err)
    os.Exit(1)
}
```

`Run` owns window creation, observation, tray integration, auto-connect, and shutdown. A load failure appears in the window. It never becomes a successful empty-store message.

### Screen action

Within the package, one action owns save-before-connect. Neither the button nor the tray rebuilds profile business rules.

```go
if action.Button(c, "Connect", action.ButtonProps{Variant: action.Primary}).Clicked() {
    w.connectSelected()
}
```

The method captures an immutable draft and profile ID, validates it, saves it through `Application.Save`, and calls `Application.Connect` only after success. Login and the window's Connect All use the same save step. A per-profile tray Connect deliberately uses the saved profile directly, matching the existing app.

### Browser preview

A development-only `cmd/wireproxy-ui-preview` constructs the real `application.Service` with a memory repository, existing codec, and scripted runtime. It must not construct real embedded backends or open the user's store.

```go
core := application.New(memoryProfiles, codec, scriptedRuntime)
if err := core.Load(); err != nil {
    panic(err)
}
if err := ui.Preview(core); err != nil {
    panic(err)
}
```

`Preview` is available only in `js/wasm` builds. It renders the same workbench function as desktop. The preview command, not the shared UI, imports the concrete codec and supplies synthetic profiles. There is no browser network proxy to the desktop core.

## Grounded constraints

Evidence labels below refer to the supplied checkouts, not a claim about the latest released API. `MYGO` means the MyGo module directory named in `../GROUNDING.md`. `LOBE` means `.pi/lobe-mygo` at the repository root.

| Evidence | What it establishes |
| --- | --- |
| `cmd/wireproxy-gui/main.go:17-37` | The composition root chooses the store, probes the daemon lock, constructs adapters, loads the service, and calls the UI. The probe warns; it does not enforce GUI exclusivity. |
| `internal/ui/app.go:131-158` | The existing inbound `Application` port already covers the desktop feature set. |
| `internal/application/service.go:24-128` | Persistence and runtime are outbound ports. The service owns statuses, logs, pending starts, and shutdown. |
| `internal/application/service.go:219-276, 325-398, 592-674` | Save validates and rolls back failed live exit-node updates. Export selects saved profiles. Logout and shutdown are application operations. |
| `internal/application/service.go:784-802` | Logs are bounded to 1,000 entries per profile. Changes are best-effort invalidations, not a durable event log. |
| `internal/architecture/architecture_test.go:18-36` | `ui` may import application, buildinfo, connection, profile, and svcinstall, but not concrete storage or network backends. |
| `LOBE/examples/desktop/main.go`, `state.go`, `pkg/theme/apply.go` | The starter uses native `ui.View`, application-owned state, and generated light/dark tokens. Its chat tabs and workspace concepts are examples, not product requirements. |
| `LOBE/pkg/field/autocomplete.go`, `pkg/field/input.go`, `pkg/layout/panel.go` | Editable completion supports separate labels and values. Inputs use pointers. Panels resize and collapse. Loading props are not assumed. |
| `MYGO/docs/ui/views.md:183-216` | Background results enter main-thread state through `Window.Update`. Rendering must not share mutable fields with background jobs. |
| `MYGO/docs/menus.md:361-504`, `docs/native.md:7-28` | Tray menus, close-to-tray, native file dialogs, and cancellation are documented. Native dialog calls block and must not block event listeners. |
| `MYGO/docs/ui/browsers.md:1-109` | The same native UI draws on browser canvases. Native trays and file dialogs are unavailable there. Browser assistive technology cannot yet read the canvas content. |
| `MYGO/docs/architecture.md:22-35`, `docs/distribution.md:26-42` | MyGo documents cgo-free native bindings and cross-compilation. This is source evidence, not a successful build of this app. |

The current architecture comment in `internal/application/service.go` explicitly assigns business state to the service and desktop UI to an adapter. The import test makes that ownership a constraint. Preserve it rather than infer a new owner from the Fyne widget layout.

## Chosen surface

**Audience and mode.** This is an Operate surface for people configuring local SOCKS5 connections. The primary task is editing and controlling one profile while watching its activity. It is not a dashboard or marketing page.

**Visual authority.** Keep the existing app identity and assets. Use lobe-mygo's generated neutral surfaces, radii, text hierarchy, focus treatment, and semantic colors. Keep the existing connection meanings of gray, amber, green, and red. Every status also has text. Do not copy the starter's chat composer, model picker, workspace switcher, or browser-style tab history.

**Desktop composition at 1280 px.** A roughly 260 px resizable profile rail sits beside a workbench. The rail has profile and connected counts, Add Profile, Import, Help, and all-profile actions. The workbench has a quiet name/backend/bind header with its connection action. The editor occupies the center. A horizontally spanning Activity panel occupies the bottom of the workbench, with a draggable seam. This replaces the Fyne editor/log side-by-side split while retaining resizing and simultaneous visibility. No fake speed chart or traffic metric is added.

**Narrow composition at 390 px.** Show the profile list first. Selecting a row opens the profile workbench with an explicit Back to Profiles control. Configuration and Activity become two tabs, sharing the same underlying draft and log position. A full-height editor scrolls vertically. Rows of port-forward inputs stack. Save and Connect remain reachable without horizontal page scrolling. The only horizontal scroll belongs to unwrapped configuration text when needed.

**Density.** Provide comfortable and compact UI density as a session-level view choice. Comfortable is the default. Map them to existing lobe control sizes and spacing tokens. On narrow or touch layouts, hit areas remain at least 44 px even when text density is compact. Do not add a profile-store schema field for appearance settings.

**Typography and behavior.** Use locally available system UI fonts, with a local monospace face for configuration and activity. No remote font, avatar, icon, analytics, script, or image requests originate from the shipped UI. Existing embedded backend networking and explicit Tailscale sign-in are product functionality, not presentation dependencies. Motion only explains state changes and respects reduced motion.

**First-run and feedback.** An empty store shows Add Profile and Import plus a short explanation of supported formats. Load failure is a persistent alert distinct from empty state. Save/import/export success appears next to the operation. Errors remain readable until dismissed or corrected. Long operations show textual progress and disable their own duplicate action, not the whole window.

## Module map and ownership

All production presentation files remain in the existing `internal/ui` package. Files divide knowledge, not a pipeline of generic controllers, presenters, and repositories. Target about 150 to 250 lines per production file and split by feature if a file approaches 300.

| Proposed file or location | Owned knowledge |
| --- | --- |
| `internal/ui/application.go` | The existing frontend-owned `Application` port, unchanged in method set and domain types. No toolkit objects on this port. |
| `internal/ui/workbench.go` | Root view, selected profile, responsive topology, and main-thread-only UI state. |
| `internal/ui/observe.go` | One consumer of `Changes`, focus requests, and query projection. Publishes read-only frames through `Window.Update`. |
| `internal/ui/profile_draft.go` | UI text input, conversion to `profile.Profile`, secret reconciliation, and deep copying mutable forward slices. |
| `internal/ui/profile_actions.go` | Save-before-connect/login/all, operation feedback, import/export selection, deletion, and captured operation identity. |
| `internal/ui/profile_list.go`, `profile_editor.go` | Profile rail, shared identity/bind fields, connection header, and backend selection. |
| `internal/ui/wireguard_form.go`, `tailscale_form.go`, `port_forwards.go` | Backend-specific controls, exit-node choices, and relay-rule editing. |
| `internal/ui/activity.go`, `help.go`, `appearance.go` | Per-profile log position/follow behavior, usage content, density, and token application. |
| `internal/ui/run_desktop.go`, `files_desktop.go`, `tray_desktop.go`, `service_desktop.go`, `preferences_desktop.go` | Desktop-only startup, native dialogs/file writes, tray and quit lifecycle, svcinstall workflow, and local Dock preference migration. Excluded from `js/wasm`. |
| `internal/ui/run_preview.go` | `js/wasm` window host and explicit preview substitutes for desktop-only actions. |
| `internal/ui/i18n.go`, `translations/en.json`, existing assets | Retained message keys, structured error/log translation, app icon, and tray identity. Remove Fyne imports without changing catalog meaning. |
| `cmd/wireproxy-ui-preview` | Development fixture selector, memory repository, synthetic runtime, and WASM entrypoint. No real secrets, real repository reads, or real service installation. |
| `cmd/wireproxy-gui` | Existing composition plus propagation of UI startup errors. |

The import direction stays `cmd -> ui -> application -> profile/connection`. `svcinstall` remains a desktop adapter dependency. Do not create `ui/native` or `ui/screens` subpackages that escape the existing test's shallow directory scan. The architecture test currently parses build-tagged files too, so preview code inside `ui` must obey the same allowed-import list.

There is one production renderer and one application port. Build constraints separate actual platform effects, not alternate implementations of business policy. Desktop `Run` and browser `Preview` are the only final entrypoint distinction. The `_desktop.go` suffix alone has no Go build-selection meaning; desktop-only files need explicit `//go:build !js` constraints.

During staging, keep the old exported Fyne `Run` as the production entrypoint and implement the new native host privately. Move the existing `Application` declaration to its framework-free file once, rather than duplicate it. Explicitly exclude the old Fyne widget, resource-wrapper, theme, dialog, and Dock files from `js/wasm`; remove Fyne logging from shared translation code. `Preview` then imports only the shared lobe renderer. Phase 4 replaces `Run` with the new host and removes the temporary Fyne files and constraints. A passing preview build before that step must not be presented as a cgo-free build of the still-Fyne desktop command.

## Data structures and type sketch

The following Go snippets are declarations of the proposed shape. Bodies intentionally say `not implemented`. They are not proposed source files to compile in this runner.

### Entry and UI state

```go
// Run owns the desktop lifecycle and returns startup or shutdown failures.
func Run(core Application, loadErr error) error {
    return errors.New("not implemented")
}

// Preview runs the shared screens in js/wasm against a supplied synthetic core.
func Preview(core Application) error {
    return errors.New("not implemented")
}

type workbench struct {
    core       Application
    window     *mygo.Window
    selectedID string
    frame      presentationFrame
    profiles   map[string]*profileViewState
    density    theme.Size
    platform   platformState
    cancel     context.CancelFunc
}

type profileRow struct {
    ID, Name, Bind, Network string
    Kind                   profile.BackendKind
    Status                 application.Status
}

type selectedProfile struct {
    Profile       profile.Profile
    Status        application.Status
    RuntimeLocked bool
    Logs          []application.LogEntry
}

type presentationFrame struct {
    Rows     []profileRow
    Selected *selectedProfile
}

type profileViewState struct {
    Draft       profileDraft
    Log         myui.ScrollState
    Follow      bool
    ExitNodes   []connection.ExitNode
    ExitRequest uint64
    Saving      bool
    Refreshing  bool
}

func (w *workbench) view(c *myui.Context) {
    panic("not implemented")
}

func (w *workbench) observe(ctx context.Context, focus <-chan string) {
    panic("not implemented")
}

func readFrame(core Application, selectedID string) presentationFrame {
    panic("not implemented")
}

func (w *workbench) apply(frame presentationFrame) {
    panic("not implemented")
}
```

`Application` is copied exactly from the current `internal/ui/app.go:131-158`, not expanded into one interface per button. `platformState` has target-specific declarations. Desktop owns tray, installer status, quit phase, and file-dialog progress. Browser owns only preview notices. Shared screen code cannot reach native service or file handles.

`presentationFrame` is a disposable query projection, not another authoritative profile store. Rows retain no auth key or WireGuard configuration. Only selected detail contains editable configuration and its bounded log slice. Row order follows `Profiles()`. The per-ID map holds only presentation state. It permits returning to a profile without losing unsaved edits or a paused log. It is never serialized or exported. Delete removes its entry.

### Editable input and actions

```go
type profileDraft struct {
    Value     profile.Profile
    PortText  string
}

func draftOf(saved profile.Profile) profileDraft {
    panic("not implemented")
}

func (d profileDraft) profile(current profile.Profile) (profile.Profile, error) {
    return profile.Profile{}, errors.New("not implemented")
}

func (w *workbench) saveSelected() {
    panic("not implemented")
}

func (w *workbench) connectSelected() {
    panic("not implemented")
}

func (w *workbench) loginSelected() {
    panic("not implemented")
}

func (w *workbench) refreshExitNodes(profileID string) {
    panic("not implemented")
}

func (w *workbench) connectFromTray(profileID string) {
    panic("not implemented")
}

func (w *workbench) disconnectFromTray(profileID string) {
    panic("not implemented")
}
```

`Value` is a detached copy, including its forward slice. ID, timestamps, and `Authenticated` are not editable controls. `profile(current)` starts from the current saved identity, transfers editable fields, and preserves the current authentication marker. On authentication success, `apply` removes auth-key text even from an unsaved draft. The input never restores a consumed key from an old copy.

`PortText` is deliberately text so malformed input is representable without becoming zero. Parse it once at the form boundary. Reject empty host, nonnumeric or out-of-range port, unsupported backend, invalid WireGuard configuration, and invalid forwards using domain errors. Validate before normalization can turn invalid required fields into defaults. For automatic exit, clearing a manual selection is an explicit mode change, not an error fallback. Saved backend changes clear the inactive backend configuration as today.

No snapshot is atomic across the service's several query methods. Treat `Change` as invalidation and derive fresh output, rather than applying event deltas as truth. The observer is the only background producer of presentation frames. It also receives selected IDs from the UI, avoiding reads of live UI fields from another goroutine. It queries rows and only the focused profile's logs, then applies on the window thread. A selected-detail result is accepted only for the still-selected ID. Focus changes request a fresh frame. There is no idle polling timer.

Commands capture detached input before starting goroutines. They report success/error for the captured profile ID. Only the observer replaces query frames. Main-thread code alone edits drafts, scroll states, pending indicators, or exit-node choices. Busy state prevents duplicate saves for a profile but never turns an in-progress connection's Cancel into an unavailable action. Closing cancels outstanding UI work. An exit-node result carries both profile ID and request generation and is ignored after deletion, replacement, or a newer request.

### Platform effects

```go
func (w *workbench) importProfiles() {
    panic("not implemented")
}

func (w *workbench) exportProfiles(profileIDs []string, suggestedName string) {
    panic("not implemented")
}

func (w *workbench) installService(force bool) {
    panic("not implemented")
}

func (w *workbench) uninstallService() {
    panic("not implemented")
}

func (w *workbench) requestQuit() {
    panic("not implemented")
}
```

Desktop definitions perform native work asynchronously and return to `Window.Update`. Preview definitions show an explicit unavailable notice or use the preview command's synthetic import/export fixture path. They never claim to have written a native file or changed login services. Browser-visible confirmation and error surfaces are the same lobe components as production.

## Feature-to-screen inventory

Every feature below is observed in `internal/ui/app.go` unless another file is named. New layout is allowed; removing the behavior is not.

| Existing feature and evidence | New home and preservation rule |
| --- | --- |
| Window identity, icon, version title, initial profile selection. `Run`, `build`, `appicon.go` | Native main window with `buildinfo` title and existing icon. Select the first saved profile at launch. |
| Profile list, name truncation, status icon/text, SOCKS5 bind, profile/connected counts. `build`, `updateProfileListItem`, `profilesSummaryText` | Profile rail. Long names truncate visually but expose their full accessible name. Count running profiles only. |
| Add and guided empty state. `addProfile`, `build` | Rail and empty-state Add Profile call `Application.Add`. Preserve default naming and application-owned port selection. |
| Backend kind, name, bind host/port, connect-when-app-opens. `profileFromForm` | General section in profile editor. Preserve these fields, not a new profile schema. |
| WireGuard raw multiline editor with small minimum and no wrapping. `newWireGuardConfigEntry` | WireGuard configuration section. Use a lobe field where possible; use MyGo's existing code-editor primitive, themed with lobe tokens, if TextArea cannot retain unwrapped multiline editing. No custom editor engine. |
| Tailscale hostname, optional auth key, control URL, ephemeral registration. `setupTailscaleForm` | Tailscale identity section with persistent visible labels and hints. Auth remains a password control. |
| Login without key, browser URL and approval messages, authenticated/keyless state, Logout. Usage guide, `tailscaleAuthActionSelected`, `handleChange` | Authentication group and readable Activity. Preserve selectable sign-in URL and all approval messages. Save before Login. Logout calls the core and unlocks auth only after success. No automatic URL navigation from arbitrary logs. |
| Exit refresh, automatic refresh on selected profile start, manual ID/host/IP, labels mapped to stable ID, empty list, duplicate display labels. `refreshExitNodeOptions`, `applyExitNodeOptions`, `handleChange` | Exit routing section. `field.AutoComplete` separates stable values from labels. Keep manual text entry. Refresh explicitly reports loading/error. Choices do not become an invented live tailnet subscription. |
| Automatic exit and allow LAN access. `updateExitNodeControlState`, `profileFromForm` | Exit routing section. Automatic mode disables manual node input and clears manual value on save. Connected Save applies permitted exit changes through the existing rollback-capable core. |
| TCP/UDP port forwards with listen port, target, add/remove. `setupTailscaleForm`, `addPortForwardFromInputs` | Forward rules section. Keep both protocols and rule order. Validate via the domain. Runtime-affecting edits remain subject to `Application.Save` lock policy. |
| Runtime field locks and editable name/startup preference. `updateRuntimeFieldState` | Derive enablement from `RuntimeLocked`, status, and canonical auth marker. Lock backend/bind/config/identity/auth/lifetime and runtime forward settings during activity. Exit routing is editable while running or stopped, not starting/stopping. Name and startup remain editable. |
| Save, feedback, no-op save result. `saveSelected` | Editor action row. Keep success/error feedback and the draft on failure. No implicit persistence on every keystroke. |
| Connect, Cancel Connection, Disconnect, disabled Disconnecting. `refreshButtons` | Header primary action. Starting remains cancellable; stopping is progress, not a second Connect button. |
| Connect All and Disconnect All. `connectAll`, `disconnectAll` | Rail footer or narrow overflow. Window and global tray Connect All save the current draft first, as today. Disconnect All never requires saving. |
| Delete confirmation, active-profile stop, first-profile fallback. `deleteSelected` | Destructive profile action with lobe confirmation. Application owns persistence and stop. Remove UI state only after success. |
| Import JSON or WireGuard `.conf`, cancel no-op, first-import selection, feedback. `importProfilesFromPath`, `native_file_dialog.go` | Import action opens native picker. Read exact chosen path and pass basename/bytes to `Application.Import`. Keep errors visible. |
| Export selected/all, filename normalization, JSON, chosen path, restrictive permissions. `exportProfiles`, `exportProfilesToPath`, `safeFileName` | Profile Export and Export All. Export saved profiles, not unsaved drafts. Confirm replacement. Preserve `0600` creation and tightening of existing-file permissions where supported. |
| Export excludes Tailscale local state/authenticated marker. Usage guide and `Application.Export` | Remains codec policy. UI never serializes the draft or tsnet state itself. Help explains re-login on import. |
| Activity timestamps, localized messages, blank-line skipping, default follow, pause on scroll, resume at bottom, per-profile offsets. `setupLogView`, `setLogText`, `profileLogText` | Bottom Activity panel or narrow Activity tab. Use keyed `ui.ScrollState`; preserve independent follow/offset per profile. No second log buffer beyond service retention. |
| Resizable profile/detail and editor/activity allocation. `build`, `newConfigLogSplit` | Resizable rail and bottom Activity panel. Narrow tabs preserve both views without imposing the desktop minimum width. |
| Load, synchronous, and asynchronous operation errors. `Run`, `handleChange`, `showError`, `i18n.go` | Persistent lobe alert or modal with operation and message, without duplicated title prefix. No transient-only failure toast. |
| Auto-connect on application startup. `startAutoProfiles` | Call `AutoConnect` once after observation starts. Show its returned failures and retained profile logs. A rerender never starts connections. |
| Tray Show/focus, profile submenus and status icons, status/bind/backend details, per-profile actions. `trayMenu`, `profileTrayMenuItem` | Native `mygo.Tray` with real nested `mygo.Menu` items. Stable ID captures avoid index-based actions. Connect is saved-profile-only; Disconnect uses `DisconnectFromTray` to keep its log provenance. |
| Tray Connect All, Disconnect All, Quit. `trayMenu`, `trayQuitMenuItem` | Native menu calls the same workbench actions. Quit has no new confirmation requirement. It follows bounded shutdown rather than destroying the tray first. |
| Start on Login supported/unsupported state, install/uninstall progress, success/failure, force after lock conflict, confirmation before uninstall. `serviceInstallMenuItem` through `uninstallService` | Native tray service section. Keep `svcinstall`, per-user daemon semantics, and no elevation. Never substitute MyGo's GUI auto-launch API for the daemon installer. Query status asynchronously, not while building a menu. |
| Hide Dock Icon preference on macOS. `hideDockIconMenuItem`, `dock_darwin.go` | Checked tray item backed by MyGo Dock/activation APIs. Preserve stored preference through an explicit one-time migration. No Objective-C cgo helper remains. |
| Close hides only with tray, Show restores/focuses, Quit drains tunnels. `installTray`, `shutdown` | Install close interception only after successful tray creation. No tray means normal close enters bounded shutdown. Global Quit, tray Quit, smoke-test exit, and OS close converge on one quit path. |
| English catalog, structured error/log localization, scrollable translated usage guide. `i18n.go`, `showUsageGuide` | Retain keys and interpolation behavior. Help is a scrollable lobe surface containing every existing usage paragraph, updated only where the toolkit/layout changes factual claims. |
| Release GUI smoke environment variable. `Run` | Preserve `WIREPROXY_GUI_SMOKE_TEST=1`. Schedule exit after the real window starts and use the normal shutdown path. |

The runtime lock row makes forward editing consistent with the service's existing rejection of runtime configuration changes. It does not add a new core restriction. The new frontend must not promise unsupported live forward reconfiguration.

## Tray, native dialogs, preferences, and quit

Create a single native tray after MyGo readiness. Use the existing monochrome identity as a locally rasterized PNG template on macOS and the application icon elsewhere. Native submenu status icons can use `NewMenuIconSVG` with the existing status artwork. Keep status words because some Linux panels omit menu icons.

On Linux, `NewTray` can fail without `libayatana-appindicator3` or an available panel. Surface that failure nonfatally and leave the window available. Never hide its only recovery surface. Keep a normal application menu and keyboard-accessible Quit even without a tray. On macOS retain application and Edit roles so standard quit, text editing, and clipboard behavior work.

`Dialog.Open` uses `conf` and `json` filters with single selection. `Dialog.Save` supplies the existing safe suggested JSON filename and overwrite behavior. Empty selection is cancellation, not an import error. Picker, file read/write, service status, install/uninstall, Logout, and live Save do not run in the rendering callback. Native dialog failure reports the operation without logging contents or keys. File filters assist selection but are not validation; the application codec remains the trust boundary.

The old `hideDockIcon` setting is stored by Fyne, not by the profile repository. Read only that setting from Fyne's documented per-platform preference location during migration, without linking Fyne. Write a minimal application-owned preferences file in the existing application identity's config area. Check types and report malformed/unreadable preferences; never replace a corrupt file silently. Save changes atomically before changing the actual Dock state. Do not change profile-store paths to MyGo defaults. The exact old preference locations are an implementation discovery gate, not guessed paths in this plan.

Quit uses a three-phase local state of idle, stopping, and permitted. `OnBeforeQuit` prevents default while stopping begins in a worker. `Application.Shutdown` gets the existing five-second context. Report deadline/failure to stderr and visible feedback when a window can still show it, then permit quitting as the current bounded-exit behavior requires. The second `App.Quit` is allowed through; it must not start another shutdown. Service shutdown already uses `sync.Once`. Closing a window with a healthy tray only hides it and never cancels the workbench. Hide Dock is disabled unless tray recovery is available.

Single-instance support is available in MyGo but is not an existing Fyne feature here. Do not silently introduce its default global lock or claim it replaces the advisory daemon-store probe. This design preserves the current probe and leaves stronger multi-writer coordination outside the UI migration. The known daemon-versus-GUI warning remains important when Start on Login is enabled.

## WASM preview and design-skill browser loop

Build only `cmd/wireproxy-ui-preview` for `js/wasm`; do not attempt to compile the production composition with tsnet, real profile storage, or native service execution. Reuse MyGo's documented browser host and the local lobe build pattern in `scripts/build-wasm.ts`, not its entire docs website. The host contains only local `app.wasm`, Go's matching `wasm_exec.js`, and minimal HTML. Browser size controls the canvas, so production window minimums must not defeat the 390 px check.

The preview command provides deterministic scenarios for empty state, WireGuard, Tailscale unauthenticated/authenticated, starting/running/stopping/error, manual/automatic exit, forwards, long logs, paused logs, long names, validation failure, load failure, operation failure, and confirmation surfaces. Use the same application domain validation and existing codec where applicable. Synthetic runtime implements the existing `application.Runtime` port, which is a genuine external boundary. No real tunnel or authentication provider is contacted. Auth keys and sign-in links are absent or visibly synthetic and never copied from a user's store.

For each implemented visual slice:

1. Load `design-taste-frontend` and its app-screen guidance. The installed copy inspected for this design lacks `references/app-screens.md`; resolve that missing reference before calling the implementation gate complete. Do not substitute a marketing-page hero workflow.
2. Run Impeccable context, then critique and audit against the real rendered state. This design-only runner did not execute generators that could write outside its directory. Operate mode and the existing tokens govern the chosen restrained composition.
3. Start the local WASM host. Use the taste skill's `scripts/inspect.mjs --app` flow against it. Inspect actual canvas screenshots and drive input with the browser. It is not a DOM mock of the screen.
4. Exercise light/dark and comfortable/compact at 390 px and 1280 px. Keep viewport height recorded with each capture. Check all scenario states relevant to the slice, not only the attractive default.
5. Fix what the screenshots show, then polish. Use quieter only if the result is noisy and bolder only if hierarchy is weak. Run `impeccable detect --json <changed files>` and report unsupported Go/canvas checks rather than claiming a DOM detector proves them.
6. Keep review JPEGs at no more than 1280 px wide. Scrub screenshots, errors, native selected paths, fixtures, and logs for PII/secrets before publishing. Publish implementation evidence under a stable project artifact name; artifact links belong in review chat, never PR text or commits.

Canvas screenshots can prove layout, contrast, clipping, focus visibility, and keyboard interaction. They cannot prove native tray recovery, file permissions, OS file dialogs, Dock migration, or screen-reader access. MyGo explicitly lacks browser assistive-technology exposure. Run the native accessibility inspector and screen-reader pass on each supported desktop. Label results separately. Do not waive accessibility because the browser DOM is empty.

The planned accessibility pass checks visible labels, named icon controls, status text, focus order, input errors, modal focus containment/return, Escape behavior, keyboard selection and scrolling, reduced motion, contrast, and narrow-layout target size. It also checks that rerendering logs does not steal focus or reset input composition.

## Cgo verification and dependency gate

**Observed, not built.** The local MyGo docs specify `CGO_ENABLED=0`. macOS source `internal/darwin/objc.go` calls Objective-C through purego; Windows `internal/windows/win32.go` loads system DLLs through syscall. Linux bindings use runtime-loaded libraries. The current `internal/ui/dock_darwin.go` explicitly imports `C`, so retaining that helper would violate the new build requirement.

**No misleading equivalence.** Cgo-free does not mean statically linked, no native libraries, or no GPU driver. Native Linux still needs the documented toolkit and renderer libraries, and tray support needs AppIndicator. A cross-build alone does not prove those libraries exist on the target system.

**Dependency gate.** Use the latest stable Go. lobe-mygo and MyGo have no tagged release, so pin their newest commit pseudo-versions under the owner-approved exception in AGENTS.md, and verify Apache-2.0-compatible licenses. Run stale and vulnerability checks before each commit.

For every actual release target, require a successful production build with `CGO_ENABLED=0`, plus dependency inspection for nonempty `CgoFiles`. Cover macOS amd64/arm64, Windows amd64/arm64, and Linux amd64/arm64 if retained by the release matrix; do not advertise a new architecture merely because MyGo documents it. Include GUI, TUI, and daemon outputs. Exclude the old Dock helper and all Fyne imports from the migrated GUI dependency graph. Separately compile `js/wasm` preview with cgo disabled. Then smoke the native artifact on its OS, including no-tray Linux behavior. Packaging/signing is an OS-specific check, not implied by compilation.

## PR and phase sequence

This is a proposed implementation sequence, not executed work. No version bump, release, tag, commit, or push is authorized by this design runner. The Fyne entrypoint remains the working production UI until the cutover gate passes.

| Phase / proposed PR | Small deliverable | Required check before proceeding |
| --- | --- | --- |
| 0. Framework viability | Resolve latest stable APIs/licenses, missing app-screen guidance, native dependency requirements, and old preference locations. Establish cgo-free native and WASM smoke feasibility without migrating features. | Verify builds and real windows on supported OS families. Verify a real tray submenu, native picker cancellation/overwrite, Dock hide/show, and native accessibility. An incompatible stable dependency or inaccessible required control blocks the migration. |
| 1. Shared workbench and preview | Add the lobe renderer, UI state/drafts, observer, responsive topology, local assets, and preview command. Extract the shared port and translation code; explicitly exclude old Fyne files from browser builds. Keep the new desktop host private and unwired from the production Fyne command. | Build preview without importing production backends. Complete light/dark, both-density, 390/1280 browser loop for empty/list/detail/error states. Existing application and architecture checks remain green. |
| 2. Profile and transfer parity | Wire common fields, WireGuard configuration, add/save/delete, native import/export, selected/all connection actions, and Activity follow/offset. | Exercise every affected inventory row with synthetic inputs. Run existing application/profile/codec checks and port relevant existing UI assertions to the replacement. Verify existing-file export permissions and no draft overwrite on log updates. No redundant new test suite. |
| 3. Tailscale parity | Wire authentication, canonical auth-key clearing, explicit/manual exit selection, live exit Save, field locks, and TCP/UDP forwards. | Exercise authenticated/unauthenticated and every runtime status. Verify stale exit responses cannot affect another profile. Run existing Tailscale/application checks and the relevant browser scenario matrix. |
| 4. Desktop lifecycle and cutover | Wire native tray, svcinstall, force/uninstall confirmations, Dock preference migration, bounded quit, release smoke environment behavior, and the production command. Remove Fyne widgets and Objective-C Dock code in this cutover. | Full inventory signoff on real supported desktops. Require no-tray recovery, service-conflict handling, preference migration, native accessibility, normal/smoke quit, and all cgo-free release builds. Run gofmt, golangci-lint, existing repository tests, and dependency/vulnerability checks after removal. |
| 5. Packaging and subtraction | Remove obsolete Fyne resources/dependencies and build prerequisites, retain useful app assets, update release packaging and affected documents, and keep preview development-only. | Inspect built dependencies and bundles, rerun only affected packaging/build gates, confirm no Fyne/webview presentation dependency or preview tooling ships, and confirm README/SECURITY describe actual limits. |

Every behavior, dependency, configuration, environment, licensing, or platform-limit change updates affected README and SECURITY documentation in the same implementation commit. Change AGENTS only if a rule actually changes. Preserve Go doc comments on changed exported symbols. Never hand-edit CHANGELOG; release-please owns it.

Before each eventual commit, read staged/unstaged diffs and new files, scan messages and branch names, run required `gitleaks git --staged`, and manually review PII hits. Before any push, inspect every unpushed message/diff and run the required history scan. Stage explicit paths only. None of those commands authorizes exposing a discovered secret in review output.

## Risks and decisions

| Risk | Decision or blocking evidence required |
| --- | --- |
| Latest stable framework differs from the inspected pseudo-version | Phase 0 resolves APIs and maintenance status. No hidden prerelease pin or vendoring of an unmaintained workaround. |
| Native accessibility differs from browser screenshots | Native screen-reader results are a shipping gate. WASM is a visual and keyboard harness, not accessibility parity evidence. |
| Lobe props missing for loading, editable completion disablement, or unwrapped configuration | Use existing returned `ui.Element` state and MyGo primitives with lobe tokens. Do not invent unsupported props or build a general component framework. Prove the required behavior in phase 0/1. |
| GUI and daemon can both access the same store | Preserve the advisory warning and force-install confirmation. Do not claim MyGo single-instance locking fixes daemon ownership. Stronger coordination is not part of this rewrite. |
| Core calls may hold a service mutex during I/O | Query projection and operations stay off the window thread. Do not add optimistic canonical statuses to hide latency. Measure real workloads before claiming a speed improvement. |
| Best-effort Change channel can omit error events | Preserve query refresh and retained core activity. Show direct operation errors. Do not advertise durable notification delivery or add a new event bus in this UI migration. |
| Auth completion races with an unsaved form | Canonical authentication marker wins; clear consumed key text on the main thread and never rebuild it from a stale draft. |
| Native library or panel missing on Linux | Keep the window and Quit available; disclose tray/runtime prerequisites. Cgo-free is not dependency-free. |
| Preferences use a legacy framework-specific location | Discover and validate actual old locations, migrate only the existing Dock boolean, preserve malformed files, and report failure. |
| Quit deadline expires | Preserve bounded exit, surface failure, and do not claim guaranteed cleanup after timeout. |
| No profiling or runtime validation in this design task | Make no CPU, allocation, startup, or memory improvement claim. Capture representative measurements during implementation if performance changes are proposed. |

## Candidate self-review and handoff

This design is grounded and sketched only. Corrections and grafts are in `lobe-mygo-ui-plan.md`. It has not been implemented, compiled, rendered, profiled, or release-tested.

The design-red-flags pass keeps one renderer, one application owner, one observer, and one UI-state owner. It rejects a pass-through service wrapper and public wire DTOs. Feature files share one private workbench rather than importable presenter internals. Tray and window actions reuse the same policy-bearing methods except the explicitly different saved-profile tray Connect behavior. The inventory is a migration checklist, not another runtime registry to keep synchronized.

The deliberate structural choice is a single native workbench with a bottom activity surface and build-separated effects. The browser preview reuses screens and application policy instead of creating a second web frontend. The next engineer inherits the existing core boundary, not a new service architecture.
