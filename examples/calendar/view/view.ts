// The calendar's view: the coming fortnight as 14 day strips filling the
// card (<cal-day>), one day opened into a timeline (<cal-detail>), and one
// event's screen (<cal-event>). The look was settled in
// spikes/calendar-spectrum; its agreed settings are the constants below.

import { defineWidget } from "@widgetd/sdk";

import { Response, StateSchema, type Event, type State } from "./gen/calendar/v1/calendar_pb.ts";
import {
  clockLabel, dateOf, dayLabel, detectPrefs, duration, fortnight, hourLabels, lanes, nextTick, overlaps, segments, timeLabel,
  type Cat, type Day, type Prefs,
} from "./model.ts";

const C = {
  hue: "#22d3ee", // busy
  tent: "#a78bfa", // tentative or free
  dbl: "#fb923c", // double-booked
  surface: "#141417",
  lift: 0.12, // the working-hours band
  tick: 60, // minutes between ticks; every 4 h is major
  major: 240,
  tickLen: 5,
  weekendW: 0.5, // a weekend column's width, against a weekday's 1
  textOp: 0.7,
  morphMs: 450,
  autoCloseMs: 20_000,
  cols: 7,
};
const MIN = 60_000;
const FONT = "ui-monospace, Menlo, monospace";
const BACK_ICON = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 5l-7 7 7 7"/></svg>`;

