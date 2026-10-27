// SPDX-License-Identifier: GPL-2.0-or-later
//
// burnout bridge: serves window stacking, geometry, focus, monitor layout
// and the pointer position to the local burnout agent on GNOME Wayland,
// where no other process can read them (docs/adr/0010-gnome-wayland-bridge.md).
//
// The contract is schemas/v1/gnome-bridge.schema.json: newline-delimited
// JSON over an owner-only Unix socket at $XDG_RUNTIME_DIR/burnout/
// gnome-bridge.sock. Privacy & security §8.3 rules apply:
//   - no session-bus name or exported object, ever;
//   - a caller must run as this user (SO_PEERCRED) and present the token
//     the agent wrote to $XDG_RUNTIME_DIR/burnout/gnome-bridge.token;
//   - replies carry only geometry, stacking, focus and the pointer, plus
//     titles when the agent's hello asks for them (title classification
//     on). Nothing is logged except errors, and never a title.
//
// All socket and file I/O is asynchronous: a stalled agent must never
// block the compositor.

import GLib from 'gi://GLib';
import Gio from 'gi://Gio';
import Meta from 'gi://Meta';
import Shell from 'gi://Shell';
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Config from 'resource:///org/gnome/shell/misc/config.js';

// Protocol version this extension speaks. The agent accepts this and the
// one before it, since an upgraded extension only loads after a re-login.
const VERSION = 1;

const SOCKET_NAME = 'gnome-bridge.sock';
const TOKEN_NAME = 'gnome-bridge.token';
const TOKEN_RE = /^[0-9a-f]{64}$/;

// Limits, kept in step with the schema and internal/adapters/collector/wayland.
const MAX_REQUEST_BYTES = 4096;
const MAX_WINDOWS = 512;
const MAX_MONITORS = 32;
const MAX_CLASS_CHARS = 256;
const MAX_TITLE_CHARS = 1024;

// A connection with no request for this long is closed. The agent asks
// every second, so only a stuck or departed client gets here.
const IDLE_SECONDS = 15;

const encoder = new TextEncoder();
const decoder = new TextDecoder();

function windowType(w) {
    if (w.is_override_redirect())
        return 'other';
    switch (w.get_window_type()) {
    case Meta.WindowType.NORMAL:
        return 'normal';
    case Meta.WindowType.DIALOG:
    case Meta.WindowType.MODAL_DIALOG:
        return 'dialog';
    case Meta.WindowType.UTILITY:
    case Meta.WindowType.TOOLBAR:
        return 'utility';
    case Meta.WindowType.DESKTOP:
        return 'desktop';
    case Meta.WindowType.DOCK:
        return 'dock';
    default:
        return 'other';
    }
}

function rect(r) {
    return {x: r.x, y: r.y, w: r.width, h: r.height};
}

function clip(s, n) {
    return typeof s === 'string' ? s.slice(0, n) : '';
}

// snapshot reads the desktop as raw facts. Filtering, occlusion and
// hit-testing happen in the agent, where they are tested.
function snapshot(titles) {
    const display = global.display;
    const workspace = global.workspace_manager.get_active_workspace();
    const tracker = Shell.WindowTracker.get_default();

    const metas = global.get_window_actors().map(a => a.get_meta_window()).filter(w => w);
    // Bottom first, the order the schema pins.
    const stacked = display.sort_windows_by_stacking(metas).slice(-MAX_WINDOWS);

    const windows = stacked.map(w => {
        const out = {
            id: w.get_id(),
            pid: Math.max(0, w.get_pid()),
            wm_class: clip(w.get_wm_class(), MAX_CLASS_CHARS),
            app: clip(tracker.get_window_app(w)?.get_id(), MAX_CLASS_CHARS),
            type: windowType(w),
            minimised: w.minimized,
            on_workspace: w.located_on_workspace(workspace),
            frame: rect(w.get_frame_rect()),
        };
        if (titles)
            out.title = clip(w.get_title(), MAX_TITLE_CHARS);
        return out;
    });

    const monitors = [];
    const n = Math.min(display.get_n_monitors(), MAX_MONITORS);
    for (let i = 0; i < n; i++)
        monitors.push(rect(display.get_monitor_geometry(i)));

    const [px, py] = global.get_pointer();
    return {
        v: VERSION,
        op: 'snapshot',
        windows,
        monitors,
        focus: display.focus_window?.get_id() ?? 0,
        pointer: {x: px, y: py},
    };
}

