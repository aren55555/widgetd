// The weather card. The front: the place, the temperature now, the sky as
// an icon and the air quality on one line, then four day tiles. Tapping a
// day opens it: a 24-hour strip (the sky lit by where the sun really is,
// the sun's height through the day, rain by the hour), a wind compass, and
// the sky and humidity. Left alone for 30 s, the card goes
// back to the four days. All the arithmetic is in model.ts.

import { defineWidget } from "@widgetd/sdk";
import {
  ChevronLeft,
  Cloud,
  CloudDrizzle,
  CloudFog,
  CloudLightning,
  CloudMoon,
  CloudRain,
  CloudSnow,
  CloudSun,
  Droplet,
  icon,
  Moon,
  Sun,
  Sunrise,
  Sunset,
  type IconNode,
} from "@widgetd/sdk/icons";

import { StateSchema, type Day, type State } from "./gen/weather/v1/weather_pb.ts";
import {
  along,
  aqiBand,
  clock,
  compass,
  compassName,
  conditionText,
  dayName,
  dayTitle,
  degrees,
  iconFor,
  placeOf,
  speedIn,
  strip,
  sway,
  TINTS,
  unitsFor,
  upcoming,
  type IconKey,
  type Units3,
} from "./model.ts";

/** How long an open day stays open untouched. */
const IDLE_MS = 30_000;
const SVG_NS = "http://www.w3.org/2000/svg";

const NODES: Record<IconKey, IconNode> = {
  sun: Sun,
  moon: Moon,
  cloudSun: CloudSun,
  cloudMoon: CloudMoon,
  cloud: Cloud,
  fog: CloudFog,
  drizzle: CloudDrizzle,
  rain: CloudRain,
  snow: CloudSnow,
  storm: CloudLightning,
};

/** A two-tone weather icon: the sun and whatever falls are coloured, the cloud is ink. */
function weatherIcon(key: IconKey, size: number, label?: string): SVGSVGElement {
  const svg = icon(NODES[key], { size, label });
  const tints = TINTS[key];
  Array.from(svg.children).forEach((el, i) => {
    const t = tints[i];
    if (t) el.setAttribute("class", `tint-${t}`);
  });
  return svg;
}

function svgNode(
  name: string,
  attrs: Record<string, string | number> = {},
  ...kids: Array<Element | string>
): SVGElement {
  const n = document.createElementNS(SVG_NS, name);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v));
  n.append(...kids);
  return n;
}

/**
 * The day's wind as a compass: a needle pointing the way the wind blows,
 * from a tail on the side it comes from, swaying as much as the day is
 * gusty, with the speed in the middle. Beside it, the direction it comes
 * from and the gusts.
 */
function windDial(d: Day, u: Units3): HTMLElement {
  const n = speedIn(d.windMps, u);
  const gust = speedIn(d.gustMps, u);
  const calm = n === 0;
  const cell = el("div", "wind");
  cell.setAttribute("role", "img");
  cell.setAttribute(
    "aria-label",
    calm
      ? "Wind: calm"
      : `Wind ${n} ${u.speed} from the ${compassName(d.windFromDeg)}` +
          (gust > n ? `, gusts up to ${gust} ${u.speed}` : ""),
  );

  // Drawn on a 100-unit square centred on 0, so the needle turns about the middle.
  const dial = svgNode("svg", { viewBox: "-50 -50 100 100", class: "dial", "aria-hidden": "true" });
  // The ring and ticks are cut away behind each letter.
  const cut = svgNode(
    "mask",
    { id: "wx-dial-cut" },
    svgNode("rect", { x: -50, y: -50, width: 100, height: 100, fill: "#fff" }),
  );
  const ring = svgNode("g", { mask: "url(#wx-dial-cut)" }, svgNode("circle", { r: 44, class: "dial-ring" }));
  dial.append(svgNode("defs", {}, cut), ring);
  for (let a = 0; a < 360; a += 30) {
    if (a % 90 === 0) continue; // the letters stand in for these
    ring.append(svgNode("line", { y1: -44, y2: -39, class: "dial-tick", transform: `rotate(${a})` }));
  }
  for (const [a, letter] of [
    [0, "N"],
    [90, "E"],
    [180, "S"],
    [270, "W"],
  ] as const) {
    const rad = (a * Math.PI) / 180;
    const x = 44 * Math.sin(rad);
    const y = -44 * Math.cos(rad);
    cut.append(svgNode("circle", { cx: x, cy: y, r: 7, fill: "#000" }));
    dial.append(svgNode("text", { x, y, class: letter === "N" ? "dial-n" : "dial-pt" }, letter));
  }
  if (!calm) {
    const needle = svgNode(
      "g",
      { class: "needle" },
      svgNode("path", { d: "M0 -36 L-7 -22 L0 -25 L7 -22 Z", class: "needle-head" }),
      svgNode("line", { y1: -24, y2: -19, class: "needle-shaft" }),
      svgNode("line", { y1: 22, y2: 29, class: "needle-shaft" }),
      svgNode("circle", { cy: 32.5, r: 3.5, class: "needle-tail" }),
    );
    needle.style.setProperty("--sway", `${sway(d.windMps, d.gustMps)}deg`);
    dial.append(svgNode("g", { transform: `rotate(${(d.windFromDeg + 180) % 360})` }, needle));
  }
  dial.append(
    calm
      ? svgNode("text", { y: 0, class: "dial-calm" }, "Calm")
      : svgNode(
          "text",
          { y: -2, class: "dial-speed" },
          String(n),
          svgNode("tspan", { x: 0, dy: 15, class: "dial-unit" }, u.speed),
        ),
  );

  const text = el("div", "wind-text");
  text.setAttribute("aria-hidden", "true");
  if (!calm) {
    text.append(el("b", "", compass(d.windFromDeg)));
    if (gust > n) text.append(el("small", "", `gusts ${gust}`));
  }
  cell.append(dial, text);
  return cell;
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text = ""): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text) e.textContent = text;
  return e;
}

