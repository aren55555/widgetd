import { defineWidget } from "@widgetd/sdk";

import { StateSchema, State_Phase, type State, type Team } from "./gen/sportsgame/v1/sportsgame_pb.ts";
import { startLabel, teamColor } from "./game.ts";

defineWidget({
  state: StateSchema,
  mount({ root }) {
    root.innerHTML = `<div class="game"><div class="teams"></div><div class="status"></div></div>`;
    const teams = root.querySelector<HTMLElement>(".teams")!;
    const status = root.querySelector<HTMLElement>(".status")!;
    let current: State | undefined;

    const render = () => {
      const s = current;
      if (!s?.home || !s.away) return;
      const showScore = s.phase !== State_Phase.PRE;
      teams.replaceChildren(row(s.away, s.mine, showScore, s.phase), row(s.home, s.mine, showScore, s.phase));
      status.dataset.phase = State_Phase[s.phase].toLowerCase();
      status.textContent =
        s.phase === State_Phase.PRE
          ? [startLabel(Number(s.startUnixMs), Date.now()), s.broadcast].filter(Boolean).join(" · ")
          : [s.detail, s.phase === State_Phase.LIVE ? s.broadcast : ""].filter(Boolean).join(" · ");
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

function row(t: Team, mine: string, showScore: boolean, phase: State_Phase): HTMLElement {
  const r = document.createElement("div");
  r.className = "team";
  if (t.abbreviation === mine) r.classList.add("mine");
  if (phase === State_Phase.FINAL && t.winner) r.classList.add("winner");
  const color = teamColor(t.color);
  if (color) r.style.setProperty("--team", color);

  const logo = document.createElement("img");
  logo.alt = "";
  if (t.logo) logo.src = t.logo;
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = t.shortName || t.name;
  const record = document.createElement("span");
  record.className = "record";
  record.textContent = t.record;
  const score = document.createElement("span");
  score.className = "score";
  score.textContent = showScore ? t.score : "";
  r.append(logo, name, record, score);
  return r;
}