// timingSafeEqual compares two strings without stopping at the first
// difference.
function timingSafeEqual(a, b) {
    if (a.length !== b.length)
        return false;
    let diff = 0;
    for (let i = 0; i < a.length; i++)
        diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
    return diff === 0;
}

// ownedPrivateDir reports whether path is a directory (not a symlink) owned
// by this user with no group or other access.
function ownedPrivateDir(path, uid) {
    try {
        const info = Gio.File.new_for_path(path).query_info(
            'standard::type,unix::uid,unix::mode', Gio.FileQueryInfoFlags.NOFOLLOW_SYMLINKS, null);
        return info.get_file_type() === Gio.FileType.DIRECTORY &&
            info.get_attribute_uint32('unix::uid') === uid &&
            (info.get_attribute_uint32('unix::mode') & 0o077) === 0;
    } catch {
        return false;
    }
}

// Client is one agent connection: a hello with the token, then snapshot
// requests, one reply line each.
class Client {
    constructor(bridge, conn) {
        this._bridge = bridge;
        this._conn = conn;
        this._cancellable = new Gio.Cancellable();
        this._buf = new Uint8Array(0);
        this._hello = false;
        this._titles = false;
        this._busy = false;
        this._idle = 0;
        this._closed = false;
    }

    start() {
        let peer;
        try {
            peer = this._conn.get_socket().get_credentials().get_unix_user();
        } catch {
            peer = -1;
        }
        if (peer !== this._bridge.uid) {
            this.close();
            return;
        }
        this._armIdle();
        this._read();
    }

    close() {
        if (this._closed)
            return;
        this._closed = true;
        if (this._idle)
            GLib.source_remove(this._idle);
        this._idle = 0;
        this._cancellable.cancel();
        this._conn.close_async(GLib.PRIORITY_DEFAULT, null, (c, res) => {
            try {
                c.close_finish(res);
            } catch {}
        });
        this._bridge.forget(this);
    }

