import { defineWidget } from "@widgetd/sdk";
import {
  CircleCheck,
  CircleDashed,
  CircleDot,
  CircleX,
  GitMerge,
  GitPullRequest,
  GitPullRequestDraft,
  icon,
  MessageSquare,
  type IconNode,
} from "@widgetd/sdk/icons";

import {
  Checks,
  Filter,
  LocalSchema,
  Review,
  StateSchema,
  type Issue,
  type PullRequest,
  type State,
} from "./gen/ghnotif/v1/ghnotif_pb.ts";

/** "now", "5m", "3h", "2d", "6w": short enough for a meta line. */
function age(ms: number, now: number): string {
  const m = Math.floor((now - ms) / 60_000);
  if (m < 1) return "now";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  return d < 14 ? `${d}d` : `${Math.floor(d / 7)}w`;
}

function el(tag: string, cls: string, text = ""): HTMLElement {
  const e = document.createElement(tag);
  e.className = cls;
  e.textContent = text;
  return e;
}

/** An age that the minute tick keeps current. */
function stamp(ms: bigint): HTMLElement {
  const e = el("span", "age", ms ? age(Number(ms), Date.now()) : "");
  e.dataset.ms = String(ms);
  return e;
}

/** A small status mark: an icon and a word, coloured by tone. */
function chip(node: IconNode, text: string, tone: "ok" | "bad" | "wait" | "dim"): HTMLElement {
  const c = el("span", `chip ${tone}`);
  c.append(icon(node, { size: 12 }), el("span", "", text));
  return c;
}

/** One row: an icon, a title over up to two lines, and a meta line. */
function row(
  node: IconNode,
  label: string,
  tone: string,
  title: string,
  meta: Array<HTMLElement | string>,
): HTMLElement {
  const li = el("li", "");
  const ic = icon(node, { size: 16, label });
  ic.classList.add("kind", tone);
  const m = el("span", "meta");
  for (const part of meta) m.append(typeof part === "string" ? el("span", "dot", part) : part);
  li.append(ic, el("span", "title", title), m);
  return li;
}

function prIcon(pr: PullRequest): [IconNode, string, string] {
  return pr.draft ? [GitPullRequestDraft, "Draft pull request", "dim"] : [GitPullRequest, "Pull request", "open"];
}

/** The status of one of the account's own PRs: checks, review, conflicts. */
function status(pr: PullRequest): HTMLElement[] {
  const out: HTMLElement[] = [];
  if (pr.draft) out.push(chip(GitPullRequestDraft, "draft", "dim"));
  switch (pr.checks) {
    case Checks.PASSING:
      out.push(chip(CircleCheck, "checks", "ok"));
      break;
    case Checks.FAILING:
      out.push(chip(CircleX, "checks", "bad"));
      break;
    case Checks.PENDING:
      out.push(chip(CircleDashed, "checks", "wait"));
      break;
    case Checks.UNSPECIFIED: // no CI: nothing to say
      break;
  }
  switch (pr.review) {
    case Review.APPROVED:
      out.push(chip(CircleCheck, "approved", "ok"));
      break;
    case Review.CHANGES_REQUESTED:
      out.push(chip(CircleX, "changes", "bad"));
      break;
    case Review.COMMENTED:
      out.push(chip(MessageSquare, "comments", "wait"));
      break;
    default:
      if (!pr.draft) out.push(chip(CircleDashed, "review", "dim"));
  }
  if (pr.conflicts) out.push(chip(GitMerge, "conflicts", "bad"));
  return out;
}

/**
 * "owner/repo#142", each part a step lighter in one hue: one hue for repos
 * a user owns, another for an org's. The repo gives way first; the number
 * always shows.
 */
function ref(it: { repo: string; number: number; org: boolean }): HTMLElement {
  const r = el("span", "ref");
  r.dataset.owner = it.org ? "org" : "user";
  const slash = it.repo.indexOf("/");
  const repo = el("span", "repo");
  if (slash < 0) repo.append(el("span", "name", it.repo));
  else
    repo.append(
      el("span", "owner", it.repo.slice(0, slash)),
      el("span", "slash", "/"),
      el("span", "name", it.repo.slice(slash + 1)),
    );
  r.append(repo);
  if (it.number > 0) r.append(el("span", "num", `#${it.number}`));
  return r;
}

