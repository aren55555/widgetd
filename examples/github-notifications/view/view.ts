import { defineWidget } from "@widgetd/sdk";

import { StateSchema, type State } from "./gen/ghnotif/v1/ghnotif_pb.ts";

const KIND: Record<string, string> = { PullRequest: "PR", Issue: "Issue", Release: "Release", Discussion: "Discussion", CheckSuite: "CI" };

defineWidget({
  state: StateSchema,
  mount({ root }) {
    root.innerHTML = `<div class="inbox"><div class="count"></div><ul></ul><div class="more"></div></div>`;
    const count = root.querySelector<HTMLElement>(".count")!;
    const list = root.querySelector<HTMLElement>("ul")!;
    const more = root.querySelector<HTMLElement>(".more")!;
    let unread = 0;

    // Show only the rows that fit whole, and say how many more there are
    // (hidden here, or never sent: the card gets the newest few).
    const fit = () => {
      const rows = [...list.children] as HTMLElement[];
      rows.forEach((r) => (r.hidden = false));
      let shown = 0;
      for (const r of rows) {
        if (r.offsetTop + r.offsetHeight > list.clientHeight) break;
        shown++;
      }
      rows.forEach((r, i) => (r.hidden = i >= shown));
      const rest = unread - shown;
      more.textContent = shown > 0 && rest > 0 ? `+${rest} more` : "";
    };
    new ResizeObserver(fit).observe(list);

    return {
      update(s: State) {
        unread = s.unread;
        count.textContent = s.unread === 0 ? "All caught up" : `${s.unread} unread`;
        count.dataset.zero = String(s.unread === 0);
        list.replaceChildren(
          ...s.items.map((it) => {
            const li = document.createElement("li");
            const kind = document.createElement("span");
            kind.className = "kind";
            kind.textContent = KIND[it.kind] ?? it.kind;
            const title = document.createElement("span");
            title.className = "title";
            title.textContent = it.title;
            const repo = document.createElement("span");
            repo.className = "repo";
            repo.textContent = `${it.repo} · ${it.reason.replace(/_/g, " ")}`;
            li.append(kind, title, repo);
            return li;
          }),
        );
        fit();
      },
    };
  },
});
