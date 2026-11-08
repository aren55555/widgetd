// The admin pages a phone opens from a QR code:
//
//   <base>enroll/<token>   create the first passkey
//   <base>pair/<id>        approve a screen
//   <base>configure/<t>    set up or change one widget instance (QR ticket)
//   <base>edit/<id>        the same, from the admin page
//   <base>admin            instances, network access, screens, passkeys
//
// Plain DOM, no framework: a handful of small forms. The settings form is
// generated from the widget's schema (lib/form.ts).

import { ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { timestampDate } from "@bufbuild/protobuf/wkt";

import { create, createFileRegistry, fromBinary, toBinary, type DescMessage, type Message } from "@bufbuild/protobuf";
import { FileDescriptorSetSchema } from "@bufbuild/protobuf/wkt";

import { AuthService, Role } from "./gen/widgetd/v1/auth_pb.ts";
import { WidgetAdminService, type WidgetInfo } from "./gen/widgetd/v1/admin_pb.ts";
import { InstanceStatus_State } from "./gen/widgetd/v1/stream_pb.ts";
import { buildForm, sameChoices, type Choices } from "./lib/form.ts";

const base = new URL(".", import.meta.url).pathname; // the bundle sits at <base>admin.js
const transport = createConnectTransport({ baseUrl: location.origin, useBinaryFormat: true });
const auth = createClient(AuthService, transport);
const admin = createClient(WidgetAdminService, transport);
const app = document.getElementById("app")!;

type Child = Node | string;
function h(tag: string, props: Record<string, unknown> = {}, ...children: Child[]): HTMLElement {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v as EventListener);
    else if (v !== undefined && v !== false) el.setAttribute(k, v === true ? "" : String(v));
  }
  el.append(...children);
  return el;
}

function page(title: string, ...children: Child[]) {
  app.replaceChildren(h("h1", {}, title), ...children);
}

function errorText(e: unknown): string {
  if (e instanceof ConnectError) return e.rawMessage;
  if (e instanceof DOMException && e.name === "NotAllowedError") return "Cancelled.";
  return e instanceof Error ? e.message : String(e);
}

function button(label: string, action: (status: HTMLElement) => Promise<void>) {
  const status = h("p", { class: "status", role: "status" });
  const btn = h("button", {}, label) as HTMLButtonElement;
  btn.addEventListener("click", async () => {
    btn.disabled = true;
    status.textContent = "";
    try {
      await action(status);
    } catch (e) {
      status.textContent = errorText(e);
      status.className = "status error";
    } finally {
      btn.disabled = false;
    }
  });
  return [btn, status] as const;
}

if (!window.isSecureContext || !window.PublicKeyCredential) {
  page("Can't use passkeys here", h("p", {}, "Open this page over HTTPS, in a browser that supports passkeys."));
} else {
  void route();
}

async function route() {
  const rest = location.pathname.slice(base.length);
  const [section, arg] = rest.split("/");
  try {
    if (section === "enroll") return await enroll(arg ?? "");
    if (section === "pair") return await approve(arg ?? "");
    if (section === "configure") return await configure({ ticket: arg ?? "" });
    if (section === "edit") return await configure({ instanceId: decodeURIComponent(arg ?? "") });
    return await adminHome();
  } catch (e) {
    page("Something went wrong", h("p", { class: "error" }, errorText(e)));
  }
}

// ---- passkeys ----

