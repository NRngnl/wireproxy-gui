# Architecture

Wireproxy GUI uses a pragmatic domain-driven, clean-architecture boundary. The
dependency direction is inward:

```text
Fyne UI ───────────────┐
                      v
                 Application ──────> Profile + Connection domain
                      ^                         ^
                      │                         │
JSON persistence ─────┘       Runtime adapters ┘

cmd/wireproxy-gui is the composition root that supplies every adapter.
```

## Layers

- `internal/profile` is the profile domain. It owns entities, validation,
  normalization, bind-address policies, and the distinction between changes
  that require a restart and Tailscale preferences that can be changed live.
- `internal/connection` is shared connection-domain vocabulary: lifecycle
  events, exit-node values, and stable port errors.
- `internal/application` owns use cases and application state. It coordinates
  profile CRUD, import/export, persistence, connection lifecycle, edit locks,
  auto-connect, Tailscale authentication state, logs, and graceful shutdown.
  Its `Repository`, `Codec`, and `Runtime` interfaces are outbound ports.
- `internal/profilejson` is the JSON repository and import/export adapter. It
  owns the persistence DTOs, maps them to domain entities, handles filesystem
  permissions and serialization, and contains no UI logic. The domain model is
  therefore independent of the JSON schema.
- `internal/wireproxy`, `internal/tailscale`, and `internal/runner` are runtime
  adapters. They translate domain profiles into concrete embedded backends;
  the composition root injects those backends into the dispatcher.
- `internal/ui` is the inbound Fyne adapter. It owns widgets, selection,
  localization, dialogs, tray presentation, and form-to-domain mapping. It
  invokes application use cases and never constructs or imports persistence or
  runtime adapters.
- `cmd/wireproxy-gui` is the only composition root. It chooses concrete
  adapters, loads application state, and passes the application boundary to the
  UI.

## Boundary rules

1. Domain packages do not import application, UI, persistence, or runtime
   adapters.
2. Application code depends only on domain packages and interfaces it owns.
3. UI depends inward on application/domain types; it does not persist profiles
   or control backend processes directly.
4. Outbound adapters implement application ports and may depend inward on
   application/domain contracts.
5. Wiring concrete adapters is restricted to the executable composition root.

`internal/architecture/architecture_test.go` enforces these import directions.
Behavioral tests live beside the layer that owns the behavior: domain policy in
`profile`, use cases in `application`, serialization in `profilejson`, backend
details in their runtime adapter, and widget behavior in `ui`.