const colorOf = (cat: Cat) => (cat === "double" ? C.dbl : cat === "tent" ? C.tent : C.hue);
const rgba = (hex: string, a: number) => {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${n >> 16},${(n >> 8) & 255},${n & 255},${a})`;
};
const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);
const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
type Rect = { x: number; y: number; w: number; h: number };
const lerpR = (a: Rect, b: Rect, t: number): Rect => ({ x: lerp(a.x, b.x, t), y: lerp(a.y, b.y, t), w: lerp(a.w, b.w, t), h: lerp(a.h, b.h, t) });

/** Truncates s with an ellipsis to fit width w in g's current font. */
function fitText(g: CanvasRenderingContext2D, s: string, w: number): string {
  if (g.measureText(s).width <= w) return s;
  let lo = 0, hi = s.length;
  while (lo < hi) {
    const m = (lo + hi + 1) >> 1;
    g.measureText(s.slice(0, m) + "…").width <= w ? (lo = m) : (hi = m - 1);
  }
  return lo ? s.slice(0, lo) + "…" : "";
}

/** Sizes a canvas to its box at the screen's pixel density; null if it has no size yet. */
function context(cv: HTMLCanvasElement): { g: CanvasRenderingContext2D; W: number; H: number } | null {
  const dpr = devicePixelRatio || 1, W = cv.clientWidth, H = cv.clientHeight;
  if (!W || !H) return null;
  cv.width = Math.round(W * dpr);
  cv.height = Math.round(H * dpr);
  const g = cv.getContext("2d")!;
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  return { g, W, H };
}

/** What a day needs to draw itself, beyond its own data. */
type DayProps = { d: Day; labels: ("left" | "right")[]; prefs: Prefs; wh: { start: number; end: number } };

// ---------- <cal-day>: one day ----------
// Drawn only from its props: it knows nothing about its neighbours or the
// clock; the card works those out and hands them in.
class CalDay extends HTMLElement {
  p?: DayProps;
  cv!: HTMLCanvasElement;
  private lbl!: HTMLElement;

  connectedCallback() {
    if (this.cv) return;
    this.lbl = document.createElement("div");
    this.lbl.className = "lbl";
    this.cv = document.createElement("canvas");
    this.append(this.lbl, this.cv);
    new ResizeObserver(() => this.draw()).observe(this);
  }

  set props(p: DayProps) {
    this.p = p;
    const { d, prefs } = p;
    this.classList.toggle("today", d.today);
    this.classList.toggle("weekend", d.weekend);
    const name = document.createElement("b");
    name.textContent = dayLabel(d.day, prefs.locale, { weekday: "short" });
    const left = document.createElement("span");
    left.append(name, ` ${dateOf(d.day)}`);
    const dots = document.createElement("span");
    dots.textContent = "•".repeat(Math.min(d.allDay.length, 4));
    this.lbl.replaceChildren(left, dots);
    this.draw();
  }

  draw() {
    const p = this.p, c = this.cv && context(this.cv);
    if (!p || !c) return;
    const { g, W, H } = c, { d } = p, y = (m: number) => (m / d.len) * H;

    // Box and working-hours band.
    g.fillStyle = C.surface;
    g.fillRect(0, 0, W, H);
    if (d.working) {
      g.fillStyle = `rgba(255,255,255,${C.lift})`;
      g.fillRect(0, y(d.work[0]), W, y(d.work[1]) - y(d.work[0]));
      // Ticks down the left edge, every hour over the working hours.
      for (let t = Math.ceil(p.wh.start / C.tick) * C.tick; t <= p.wh.end; t += C.tick) {
        const yy = Math.round(y(d.work[0] + t - p.wh.start)) + 0.5, major = t % C.major === 0;
        g.strokeStyle = major ? "#a1a1aa" : "#71717a";
        g.lineWidth = 1;
        g.beginPath();
        g.moveTo(0, yy);
        g.lineTo(major ? C.tickLen * 1.8 : C.tickLen, yy);
        g.stroke();
      }
    }

    // Events as solid bands, coloured by what each stretch of time is
    // (busy, double-booked, tentative), with a hairline at every event
    // edge so back-to-back meetings stay apart.
    const x0 = C.tickLen * 1.8 + 2;
    for (const seg of segments(d.spans)) {
      g.fillStyle = colorOf(seg.cat);
      g.fillRect(x0, y(seg.s), W - x0, Math.max(1, y(seg.e) - y(seg.s)));
    }
    g.fillStyle = C.surface;
    for (const x of d.spans) for (const m of [x.s, x.e]) g.fillRect(x0, y(m) - 0.5, W - x0, 1);

    // The past, dimmed; now, a red line.
    if (d.pastUntil > 0) {
      g.fillStyle = "rgba(24,24,27,0.6)";
      g.fillRect(0, 0, W, Math.min(H, y(d.pastUntil)));
    }
    if (d.now != null) {
      const yy = Math.round(y(d.now)) + 0.5;
      g.strokeStyle = g.fillStyle = "#f87171";
      g.lineWidth = 1;
      g.beginPath();
      g.moveTo(0, yy);
      g.lineTo(W, yy);
      g.stroke();
      g.beginPath();
      g.moveTo(0, yy - 3);
      g.lineTo(4, yy);
      g.lineTo(0, yy + 3);
      g.fill();
    }

    // Working hours, on the edges the card asked for: start at the band's
    // top, end at its bottom. Inside a workday they clear the ticks.
    if (p.labels.length) {
      const top = y(d.work[0]), bot = y(d.work[1]);
      const a = clockLabel(p.wh.start, p.prefs.hour12), b = clockLabel(p.wh.end, p.prefs.hour12);
      g.save();
      g.globalAlpha = C.textOp;
      g.fillStyle = "#e5e7eb";
      g.font = `600 9px ${FONT}`;
      g.shadowColor = "#000";
      g.shadowBlur = 3;
      for (const edge of p.labels) {
        const x = edge === "left" ? (d.working ? C.tickLen * 1.8 : 0) + 3 : W - 3;
        g.textAlign = edge;
        g.textBaseline = "top";
        g.fillText(a, x, top + 2);
        g.textBaseline = "bottom";
        g.fillText(b, x, bot - 2);
      }
      g.restore();
    }
  }
}
customElements.define("cal-day", CalDay);

// ---------- <cal-detail>: one day, opened ----------
// Tapping a day morphs its strip into a horizontal timeline that fills the
// card: time runs left to right, overlapping events take separate lanes,
// and each bar carries its title and time. Every piece is laid out twice,
// as the strip it starts as and as the timeline, and drawn in between by
// the eased progress. The back button (or Escape, or waiting) folds it back.
type Layout = {
  box: Rect;
  band: Rect;
  ev: Rect[];
  past: Rect;
  now: Rect | null;
};
type Timeline = Layout & { lo: number; hi: number; xh: (m: number) => number; pad: number; headH: number; axisTop: number };

class CalDetail extends HTMLElement {
  p: DayProps | null = null;
  sel = -1;
  onTap?: (j: number, at: { x: number; y: number }) => void;
  private cv!: HTMLCanvasElement;
  private back!: HTMLButtonElement;
  private from: Rect = { x: 0, y: 0, w: 0, h: 0 };
  private t = 0;
  private way = 0;
  private raf = 0;
  private timer = 0;
  private hz?: Timeline;

  connectedCallback() {
    if (this.cv) return;
    this.cv = document.createElement("canvas");
    this.back = document.createElement("button");
    this.back.className = "back";
    this.back.setAttribute("aria-label", "Back to two weeks");
    this.back.innerHTML = `${BACK_ICON}<span>2 weeks</span>`;
    this.back.addEventListener("click", (ev) => {
      ev.stopPropagation();
      this.close();
    });
    this.append(this.cv, this.back);
    this.cv.addEventListener("click", (ev) => {
      if (this.t < 1 || !this.hz) return;
      this.arm();
      const j = this.hit(ev);
      if (j < 0) return;
      const r = this.hz.ev[j]!;
      this.sel = j;
      this.draw();
      this.onTap?.(j, { x: r.x + r.w / 2, y: r.y + r.h / 2 });
    });
    this.cv.addEventListener("pointermove", (ev) => (this.cv.style.cursor = this.t === 1 && this.hit(ev) >= 0 ? "pointer" : "default"));
    new ResizeObserver(() => this.draw()).observe(this);
  }

  get isOpen() {
    return !!this.p;
  }

  /** The bar under a pointer event (narrow bars get a 16px-wide target), or -1. */
  private hit(ev: MouseEvent): number {
    if (!this.hz) return -1;
    const b = this.cv.getBoundingClientRect(), x = ev.clientX - b.left, y = ev.clientY - b.top;
    for (let j = this.hz.ev.length - 1; j >= 0; j--) {
      const r = this.hz.ev[j]!, pad = Math.max(0, (16 - r.w) / 2);
      if (x >= r.x - pad && x <= r.x + r.w + pad && y >= r.y && y <= r.y + r.h) return j;
    }
    return -1;
  }

  /** p: the day; from: its strip's rectangle, in this element's coordinates. */
  open(p: DayProps, from: Rect) {
    this.sel = -1;
    this.p = p;
    this.from = from;
    this.way = 1;
    this.classList.add("open");
    this.run();
    this.arm();
  }

  close() {
    if (!this.p) return;
    clearTimeout(this.timer);
    this.onClose?.();
    this.way = -1;
    this.run();
  }
  onClose?: () => void;

  /** New data or a new minute: redraw the open day, if it's still shown. */
  update(p: DayProps | undefined) {
    if (!this.p) return;
    if (!p) return this.close();
    this.p = p;
    this.draw();
  }

  /** (Re)starts the idle timer that folds the day back. */
  arm() {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.close(), C.autoCloseMs) as unknown as number;
  }

  private run() {
    if (this.raf) return;
    let last = performance.now();
    const step = (now: number) => {
      this.t = Math.min(1, Math.max(0, this.t + (this.way * (now - last)) / C.morphMs));
      last = now;
      this.draw();
      if ((this.way > 0 && this.t < 1) || (this.way < 0 && this.t > 0)) this.raf = requestAnimationFrame(step);
      else {
        this.raf = 0;
        if (this.t === 0) {
          this.classList.remove("open");
          this.p = null;
          this.back.style.opacity = "0";
          this.back.style.pointerEvents = "none";
          this.draw();
        }
      }
    };
    this.raf = requestAnimationFrame(step);
  }

  /** The strip, as <cal-day> draws it, inside A. */
  private strip(p: DayProps, A: Rect): Layout {
    const d = p.d, yv = (m: number) => A.y + (Math.min(Math.max(m, 0), d.len) / d.len) * A.h, x0 = A.x + C.tickLen * 1.8 + 2;
    return {
      box: A,
      band: { x: A.x, y: yv(d.work[0]), w: A.w, h: yv(d.work[1]) - yv(d.work[0]) },
      ev: d.spans.map((x) => ({ x: x0, y: yv(x.s), w: A.x + A.w - x0, h: Math.max(1, yv(x.e) - yv(x.s)) })),
      past: { x: A.x, y: A.y, w: A.w, h: yv(d.pastUntil) - A.y },
      now: d.now == null ? null : { x: A.x, y: yv(d.now), w: A.w, h: 1 },
    };
  }

  /** The timeline, filling B. */
  private timeline(p: DayProps, B: Rect): Timeline {
    const d = p.d, pad = 12, headH = 40, allH = d.allDay.some((t) => t) ? 22 : 0, axisH = 16, top = B.y + headH + allH + axisH;
    let lo = d.working ? d.work[0] : 8 * 60, hi = d.working ? d.work[1] : 20 * 60;
    for (const x of d.spans) {
      lo = Math.min(lo, x.s);
      hi = Math.max(hi, x.e);
    }
    if (d.now != null) {
      lo = Math.min(lo, d.now);
      hi = Math.max(hi, d.now + 30);
    }
    lo = Math.max(0, Math.floor(lo / 60) * 60);
    hi = Math.min(d.len, Math.ceil(hi / 60) * 60);
    const xh = (m: number) => B.x + pad + ((Math.min(Math.max(m, lo), hi) - lo) / Math.max(1, hi - lo)) * (B.w - 2 * pad);
    const { lane, count } = lanes(d.spans), gap = 4, avail = B.y + B.h - pad - top;
    const laneH = Math.max(14, (avail - gap * (count - 1)) / count);
    return {
      box: B, lo, hi, xh, pad, headH, axisTop: B.y + headH + allH,
      band: { x: xh(d.work[0]), y: B.y + headH + allH, w: xh(d.work[1]) - xh(d.work[0]), h: B.h - headH - allH },
      ev: d.spans.map((x, j) => ({ x: xh(x.s), y: top + lane[j]! * (laneH + gap), w: Math.max(2, xh(x.e) - xh(x.s)), h: laneH })),
      past: { x: B.x, y: B.y, w: d.pastUntil >= d.len ? B.w : Math.max(0, xh(d.pastUntil) - B.x), h: B.h },
      now: d.now == null ? null : { x: xh(d.now), y: B.y + headH + allH, w: 1, h: B.h - headH - allH },
    };
  }

  draw() {
    const c = this.cv && context(this.cv);
    if (!c) return;
    const { g, W, H } = c, p = this.p;
    if (!p) return;
    const { d, prefs } = p, t = ease(this.t);
    const V = this.strip(p, this.from), Hz = this.timeline(p, { x: 0, y: 0, w: W, h: H });
    this.hz = Hz;
    const box = lerpR(V.box, Hz.box, t);
    g.save();
    g.beginPath();
    g.rect(box.x, box.y, box.w, box.h);
    g.clip();
    g.fillStyle = C.surface;
    g.fillRect(box.x, box.y, box.w, box.h);
    if (d.working) {
      const r = lerpR(V.band, Hz.band, t);
      g.fillStyle = `rgba(255,255,255,${C.lift})`;
      g.fillRect(r.x, r.y, r.w, r.h);
    }

    const ta = Math.max(0, (this.t - 0.55) / 0.45); // text and axis fade in late
    this.back.style.opacity = String(ta);
    this.back.style.pointerEvents = this.way > 0 && ta > 0 ? "auto" : "none";
    if (ta > 0) {
      // Hour grid and labels along the top of the timeline.
      g.save();
      g.globalAlpha = ta;
      g.font = `10px ${FONT}`;
      g.textBaseline = "top";
      for (let m = Hz.lo; m <= Hz.hi; m += 60) {
        const x = Math.round(Hz.xh(m)) + 0.5;
        g.strokeStyle = "rgba(255,255,255,0.06)";
        g.beginPath();
        g.moveTo(x, Hz.axisTop + 14);
        g.lineTo(x, H);
        g.stroke();
        g.fillStyle = "#a1a1aa";
        g.textAlign = m === Hz.lo ? "left" : m === Hz.hi ? "right" : "center";
        g.fillText(timeLabel(d.start + m * MIN, prefs), x, Hz.axisTop + 1);
      }
      g.restore();
    }

    // Events: the strip's bands become solid bars with their title and
    // time on them, in dark ink.
    d.spans.forEach((x, j) => {
      let r = lerpR(V.ev[j]!, Hz.ev[j]!, t);
      const c = x.k ? C.tent : overlaps(d.spans, j).length ? C.dbl : C.hue;
      const sel = this.sel === j, dim = this.sel >= 0 && !sel;
      g.fillStyle = rgba(c, lerp(1, dim ? 0.3 : 0.9, t));
      g.fillRect(r.x, r.y, r.w, r.h);
      if (sel) {
        g.strokeStyle = "#fff";
        g.lineWidth = 1.5;
        g.strokeRect(r.x + 0.75, r.y + 0.75, r.w - 1.5, r.h - 1.5);
      }
      if (t < 0.5) {
        g.fillStyle = C.surface;
        g.fillRect(r.x, r.y - 0.5, r.w, 1);
      }
      const la = Math.max(0, (t - 0.6) / 0.4);
      if (la <= 0 || Math.max(r.w, r.h) < 22 || Math.min(r.w, r.h) < 10) return;
      const title = x.ev?.title ?? "", when = `${timeLabel(x.from, prefs)}–${timeLabel(x.to, prefs)}`;
      g.save();
      g.beginPath();
      g.rect(r.x, r.y, r.w, r.h);
      g.clip();
      g.globalAlpha = la * (dim ? 0.45 : 1);
      g.fillStyle = "#0c0c0e";
      g.textAlign = "left";
      g.textBaseline = "top";
      // A tall, narrow bar whose text won't fit across reads sideways.
      g.font = `600 11px ${FONT}`;
      if (r.h > r.w * 1.5 && g.measureText(title || when).width > r.w - 9) {
        g.translate(r.x, r.y + r.h);
        g.rotate(-Math.PI / 2);
        r = { x: 0, y: 0, w: r.h, h: r.w };
      }
      const fs = Math.min(13, Math.max(9, Math.floor(Math.min(r.h / 2.6, 13)))), px = r.x + 5, pw = r.w - 9;
      const two = !!title && r.h >= fs * 2 + 8, y0 = two ? r.y + (r.h - (fs * 2 + 3)) / 2 : r.y + (r.h - fs) / 2;
      g.font = `600 ${fs}px ${FONT}`;
      if (two) {
        g.fillText(fitText(g, title, pw), px, y0);
        g.font = `${fs - 1}px ${FONT}`;
        g.globalAlpha *= 0.75;
        g.fillText(fitText(g, when, pw), px, y0 + fs + 3);
      } else {
        const both = title ? `${title} · ${when}` : when;
        g.fillText(fitText(g, g.measureText(both).width <= pw ? both : title || when, pw), px, y0);
      }
      g.restore();
    });

    // The past, and now.
    const past = lerpR(V.past, Hz.past, t);
    if (past.w > 0 && past.h > 0) {
      g.fillStyle = "rgba(24,24,27,0.6)";
      g.fillRect(past.x, past.y, past.w, past.h);
    }
    if (V.now && Hz.now) {
      const r = lerpR(V.now, Hz.now, t);
      g.fillStyle = "#f87171";
      g.fillRect(r.x, r.y, Math.max(1, r.w), Math.max(1, r.h));
    }

    if (ta > 0) {
      g.save();
      g.globalAlpha = ta;
      g.shadowColor = "#000";
      g.shadowBlur = 3;
      // Header: the date, and a summary.
      const busy = d.spans.filter((x) => !x.k).reduce((n, x) => n + (x.e - x.s), 0);
      g.textBaseline = "middle";
      g.textAlign = "left";
      g.fillStyle = "#e5e7eb";
      g.font = `600 13px ${FONT}`;
      g.fillText(dayLabel(d.day, prefs.locale, { weekday: "long", month: "short", day: "numeric" }), this.back.offsetWidth + 18, Hz.headH / 2 + 2);
      g.textAlign = "right";
      g.fillStyle = "#a1a1aa";
      g.font = `11px ${FONT}`;
      const n = d.spans.length;
      g.fillText(`${n} event${n === 1 ? "" : "s"} · ${duration(busy)} busy`, W - Hz.pad, Hz.headH / 2 + 2);
      // All-day chips.
      let cx = Hz.pad;
      g.shadowBlur = 0;
      for (const title of d.allDay) {
        if (!title) continue;
        const w = Math.min(g.measureText(title).width + 14, W - cx - Hz.pad);
        if (w < 30) break;
        g.fillStyle = rgba(C.hue, 0.18);
        g.beginPath();
        g.roundRect(cx, Hz.headH, w, 17, 4);
        g.fill();
        g.fillStyle = "#e5e7eb";
        g.textAlign = "left";
        g.fillText(fitText(g, title, w - 14), cx + 7, Hz.headH + 9);
        cx += w + 6;
      }
      g.restore();
    }
    g.restore();
  }
}
customElements.define("cal-detail", CalDetail);

// ---------- <cal-event>: one event ----------
// The screen dissolves in radially from the tapped bar, then the details
// fade in: when, where, video, organizer, your response, overlaps, notes
// and the guest list. Back dissolves it into the same point.
const RSVP: Record<number, string> = {
  [Response.ACCEPTED]: "accepted", [Response.TENTATIVE]: "maybe", [Response.DECLINED]: "declined", [Response.NEEDS_ACTION]: "not answered",
};
const MARK: Record<number, [string, string]> = {
  [Response.ACCEPTED]: ["yes", "✓"], [Response.TENTATIVE]: ["maybe", "?"], [Response.DECLINED]: ["no", "✕"],
  [Response.NEEDS_ACTION]: ["none", "·"], [Response.UNSPECIFIED]: ["none", "·"],
};

class CalEvent extends HTMLElement {
  onActivity?: () => void;
  onClosing?: () => void;
  private panel!: HTMLElement;
  private at = { x: 0, y: 0 };
  private k = 0;
  private way = 0;
  private raf = 0;

  connectedCallback() {
    if (this.panel) return;
    this.innerHTML = `<div class="ev-panel"><div class="ev-content">
      <header><button class="back" aria-label="Back to the day">${BACK_ICON}<span class="ev-back"></span></button>
        <div class="ev-head"><div class="ev-title"></div><div class="ev-when"></div></div><div class="ev-chips"></div></header>
      <div class="ev-track"></div>
      <div class="ev-body"><dl class="ev-facts"></dl><section class="ev-people"><h4></h4><ul></ul></section></div>
    </div></div>`;
    this.panel = this.querySelector(".ev-panel")!;
    this.querySelector(".back")!.addEventListener("click", (ev) => {
      ev.stopPropagation();
      this.close();
    });
    this.addEventListener("pointerdown", () => this.onActivity?.());
  }

  get isOpen() {
    return this.classList.contains("open");
  }

  /** at: the centre of the tapped bar, in this element's coordinates. */
  open(p: DayProps, j: number, at: { x: number; y: number }) {
    this.at = at;
    this.fill(p, j);
    this.classList.add("open");
    this.classList.remove("shown");
    this.dissolve(1);
  }

  close() {
    if (!this.isOpen) return;
    this.classList.remove("shown");
    this.onClosing?.();
    this.dissolve(-1);
  }

  private dissolve(way: number) {
    this.way = way;
    if (this.raf) return;
    let last = performance.now();
    const step = (now: number) => {
      this.k = Math.min(1, Math.max(0, this.k + (this.way * Math.max(0, now - last)) / C.morphMs));
      last = now;
      this.paint();
      if ((this.way > 0 && this.k < 1) || (this.way < 0 && this.k > 0)) this.raf = requestAnimationFrame(step);
      else {
        this.raf = 0;
        if (this.k === 0) this.classList.remove("open");
      }
    };
    this.raf = requestAnimationFrame(step);
  }

  /** A soft-edged circle growing from the bar until it covers the card. */
  private paint() {
    const W = this.clientWidth, H = this.clientHeight, { x, y } = this.at, feather = 90;
    const far = Math.max(Math.hypot(x, y), Math.hypot(W - x, y), Math.hypot(x, H - y), Math.hypot(W - x, H - y));
    const r = ease(this.k) * (far + feather);
    this.classList.toggle("shown", this.way > 0 && this.k > 0.45);
    const mask = this.k >= 1 ? "none" : `radial-gradient(circle at ${x}px ${y}px, #000 ${Math.max(0, r - feather)}px, transparent ${r}px)`;
    this.panel.style.maskImage = mask;
    this.panel.style.setProperty("-webkit-mask-image", mask);
    this.panel.style.opacity = String(Math.min(1, this.k * 2.5));
  }

  private fill(p: DayProps, j: number) {
    const { d, prefs } = p, x = d.spans[j]!, e: Partial<Event> = x.ev ?? {};
    const dbl = overlaps(d.spans, j);
    const $ = <T extends HTMLElement>(s: string) => this.querySelector<T>(s)!;
    this.style.setProperty("--ev", x.k ? C.tent : dbl.length ? C.dbl : C.hue);
    $(".ev-title").textContent = e.title || (x.k ? "Tentative" : "Busy");
    $(".ev-back").textContent = `${dayLabel(d.day, prefs.locale, { weekday: "short" })} ${dateOf(d.day)}`;
    $(".ev-when").textContent =
      `${dayLabel(d.day, prefs.locale, { weekday: "long", month: "short", day: "numeric" })} · ` +
      `${timeLabel(x.from, prefs)}–${timeLabel(x.to, prefs)} · ${duration(Math.round((x.to - x.from) / MIN))}`;

    const chips: [string, string][] = [];
    if (x.k) chips.push([e.you === Response.TENTATIVE || e.you === Response.NEEDS_ACTION ? "tentative" : "free", C.tent]);
    if (dbl.length) chips.push(["double-booked", C.dbl]);
    $(".ev-chips").replaceChildren(
      ...chips.map(([t, col]) => {
        const s = document.createElement("span");
        s.style.setProperty("--chip", col);
        s.textContent = t;
        return s;
      }),
    );

    // Where this event sits in its day.
    const lo = Math.min(...d.spans.map((y) => y.s)), hi = Math.max(...d.spans.map((y) => y.e));
    const pos = (m: number) => ((m - lo) / Math.max(1, hi - lo)) * 100;
    const track = $(".ev-track");
    track.replaceChildren(
      ...d.spans.map((y) => {
        const i = document.createElement("i");
        if (y === x) i.className = "me";
        i.style.left = `${pos(y.s)}%`;
        i.style.width = `${Math.max(0.6, pos(y.e) - pos(y.s))}%`;
        return i;
      }),
      ...[["left", lo], ["right", hi]].map(([side, m]) => {
        const b = document.createElement("b");
        b.style[side as "left" | "right"] = "0";
        b.textContent = timeLabel(d.start + (m as number) * MIN, prefs);
        return b;
      }),
    );

    const facts: [string, string][] = (
      [
        ["Where", e.where ?? ""], ["Video", e.video ?? ""], ["Organizer", e.organizer ?? ""],
        ["You", RSVP[e.you ?? 0] ?? ""], ["Overlaps", dbl.map((y) => y.ev?.title || "Busy").join(", ")], ["Notes", e.notes ?? ""],
      ] as [string, string][]
    ).filter(([, v]) => v);
    const dl = $(".ev-facts");
    if (facts.length) {
      dl.replaceChildren(
        ...facts.flatMap(([k, v]) => {
          const dt = document.createElement("dt"), dd = document.createElement("dd");
          dt.textContent = k;
          dd.textContent = v;
          if (k === "Notes") dd.className = "notes";
          return [dt, dd];
        }),
      );
    } else {
      const dd = document.createElement("dd");
      dd.className = "none";
      dd.textContent = "No other details.";
      dl.replaceChildren(document.createElement("dt"), dd);
    }

    const guests = e.guests ?? [], people = $(".ev-people");
    people.hidden = !guests.length;
    const n = (r: Response) => guests.filter((gu) => gu.response === r).length;
    const total = Math.max(e.guestCount ?? 0, guests.length);
    people.querySelector("h4")!.textContent = [
      `${total} guest${total === 1 ? "" : "s"}`,
      `${n(Response.ACCEPTED)} yes`,
      n(Response.TENTATIVE) ? `${n(Response.TENTATIVE)} maybe` : "",
      n(Response.DECLINED) ? `${n(Response.DECLINED)} no` : "",
      n(Response.NEEDS_ACTION) ? `${n(Response.NEEDS_ACTION)} awaiting` : "",
    ].filter(Boolean).join(" · ");
    people.querySelector("ul")!.replaceChildren(
      ...guests.map((gu) => {
        const [cls, mark] = MARK[gu.response] ?? MARK[0]!;
        const li = document.createElement("li"), m = document.createElement("span");
        li.className = cls;
        m.className = "mark";
        m.textContent = mark;
        li.append(m, gu.name);
        if (gu.organizer) {
          const em = document.createElement("em");
          em.textContent = "organizer";
          li.append(" ", em);
        }
        return li;
      }),
    );
  }
}
customElements.define("cal-event", CalEvent);

