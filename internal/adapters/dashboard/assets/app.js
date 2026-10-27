// burntout dashboard. Everything is built with DOM methods and textContent:
// app names and notes come from the user's data and are never parsed as HTML.

const SVG = "http://www.w3.org/2000/svg";
const views = ["overview", "today", "week", "privacy"];
const loaded = {};

function el(tag, attrs = {}, ...children) {
  const node = tag.startsWith("svg:") ? document.createElementNS(SVG, tag.slice(4)) : document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "text") node.textContent = v;
    else if (v !== undefined && v !== null && v !== false) node.setAttribute(k, v);
  }
  for (const c of children) {
    if (c !== null && c !== undefined) node.append(c);
  }
  return node;
}

function num(text, attrs = {}) {
  return el("span", { ...attrs, class: ((attrs.class || "") + " num").trim(), text: String(text) });
}

function hm(seconds) {
  const m = Math.round(seconds / 60);
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, "0")}m`;
}

function label(name) {
  return name.replaceAll("_", " ").replaceAll("-", " ");
}

function catClass(name) {
  const known = ["code", "terminal", "browser", "comms", "meeting", "docs", "media", "remote-desktop", "private", "uncategorized", "other"];
  return "cat-" + (known.includes(name) ? name : "unknown");
}

async function get(path) {
  const r = await fetch(path, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (r.status === 401) throw new Error("This tab's session has ended. Run `burntout dashboard` again.");
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}

// session holds the CSRF token every write carries, and whether this
// dashboard may change data at all. Only this page can read it.
let session = null;
async function getSession() {
  if (!session) session = await get("/api/session");
  return session;
}

// send makes a change. Afterwards every view reloads when next shown, so
// notices, the week and the privacy rows reflect it.
async function send(method, path, body) {
  const s = await getSession();
  const headers = { Accept: "application/json", "X-CSRF-Token": s.csrf };
  const init = { method, credentials: "same-origin", headers };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const r = await fetch(path, init);
  if (r.status === 401) throw new Error("This tab's session has ended. Run `burntout dashboard` again.");
  if (!r.ok) throw new Error(await r.text());
  for (const v of views) loaded[v] = false;
  return r.status === 204 ? null : r.json();
}

// action runs a change from a button: every button in its group waits,
// and the outcome is announced in status.
async function action(group, status, done, fn) {
  const buttons = group.querySelectorAll("button");
  for (const b of buttons) b.disabled = true;
  status.textContent = "Working…";
  try {
    await fn();
    status.textContent = done;
    return true;
  } catch (err) {
    status.textContent = String(err.message || err).trim();
    return false;
  } finally {
    for (const b of buttons) b.disabled = false;
  }
}

function button(text, onClick, attrs = {}) {
  const b = el("button", { type: "button", ...attrs, text });
  b.addEventListener("click", onClick);
  return b;
}

function showError(err) {
  const box = document.getElementById("error");
  box.textContent = String(err.message || err);
  box.hidden = false;
}

// ---- Overview ----------------------------------------------------------

function bandOf(v) {
  return v >= 70 ? "high" : v >= 40 ? "elevated" : "low";
}

function renderOverview(o) {
  const notices = document.getElementById("notices");
  notices.replaceChildren(...(o.notices || []).map((n) => el("p", { class: "notice", text: n })));
  document.getElementById("profile").textContent = `Profile: ${o.profile}.`;

  const latest = document.getElementById("latest");
  latest.replaceChildren();
  if (o.calibrating) {
    latest.append(el("div", { class: "card" },
      el("p", { class: "big" }, num(`${o.calibrating.usable_days} of ${o.calibrating.needed}`)),
      el("p", { text: "days with enough data so far. Scores start once BurntOut knows your usual weekdays and weekends." })));
  } else if (o.latest) {
    const d = o.latest;
    const card = el("div", { class: "card" });
    if (d.status === "ok") {
      card.append(el("p", {}, el("span", { class: "big" }, num(Math.round(d.strain))), " / 100",
        el("span", { class: `band band-${d.band} num`, text: d.band })));
      card.append(el("p", { class: "muted", text: `on ${d.date}` }));
    } else {
      card.append(el("p", { text: `No score for ${d.date}: ${d.reason || d.status}.` }));
    }
    if (d.drivers && d.drivers.length) {
      card.append(el("h3", { text: "What stood out" }), el("ul", {}, ...d.drivers.map((t) => el("li", { text: t }))));
    }
    if (d.guardrails && d.guardrails.length) {
      card.append(el("h3", { text: "Limits crossed" }), el("ul", {}, ...d.guardrails.map((t) => el("li", { text: t }))));
    }
    latest.append(card);
  }
  renderTrend(o.days || [], o.today);
}

function renderTrend(days, today) {
  const box = document.getElementById("trend");
  const caption = document.getElementById("trend-caption");
  const body = document.querySelector("#trend-table tbody");
  body.replaceChildren();
  box.replaceChildren();
  const scored = days.filter((d) => d.status === "ok");
  if (!days.length) {
    caption.textContent = "No completed days yet.";
    return;
  }
  const W = 720, H = 240, L = 34, R = 8, T = 8, B = 28;
  const end = new Date(today + "T00:00:00Z");
  const start = new Date(end.getTime() - 90 * 86400e3);
  const x = (date) => L + ((new Date(date + "T00:00:00Z") - start) / (end - start)) * (W - L - R);
  const y = (v) => T + (1 - v / 100) * (H - T - B);
  const svg = el("svg:svg", { viewBox: `0 0 ${W} ${H}`, role: "img", "aria-labelledby": "trend-caption" });
  for (const [from, to, cls] of [[0, 40, "zone-low"], [40, 70, "zone-elevated"], [70, 100, "zone-high"]]) {
    svg.append(el("svg:rect", { x: L, width: W - L - R, y: y(to), height: y(from) - y(to), class: cls }));
  }
  for (const v of [0, 40, 70, 100]) {
    svg.append(el("svg:line", { x1: L, x2: W - R, y1: y(v), y2: y(v), class: "gridline" }));
    svg.append(el("svg:text", { x: L - 6, y: y(v) + 4, "text-anchor": "end", class: "axis", text: String(v) }));
  }
  for (let back = 90; back >= 0; back -= 30) {
    const d = new Date(end.getTime() - back * 86400e3).toISOString().slice(0, 10);
    const anchor = back === 0 ? "end" : back === 90 ? "start" : "middle";
    svg.append(el("svg:text", { x: x(d), y: H - 8, "text-anchor": anchor, class: "axis", text: d.slice(5) }));
  }
  // A line joins consecutive scored days only: a missing day is a gap, never rest.
  let path = "", prev = null;
  for (const d of days) {
    if (d.status === "ok") {
      const dayGap = prev ? (new Date(d.date) - new Date(prev)) / 86400e3 : 99;
      path += `${dayGap === 1 ? "L" : "M"}${x(d.date).toFixed(1)},${y(d.strain).toFixed(1)}`;
      prev = d.date;
    } else {
      prev = null;
    }
  }
  if (path) svg.append(el("svg:path", { d: path, class: "trendline num" }));
  for (const d of days) {
    const ok = d.status === "ok";
    const dot = el("svg:circle", {
      cx: x(d.date), cy: ok ? y(d.strain) : y(0), r: ok ? 3 : 2.5, class: ok ? "dot num" : "dot-missing",
    });
    dot.append(el("svg:title", { text: ok ? `${d.date}: ${Math.round(d.strain)} (${d.band})` : `${d.date}: ${d.reason || d.status}` }));
    svg.append(dot);
  }
  box.append(svg);

  const highs = scored.filter((d) => d.strain >= 70).length;
  caption.textContent = scored.length
    ? `Strain over the last 90 days: ${scored.length} scored days, ${highs} in the high zone. Shaded bands: low below 40, elevated 40 to 69, high 70 and above. Days without a score are hollow, at the bottom.`
    : "No scored days yet in the last 90 days.";
  for (const d of [...days].reverse()) {
    body.append(el("tr", {},
      el("th", { scope: "row", text: d.date }),
      el("td", { class: "n" }, d.status === "ok" ? num(`${Math.round(d.strain)} (${d.band})`) : el("span", { text: d.status })),
      el("td", { text: [d.reason, d.marked, ...(d.guardrails || [])].filter(Boolean).join("; ") })));
  }
}

// ---- Today -------------------------------------------------------------

function renderToday(d) {
  const body = document.getElementById("today-body");
  body.replaceChildren();
  body.append(el("p", {}, el("span", { class: "big" }, num(hm(d.attended_s))), ` attended on ${d.date}`));
  if (!d.attended_s && !d.commands && !d.agent_sessions && !d.meetings.length) {
    body.append(el("p", { text: "Nothing recorded yet today. Is the daemon running? Try `burntout doctor`." }));
    return;
  }
  if (d.categories.length) {
    const total = d.categories.reduce((s, c) => s + c.value, 0) || 1;
    const bar = el("div", { class: "bar", role: "img", "aria-label": "Share of attended time by category; listed below" });
    for (const c of d.categories) {
      const seg = el("span", { class: catClass(c.name), title: `${label(c.name)}: ${hm(c.value)}` });
      seg.style.width = `${(100 * c.value) / total}%`;
      bar.append(seg);
    }
    body.append(el("h3", { text: "By category" }), bar, table(["Category", "Time"], d.categories.map((c) => [
      el("span", {}, el("span", { class: `swatch ${catClass(c.name)}`, "aria-hidden": "true" }), label(c.name)), num(hm(c.value)),
    ])));
  }
  if (d.timeline.length || d.meetings.length) body.append(el("h3", { text: "Through the day" }), timeline(d));
  if (d.top_apps.length) {
    body.append(el("h3", { text: "Top apps" }), table(["App", "Time"], d.top_apps.map((a) => [a.name, num(hm(a.value))])));
  }
  if (d.meetings.length) {
    body.append(el("h3", { text: "Calendar meetings" }), el("ul", { class: "plain" },
      ...d.meetings.map((m) => el("li", { text: `${m.start}–${m.end}${m.tentative ? " (tentative)" : ""}` }))));
  }
  if (d.commands) {
    body.append(el("h3", { text: "Terminal" }),
      el("p", {}, num(d.commands), " commands, ", num(d.failures), " failed"),
      table(["Command", "Runs"], d.top_commands.map((c) => [c.name || "(unrecognised)", num(c.value)])));
  }
  if (d.agent_sessions) {
    body.append(el("h3", { text: "AI agents" }),
      el("p", {}, num(d.agent_sessions), " sessions"),
      table(["Agent state", "Time"], d.agent_states.map((s) => [label(s.name), num(hm(s.value))])));
    if (d.human_states.length) {
      body.append(table(["You, while agents ran", "Time"], d.human_states.map((s) => [label(s.name), num(hm(s.value))])));
    }
  }
}

// timeline draws the day from 04:00 to 04:00, one block per 10 minutes,
// coloured by the category that held most attention, with calendar
// meetings outlined above.
function timeline(d) {
  const W = 720, H = 70, L = 4, R = 4;
  const toMin = (hhmm) => {
    const [h, m] = hhmm.split(":").map(Number);
    return (h * 60 + m - 240 + 1440) % 1440; // the day starts at 04:00
  };
  const x = (min) => L + (min / 1440) * (W - L - R);
  const svg = el("svg:svg", { viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": "Activity through the day, by category; the tables list the same time" });
  for (let h = 4; h <= 28; h += 4) {
    const min = (h - 4) * 60;
    svg.append(el("svg:line", { x1: x(min), x2: x(min), y1: 8, y2: 52, class: "gridline" }));
    const anchor = h === 4 ? "start" : h === 28 ? "end" : "middle";
    svg.append(el("svg:text", { x: x(min), y: 66, "text-anchor": anchor, class: "axis", text: `${String(h % 24).padStart(2, "0")}:00` }));
  }
  for (const m of d.meetings) {
    const a = toMin(m.start), b = toMin(m.end) > a ? toMin(m.end) : 1440;
    const r = el("svg:rect", { x: x(a), width: Math.max(1, x(b) - x(a)), y: 8, height: 10, class: "cat-calendar" });
    r.append(el("svg:title", { text: `Meeting ${m.start}–${m.end}` }));
    svg.append(r);
  }
  for (const s of d.timeline) {
    const a = toMin(s.start);
    const r = el("svg:rect", { x: x(a), width: Math.max(1, x(a + 10) - x(a) - 0.5), y: 22, height: 30, class: catClass(s.category) });
    r.append(el("svg:title", { text: `${s.start} ${label(s.category)}, ${Math.round(s.attended_s / 60)} min` }));
    svg.append(r);
  }
  const cats = [...new Set(d.timeline.map((s) => s.category))];
  const legend = el("ul", { class: "legend", "aria-label": "Colours" }, ...cats.map((c) =>
    el("li", {}, el("span", { class: `swatch ${catClass(c)}`, "aria-hidden": "true" }), label(c))));
  return el("div", { id: "timeline" }, svg, legend);
}

function table(headers, rows) {
  return el("table", {},
    el("thead", {}, el("tr", {}, ...headers.map((h, i) => el("th", { scope: "col", class: i ? "n" : null, text: h })))),
    el("tbody", {}, ...rows.map((r) => el("tr", {}, ...r.map((c, i) => {
      const cell = el("td", { class: i ? "n" : null });
      cell.append(typeof c === "string" ? document.createTextNode(c) : c);
      return cell;
    })))));
}

// ---- Week --------------------------------------------------------------

function renderWeek(w) {
  const body = document.getElementById("week-body");
  body.replaceChildren(el("p", { class: "muted", text: `${w.from} to ${w.to}` }));
  if (w.calibrating) {
    body.append(el("p", { text: "Still calibrating, so there's no week to compare yet." }));
  } else {
    const p = el("p", {}, "Strain averaged ", num(Math.round(w.average)), ` over ${w.scored} scored days`);
    if (w.prev_average != null) {
      const diff = Math.round(w.average - w.prev_average);
      p.append(" (the week before: ", num(Math.round(w.prev_average)), ", ", num(diff >= 0 ? `+${diff}` : String(diff)), ")");
    }
    body.append(p);
    if (w.peak) body.append(el("p", {}, `Highest: ${w.peak.date}, `, num(Math.round(w.peak.strain)), ` (${w.peak.band})`));
  }
  for (const m of w.marked || []) body.append(el("p", { class: "notice", text: `Marked: ${m}` }));
  if (w.drivers.length) {
    body.append(el("h3", { text: "What kept coming up" }), table(["Driver", "Days"], w.drivers.map((d) => [d.label, num(d.days)])));
  }
  if (w.limits.length) {
    body.append(el("h3", { text: "Limits crossed" }), table(["Limit", "Days"], w.limits.map((d) => [d.label, num(d.days)])));
  }
  body.append(el("h3", { text: "Check-in" }));
  if (!w.checkins.length) body.append(el("p", { text: "None yet." }));
  for (const c of w.checkins) {
    const p = el("p", {}, `${c.name}: `, num(Math.round(c.score)), ` (${c.band})`);
    if (c.previous != null) p.append(", before ", num(Math.round(c.previous)));
    body.append(p);
  }
  if (w.checkin_due) body.append(el("p", { class: "notice", text: "This week's check-in is due: about a minute, below or with `burntout checkin`." }));
  const bounds = el("div");
  if (w.suggestions.length) {
    const list = el("ul");
    for (const s of w.suggestions) {
      const item = el("li", {}, el("span", { text: s }));
      for (const o of (w.offers || []).filter((o) => o.for === s)) offerButton(item, o, bounds, w).catch(showError);
      list.append(item);
    }
    body.append(el("h3", { text: "One or two things to try" }), list);
  }
  const notes = el("div"), form = el("div");
  body.append(bounds, notes, form);
  renderBoundaries(bounds, w).catch(showError);
  renderAnnotations(notes).catch(showError);
  renderCheckinForm(form, w.checkin_due).catch(showError);
}

// ---- Boundaries --------------------------------------------------------

// [kind, label, input type, starting value, step, min, max], as the
// server parses them.
const boundaryKinds = [
  ["shutdown", "Stop work by", "time", "21:30", "60", "", ""],
  ["no_weekend", "No work at the weekend", "", "", "", "", ""],
  ["meetings_max", "At most this many hours of meetings", "number", "4", "0.5", "0.5", "12"],
  ["focus_block", "A focus block of at least (minutes)", "number", "90", "1", "15", "240"],
];

// offerButton offers the boundary that goes with a suggestion. It is
// marked as set from the review (system design §8.6).
async function offerButton(item, o, bounds, w) {
  const s = await getSession();
  if (!s.writable) return;
  const status = el("span", { role: "status", class: "status" });
  item.append(" ", button(`Set a boundary: ${o.text}`, () => action(item, status, "Set, from tomorrow.", async () => {
    await send("POST", "/api/boundaries", { kind: o.kind, value: o.value, from_review: true });
    await renderBoundaries(bounds, w, "Set, from tomorrow.");
  }), { class: "small" }), status);
}

async function renderBoundaries(box, w, said = "") {
  const [s, bs] = await Promise.all([getSession(), get("/api/boundaries")]);
  const status = el("p", { role: "status", class: "status", text: said });
  const kept = new Map((w.adherence || []).map((a) => [a.text, a.summary]));
  const list = el("ul", { class: "plain annotations" });
  for (const b of bs) {
    const summary = kept.get(b.text) || `applies from ${b.since}`;
    const item = el("li", {}, el("span", { text: `${b.text[0].toUpperCase()}${b.text.slice(1)}: ${summary}` }));
    if (s.writable) {
      item.append(button("Remove", () => action(item, status, "Removed.", async () => {
        await send("DELETE", `/api/boundaries/${encodeURIComponent(b.id)}`);
        await renderBoundaries(box, w, "Removed.");
      }), { "aria-label": `Remove ${b.text}`, class: "small" }));
    }
    list.append(item);
  }
  box.replaceChildren(el("h3", { text: "Your boundaries" }),
    el("p", { class: "fine", text: "Kept days count only days with enough data; vacation and sick days are left out. A new or changed boundary applies from tomorrow." }),
    bs.length ? list : el("p", { text: "None set." }));
  if (!s.writable) {
    box.append(el("p", { class: "fine", text: "Set boundaries with `burntout boundary add`." }));
    return;
  }
  const kind = el("select", { id: "boundary-kind", name: "kind" }, ...boundaryKinds.map(([v, t]) => el("option", { value: v, text: t })));
  const value = el("input", { id: "boundary-value", name: "value", type: "time", value: "21:30", autocomplete: "off" });
  const valueLabel = el("label", { for: "boundary-value", text: "Value" });
  const sync = () => {
    const [, , type, start, step, min, max] = boundaryKinds.find(([v]) => v === kind.value);
    value.hidden = valueLabel.hidden = type === "";
    value.type = type || "text";
    // Reset every time: a fractional step left on a time input makes it
    // send seconds, which the server refuses.
    value.step = step;
    value.min = min;
    value.max = max;
    value.value = start;
  };
  kind.addEventListener("change", sync);
  sync();
  const add = el("form", { class: "inline-form", "aria-label": "Set a boundary" },
    el("label", { for: "boundary-kind", text: "Boundary" }), kind, valueLabel, value,
    el("button", { type: "submit", text: "Set" }));
  add.addEventListener("submit", (e) => {
    e.preventDefault();
    action(add, status, "Set, from tomorrow.", async () => {
      await send("POST", "/api/boundaries", { kind: kind.value, value: value.hidden ? "" : value.value });
      await renderBoundaries(box, w, "Set, from tomorrow.");
    });
  });
  box.append(add, status);
}

// ---- Annotations -------------------------------------------------------

const kinds = [["vacation", "Vacation"], ["sick", "Sick"], ["on_call", "On call"], ["release", "Release"], ["exam", "Exam"], ["other", "Other"]];

async function renderAnnotations(box, said = "") {
  const [s, notes] = await Promise.all([getSession(), get("/api/annotations")]);
  const status = el("p", { role: "status", class: "status", text: said });
  const list = el("ul", { class: "plain annotations" });
  for (const n of notes) {
    const item = el("li", {}, el("span", { text: n.text }));
    if (s.writable) {
      item.append(button("Remove", () => action(item, status, "Removed.", async () => {
        await send("DELETE", `/api/annotations/${encodeURIComponent(n.id)}`);
        await renderAnnotations(box, "Removed.");
      }), { "aria-label": `Remove ${n.text}`, class: "small" }));
    }
    list.append(item);
  }
  box.replaceChildren(el("h3", { text: "Marked days" }),
    el("p", { class: "fine", text: "Vacation and sick days are left out of your baseline and get no score; the others are scored and tagged." }),
    notes.length ? list : el("p", { text: "Nothing marked." }));
  if (!s.writable) {
    box.append(el("p", { class: "fine", text: "Mark days with `burntout annotate`." }));
    return;
  }
  const now = new Date();
  const today = new Date(now - now.getTimezoneOffset() * 60e3).toISOString().slice(0, 10); // local date
  const kind = el("select", { id: "note-kind", name: "kind" }, ...kinds.map(([v, t]) => el("option", { value: v, text: t })));
  const from = el("input", { id: "note-from", name: "from", type: "date", required: true, value: today });
  const to = el("input", { id: "note-to", name: "to", type: "date" });
  const note = el("input", { id: "note-text", name: "note", type: "text", maxlength: 200, autocomplete: "off" });
  const add = el("form", { class: "inline-form", "aria-label": "Mark days" },
    el("label", { for: "note-kind", text: "What" }), kind,
    el("label", { for: "note-from", text: "From" }), from,
    el("label", { for: "note-to", text: "To (optional)" }), to,
    el("label", { for: "note-text", text: "Note (optional)" }), note,
    el("button", { type: "submit", text: "Mark" }));
  add.addEventListener("submit", (e) => {
    e.preventDefault();
    action(add, status, "Marked.", async () => {
      await send("POST", "/api/annotations", { kind: kind.value, from: from.value, to: to.value || from.value, note: note.value });
      await renderAnnotations(box, "Marked.");
    });
  });
  box.append(add, status);
}

// ---- Check-in ----------------------------------------------------------

async function renderCheckinForm(box, due) {
  const s = await getSession();
  box.replaceChildren(el("h3", { text: "Weekly check-in" }));
  if (!s.writable) {
    box.append(el("p", { class: "fine", text: "Answer it with `burntout checkin`." }));
    return;
  }
  const open = button(due ? "Take this week's check-in" : "Take the check-in again", async () => {
    open.disabled = true;
    try {
      box.append(checkinForm(box, await get("/api/checkin")));
      open.remove();
      box.querySelector("fieldset input").focus();
    } catch (err) {
      open.disabled = false;
      showError(err);
    }
  });
  box.append(el("p", { class: "fine", text: "13 questions from the Copenhagen Burnout Inventory, about a minute. Nothing is saved until you send it." }), open);
}

// supportMessage reflows the CLI's hard-wrapped message: its opening
// paragraph, then one list item per "•".
function supportMessage(text) {
  let intro = "";
  const points = [];
  for (const line of text.split("\n").map((l) => l.trim()).filter(Boolean)) {
    if (line.startsWith("•")) points.push(line.slice(1).trim());
    else if (points.length) points[points.length - 1] += " " + line;
    else intro += (intro ? " " : "") + line;
  }
  return [el("p", { text: intro }), el("ul", {}, ...points.map((p) => el("li", { text: p })))];
}

function checkinForm(box, instruments) {
  const form = el("form", { class: "checkin", "aria-label": "Weekly check-in" });
  const total = instruments.reduce((n, i) => n + i.items.length, 0);
  let n = 0;
  for (const inst of instruments) {
    form.append(el("p", { class: "intro", text: inst.intro }));
    for (const item of inst.items) {
      n++;
      const name = `${inst.id}.${item.id}`;
      const set = el("fieldset", {}, el("legend", { text: `${n}/${total}  ${item.text}` }));
      for (const c of item.choices) {
        const id = `${name}.${c.value}`;
        set.append(el("div", { class: "choice" },
          el("input", { type: "radio", id, name, value: String(c.value), required: true }),
          el("label", { for: id, text: c.label })));
      }
      form.append(set);
    }
  }
  const status = el("p", { role: "status", class: "status" });
  form.append(el("button", { type: "submit", text: "Save check-in" }), status);
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const answers = {};
    for (const inst of instruments) {
      answers[inst.id] = {};
      for (const item of inst.items) {
        const picked = form.querySelector(`input[name="${CSS.escape(inst.id + "." + item.id)}"]:checked`);
        if (picked) answers[inst.id][item.id] = Number(picked.value);
      }
    }
    action(form, status, "Saved.", async () => {
      const res = await send("POST", "/api/checkins", { answers });
      const out = el("div", { class: "card", role: "status", tabindex: "-1" }, el("h4", { text: "Saved" }),
        ...res.scores.map((c) => el("p", {}, `${c.name}: `, num(Math.round(c.score)), ` / 100 (${c.band})`)));
      if (res.support) {
        // The safety flow (system design §8.4): the same words the CLI shows.
        out.classList.add("support");
        out.append(...supportMessage(res.message),
          el("ul", {}, ...res.resources.map((r) => el("li", { text: `${r.name}: ${r.contact}` }))));
      }
      box.replaceChildren(el("h3", { text: "Weekly check-in" }), out);
      out.focus();
    });
  });
  return form;
}

// ---- Privacy -----------------------------------------------------------

function renderPrivacy(p) {
  const body = document.getElementById("privacy-body");
  const controls = el("div");
  renderControls(controls).catch(showError);
  body.replaceChildren(
    controls,
    el("h3", { text: "What is collected" }),
    el("ul", {}, ...p.collected.map((c) => el("li", { text: c }))),
    el("h3", { text: "Your settings" }),
    el("dl", { class: "settings" }, ...p.settings.flatMap((s) => [el("dt", { text: s.name }), el("dd", { text: s.value })])),
    el("p", { class: "fine", text: "Change settings with `burntout config`; remove everything with `burntout uninstall --purge`. Details: docs/privacy-and-security.md" }));
}

// renderControls is pause, resume and delete. Deleting asks first, in the
// page: it can't be undone.
async function renderControls(box) {
  const s = await getSession();
  box.replaceChildren(el("h3", { text: "Your controls" }));
  if (!s.writable) {
    box.append(el("p", { text: "Pause with `burntout pause 1h` or `burntout pause tomorrow`, resume with `burntout resume`, and delete with `burntout forget --last 1h` or `--today`." }));
    return;
  }
  const status = el("p", { role: "status", class: "status" });
  const reload = async () => {
    const p = await get("/api/privacy");
    const dl = document.querySelector("#privacy-body dl.settings");
    if (dl) dl.replaceChildren(...p.settings.flatMap((r) => [el("dt", { text: r.name }), el("dd", { text: r.value })]));
    loaded.privacy = true;
  };
  const pause = el("div", { class: "controls", role: "group", "aria-label": "Recording" },
    button("Pause for an hour", () => action(pause, status, "Paused for an hour.", async () => { await send("POST", "/api/pause", { for: "1h" }); await reload(); })),
    button("Pause until tomorrow", () => action(pause, status, "Paused until tomorrow.", async () => { await send("POST", "/api/pause", { for: "tomorrow" }); await reload(); })),
    button("Pause until I resume", () => action(pause, status, "Paused until you resume.", async () => { await send("POST", "/api/pause", { for: "until-resumed" }); await reload(); })),
    button("Resume", () => action(pause, status, "Recording again.", async () => { await send("POST", "/api/resume", {}); await reload(); })));

  const confirmBox = el("div", { class: "confirm", hidden: true });
  const ask = (range, what) => {
    const yes = button(`Delete ${what}`, () => action(del, status, `Deleted ${what}.`, async () => {
      confirmBox.hidden = true;
      await send("DELETE", `/api/data?range=${range}`);
      await reload();
    }), { class: "danger" });
    const no = button("Cancel", () => { confirmBox.hidden = true; status.textContent = "Nothing was deleted."; });
    confirmBox.replaceChildren(
      el("p", { text: `This deletes everything recorded ${what === "today" ? "today" : "in the last hour"}, including recovery copies made since then. It can't be undone.` }),
      yes, no);
    confirmBox.hidden = false;
    no.focus();
  };
  const del = el("div", { class: "controls", role: "group", "aria-label": "Delete recent data" },
    button("Delete the last hour…", () => ask("hour", "the last hour")),
    button("Delete today…", () => ask("today", "today")),
    confirmBox);
  box.append(el("h4", { text: "Recording" }), pause, el("h4", { text: "Delete recent data" }), del, status);
}

