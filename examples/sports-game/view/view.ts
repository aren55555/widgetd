import { defineWidget } from "@widgetd/sdk";
import { icon, Star } from "@widgetd/sdk/icons";

import { StateSchema, State_Phase, type State, type Team } from "./gen/sportsgame/v1/sportsgame_pb.ts";
import { Chart } from "./chart.ts";
import { creditLine, lineColors, startLabel, teamColor, TYPICAL_MS } from "./game.ts";

defineWidget({
  state: StateSchema,
  mount({ root, prefs }) {
    root.innerHTML = `<div class="card"><div class="game"><div class="teams"></div><div class="status"></div></div></div>`;
    const card = root.querySelector<HTMLElement>(".card")!;
    const game = root.querySelector<HTMLElement>(".game")!;
    const teams = root.querySelector<HTMLElement>(".teams")!;
    const status = root.querySelector<HTMLElement>(".status")!;
    const chart = new Chart(card, prefs);
    card.prepend(chart.el);
    // Built once and updated in place: a new <img> on every update would
    // blank the logos for a moment each time (every state, and every minute).
    const awayRow = new TeamRow();
    const homeRow = new TeamRow();
    teams.append(awayRow.el, homeRow.el);
    let current: State | undefined;

    const render = () => {
      const s = current;
      if (!s?.home || !s.away) return;
      const pre = s.phase === State_Phase.PRE;
      const live = s.phase === State_Phase.LIVE;
      const m = !pre && s.market?.away.length ? s.market : undefined;
      const colors = lineColors(s.away, s.home);

      chart.set(
        m && {
          market: m,
          series: [
            { key: "away", label: s.away.abbreviation, color: colors.away, prices: m.away },
            { key: "home", label: s.home.abbreviation, color: colors.home, prices: m.home },
          ],
          typicalMs: TYPICAL_MS[s.league] ?? 3 * 3_600_000,
          marks: s.scores.map((sc) => ({
            unixMs: Number(sc.unixMs),
            color: sc.home ? colors.home : colors.away,
            credit: creditLine(sc.scorer, sc.assists),
          })),
          live,
        },
      );
      game.classList.toggle("charted", !!m);

      const opts = (t: Team, color: string, prices: number[] | undefined): RowOpts => ({
        mine: t.abbreviation === s.mine,
        winner: s.phase === State_Phase.FINAL && t.winner,
        score: pre ? "" : t.score,
        record: pre ? t.record : "", // once it starts, the score and the market say more
        swatch: m ? color : undefined,
        pct: live && prices?.length ? `${prices[prices.length - 1]}%` : "",
      });
      awayRow.update(s.away, opts(s.away, colors.away, m?.away));
      homeRow.update(s.home, opts(s.home, colors.home, m?.home));

      status.dataset.phase = State_Phase[s.phase].toLowerCase();
      status.textContent = pre
        ? [startLabel(Number(s.startUnixMs), Date.now()), s.broadcast].filter(Boolean).join(" · ")
        : [s.detail, live ? s.broadcast : ""].filter(Boolean).join(" · ");
    };
    // "Tonight 7:00 PM" turns into "Today …" at midnight without a new state.
    const timer = setInterval(render, 60_000);

    return {
      update(s) {
        current = s;
        render();
      },
      destroy: () => clearInterval(timer),
    };
  },
});

interface RowOpts {
  mine: boolean;
  winner: boolean;
  score: string;
  record: string;
  swatch?: string;
  pct: string;
}

/** A team's row: logo · legend swatch · name ★ record · win % · score. */
class TeamRow {
  readonly el = document.createElement("div");
  private logo = document.createElement("img");
  private name = span("name");
  // A superscript: smaller than the "sm" step, set high against the name.
  private star = icon(Star, { size: 10, label: "Following" });
  private record = span("record");
  private pct = span("pct");
  private score = span("score");

  constructor() {
    this.el.className = "team";
    this.logo.alt = "";
    const who = span("who");
    who.append(this.name, this.star, this.record);
    // The legend: the team's line colour, beside its name.
    this.el.append(this.logo, span("swatch"), who, this.pct, this.score);
  }

  update(t: Team, o: RowOpts): void {
    const r = this.el;
    r.classList.toggle("mine", o.mine);
    r.classList.toggle("winner", o.winner);
    setVar(r, "--team", teamColor(t.color));
    setVar(r, "--line", o.swatch);
    // Only a different logo reloads the image.
    if (t.logo && this.logo.getAttribute("src") !== t.logo) this.logo.src = t.logo;
    if (!t.logo) this.logo.removeAttribute("src");
    this.name.textContent = t.shortName || t.name;
    this.star.style.display = o.mine ? "" : "none";
    this.record.textContent = o.record;
    this.record.hidden = !o.record;
    this.pct.textContent = o.pct;
    this.score.textContent = o.score;
  }
}

function setVar(el: HTMLElement, name: string, value: string | undefined): void {
  if (value) el.style.setProperty(name, value);
  else el.style.removeProperty(name);
}

function span(cls: string, text = ""): HTMLElement {
  const el = document.createElement("span");
  el.className = cls;
  el.textContent = text;
  return el;
}