async function createPasskey(setupToken: string, name: string) {
  const begin = await auth.beginEnroll({ setupToken });
  const options = PublicKeyCredential.parseCreationOptionsFromJSON(JSON.parse(begin.optionsJson));
  const cred = (await navigator.credentials.create({ publicKey: options })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was created.");
  await auth.finishEnroll({ ceremonyId: begin.ceremonyId, credentialJson: JSON.stringify(cred.toJSON()), name });
}

async function signIn() {
  const begin = await auth.beginLogin({});
  const options = PublicKeyCredential.parseRequestOptionsFromJSON(JSON.parse(begin.optionsJson));
  const cred = (await navigator.credentials.get({ publicKey: options })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was chosen.");
  await auth.finishLogin({ ceremonyId: begin.ceremonyId, credentialJson: JSON.stringify(cred.toJSON()) });
}

/** Resolve once the caller is an admin, offering sign-in if not. */
async function requireAdmin(next: () => Promise<void>) {
  const s = await auth.getSession({});
  if (s.role === Role.ADMIN) return next();
  const [btn, status] = button("Sign in with a passkey", async () => {
    await signIn();
    await next();
  });
  page("Sign in", h("p", {}, "This needs an admin. Sign in with your passkey to continue."), btn, status);
}

async function enroll(token: string) {
  const s = await auth.getSession({});
  if (!s.needsEnrollment) {
    page("Already set up", h("p", {}, "An admin passkey exists. "), h("a", { href: `${base}admin` }, "Go to admin"));
    return;
  }
  const name = h("input", { value: deviceGuess(navigator.userAgent), maxlength: 64, "aria-label": "Passkey name" }) as HTMLInputElement;
  const [btn, status] = button("Create passkey", async (st) => {
    await createPasskey(token, name.value);
    st.textContent = "Done. This phone is now an admin.";
    location.replace(`${base}admin`);
  });
  page(
    "Set up widgetd",
    h("p", {}, "Create a passkey on this phone. It's how you'll approve screens and change settings from now on."),
    h("label", {}, "Name ", name),
    btn,
    status,
  );
}

// ---- pairing ----

async function approve(pairingId: string) {
  await requireAdmin(async () => {
    const p = await auth.getPairing({ pairingId });
    const name = h("input", { value: deviceGuess(p.userAgent), maxlength: 64, "aria-label": "Screen name" }) as HTMLInputElement;
    const [btn, status] = button("Approve this screen", async (st) => {
      await auth.approvePairing({ pairingId, deviceName: name.value.trim() });
      page("Approved", h("p", {}, `${name.value.trim()} can now show widgets.`), h("a", { href: `${base}admin` }, "Paired screens"));
      void st;
    });
    page(
      "Pair a screen",
      h("p", {}, "Check the screen shows this code:"),
      h("p", { class: "code" }, p.code),
      h("p", { class: "muted" }, p.userAgent || "unknown browser"),
      h("label", {}, "Name ", name),
      btn,
      status,
    );
  });
}

// ---- configure an instance ----

/** A QR ticket from a screen, or an instance id from the admin page. */
type ConfigTarget = { ticket: string } | { instanceId: string };

async function configure(target: ConfigTarget) {
  await requireAdmin(async () => {
    const form = await admin.getConfigForm(target);
    const w = form.widget!;
    const label = h("input", { value: form.label, maxlength: 64, placeholder: w.title, "aria-label": "Name" }) as HTMLInputElement;
    const fields = h("div");
    let validate: (server?: { field: string; message: string }[]) => boolean = () => true;
    let msg: Message | undefined;
    let desc: DescMessage | undefined;
    if (form.configType) {
      const registry = createFileRegistry(fromBinary(FileDescriptorSetSchema, form.schema));
      desc = registry.getMessage(form.configType);
      if (!desc) throw new Error(`the schema has no ${form.configType}`);
      msg = form.config.length ? fromBinary(desc, form.config) : create(desc);
      const d = desc;
      const m = msg;
      // The widget may offer pick-lists for some fields (a league's teams),
      // which depend on what's been picked so far: ask again after each
      // edit settles, and redraw only when the lists change.
      let choices: Choices = new Map();
      let asked = 0;
      let timer: ReturnType<typeof setTimeout> | undefined;
      const draw = () => ({ validate } = buildForm(fields, d, m, edited, choices));
      const refreshChoices = async () => {
        const mine = ++asked;
        try {
          const res = await admin.getConfigChoices({ ...target, config: toBinary(d, m) });
          if (mine !== asked) return; // a newer edit already asked
          const next: Choices = new Map(res.fields.map((f) => [f.field, f.choices.map((c) => ({ value: c.value, label: c.label }))]));
          if (!sameChoices(choices, next)) {
            choices = next;
            draw();
          }
        } catch {
          // Choices are a convenience: without them, fields stay free text.
        }
      };
      function edited() {
        validate();
        clearTimeout(timer);
        timer = setTimeout(refreshChoices, 400);
      }
      draw();
      void refreshChoices();
    } else {
      fields.append(h("p", { class: "muted" }, "This widget has no settings."));
    }
    const [save, status] = button(form.status?.state === InstanceStatus_State.NEEDS_CONFIG ? "Add to screens" : "Save", async (st) => {
      if (!validate()) {
        st.textContent = "Fix the highlighted fields first.";
        return;
      }
      const res = await admin.saveConfig({ ...target, label: label.value.trim(), config: desc && msg ? toBinary(desc, msg) : new Uint8Array() });
      if (res.violations.length) {
        validate(res.violations.map((v) => ({ field: v.field, message: v.message })));
        st.textContent = "The widget didn't accept that; see the fields above.";
        return;
      }
      page("Saved", h("p", {}, `${label.value.trim() || w.title} is running. It'll appear on every paired screen.`), h("a", { href: `${base}admin` }, "Manage widgets"));
    });
    page(
      w.title,
      ...("instanceId" in target ? [h("p", {}, h("a", { href: `${base}admin` }, "← All widgets"))] : []),
      networkNote(w),
      h("label", {}, h("span", { class: "label" }, "Name"), label),
      fields,
      save,
      status,
    );
  });
}

function networkNote(w: WidgetInfo): Node {
  if (!w.requestedHosts.length) return h("p", { class: "muted" }, "This widget doesn't use the network.");
  const hosts = w.grantSet ? w.grantedHosts : w.requestedHosts;
  const text = w.networkDisabled ? "Network access is turned off for this widget." : `Reaches: ${hosts.join(", ") || "nothing"}.`;
  return h("p", { class: "muted" }, `${text} ${w.networkWhy}`);
}

// ---- admin home ----

async function adminHome() {
  await requireAdmin(async () => {
    const [{ instances }, { widgets }, { devices }] = await Promise.all([
      admin.listInstances({}),
      admin.listWidgets({}),
      auth.listDevices({}),
    ]);

    const widgetList = h("ul", { class: "devices" });
    for (const inst of instances) {
      const title = inst.label || widgets.find((w) => w.name === inst.widget)?.title || inst.widget;
      const state = stateText(inst.status?.state, inst.status?.message);
      const actions = h("div", { class: "actions" });
      const settings = h("button", {}, inst.status?.state === InstanceStatus_State.NEEDS_CONFIG ? "Set up" : "Settings");
      settings.addEventListener("click", () => location.assign(`${base}edit/${encodeURIComponent(inst.id)}`));
      const [rename, rs] = button("Rename", async () => {
        const name = prompt("Name", inst.label);
        if (name === null) return;
        await admin.renameInstance({ instanceId: inst.id, label: name });
        await adminHome();
      });
      const [activity, as] = button("Network log", async (st) => {
        const { fetches } = await admin.getInstanceActivity({ instanceId: inst.id });
        st.replaceChildren(
          fetches.length
            ? h("ul", { class: "log" }, ...fetches.slice().reverse().map((f) => h("li", {}, `${f.method} ${f.host}${f.path} → ${f.denied ? `refused: ${f.denied}` : f.error ? f.error : f.status}`)))
            : "No requests yet.",
        );
      });
      const [del, ds] = button("Delete", async () => {
        if (!confirm(`Delete ${title}? Its saved data goes too.`)) return;
        await admin.deleteInstance({ instanceId: inst.id });
        await adminHome();
      });
      del.className = "danger";
      actions.append(settings, rename, activity, del);
      widgetList.append(h("li", {}, h("strong", {}, title), h("span", { class: "muted" }, ` ${inst.widget} · ${state}`), actions, rs, as, ds));
    }
    if (!instances.length) widgetList.append(h("li", { class: "muted" }, "No widgets yet. Add one from a screen."));

    const grants = h("ul", { class: "devices" });
    for (const w of widgets.filter((w) => w.requestedHosts.length)) {
      const granted = new Set(w.grantSet ? w.grantedHosts : w.requestedHosts);
      const boxes = w.requestedHosts.map((host) => {
        const box = h("input", { type: "checkbox" }) as HTMLInputElement;
        box.checked = !w.networkDisabled && granted.has(host);
        return [host, box] as const;
      });
      const [save, st] = button("Save access", async (s) => {
        const hosts = boxes.filter(([, b]) => b.checked).map(([host]) => host);
        await admin.setNetworkGrant({ widget: w.name, hosts, disabled: hosts.length === 0 });
        s.textContent = "Saved. Running instances use it from their next request.";
      });
      grants.append(
        h(
          "li",
          {},
          h("strong", {}, w.title),
          h("p", { class: "muted" }, w.networkWhy),
          ...boxes.map(([host, box]) => h("label", { class: "check" }, box, h("span", {}, host))),
          save,
          st,
        ),
      );
    }

    const screens = h("ul", { class: "devices" });
    for (const d of devices) {
      const [revoke, status] = button("Unpair", async () => {
        await auth.revokeDevice({ id: d.id });
        await adminHome();
      });
      revoke.className = "danger";
      const seen = d.lastSeen ? timestampDate(d.lastSeen).toLocaleString() : "never";
      screens.append(h("li", {}, h("strong", {}, d.name), h("span", { class: "muted" }, ` last seen ${seen}`), revoke, status));
    }
    if (!devices.length) screens.append(h("li", { class: "muted" }, "No screens paired yet. Open a board on a screen and scan its code."));

    const [add, status] = button("Add another passkey", async (st) => {
      await createPasskey("", deviceGuess(navigator.userAgent));
      st.textContent = "Passkey added.";
    });
    page("Widgets", widgetList, h("h2", {}, "Network access"), grants, h("h2", {}, "Screens"), screens, h("h2", {}, "Passkeys"), add, status);
  });
}

function stateText(s: InstanceStatus_State | undefined, message?: string): string {
  switch (s) {
    case InstanceStatus_State.NEEDS_CONFIG:
      return "waiting to be set up";
    case InstanceStatus_State.INVALID_CONFIG:
      return "settings need fixing";
    case InstanceStatus_State.FAULTED:
      return `stopped: ${message ?? "error"}`;
    case InstanceStatus_State.RUNNING:
      return "running";
    default:
      return "unknown";
  }
}

function deviceGuess(ua: string): string {
  if (/iPhone/.test(ua)) return "iPhone";
  if (/iPad/.test(ua)) return "iPad";
  if (/Android/.test(ua)) return /Mobile/.test(ua) ? "Android phone" : "Android tablet";
  if (/Macintosh/.test(ua)) return "Mac";
  if (/Windows/.test(ua)) return "Windows PC";
  if (/CrOS/.test(ua)) return "Chromebook";
  if (/Linux/.test(ua)) return "Linux";
  return "Screen";
}