// ---- Navigation --------------------------------------------------------

const renderers = {
  overview: ["/api/overview", renderOverview],
  today: ["/api/day", renderToday],
  week: ["/api/week", renderWeek],
  privacy: ["/api/privacy", renderPrivacy],
};

async function show(view) {
  if (!views.includes(view)) view = "overview";
  for (const v of views) {
    document.getElementById(v).hidden = v !== view;
    const link = document.querySelector(`nav a[data-view="${v}"]`);
    if (v === view) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  document.getElementById("error").hidden = true;
  if (!loaded[view]) {
    const [path, render] = renderers[view];
    try {
      render(await get(path));
      loaded[view] = true;
    } catch (err) {
      showError(err);
    }
  }
}

function setBlur(on) {
  document.documentElement.classList.toggle("blurred", on);
  document.getElementById("blur").setAttribute("aria-pressed", String(on));
  try { localStorage.setItem("burntout-blur", on ? "1" : "0"); } catch { /* storage may be off */ }
}

document.getElementById("blur").addEventListener("click", () => {
  setBlur(!document.documentElement.classList.contains("blurred"));
});
let saved = false;
try { saved = localStorage.getItem("burntout-blur") === "1"; } catch { /* storage may be off */ }
setBlur(saved);

window.addEventListener("hashchange", async () => {
  await show(location.hash.slice(1));
  document.getElementById("main").focus(); // screen readers start at the new section
});
show(location.hash.slice(1));