    _armIdle() {
        if (this._idle)
            GLib.source_remove(this._idle);
        this._idle = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, IDLE_SECONDS, () => {
            this._idle = 0;
            this.close();
            return GLib.SOURCE_REMOVE;
        });
    }

    _read() {
        this._conn.get_input_stream().read_bytes_async(MAX_REQUEST_BYTES, GLib.PRIORITY_DEFAULT,
            this._cancellable, (stream, res) => {
                let bytes;
                try {
                    bytes = stream.read_bytes_finish(res);
                } catch {
                    this.close();
                    return;
                }
                const chunk = bytes.toArray();
                if (chunk.length === 0) {
                    this.close();
                    return;
                }
                const buf = new Uint8Array(this._buf.length + chunk.length);
                buf.set(this._buf);
                buf.set(chunk, this._buf.length);
                this._buf = buf;
                this._drain();
            });
    }

    // _drain handles one complete line at a time. The agent waits for each
    // reply before it asks again, so a second line arriving early is kept
    // until the first is answered.
    _drain() {
        if (this._closed || this._busy)
            return;
        const nl = this._buf.indexOf(10);
        if (nl < 0) {
            if (this._buf.length > MAX_REQUEST_BYTES)
                this.close();
            else
                this._read();
            return;
        }
        const line = this._buf.slice(0, nl);
        this._buf = this._buf.slice(nl + 1);
        this._armIdle();
        let req;
        try {
            req = JSON.parse(decoder.decode(line));
        } catch {
            this._fail('bad request');
            return;
        }
        if (req === null || typeof req !== 'object') {
            this._fail('bad request');
        } else if (req.op === 'hello') {
            this._onHello(req);
        } else if (req.op === 'snapshot' && this._hello) {
            let reply;
            try {
                reply = snapshot(this._titles);
            } catch (e) {
                console.warn(`burnout-bridge: snapshot failed: ${e.message}`);
                reply = {v: VERSION, op: 'error', error: 'snapshot failed'};
            }
            this._send(reply, false);
        } else {
            this._fail(this._hello ? 'unknown op' : 'hello first');
        }
    }

    _onHello(req) {
        if (this._hello || typeof req.token !== 'string' || !TOKEN_RE.test(req.token)) {
            this._fail('bad hello');
            return;
        }
        this._busy = true;
        // Read fresh each time, so a token the agent rotated is honoured.
        Gio.File.new_for_path(this._bridge.tokenPath).load_contents_async(this._cancellable, (file, res) => {
            this._busy = false;
            let want = '';
            try {
                const [, contents] = file.load_contents_finish(res);
                want = decoder.decode(contents).trim();
            } catch {}
            if (this._closed)
                return;
            if (!TOKEN_RE.test(want) || !timingSafeEqual(want, req.token)) {
                this._fail('bad token');
                return;
            }
            this._hello = true;
            this._titles = req.titles === true;
            this._send({v: VERSION, op: 'hello', extension: this._bridge.version, shell: this._bridge.shellVersion}, false);
        });
    }

    _fail(reason) {
        this._send({v: VERSION, op: 'error', error: reason}, true);
    }

    _send(obj, thenClose) {
        this._busy = true;
        const data = Gio.MemoryInputStream.new_from_bytes(new GLib.Bytes(encoder.encode(`${JSON.stringify(obj)}\n`)));
        this._conn.get_output_stream().splice_async(data, Gio.OutputStreamSpliceFlags.CLOSE_SOURCE,
            GLib.PRIORITY_DEFAULT, this._cancellable, (out, res) => {
                this._busy = false;
                let ok = true;
                try {
                    out.splice_finish(res);
                } catch {
                    ok = false;
                }
                if (!ok || thenClose)
                    this.close();
                else
                    this._drain();
            });
    }
}

export default class BurnoutBridge extends Extension {
    enable() {
        this.uid = new Gio.Credentials().get_unix_user();
        this.version = String(this.metadata.version ?? '');
        this.shellVersion = Config.PACKAGE_VERSION;
        const dir = GLib.build_filenamev([GLib.get_user_runtime_dir(), 'burnout']);
        this.tokenPath = GLib.build_filenamev([dir, TOKEN_NAME]);
        this._socketPath = GLib.build_filenamev([dir, SOCKET_NAME]);
        this._clients = new Set();

        GLib.mkdir_with_parents(dir, 0o700);
        if (!ownedPrivateDir(dir, this.uid)) {
            console.warn(`burnout-bridge: ${dir} is not an owner-only directory; not listening`);
            return;
        }
        this._removeStaleSocket();
        try {
            this._service = new Gio.SocketService();
            this._service.add_address(Gio.UnixSocketAddress.new(this._socketPath),
                Gio.SocketType.STREAM, Gio.SocketProtocol.DEFAULT, null);
            this._service.connect('incoming', (_svc, conn) => {
                const c = new Client(this, conn);
                this._clients.add(c);
                c.start();
                return true;
            });
            this._service.start();
        } catch (e) {
            console.warn(`burnout-bridge: cannot listen: ${e.message}`);
            this._service?.close();
            this._service = null;
        }
    }

    disable() {
        if (this._service) {
            this._service.stop();
            this._service.close();
            this._service = null;
            this._removeStaleSocket();
        }
        for (const c of [...(this._clients ?? [])])
            c.close();
        this._clients = null;
    }

    forget(client) {
        this._clients?.delete(client);
    }

    // _removeStaleSocket deletes a socket left at our path, but only if it
    // is a socket we own; anything else stays and makes listening fail.
    _removeStaleSocket() {
        const file = Gio.File.new_for_path(this._socketPath);
        try {
            const info = file.query_info('standard::type,unix::uid',
                Gio.FileQueryInfoFlags.NOFOLLOW_SYMLINKS, null);
            if (info.get_file_type() === Gio.FileType.SPECIAL &&
                info.get_attribute_uint32('unix::uid') === this.uid)
                file.delete(null);
        } catch {}
    }
}
