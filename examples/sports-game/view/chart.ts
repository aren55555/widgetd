// The market chart behind a started game's card: each team's chance to win,
// a line each in its colour, from the start across a typical game's length
// (longer if it runs over). The 50% line stays at the centre; the scale
// stretches to the biggest swing so far. Each score is a mark in the scoring
// team's colour. Hovering shows each line's price at that minute, and near a
// mark, who scored.

import { HourCycle, type DisplayPrefs } from "@widgetd/sdk";
import type { Market } from "./gen/sportsgame/v1/sportsgame_pb.ts";
import { halfRange, spread } from "./game.ts";

const SVG = "http://www.w3.org/2000/svg";
const MIN = 60_000;
/** How near a mark, in px, the pointer must be to show who scored. */
const NEAR = 6;

export interface Series {
  key: "away" | "home";
  label: string;
  color: string;
  prices: number[];
}

/** A score's mark on the chart. */
export interface Mark {
  unixMs: number;
  /** The scoring team's line colour. */
  color: string;
  /** Who scored, as creditLine writes it; empty if ESPN doesn't say. */
  credit: string;
}

export interface ChartData {
  market: Market;
  series: Series[];
  /** The time axis's least length, in ms: a typical game. */
  typicalMs: number;
  marks: Mark[];
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
      return l;
    };
    line("mid", 0, 50, 100, 50);
    const last = start + (Math.max(...d.series.map((s) => s.prices.length)) - 1) * MIN;
    for (const m of this.marks(d, start, last)) {
      const at = x(m.unixMs - start);
      line("scored", at, 0, at, 100).style.stroke = m.color;
    }

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

  /** The marks within the chart's time so far. */
  private marks(d: ChartData, start: number, last: number): Mark[] {
    return d.marks.filter((m) => m.unixMs >= start && m.unixMs <= last);
  }

  /**
   * The rule, a point on each line and their prices at the minute under the
   * pointer. Near a score's mark, the rule snaps to it and says who scored.
   */
  private drawHover(): void {
    const d = this.data;
    const r = this.plot.getBoundingClientRect();
    const fx = this.pointerX === undefined || !r.width ? -1 : (this.pointerX - r.left) / r.width;
    const { n, x, y, minuteAt } = d ? this.scale(d) : { n: 0, x: Number, y: Number, minuteAt: () => -1 };
    let i = minuteAt(fx);
    if (!d || fx < 0 || i < 0 || i >= n) {
      this.host.classList.remove("hovering");
      this.hover.replaceChildren();
      return;
    }
    this.host.classList.add("hovering");

    const start = Number(d.market.startUnixMs);
    const px = fx * r.width;
    const near = this.marks(d, start, start + (n - 1) * MIN)
      .map((m) => ({ m, gap: Math.abs((x(m.unixMs - start) / 100) * r.width - px) }))
      .filter((v) => v.gap <= NEAR)
      .sort((a, b) => a.gap - b.gap)
      .at(0)?.m;
    if (near) i = Math.min(n - 1, Math.round((near.unixMs - start) / MIN));

    const at = near ? x(near.unixMs - start) : x(i * MIN);
    this.hover.style.left = `${at}%`;
    const shown = d.series.filter((s) => i < s.prices.length);
    // Tags stay whole inside the card, however near an edge their line is,
    // and below the credit when there is one.
    const top0 = near?.credit ? 34 : 9; // the credit is 4–23px from the top
    const tops = spread(
      shown.map((s) => (Math.max(0, Math.min(100, y(s.prices[i]))) / 100) * r.height),
      18,
    ).map((top) => Math.max(top0, Math.min(r.height - 9, top)));
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
    let credit: HTMLElement | undefined;
    if (near?.credit) {
      credit = div("credit");
      credit.textContent = near.credit;
      credit.style.setProperty("--line", near.color);
      parts.push(credit);
    }
    const when = div("when");
    when.textContent = this.timeFmt.format(near ? near.unixMs : start + i * MIN);
    parts.push(when);
    this.hover.replaceChildren(...parts);
    // Centred on the mark where it fits, slid along to stay inside the card
    // (it can be wider than the space either side of the mark).
    if (credit) {
      const atPx = (at / 100) * r.width;
      const w = credit.offsetWidth;
      const left = Math.max(4, Math.min(r.width - w - 4, atPx - w / 2));
      credit.style.left = `${left - atPx}px`;
    }
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
