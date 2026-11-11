// The market chart behind a started game's card: each team's chance to win,
// a line each in its colour, from the start across a typical game's length
// (longer if it runs over). The 50% line stays at the centre; the scale
// stretches to the biggest swing so far. Hovering shows each line's price at
// that minute.

import { HourCycle, type DisplayPrefs } from "@widgetd/sdk";
import type { Market } from "./gen/sportsgame/v1/sportsgame_pb.ts";
import { halfRange, spread } from "./game.ts";

const SVG = "http://www.w3.org/2000/svg";
const MIN = 60_000;

export interface Series {
  key: "away" | "home";
  label: string;
  color: string;
  prices: number[];
}

export interface ChartData {
  market: Market;
  series: Series[];
  /** The time axis's least length, in ms: a typical game. */
  typicalMs: number;
  scoredUnixMs: number[];
  /** Mark where each line ends: the game is still on. */
  live: boolean;
}

export class Chart {
  readonly el: HTMLElement;
  private plot: HTMLElement;
  private svg: SVGSVGElement;
  private ends: HTMLElement;
  private hover: HTMLElement;
  private data?: ChartData;
  private pointerX?: number;
  private timeFmt: Intl.DateTimeFormat;

  /** host gets the pointer events and the "hovering" class (it dims the score). */
  constructor(
    private host: HTMLElement,
    prefs: DisplayPrefs,
  ) {
    this.el = div("market");
    this.plot = div("plot");
    this.svg = document.createElementNS(SVG, "svg");
    this.svg.setAttribute("viewBox", "0 0 100 100");
    this.svg.setAttribute("preserveAspectRatio", "none");
    this.svg.setAttribute("aria-hidden", "true");
    this.ends = div("ends");
    this.hover = div("hover");
    this.plot.append(this.svg, this.ends, this.hover);
    this.el.append(this.plot);
    this.timeFmt = new Intl.DateTimeFormat(prefs.locale, {
      timeZone: prefs.timeZone,
      hour: "numeric",
      minute: "2-digit",
      hourCycle: prefs.hourCycle === HourCycle.H24 ? "h23" : "h12",
    });
    host.addEventListener("pointermove", (e) => {
      this.pointerX = e.clientX;
      this.drawHover();
    });
    const leave = () => {
      this.pointerX = undefined;
      this.drawHover();
    };
    host.addEventListener("pointerleave", leave);
    // Leaving the card's iframe fast can skip pointerleave on the card; the
    // document still sees the pointer go out to nowhere.
    document.addEventListener("pointerout", (e) => {
      if (!e.relatedTarget) leave();
    });
    window.addEventListener("blur", leave);
  }

  /** Draws data, or clears the chart if there's none. */
  set(data: ChartData | undefined): void {
    this.data = data;
    this.el.hidden = !data;
    this.svg.replaceChildren();
    this.ends.replaceChildren();
    if (data) this.draw(data);
    this.drawHover();
  }

  // The scales: x from the start, in ms, to 0–100; y from a price to 0–100, top down.
  private scale(d: ChartData) {
    const n = Math.max(...d.series.map((s) => s.prices.length));
    const span = Math.max(d.typicalMs, (n - 1) * MIN);
    const half = halfRange(...d.series.map((s) => s.prices));
    return {
      n,
      x: (ms: number) => (ms / span) * 100,
      y: (p: number) => ((50 + half - p) / (2 * half)) * 100,
      minuteAt: (fx: number) => Math.round((fx * span) / MIN),
    };
  }

  private draw(d: ChartData): void {
    const { x, y } = this.scale(d);
    const start = Number(d.market.startUnixMs);
    const line = (cls: string, x1: number, y1: number, x2: number, y2: number) => {
      const l = svg("line", { class: cls, x1, y1, x2, y2 });
      this.svg.append(l);
    };
    line("mid", 0, 50, 100, 50);
    const last = start + (Math.max(...d.series.map((s) => s.prices.length)) - 1) * MIN;
    for (const t of d.scoredUnixMs) if (t >= start && t <= last) line("scored", x(t - start), 0, x(t - start), 100);

    for (const s of d.series) {
      if (!s.prices.length) continue;
      const path = s.prices.map((p, i) => `${i ? "L" : "M"}${x(i * MIN).toFixed(2)},${y(p).toFixed(2)}`).join("");
      const p = svg("path", { class: "line", d: path });
      p.style.stroke = s.color;
      this.svg.append(p);
      if (d.live) {
        const i = s.prices.length - 1;
        const dot = div("end");
        dot.style.left = `${x(i * MIN)}%`;
        dot.style.top = `${y(s.prices[i])}%`;
        dot.style.setProperty("--line", s.color);
        this.ends.append(dot);
      }
    }
  }

  /** The rule, a point on each line and their prices at the minute under the pointer. */
  private drawHover(): void {
    const d = this.data;
    const r = this.plot.getBoundingClientRect();
    const fx = this.pointerX === undefined || !r.width ? -1 : (this.pointerX - r.left) / r.width;
    const { n, x, y, minuteAt } = d ? this.scale(d) : { n: 0, x: Number, y: Number, minuteAt: () => -1 };
    const i = minuteAt(fx);
    if (!d || fx < 0 || i < 0 || i >= n) {
      this.host.classList.remove("hovering");
      this.hover.replaceChildren();
      return;
    }
    this.host.classList.add("hovering");

    const at = x(i * MIN);
    this.hover.style.left = `${at}%`;
    const shown = d.series.filter((s) => i < s.prices.length);
    // Tags stay whole inside the card, however near an edge their line is.
    const tops = spread(
      shown.map((s) => (Math.max(0, Math.min(100, y(s.prices[i]))) / 100) * r.height),
      18,
    ).map((top) => Math.max(9, Math.min(r.height - 9, top)));
    const parts: HTMLElement[] = [div("rule")];
    shown.forEach((s, k) => {
      const p = s.prices[i];
      const pt = div("pt");
      pt.style.top = `${y(p)}%`;
      const tag = div("tag");
      tag.textContent = `${s.label} ${p}%`;
      tag.style.top = `${tops[k]}px`;
      tag.classList.toggle("left", at > 70); // near the right edge, tags go left of the rule
      for (const el of [pt, tag]) el.style.setProperty("--line", s.color);
      parts.push(pt, tag);
    });
    const when = div("when");
    when.textContent = this.timeFmt.format(Number(d.market.startUnixMs) + i * MIN);
    parts.push(when);
    this.hover.replaceChildren(...parts);
  }
}

function div(cls: string): HTMLElement {
  const el = document.createElement("div");
  el.className = cls;
  return el;
}

function svg(tag: string, attrs: Record<string, string | number>): SVGElement {
  const el = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  return el;
}