defineWidget({
  state: StateSchema,
  mount({ root, prefs }) {
    const card = el("div", "wx");
    const front = el("div", "front");
    const detail = el("div", "detail");
    detail.hidden = true;
    const empty = el("div", "empty", "Pick a place in this card's settings.");
    empty.hidden = true;
    card.append(front, detail, empty);
    root.replaceChildren(card);

    let st: State | undefined;
    let open: number | null = null; // index into upcoming(), while a day is open
    let idle = 0;
    let armedAt = 0;

    const close = () => {
      clearTimeout(idle);
      open = null;
      render();
    };
    const arm = () => {
      clearTimeout(idle);
      idle = window.setTimeout(close, IDLE_MS);
      armedAt = Date.now();
      // Restart the drain line from full.
      const drain = detail.querySelector<HTMLElement>(".drain i");
      if (drain) {
        drain.style.animation = "none";
        drain.getBoundingClientRect(); // a reflow, so the animation starts over
        drain.style.animation = "";
        drain.style.animationDelay = "0ms";
      }
    };
    detail.addEventListener("pointerdown", arm);

    const renderFront = (s: State, days: Day[], u: Units3, nowSec: number) => {
      const cur = s.current;
      const head = el("div", "head");
      const left = el("div", "head-left");
      left.append(el("span", "place", s.name));
      if (cur) {
        left.append(el("span", "now", degrees(cur.temperatureC, u) + u.temp));
        left.append(weatherIcon(iconFor(cur.condition, cur.isDay), 16));
      }
      head.append(left);
      let said = `${s.name}.`;
      if (cur) said += ` Now ${degrees(cur.temperatureC, u)}${u.temp}, ${conditionText(cur.condition, cur.isDay)}.`;
      if (cur?.usAqi != null) {
        const band = aqiBand(cur.usAqi);
        const aqi = el("span", `aqi aqi-${band.key}`);
        aqi.append(el("i"), el("span", "aqi-k", "AQI"), el("b", "", String(cur.usAqi)));
        head.append(aqi);
        said += ` Air quality ${cur.usAqi}, ${band.label}.`;
      }
      // One sentence for a screen reader, rather than five fragments.
      head.setAttribute("role", "group");
      head.setAttribute("aria-label", said);
      for (const c of Array.from(head.children)) c.setAttribute("aria-hidden", "true");

      const tiles = el("div", "tiles");
      days.slice(0, 4).forEach((d, i) => {
        const b = el("button", "tile");
        const name = dayName(d, nowSec, s.timeZone, u);
        if (name === "Today") b.classList.add("today");
        b.type = "button";
        b.setAttribute(
          "aria-label",
          `${dayTitle(d, nowSec, s.timeZone, u)}: ${conditionText(d.condition)}, high ${degrees(d.highC, u)}, low ${degrees(d.lowC, u)}` +
            (d.precipChance ? `, ${d.precipChance}% chance of rain` : "") +
            ". Show details",
        );
        b.append(
          el("span", "day", name),
          weatherIcon(iconFor(d.condition), 20),
          el("span", "pop", d.precipChance ? `${d.precipChance}%` : " "),
          el("span", "hi", degrees(d.highC, u)),
          el("span", "lo", degrees(d.lowC, u)),
        );
        b.addEventListener("click", () => {
          open = i;
          render();
          arm();
        });
        tiles.append(b);
      });
      front.replaceChildren(head, tiles);
    };

    const renderDetail = (s: State, d: Day, u: Units3, nowSec: number) => {
      const drain = el("div", "drain");
      drain.setAttribute("aria-hidden", "true");
      const fill = el("i");
      // A redraw mid-countdown picks the line up where the timer is.
      fill.style.animationDelay = `${armedAt ? -(Date.now() - armedAt) : 0}ms`;
      drain.append(fill);

      const top = el("div", "top");
      const back = el("button", "back");
      back.type = "button";
      back.setAttribute("aria-label", "Back to the forecast");
      back.append(icon(ChevronLeft, { size: 16 }), el("span", "", dayTitle(d, nowSec, s.timeZone, u)));
      back.addEventListener("click", close);
      // The sky and the high and low; the sky's name is in the stats below.
      const sum = el("div", "sum");
      sum.setAttribute("aria-label", `High ${degrees(d.highC, u)}, low ${degrees(d.lowC, u)}`);
      const temps = el("span", "temps");
      temps.setAttribute("aria-hidden", "true");
      temps.append(el("b", "", degrees(d.highC, u)), el("span", "slash", "/"), el("span", "", degrees(d.lowC, u)));
      sum.append(weatherIcon(iconFor(d.condition), 18), temps);
      top.append(back, sum);

      // The strip: measured once laid out, since the sun's track is drawn in pixels.
      const box = el("div", "strip");
      box.setAttribute("role", "img");
      const sky = Number(d.sunrise) && Number(d.sunset);
      const rainy = d.hourlyPrecipMm.filter((mm) => mm >= 0.05).length;
      box.setAttribute(
        "aria-label",
        (sky
          ? `Sunrise ${clock(Number(d.sunrise), s.timeZone, u)}, sunset ${clock(Number(d.sunset), s.timeZone, u)}. `
          : "") + (rainy ? `Rain in ${rainy} hour${rainy === 1 ? "" : "s"}.` : "No rain."),
      );
      const labels = el("div", "sunlabels");
      labels.setAttribute("aria-hidden", "true");
      for (const [t, node] of [
        [Number(d.sunrise), Sunrise],
        [Number(d.sunset), Sunset],
      ] as const) {
        if (!t) continue;
        const l = el("span", "sunlabel");
        l.style.left = `${along(d, t)}%`;
        l.append(icon(node, { size: 11 }), el("span", "", clock(t, s.timeZone, u)));
        labels.append(l);
      }

      // Under the strip: the wind as a compass on the left, and the sky and
      // humidity on the right, each an icon and a value with its label above
      // when there's room. Rain needs no line of its own: the strip draws it.
      const stats = el("dl", "stats");
      const stat = (label: string, glyph: Element, value: string, note = "") => {
        const c = el("div", "stat");
        c.title = label;
        const k = el("dt", "", label);
        const v = el("dd");
        glyph.setAttribute("aria-hidden", "true");
        v.append(glyph);
        v.append(el("span", "", value));
        if (note) v.append(el("small", "", note));
        c.append(k, v);
        return c;
      };
      stats.append(
        stat("Sky", weatherIcon(iconFor(d.condition), 13), conditionText(d.condition)),
        stat("Humidity", icon(Droplet, { size: 12 }), `${d.humidity}%`),
      );
      const lower = el("div", "lower");
      lower.append(windDial(d, u), stats);

      detail.replaceChildren(drain, top, box, labels, lower);
      requestAnimationFrame(() => drawStrip(box, s, d, u, nowSec));
    };

    const drawStrip = (box: HTMLElement, s: State, d: Day, u: Units3, nowSec: number) => {
      const W = box.clientWidth;
      const H = box.clientHeight;
      if (!W || !H) return;
      const m = strip(d, nowSec, W, H, placeOf(s));
      const svg = (cls: string) => {
        const g = document.createElementNS(SVG_NS, "svg");
        g.setAttribute("width", String(W));
        g.setAttribute("height", String(H));
        g.setAttribute("aria-hidden", "true");
        g.setAttribute("class", cls);
        return g;
      };
      const node = (name: string, attrs: Record<string, string | number> = {}, ...kids: Element[]) => {
        const n = document.createElementNS(SVG_NS, name);
        for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v));
        n.append(...kids);
        return n;
      };
      const parts: Element[] = [];

      // Under the rain: the sky, lit as the sun's elevation says, glowing
      // near the horizon at dawn and dusk, and the day's sunlight between the
      // sun's track and the horizon.
      const sun = m.sun;
      if (sun) {
        const hz = sun.horizon;
        const byTime = (id: string, cls: string, key: "light" | "glow") =>
          node(
            "linearGradient",
            { id, x1: 0, y1: 0, x2: 1, y2: 0 },
            ...m.sky.map((k) => node("stop", { offset: k.offset, class: cls, "stop-opacity": k[key].toFixed(3) })),
          );
        const sky = svg("sky");
        sky.append(
          node(
            "defs",
            {},
            byTime("wx-light", "sky-stop", "light"),
            byTime("wx-glow", "glow-stop", "glow"),
            // The glow hugs the horizon.
            node(
              "linearGradient",
              { id: "wx-glow-fade", x1: 0, y1: 0, x2: 0, y2: H, gradientUnits: "userSpaceOnUse" },
              node("stop", { offset: 0, "stop-color": "#fff", "stop-opacity": 0 }),
              node("stop", { offset: hz / H, "stop-color": "#fff", "stop-opacity": 1 }),
              node("stop", { offset: 1, "stop-color": "#fff", "stop-opacity": 0.2 }),
            ),
            node("mask", { id: "wx-glow-mask" }, node("rect", { width: W, height: H, fill: "url(#wx-glow-fade)" })),
            node(
              "linearGradient",
              { id: "wx-sunlight", x1: 0, y1: 4, x2: 0, y2: hz, gradientUnits: "userSpaceOnUse" },
              node("stop", { offset: 0, class: "sunlight-top" }),
              node("stop", { offset: 1, class: "sunlight-bottom" }),
            ),
            node("clipPath", { id: "wx-above" }, node("rect", { width: W, height: hz })),
            node("clipPath", { id: "wx-below" }, node("rect", { y: hz, width: W, height: H - hz })),
          ),
          node("rect", { width: W, height: H, fill: "url(#wx-light)" }),
          node("rect", { width: W, height: H, fill: "url(#wx-glow)", mask: "url(#wx-glow-mask)", class: "glow" }),
          node("path", { d: sun.area, fill: "url(#wx-sunlight)", "clip-path": "url(#wx-above)" }),
        );
        parts.push(sky);
      }

      for (const b of m.bars) {
        const bar = el("div", "bar");
        Object.assign(bar.style, {
          left: `${b.left}%`,
          width: `${b.width}%`,
          height: `${b.height}%`,
          opacity: String(b.opacity),
        });
        parts.push(bar);
      }
      if (m.now) {
        const line = el("div", "nowline");
        line.style.left = `${m.now.left}%`;
        parts.push(line);
      }

      // Over the rain: the horizon, the sun's track (dim below the horizon),
      // its highest point and where it is now.
      if (sun) {
        const track = svg("track");
        track.append(
          node("line", { x1: 0, y1: sun.horizon, x2: W, y2: sun.horizon, class: "horizon" }),
          node("path", { d: sun.path, class: "sunpath-night", "clip-path": "url(#wx-below)" }),
          node("path", { d: sun.path, class: "sunpath", "clip-path": "url(#wx-above)" }),
        );
        if (m.now?.y != null) {
          const up = (m.now.deg ?? 0) > 0;
          // Deep in the night the sun is off the bottom of the strip: pin it there.
          const cy = Math.min(m.now.y, H - 4);
          track.append(node("circle", { cx: m.now.x, cy, r: up ? 4 : 3, class: up ? "sundot" : "sundot-night" }));
        }
        parts.push(track);

        if (sun.peak) {
          // The noon altitude, under the top of the track if there's room in the sunlight, else above it.
          const peak = el("span", "peak", `${Math.round(sun.peak.deg)}°`);
          peak.setAttribute("aria-hidden", "true");
          peak.style.left = `${sun.peak.x}px`;
          peak.style.top = `${sun.peak.y}px`;
          if (sun.horizon - sun.peak.y < 24) peak.classList.add("above");
          parts.push(peak);
          box.setAttribute(
            "aria-label",
            `${box.getAttribute("aria-label") ?? ""} The sun is highest at ${Math.round(sun.peak.deg)}° at ${clock(sun.peak.t, s.timeZone, u)}.`,
          );
        }
      }
      box.replaceChildren(...parts);
    };

    const render = () => {
      if (!st) return;
      const nowSec = Math.floor(Date.now() / 1000);
      const u = unitsFor(st.temperature, prefs);
      const days = upcoming(st, nowSec);
      const blank = !st.name || !days.length;
      empty.hidden = !blank;
      if (blank) {
        empty.textContent = st.name ? "No forecast yet." : "Pick a place in this card's settings.";
        front.hidden = detail.hidden = true;
        return;
      }
      if (open != null && open >= days.length) open = null;
      front.hidden = open != null;
      detail.hidden = open == null;
      if (open == null) renderFront(st, days, u, nowSec);
      else renderDetail(st, days[open], u, nowSec);
    };

    // Once a minute: the sun moves, and at midnight today becomes tomorrow.
    const tick = window.setInterval(render, 60_000);
    let width = 0;
    const resize = new ResizeObserver(() => {
      if (card.clientWidth !== width) {
        width = card.clientWidth;
        render();
      }
    });
    resize.observe(card);

    return {
      update(next) {
        st = next;
        render();
      },
      destroy() {
        clearInterval(tick);
        clearTimeout(idle);
        resize.disconnect();
      },
    };
  },
});
