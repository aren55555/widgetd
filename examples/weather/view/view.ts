// The weather card. The front: the place, the temperature now, the sky as
// an icon and the air quality on one line, then four day tiles. Tapping a
// day opens it: a 24-hour strip (night, sunlight under the sun's arc, rain
// by the hour) and a row of humidity, wind and rain. Left alone for 30 s, the card goes
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
  Navigation2,
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
  conditionText,
  dayName,
  dayTitle,
  degrees,
  iconFor,
  rain,
  speed,
  strip,
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

      // The strip: measured once laid out, since the arc is drawn in pixels.
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

      // Four cells, each an icon and a value; the label shows above when there's room.
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
      const arrow = icon(Navigation2, { size: 12 });
      arrow.style.transform = `rotate(${(d.windFromDeg + 180) % 360}deg)`;
      stats.append(
        stat("Sky", weatherIcon(iconFor(d.condition), 13), conditionText(d.condition)),
        stat("Rain", weatherIcon("rain", 13), rain(d.precipMm, u), d.precipChance ? `${d.precipChance}%` : ""),
        stat("Wind", arrow, `${speed(d.windMps, u)} ${compass(d.windFromDeg)}`),
        stat("Humidity", icon(Droplet, { size: 12 }), `${d.humidity}%`),
      );

      detail.replaceChildren(drain, top, box, labels, stats);
      requestAnimationFrame(() => drawStrip(box, d, nowSec));
    };

    const drawStrip = (box: HTMLElement, d: Day, nowSec: number) => {
      const W = box.clientWidth;
      const H = box.clientHeight;
      if (!W || !H) return;
      const m = strip(d, nowSec, W, H);
      const svg = (cls: string) => {
        const g = document.createElementNS(SVG_NS, "svg");
        g.setAttribute("width", String(W));
        g.setAttribute("height", String(H));
        g.setAttribute("aria-hidden", "true");
        g.setAttribute("class", cls);
        return g;
      };
      const node = (name: string, attrs: Record<string, string | number>) => {
        const n = document.createElementNS(SVG_NS, name);
        for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v));
        return n;
      };
      const parts: Element[] = [];

      // The sky, under the rain: the day lighter than the night, fading
      // through twilight; sunlight under the arc; a glow where the sun meets
      // the horizon at dawn and dusk.
      if (m.daylight) {
        const { left, width, twilight } = m.daylight;
        const day = el("div", "daylight");
        day.style.left = `${left - twilight}%`;
        day.style.width = `${width + 2 * twilight}%`;
        day.style.setProperty("--fade", `${(twilight / (width + 2 * twilight)) * 100}%`);
        parts.push(day);
      }
      if (m.area && m.horizon) {
        const sky = svg("sky");
        const defs = node("defs", {});
        const light = node("linearGradient", { id: "wx-sunlight", x1: 0, y1: 0, x2: 0, y2: 1 });
        light.append(
          node("stop", { offset: 0, class: "sunlight-top" }),
          node("stop", { offset: 1, class: "sunlight-bottom" }),
        );
        const glow = node("radialGradient", { id: "wx-glow" });
        glow.append(node("stop", { offset: 0, class: "glow-in" }), node("stop", { offset: 1, class: "glow-out" }));
        defs.append(light, glow);
        sky.append(defs);
        const { rise, set, y, reach } = m.horizon;
        for (const x of [rise, set]) {
          sky.append(node("ellipse", { cx: x, cy: y, rx: reach * 1.6, ry: H * 0.7, fill: "url(#wx-glow)" }));
        }
        sky.append(node("path", { d: m.area, fill: "url(#wx-sunlight)" }));
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

      // The arc and the sun, over the rain.
      const sun = svg("sun");
      if (m.arc) sun.append(node("path", { d: m.arc, class: "arc" }));
      if (m.now?.y != null) sun.append(node("circle", { cx: m.now.x, cy: m.now.y, r: 4, class: "sundot" }));
      parts.push(sun);
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
