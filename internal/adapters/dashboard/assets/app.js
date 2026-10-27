// burnout dashboard. Everything is built with DOM methods and textContent:
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
  if (r.status === 401) throw new Error("This tab's session has ended. Run `burnout dashboard` again.");
  if (!r.ok) throw new Error(await r.text());
  return r.json();
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
      el("p", { text: "days with enough data so far. Scores start once burnout knows your usual weekdays and weekends." })));
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
    body.append(el("p", { text: "Nothing recorded yet today. Is the daemon running? Try `burnout doctor`." }));
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
  if (w.checkin_due) body.append(el("p", { class: "notice", text: "This week's check-in is due: run `burnout checkin` (about a minute)." }));
  if (w.suggestions.length) {
    body.append(el("h3", { text: "One or two things to try" }), el("ul", {}, ...w.suggestions.map((s) => el("li", { text: s }))));
  }
}

// ---- Privacy -----------------------------------------------------------

function renderPrivacy(p) {
  const body = document.getElementById("privacy-body");
  body.replaceChildren(
    el("h3", { text: "What is collected" }),
    el("ul", {}, ...p.collected.map((c) => el("li", { text: c }))),
    el("h3", { text: "Your settings" }),
    el("dl", { class: "settings" }, ...p.settings.flatMap((s) => [el("dt", { text: s.name }), el("dd", { text: s.value })])),
    el("p", { class: "fine", text: "Change these with `burnout config`, `burnout pause` and `burnout forget`; remove everything with `burnout uninstall --purge`. Details: docs/privacy-and-security.md" }));
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
  try { localStorage.setItem("burnout-blur", on ? "1" : "0"); } catch { /* storage may be off */ }
}

document.getElementById("blur").addEventListener("click", () => {
  setBlur(!document.documentElement.classList.contains("blurred"));
});
let saved = false;
try { saved = localStorage.getItem("burnout-blur") === "1"; } catch { /* storage may be off */ }
setBlur(saved);

window.addEventListener("hashchange", async () => {
  await show(location.hash.slice(1));
  document.getElementById("main").focus(); // screen readers start at the new section
});
show(location.hash.slice(1));