// ---------- the card ----------

/** The narrowest a day can be and still fit its label and the working-hour labels. */
function labelWidth(prefs: Prefs, wh: { start: number; end: number }): number {
  const g = document.createElement("canvas").getContext("2d")!;
  const w = (font: string, s: string) => {
    g.font = `${font} ${FONT}`;
    return g.measureText(s).width;
  };
  return Math.ceil(Math.max(
    w("600 11px", `${dayLabel(0, prefs.locale, { weekday: "short" })} 30`) + 12,
    w("600 9px", clockLabel(wh.start, prefs.hour12)) + 8,
    w("600 9px", clockLabel(wh.end, prefs.hour12)) + 8,
  ));
}

defineWidget({
  state: StateSchema,
  mount({ root }) {
    // Board options will pass the display's own; until then, the browser's.
    const prefs = detectPrefs();
    const cal = document.createElement("div");
    cal.className = "cal";
    const grid = document.createElement("div");
    grid.className = "days";
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.textContent = "Nothing in the next two weeks";
    empty.hidden = true;
    const detail = document.createElement("cal-detail") as CalDetail;
    const eventView = document.createElement("cal-event") as CalEvent;
    cal.append(grid, empty, detail, eventView);
    root.replaceChildren(cal);
    const cells = Array.from({ length: 14 }, () => document.createElement("cal-day") as CalDay);
    grid.append(...cells);

    let st: State | undefined;
    let days: Day[] = [];
    let timer = 0;
    const wh = () => ({ start: st?.workingHours?.start ?? 540, end: st?.workingHours?.end ?? 1050 });
    const props = (i: number): DayProps => ({ d: days[i]!, labels: hourLabels(days, i, C.cols), prefs, wh: wh() });

    const render = () => {
      if (!st) return;
      days = fortnight(st, Date.now(), prefs);
      // Weekend columns are narrower, and shrink to their labels when
      // neither of their days has anything on.
      const quiet = (col: number) => [col, col + C.cols].every((i) => !days[i]!.spans.length && !days[i]!.allDay.length);
      grid.style.gridTemplateColumns = Array.from({ length: C.cols }, (_, col) =>
        !days[col]!.weekend ? "minmax(0,1fr)" : quiet(col) ? `${labelWidth(prefs, wh())}px` : `minmax(0,${C.weekendW}fr)`,
      ).join(" ");
      cells.forEach((el, i) => (el.props = props(i)));
      empty.hidden = days.some((d) => d.spans.length || d.allDay.length);
      if (detail.p) {
        const i = days.findIndex((d) => d.day === detail.p!.d.day);
        detail.update(i < 0 ? undefined : props(i));
      }
    };
    // Redraw on the minute, so "now" and the past move, and a new day rolls in at midnight.
    const tick = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        render();
        tick();
      }, nextTick(Date.now())) as unknown as number;
    };
    tick();

    cells.forEach((el, i) =>
      el.addEventListener("click", () => {
        if (detail.isOpen) return detail.close();
        if (!days[i]) return;
        const b = detail.getBoundingClientRect(), r = el.cv.getBoundingClientRect();
        detail.open(props(i), { x: r.left - b.left, y: r.top - b.top, w: r.width, h: r.height });
      }),
    );
    detail.onTap = (j, at) => detail.p && eventView.open(detail.p, j, at);
    detail.onClose = () => eventView.close();
    eventView.onActivity = () => detail.arm();
    eventView.onClosing = () => {
      detail.sel = -1;
      detail.draw();
    };
    window.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") eventView.isOpen ? eventView.close() : detail.close();
    });

    return {
      update(s: State) {
        st = s;
        render();
      },
      destroy() {
        clearTimeout(timer);
      },
    };
  },
});
