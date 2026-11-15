// Split-flap text, like a station's departure board. Digits sit on flap
// tiles; ":" and "." sit between them. A tile that changes turns through
// every character between the old one and the new (drumSteps), each turn a
// flip: the top half falls away, darkening, and the new bottom half swings
// down, brightening, over the old one, which is in shadow while covered.
// Tiles start in turn, left to right. A field that is new, or changes shape
// ("9" to "10"), starts from blank flaps.

import { drumSteps } from "./model.ts";

const SEPARATORS = new Set([":", "."]);
// A field is clock digits and separators: ASCII, so one character per code unit.
const chars = (t: string): string[] => Array.from(t);
/** Each half of one flip, in ms. */
export const FLIP_MS = 55;
/** How far apart, in ms, neighbouring tiles start. */
const STAGGER_MS = 45;

interface Tile extends HTMLElement {
  flipping?: ReturnType<typeof setTimeout>;
  landing?: ReturnType<typeof setTimeout>;
}

/** Shows text in el, a split-flap field; with animate, the changes flip. */
export function setFlaps(el: HTMLElement, text: string, animate = true): void {
  const prev = el.dataset.v;
  if (prev === text) return;
  el.dataset.v = text;
  const shape = (t: string) =>
    chars(t)
      .map((c) => (SEPARATORS.has(c) ? c : "#"))
      .join("");
  let from = prev ?? "";
  if (prev === undefined || shape(prev) !== shape(text)) {
    el.replaceChildren(...chars(text).map((c) => (SEPARATORS.has(c) ? separator(c) : tile(" "))));
    from = chars(text)
      .map((c) => (SEPARATORS.has(c) ? c : " "))
      .join("");
  }
  chars(text).forEach((c, i) => {
    if (SEPARATORS.has(c)) return;
    const t = el.children[i] as Tile;
    if (animate) flipTo(t, c, i * STAGGER_MS);
    else if (c !== from[i]) set(t, c);
  });
}

function separator(c: string): HTMLElement {
  const e = document.createElement("span");
  e.className = "sep";
  e.textContent = c;
  return e;
}

// A tile: a static top half and bottom half, each showing half of the same
// full-height face. A flip lays two moving leaves over them.
function tile(c: string): Tile {
  const t = document.createElement("span") as Tile;
  t.className = "flap";
  t.append(half("t"), half("b"));
  set(t, c);
  return t;
}

function half(which: "t" | "b", c?: string): HTMLElement {
  const h = document.createElement("span");
  h.className = `h ${which}`;
  const face = document.createElement("span");
  face.className = "face";
  const g = document.createElement("span");
  g.className = "g";
  if (c !== undefined) g.textContent = c;
  face.append(g);
  h.append(face);
  return h;
}

function face(t: Tile, which: "t" | "b", c: string): void {
  const g = t.querySelector(`:scope > .${which}:not(.leaf) .g`);
  if (g) g.textContent = c;
}

function set(t: Tile, c: string): void {
  clearTimeout(t.flipping);
  clearTimeout(t.landing);
  t.querySelectorAll(".leaf").forEach((l) => {
    l.remove();
  });
  t.classList.remove("flipping");
  face(t, "t", c);
  face(t, "b", c);
  t.dataset.c = c;
}

function flipTo(t: Tile, c: string, delay: number): void {
  const steps = drumSteps(t.dataset.c ?? " ", c);
  clearTimeout(t.flipping);
  if (!steps.length) return;
  let k = 0;
  const next = () => {
    flip(t, steps[k] ?? c);
    if (++k < steps.length) t.flipping = setTimeout(next, 2 * FLIP_MS);
  };
  t.flipping = setTimeout(next, delay);
}

function flip(t: Tile, n: string): void {
  const o = t.dataset.c ?? " ";
  t.dataset.c = n;
  t.querySelectorAll(".leaf").forEach((l) => {
    l.remove();
  });
  face(t, "t", n);
  const top = half("t", o);
  const bottom = half("b", n);
  top.classList.add("leaf");
  bottom.classList.add("leaf");
  top.style.animation = `flap-top ${FLIP_MS}ms ease-in forwards`;
  bottom.style.animation = `flap-bottom ${FLIP_MS}ms ease-out ${FLIP_MS}ms both`;
  t.classList.add("flipping");
  t.append(top, bottom);
  clearTimeout(t.landing);
  t.landing = setTimeout(() => {
    face(t, "b", n);
    top.remove();
    bottom.remove();
    t.classList.remove("flipping");
  }, 2 * FLIP_MS);
}
