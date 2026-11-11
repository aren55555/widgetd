import { defineWidget } from "@widgetd/sdk";
import { icon, Star } from "@widgetd/sdk/icons";

import { StateSchema, State_Phase, type State, type Team } from "./gen/sportsgame/v1/sportsgame_pb.ts";
import { Chart } from "./chart.ts";
import { lineColors, startLabel, teamColor, TYPICAL_MS } from "./game.ts";

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
          scoredUnixMs: s.scoredUnixMs.map(Number),
          live,
        },
      );
      game.classList.toggle("charted", !!m);

      const row = (t: Team, color: string, prices: number[] | undefined): HTMLElement =>
        teamRow(t, {
          mine: t.abbreviation === s.mine,
          winner: s.phase === State_Phase.FINAL && t.winner,
          score: pre ? "" : t.score,
          record: pre ? t.record : "", // once it starts, the score and the market say more
          swatch: m ? color : undefined,
          pct: live && prices?.length ? `${prices[prices.length - 1]}%` : "",
        });
      teams.replaceChildren(row(s.away, colors.away, m?.away), row(s.home, colors.home, m?.home));

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

function teamRow(t: Team, o: RowOpts): HTMLElement {
  const r = document.createElement("div");
  r.className = "team";
  r.classList.toggle("mine", o.mine);
  r.classList.toggle("winner", o.winner);
  const color = teamColor(t.color);
  if (color) r.style.setProperty("--team", color);
  if (o.swatch) r.style.setProperty("--line", o.swatch);

  const logo = document.createElement("img");
  logo.alt = "";
  if (t.logo) logo.src = t.logo;
  // The legend: the team's line colour, beside its name.
  const swatch = span("swatch");
  const who = span("who");
  who.append(span("name", t.shortName || t.name));
  if (o.mine) who.append(icon(Star, { size: "sm", label: "Following" }));
  if (o.record) who.append(span("record", o.record));
  r.append(logo, swatch, who, span("pct", o.pct), span("score", o.score));
  return r;
}

function span(cls: string, text = ""): HTMLElement {
  const el = document.createElement("span");
  el.className = cls;
  el.textContent = text;
  return el;
}
