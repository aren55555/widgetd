import { defineWidget } from "@widgetd/sdk";

import { StateSchema, type State } from "./gen/ghnotif/v1/ghnotif_pb.ts";

const KIND: Record<string, string> = { PullRequest: "PR", Issue: "Issue", Release: "Release", Discussion: "Discussion", CheckSuite: "CI" };

defineWidget({
  state: StateSchema,
  mount({ root }) {
    root.innerHTML = `<div class="inbox"><div class="count"></div><ul></ul></div>`;
    const count = root.querySelector<HTMLElement>(".count")!;
    const list = root.querySelector<HTMLElement>("ul")!;
    return {
      update(s: State) {
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
      },
    };
  },
});