function mineRow(pr: PullRequest): HTMLElement {
  const [node, label, tone] = prIcon(pr);
  const li = row(node, label, tone, pr.title, [ref(pr), stamp(pr.updatedUnixMs)]);
  const s = el("span", "status");
  s.append(...status(pr));
  li.append(s);
  return li;
}

function reviewRow(pr: PullRequest): HTMLElement {
  const [node, label, tone] = prIcon(pr);
  return row(node, label, tone, pr.title, [ref(pr), `@${pr.author}`, stamp(pr.createdUnixMs)]);
}

function issueRow(it: Issue): HTMLElement {
  return row(CircleDot, "Issue", "open", it.title, [ref(it), it.reason.replace(/_/g, " "), stamp(it.updatedUnixMs)]);
}

type Shown = Exclude<Filter, Filter.UNSPECIFIED>;

const FILTERS: Record<Shown, { name: string; empty: string; rows: (s: State) => HTMLElement[] }> = {
  [Filter.MINE]: { name: "Your PRs", empty: "No open pull requests", rows: (s) => s.mine.map(mineRow) },
  [Filter.REVIEWS]: { name: "To review", empty: "Nothing waiting on you", rows: (s) => s.reviews.map(reviewRow) },
  [Filter.ISSUES]: { name: "Issues", empty: "No new issue activity", rows: (s) => s.issues.map(issueRow) },
};
const ORDER: Shown[] = [Filter.MINE, Filter.REVIEWS, Filter.ISSUES];

function count(s: State, f: Shown): number {
  return f === Filter.MINE ? s.mine.length : f === Filter.REVIEWS ? s.reviews.length : s.issues.length;
}

defineWidget({
  state: StateSchema,
  local: LocalSchema,
  mount({ root, local }) {
    root.innerHTML = `<div class="card"><nav></nav><div class="scroll"><ul></ul></div></div>`;
    const nav = root.querySelector<HTMLElement>("nav")!;
    const scroll = root.querySelector<HTMLElement>(".scroll")!;
    const list = root.querySelector<HTMLElement>("ul")!;
    let last: State | undefined;
    const current = (): Shown => (local.value.filter === Filter.UNSPECIFIED ? Filter.MINE : local.value.filter);

    // One button per list, each with its count; the screen remembers the pick.
    const buttons = ORDER.map((f) => {
      const b = document.createElement("button");
      b.type = "button";
      b.append(el("span", "", FILTERS[f].name), el("span", "n", ""));
      b.addEventListener("click", () => {
        if (f === current()) return;
        // If the board won't keep it, the pick still holds until a reload.
        void local.set({ ...local.value, filter: f }).catch(() => undefined);
        scroll.scrollTop = 0;
        draw();
      });
      nav.append(b);
      return b;
    });

    // Fade the edge the card scrolls past, so a cut-off row reads as "more".
    const edges = () => {
      scroll.dataset.above = String(scroll.scrollTop > 1);
      scroll.dataset.below = String(scroll.scrollTop + scroll.clientHeight < scroll.scrollHeight - 1);
    };
    scroll.addEventListener("scroll", edges, { passive: true });
    new ResizeObserver(edges).observe(scroll);

    // Keep the ages current between snapshots, without redrawing the rows
    // (which would lose the scroll position).
    setInterval(() => {
      const now = Date.now();
      for (const e of scroll.querySelectorAll<HTMLElement>(".age")) {
        const ms = Number(e.dataset.ms);
        if (ms) e.textContent = age(ms, now);
      }
    }, 60_000);

    function draw() {
      const s = last;
      if (!s) return;
      const f = current();
      ORDER.forEach((g, i) => {
        const b = buttons[i];
        const n = count(s, g);
        b.setAttribute("aria-pressed", String(g === f));
        b.dataset.zero = String(n === 0);
        b.querySelector(".n")!.textContent = String(n);
      });
      const rows = FILTERS[f].rows(s);
      list.replaceChildren(...(rows.length ? rows : [el("li", "empty", FILTERS[f].empty)]));
      edges();
    }

    return {
      update(s: State) {
        last = s;
        draw();
      },
    };
  },
});
