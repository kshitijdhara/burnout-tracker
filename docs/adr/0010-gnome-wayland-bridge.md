# ADR-0010: GNOME Wayland bridge over a private socket

**Status:** Accepted
**Date:** 2026-10-27
**Deciders:** Project maintainers

## Context

ADR-0002 measures attention over *visible* windows, which needs the
stacking order, window geometry, focus, monitor layout and the pointer.
Wayland gives a client none of that about other clients. On GNOME only
code running inside GNOME Shell can read it, so we need a GNOME Shell
extension (system design §3.1, JavaScript, GPL-2.0-or-later) that hands it
to the agent.

The rules for that channel are in privacy & security §8.3 and threat T3
("other apps in the desktop session"):

- no public service on the session bus;
- a private peer-to-peer connection with a token handshake, and the
  extension verifies the caller;
- only geometry, stacking, focus and the pointer, plus titles only while
  title classification is on.

§15 adds that every cross-process contract has a JSON Schema in
`schemas/v1`.

## Decision

**The extension listens on an owner-only Unix socket. The agent connects
and says hello with a token it wrote to an owner-only file. The protocol
is newline-delimited JSON** (`schemas/v1/gnome-bridge.schema.json`).

- **Rendezvous.** Both sides use `$XDG_RUNTIME_DIR/burnout/`, the agent's
  runtime directory (`paths.RuntimeDir`). Each side creates it with mode
  0700 if needed. The extension refuses to listen unless it is a real
  directory owned by the user with no group or other access. The agent
  checks the same (`paths.EnsurePrivateDir`).
- **Token.** On first use the agent writes 32 random bytes, hex-encoded,
  to `gnome-bridge.token` (mode 0600, published with a hard link so no
  reader sees half a file). Every agent process, the daemon and `doctor`
  alike, reuses a valid token. A token with loose permissions or damaged
  contents is replaced. `$XDG_RUNTIME_DIR` is emptied at logout, so each
  login gets a new token.
- **Socket.** The extension listens on `gnome-bridge.sock` with
  `Gio.SocketService`. It removes a stale socket only when the old file is
  a socket owned by the user.
- **Handshake.** The first line on a connection is
  `{"v":1,"op":"hello","token":…,"titles":bool}`. Before it reads any
  bytes, the extension checks the peer's uid through `SO_PEERCRED`
  (`Gio.Credentials`). It then reads the token file fresh and compares in
  constant time. Any failure gets an error reply and the connection is
  closed. `titles` holds for the life of the connection.
- **Verifying the server.** Before sending the token, the agent checks
  that the socket's peer runs as the same uid and that its process is
  `gnome-shell`. A different process listening on the path never sees the
  token.
- **Snapshots.** After the hello, the agent sends `{"v":1,"op":"snapshot"}`
  once per tick. Each reply is raw facts: every window bottom-first with
  id, pid, WM class, GNOME app id, type, minimised, on-active-workspace and
  frame rect; the monitors; the focused window id; the pointer. Titles are
  included only when the hello asked. The extension filters nothing and
  computes nothing. Occlusion, workspace filtering and pointer hit-testing
  happen in the agent's pure, tested and fuzzed translate step (the same
  geometry code as X11).
- **Idle and lock** come from outside the extension: idle from
  `org.gnome.Mutter.IdleMonitor`, lock from logind (else GNOME's
  screensaver), using the D-Bus reader shared with the X11 collector. The
  agent reads lock state first. GNOME disables extensions on the lock
  screen, so a locked screen is reported as locked rather than as a bridge
  error.
- **Limits.** The extension never blocks GNOME Shell: all socket and file
  I/O is asynchronous, a request line is capped at 4 KiB, and an idle
  connection is closed after 15 s. The agent caps a reply at 1 MiB and
  gives each request 250 ms, so a busy shell can't stall the 1 s tick. It
  checks every count, coordinate and string length before use.
- **Versions.** Each side writes its own protocol version in `v`. Requests
  change only by adding fields, and unknown fields are ignored. An upgraded
  extension doesn't load into a running GNOME Shell until the user logs out
  and back in, so agent N always meets extension N-1 for a while. **The
  agent reads replies of version N and N-1.** Contract tests decode
  fixtures for both, and `doctor` says "log out and back in" for anything
  older.
- **Packaging.** The `.deb` and `.rpm` install the extension in
  `/usr/share/gnome-shell/extensions/<uuid>/`. It stays disabled. The
  post-install message and `doctor` tell the user to log out and back in,
  then run `gnome-extensions enable burnout-bridge@kshitijdhara.github.io`.
  Nothing enables it silently (AC-2).

## Options Considered

### Option A: Owner-only Unix socket, JSON lines, token handshake (chosen)

- The whole contract is JSON, so one JSON Schema describes both directions,
  as §15 requires. It is the same shape as the hook wire protocol.
- The decoder is plain Go, easy to fuzz, and needs no new dependency.
- There is no bus, so nothing can be enumerated or introspected on the
  session bus (T3).

### Option B: `Gio.DBusServer` peer-to-peer D-Bus plus a token

- D-Bus EXTERNAL authentication checks the uid for free.
- But the contract would be GVariant signatures, which a JSON Schema can
  only describe loosely, or JSON strings inside D-Bus. That is two
  framings and the same token work.
- godbus peer-to-peer connections without `Hello` are a less-trodden path.

### Option C: A well-known name on the session bus

- Rejected by §8.3: any app on the session bus could read window
  geometry, and titles when they are on.

### Option D: The agent listens and the extension connects

- This would let the agent own the socket path. But the extension would
  then need a reconnect loop inside GNOME Shell, and it is the extension
  that must verify the caller. Having the client present a token to the
  data owner keeps that check where §8.3 puts it.

## Trade-off Analysis

The token adds little against code already running as the user: it can
read the token file, and it could attach a debugger to GNOME Shell
anyway. That is outside our threat model, like any same-user malware. What
the design does stop:

- other users (the uid check and the 0700 directory);
- session-bus snooping (there is no bus name);
- sandboxed apps that share the uid but not the host `$XDG_RUNTIME_DIR`
  (Flatpak and Snap see only their own subdirectories). The token is the
  second lock if a sandbox is ever granted the socket path.

The socket is a named path, not an abstract socket, because abstract
sockets are reachable from any process in the same network namespace,
sandboxed apps included.

## Consequences

- GNOME on Wayland gets full attention data: geometry, stacking, focus and
  the pointer. Input counts stay unavailable, as on X11.
- The extension is GPL-2.0-or-later and ships its licence text. The
  packages are `Apache-2.0 AND GPL-2.0-or-later`.
- Each supported GNOME release must be listed in `metadata.json`'s
  `shell-version`, or GNOME won't load the extension. A new GNOME release
  needs a burnout release.
- KDE (a KWin script) and wlroots compositors (Sway or Hyprland IPC) still
  have no collector. They can reuse the protocol and translate step.
- `install.sh` installs only the binary, so users who install with it have
  to install the extension by hand for now.

## Action Items

1. [x] Extension, schema, Go client, contract and fuzz tests, `doctor`
   status, packaging.
2. [ ] Headless Mutter in CI to run the extension end to end
   (system design §14).
3. [ ] Install the extension from `install.sh` into
   `~/.local/share/gnome-shell/extensions`.
