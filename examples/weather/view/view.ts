// The weather card. The front: the place, the temperature now, the sky as
// an icon and the air quality on one line, then four day tiles. Tapping a
// day opens it: a 24-hour strip (night, the sun's arc, rain by the hour)
// and pills for humidity, wind and rain. Left alone for 30 s, the card goes
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

/** Text hidden from sight but read out. */
const sr = (text: string) => el("span", "sr", text);

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
      const sum = el("div", "sum");
      sum.append(
        weatherIcon(iconFor(d.condition), 16),
        el("span", "", conditionText(d.condition)),
        el("b", "", degrees(d.highC, u)),
        el("span", "", degrees(d.lowC, u)),
      );
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

      const pills = el("div", "pills");
      const pill = (label: string, glyph: Element, value: string, cls = "") => {
        const p = el("span", `pill ${cls}`);
        p.title = label;
        p.append(sr(label + " "), glyph, el("span", "", value));
        return p;
      };
      const arrow = icon(Navigation2, { size: 11 });
      arrow.style.transform = `rotate(${(d.windFromDeg + 180) % 360}deg)`;
      pills.append(
        pill("Humidity", icon(Droplet, { size: 12 }), `${d.humidity}%`),
        pill("Wind", arrow, `${speed(d.windMps, u)} ${compass(d.windFromDeg)}`),
        pill("Rain", weatherIcon("rain", 12), rain(d.precipMm, u)),
      );

      detail.replaceChildren(drain, top, box, labels, pills);
      requestAnimationFrame(() => drawStrip(box, d, nowSec));
    };

    const drawStrip = (box: HTMLElement, d: Day, nowSec: number) => {
      const W = box.clientWidth;
      const H = box.clientHeight;
      if (!W || !H) return;
      const m = strip(d, nowSec, W, H);
      const parts: Element[] = [];
      if (m.daylight) {
        const day = el("div", "daylight");
        day.style.left = `${m.daylight.left}%`;
        day.style.width = `${m.daylight.width}%`;
        parts.push(day);
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
      const svg = document.createElementNS(SVG_NS, "svg");
      svg.setAttribute("width", String(W));
      svg.setAttribute("height", String(H));
      svg.setAttribute("aria-hidden", "true");
      if (m.arc) {
        const path = document.createElementNS(SVG_NS, "path");
        path.setAttribute("d", m.arc);
        path.setAttribute("class", "arc");
        svg.append(path);
      }
      if (m.now?.y != null) {
        const dot = document.createElementNS(SVG_NS, "circle");
        dot.setAttribute("cx", String(m.now.x));
        dot.setAttribute("cy", String(m.now.y));
        dot.setAttribute("r", "4");
        dot.setAttribute("class", "sundot");
        svg.append(dot);
      }
      parts.push(svg);
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
