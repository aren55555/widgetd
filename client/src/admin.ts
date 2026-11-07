// The admin pages a phone opens from a QR code:
//
//   <base>enroll/<token>   create the first passkey
//   <base>pair/<id>        approve a screen
//   <base>admin            paired screens; add another passkey
//
// Plain DOM, no framework: these are three small forms.

import { ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { timestampDate } from "@bufbuild/protobuf/wkt";

import { AuthService, Role } from "./gen/widgetd/v1/auth_pb.ts";

const base = new URL(".", import.meta.url).pathname; // the bundle sits at <base>admin.js
const auth = createClient(AuthService, createConnectTransport({ baseUrl: location.origin, useBinaryFormat: true }));
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
    return await admin();
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

// ---- admin home ----

async function admin() {
  await requireAdmin(async () => {
    const { devices } = await auth.listDevices({});
    const list = h("ul", { class: "devices" });
    for (const d of devices) {
      const [revoke, status] = button("Unpair", async () => {
        await auth.revokeDevice({ id: d.id });
        await admin();
      });
      const seen = d.lastSeen ? timestampDate(d.lastSeen).toLocaleString() : "never";
      list.append(h("li", {}, h("strong", {}, d.name), h("span", { class: "muted" }, ` last seen ${seen}`), revoke, status));
    }
    if (!devices.length) list.append(h("li", { class: "muted" }, "No screens paired yet. Open a board on a screen and scan its code."));
    const [add, status] = button("Add another passkey", async (st) => {
      await createPasskey("", deviceGuess(navigator.userAgent));
      st.textContent = "Passkey added.";
    });
    page("Screens", list, h("h2", {}, "Passkeys"), add, status);
  });
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

