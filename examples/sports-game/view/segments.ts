// A score drawn the way a US scoreboard shows it: seven-segment digits,
// lit over the faint shapes of the unlit segments.

const SVG = "http://www.w3.org/2000/svg";

/** The segments, clockwise from the top, then the middle bar. */
type Segment = "a" | "b" | "c" | "d" | "e" | "f" | "g";

const LIT: Record<string, string> = {
  "0": "abcdef",
  "1": "bc",
  "2": "abdeg",
  "3": "abcdg",
  "4": "bcfg",
  "5": "acdfg",
  "6": "acdefg",
  "7": "abc",
  "8": "abcdefg",
  "9": "abcdfg",
  "-": "g",
};

/** The segments a character lights: none for a blank or anything not a digit. */
export function litSegments(ch: string): Set<Segment> {
  return new Set((LIT[ch] ?? "").split("") as Segment[]);
}

/**
 * The digits a score fills, right-aligned like a scoreboard's: at least
 * `width` of them, blanks (all unlit) on the left. "7" → [" ", "7"].
 */
export function digitCells(score: string, width: number): string[] {
  const chars = score.trim().split(""); // a score is ASCII digits
  return [...Array<string>(Math.max(0, width - chars.length)).fill(" "), ...chars];
}

// One digit's geometry, in viewBox units.
const W = 12; // width, between the vertical segments' centres plus their thickness
const H = 22;
const T = 2.4; // segment thickness
const GAP = 0.5; // between segments where they meet
const PITCH = W + 4; // digit to digit

/** A segment as a hexagon along its axis, pointed at both ends. */
function bar(x1: number, y1: number, x2: number, y2: number): string {
  const h = T / 2;
  const pts =
    y1 === y2
      ? [
          [x1, y1],
          [x1 + h, y1 - h],
          [x2 - h, y1 - h],
          [x2, y1],
          [x2 - h, y1 + h],
          [x1 + h, y1 + h],
        ]
      : [
          [x1, y1],
          [x1 + h, y1 + h],
          [x1 + h, y2 - h],
          [x1, y2],
          [x1 - h, y2 - h],
          [x1 - h, y1 + h],
        ];
  return pts.map(([x, y]) => `${x.toFixed(2)},${y.toFixed(2)}`).join(" ");
}

const L = T / 2;
const R = W - T / 2;
const TOP = T / 2;
const MID = H / 2;
const BOT = H - T / 2;
const SEGMENTS: Record<Segment, string> = {
  a: bar(L + GAP, TOP, R - GAP, TOP),
  b: bar(R, TOP + GAP, R, MID - GAP),
  c: bar(R, MID + GAP, R, BOT - GAP),
  d: bar(L + GAP, BOT, R - GAP, BOT),
  e: bar(L, MID + GAP, L, BOT - GAP),
  f: bar(L, TOP + GAP, L, MID - GAP),
  g: bar(L + GAP, MID, R - GAP, MID),
};

/**
 * A score as seven-segment digits in an <svg>, sized by its font size
 * (1em tall). Lit segments have the class "on". Anything but digits and
 * "-" draws as unlit, so the label carries the score as text too.
 */
export function sevenSegment(score: string, width = 2): SVGSVGElement {
  const cells = digitCells(score, width);
  const svg = document.createElementNS(SVG, "svg");
  const vw = cells.length * PITCH - (PITCH - W);
  svg.setAttribute("viewBox", `0 0 ${vw} ${H}`);
  svg.setAttribute("class", "segments");
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", score);
  svg.style.height = "1em";
  svg.style.width = `${vw / H}em`;
  cells.forEach((ch, i) => {
    const lit = litSegments(ch);
    const g = document.createElementNS(SVG, "g");
    g.setAttribute("transform", `translate(${i * PITCH} 0)`);
    for (const [name, points] of Object.entries(SEGMENTS) as Array<[Segment, string]>) {
      const p = document.createElementNS(SVG, "polygon");
      p.setAttribute("points", points);
      if (lit.has(name)) p.setAttribute("class", "on");
      g.append(p);
    }
    svg.append(g);
  });
  return svg;
}
