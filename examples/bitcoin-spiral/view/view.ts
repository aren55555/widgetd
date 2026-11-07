import { defineWidget } from "@widgetd/sdk";
import { create } from "@bufbuild/protobuf";

import { DeltaSchema, StateSchema, type State } from "./gen/bitcoinspiral/v1/bitcoinspiral_pb.ts";
import { cycleOf, dayToMs, formatChange, formatPrice, logScale, point, rings, HOLE, type Scale } from "./spiral.ts";

defineWidget({
  state: StateSchema,
  delta: DeltaSchema,

  apply(state, delta) {
    switch (delta.change.case) {
      case "quote":
        state.latest = delta.change.value;
        break;
      case "close": {
        const c = delta.change.value;
        const last = state.daily.at(-1);
        if (last && last.day === c.day) state.daily[state.daily.length - 1] = c;
        else if (!last || last.day < c.day) state.daily.push(c);
        break;
      }
    }
    return state;
  },

  mount({ root }) {
    root.innerHTML = `
      <div class="wrap">
        <canvas></canvas>
        <div class="readout">
          <div class="price">—</div>
          <div class="change"></div>
          <div class="cycle"></div>
        </div>
      </div>`;
    const canvas = root.querySelector("canvas")!;
    const price = root.querySelector<HTMLElement>(".price")!;
    const change = root.querySelector<HTMLElement>(".change")!;
    const cycle = root.querySelector<HTMLElement>(".cycle")!;
    let current = create(StateSchema);

    const redraw = () => {
      draw(canvas, current);
      readout(current, price, change, cycle);
    };
    const ro = new ResizeObserver(redraw);
    ro.observe(canvas);

    return {
      update(state) {
        current = state;
        redraw();
      },
      destroy: () => ro.disconnect(),
    };
  },
});

function css(name: string, fallback: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

function readout(s: State, price: HTMLElement, change: HTMLElement, cycle: HTMLElement) {
  const latest = s.latest?.price ?? s.daily.at(-1)?.close;
  if (latest === undefined) {
    price.textContent = "loading…";
    return;
  }
  price.textContent = formatPrice(latest, s.currency);
  const prev = s.daily.at(-1)?.close;
  if (s.latest && prev) {
    change.textContent = `${formatChange(prev, s.latest.price)} today`;
    change.dataset.dir = s.latest.price >= prev ? "up" : "down";
  }
  const c = cycleOf(Date.now());
  cycle.textContent = `cycle ${c.index} · ${Math.round(c.frac * 100)}% to next halving`;
}

function draw(canvas: HTMLCanvasElement, s: State) {
  const dpr = window.devicePixelRatio || 1;
  const { width, height } = canvas.getBoundingClientRect();
  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(height * dpr);
  const g = canvas.getContext("2d");
  if (!g || !s.daily.length) return;
  g.scale(dpr, dpr);

  const size = Math.min(width, height);
  const R = size / 2 - 6;
  g.translate((width - size) / 2 + 6, (height - size) / 2 + 6);

  const prices = s.daily.map((d) => d.close);
  if (s.latest) prices.push(s.latest.price);
  const scale = logScale(Math.min(...prices), Math.max(...prices));

  drawGrid(g, scale, R);

  // One path per cycle, older cycles fainter, so the turns read apart.
  const ink = css("--wd-accent", "#60a5fa");
  const byCycle = new Map<number, { x: number; y: number }[]>();
  for (const d of s.daily) {
    const p = point(dayToMs(d.day), d.close, scale, R);
    (byCycle.get(p.cycle) ?? byCycle.set(p.cycle, []).get(p.cycle)!).push(p);
  }
  const newest = Math.max(...byCycle.keys());
  for (const [c, pts] of byCycle) {
    g.globalAlpha = Math.max(0.25, 1 - (newest - c) * 0.25);
    g.strokeStyle = ink;
    g.lineWidth = c === newest ? 2 : 1.25;
    g.beginPath();
    pts.forEach((p, i) => (i ? g.lineTo(p.x, p.y) : g.moveTo(p.x, p.y)));
    g.stroke();
  }
  g.globalAlpha = 1;

  if (s.latest) {
    const p = point(Number(s.latest.unixMs), s.latest.price, scale, R);
    g.fillStyle = css("--wd-up", "#4ade80");
    g.beginPath();
    g.arc(p.x, p.y, 4, 0, 2 * Math.PI);
    g.fill();
  }
  drawLabels(g, scale, R, s.currency);
}

function drawGrid(g: CanvasRenderingContext2D, scale: Scale, R: number) {
  g.strokeStyle = css("--wd-edge", "#27272a");
  g.lineWidth = 1;
  for (const r of rings(scale).map((v) => R * (HOLE + (1 - HOLE) * ((Math.log10(v) - scale.lo) / (scale.hi - scale.lo))))) {
    g.beginPath();
    g.arc(R, R, r, 0, 2 * Math.PI);
    g.stroke();
  }
  // The halving spoke.
  g.beginPath();
  g.moveTo(R, R - R * HOLE);
  g.lineTo(R, 0);
  g.stroke();
}

/** Ring labels go on last, on a backing, so the line never runs through them. */
function drawLabels(g: CanvasRenderingContext2D, scale: Scale, R: number, currency: number) {
  const bg = css("--wd-surface", "#18181b");
  const faint = css("--wd-ink-faint", "#71717a");
  g.font = `10px ${css("--wd-font-mono", "ui-monospace, monospace")}`;
  g.textAlign = "center";
  g.textBaseline = "middle";
  for (const v of rings(scale)) {
    const r = R * (HOLE + (1 - HOLE) * ((Math.log10(v) - scale.lo) / (scale.hi - scale.lo)));
    const text = formatPrice(v, currency, true);
    const w = g.measureText(text).width + 6;
    g.fillStyle = bg;
    g.fillRect(R - w / 2, R - r - 6, w, 12);
    g.fillStyle = faint;
    g.fillText(text, R, R - r);
  }
}
