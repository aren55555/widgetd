// The admin pages a phone opens from a QR code:
//
//   <base>enroll/<token>          create the first passkey
//   <base>pair/<id>               approve a screen
//   <base>configure/<t>           set up one widget instance (QR ticket)
//
// and the admin app, one page per kind of thing, with a nav that's a bottom
// tab bar on a phone and a sidebar on a wide screen:
//
//   <base>admin/widgets           the running instances (the default)
//   <base>admin/widgets/<id>      one instance: settings, accounts, network, delete
//   <base>admin/screens           paired screens
//   <base>admin/access            accounts and per-widget network access
//   <base>admin/settings          passkeys and providers' registered apps
//   <base>edit/<id>               old link to an instance; becomes the above
//
// Solid components, styled with Tailwind (lib/ui.ts). The settings form is
// generated from the widget's schema (lib/ConfigForm.tsx).

import { ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { create, createFileRegistry, fromBinary, toBinary } from "@bufbuild/protobuf";
import { FileDescriptorSetSchema } from "@bufbuild/protobuf/wkt";
import {
  createMemo,
  createResource,
  createSignal,
  ErrorBoundary,
  For,
  Match,
  onCleanup,
  Show,
  Suspense,
  Switch,
  type JSX,
} from "solid-js";
import { render } from "solid-js/web";
import {
  ChevronRight,
  createElement,
  LayoutGrid,
  Monitor,
  Settings as SettingsIcon,
  ShieldCheck,
  type IconNode,
} from "@widgetd/sdk/icons";

import { AuthService, Role, type Device } from "./gen/widgetd/v1/auth_pb.ts";
import {
  WidgetAdminService,
  type Account,
  type GetConfigFormResponse,
  type InstanceInfo,
  type ProviderInfo,
  type WidgetInfo,
} from "./gen/widgetd/v1/admin_pb.ts";
import { InstanceStatus_State, type Icon } from "./gen/widgetd/v1/stream_pb.ts";
import { createConfigForm, type ConfigForm } from "./lib/ConfigForm.tsx";
import { deviceMatches, instanceMatches, normalizeQuery, shortIdMatches } from "./lib/find.ts";
import { widgetIcon } from "./lib/icon.ts";
import { sameChoices, type Choices, type Secrets } from "./lib/form.ts";
import * as ui from "./lib/ui.ts";

const base = new URL(".", import.meta.url).pathname; // the bundle sits at <base>admin.js
const transport = createConnectTransport({ baseUrl: location.origin, useBinaryFormat: true });
const auth = createClient(AuthService, transport);
const admin = createClient(WidgetAdminService, transport);

const [section, arg = ""] = location.pathname.slice(base.length).split("/");
const root = document.getElementById("app")!;
// The single-task pages are one narrow column; the admin app lays itself out.
const taskPage = section === "enroll" || section === "pair" || section === "configure";
root.className = taskPage ? ui.column : "";
/** A page outside the admin app's layout: a narrow column of its own, unless the root already is one. */
const column = taskPage ? "contents" : ui.column;

function App() {
  if (!window.isSecureContext || !("PublicKeyCredential" in window)) {
    // eslint-disable-next-line solid/components-return-once -- the condition never changes
    return (
      <div class={column}>
        <Page title="Can't use passkeys here">
          <p>Open this page over HTTPS, in a browser that supports passkeys.</p>
        </Page>
      </div>
    );
  }
  // An instance's page used to be edit/<id>; it's now inside the admin app.
  if (section === "edit") history.replaceState(null, "", adminHref("widgets", decodeURIComponent(arg)));
  return (
    <ErrorBoundary
      fallback={(e) => (
        <div class={column}>
          <Page title="Something went wrong">
            <p class={ui.error}>{errorText(e)}</p>
          </Page>
        </div>
      )}
    >
      <Suspense>
        <Switch
          fallback={
            <RequireAdmin>
              <AdminApp />
            </RequireAdmin>
          }
        >
          <Match when={section === "enroll"}>
            <Enroll token={arg} />
          </Match>
          <Match when={section === "pair"}>
            <RequireAdmin>
              <Approve pairingId={arg} />
            </RequireAdmin>
          </Match>
          <Match when={section === "configure"}>
            <RequireAdmin>
              <Configure target={{ ticket: arg }} />
            </RequireAdmin>
          </Match>
          <Match when={section === "edit"}>
            <RequireAdmin>
              <AdminApp />
            </RequireAdmin>
          </Match>
        </Switch>
      </Suspense>
    </ErrorBoundary>
  );
}

// ---- shared pieces ----

function Page(props: { title: string; icon?: Icon; children: JSX.Element }) {
  return (
    <>
      <h1 class="flex items-center gap-2 text-2xl font-bold">
        <WidgetIcon icon={props.icon} size={24} />
        {props.title}
      </h1>
      {props.children}
    </>
  );
}

/** A widget's own icon (its widget.toml `icon`); nothing if it has none. */
function WidgetIcon(props: { icon?: Icon; size?: number }) {
  return <>{widgetIcon(props.icon, props.size ?? 18)}</>;
}

const H2 = "mt-4 text-lg font-bold";

/** A link into the admin app from a page outside it (a full page load). */
function AdminLink(props: { tab?: Tab; children: JSX.Element }) {
  return (
    <a class={ui.link} href={adminHref(props.tab ?? "widgets")}>
      {props.children}
    </a>
  );
}

function errorText(e: unknown): string {
  if (e instanceof ConnectError) return e.rawMessage;
  if (e instanceof DOMException && e.name === "NotAllowedError") return "Cancelled.";
  return e instanceof Error ? e.message : String(e);
}

/**
 * A status line for one or more buttons: run() wraps an action so its
 * buttons are disabled while it runs and a failure shows as an error.
 */
function createStatus() {
  const [busy, setBusy] = createSignal(false);
  const [text, setText] = createSignal("");
  const [failed, setFailed] = createSignal(false);
  const run = (action: (say: (text: string) => void) => Promise<void>) => async () => {
    setBusy(true);
    setFailed(false);
    setText("");
    try {
      await action(setText);
    } catch (e) {
      setFailed(true);
      setText(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const View = () => (
    <p class={failed() ? `${ui.status} ${ui.error}` : ui.status} role="status">
      {text()}
    </p>
  );
  return { busy, run, View };
}

// ---- passkeys ----

async function createPasskey(setupToken: string, name: string) {
  const begin = await auth.beginEnroll({ setupToken });
  const options = PublicKeyCredential.parseCreationOptionsFromJSON(
    JSON.parse(begin.optionsJson) as PublicKeyCredentialCreationOptionsJSON,
  );
  const cred = (await navigator.credentials.create({ publicKey: options })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was created.");
  await auth.finishEnroll({ ceremonyId: begin.ceremonyId, credentialJson: JSON.stringify(cred.toJSON()), name });
}

async function signIn() {
  const begin = await auth.beginLogin({});
  const options = PublicKeyCredential.parseRequestOptionsFromJSON(
    JSON.parse(begin.optionsJson) as PublicKeyCredentialRequestOptionsJSON,
  );
  const cred = (await navigator.credentials.get({ publicKey: options })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was chosen.");
  await auth.finishLogin({ ceremonyId: begin.ceremonyId, credentialJson: JSON.stringify(cred.toJSON()) });
}

/** Show the children once the caller is an admin, offering sign-in if not. */
function RequireAdmin(props: { children: JSX.Element }) {
  const [session] = createResource(() => auth.getSession({}));
  const [signedIn, setSignedIn] = createSignal(false);
  const st = createStatus();
  return (
    <Show when={session()}>
      {(s) => (
        <Show
          when={signedIn() || s().role === Role.ADMIN}
          fallback={
            <div class={column}>
              <Page title="Sign in">
                <p>This needs an admin. Sign in with your passkey to continue.</p>
                <button
                  class={ui.btn.primary}
                  disabled={st.busy()}
                  onClick={st.run(async () => {
                    await signIn();
                    setSignedIn(true);
                  })}
                >
                  Sign in with a passkey
                </button>
                <st.View />
              </Page>
            </div>
          }
        >
          {props.children}
        </Show>
      )}
    </Show>
  );
}

function Enroll(props: { token: string }) {
  const [session] = createResource(() => auth.getSession({}));
  const [name, setName] = createSignal(deviceGuess(navigator.userAgent));
  const st = createStatus();
  return (
    <Show when={session()}>
      {(s) => (
        <Show
          when={s().needsEnrollment}
          fallback={
            <Page title="Already set up">
              <p>An admin passkey exists.</p>
              <AdminLink>Go to admin</AdminLink>
            </Page>
          }
        >
          <Page title="Set up widgetd">
            <p>Create a passkey on this phone. It's how you'll approve screens and change settings from now on.</p>
            <label class="block">
              Name{" "}
              <input
                class={ui.input}
                value={name()}
                onInput={(e) => setName(e.currentTarget.value)}
                maxLength={64}
                aria-label="Passkey name"
              />
            </label>
            <button
              class={ui.btn.primary}
              disabled={st.busy()}
              onClick={st.run(async (say) => {
                await createPasskey(props.token, name());
                say("Done. This phone is now an admin.");
                location.replace(`${base}admin`);
              })}
            >
              Create passkey
            </button>
            <st.View />
          </Page>
        </Show>
      )}
    </Show>
  );
}

// ---- pairing ----

function Approve(props: { pairingId: string }) {
  const [pairing] = createResource(() => auth.getPairing({ pairingId: props.pairingId }));
  const [devices] = createResource(async () => (await auth.listDevices({})).devices);
  const [name, setName] = createSignal<string>();
  /** The device this screen continues, or "" for a new one. */
  const [continued, setContinued] = createSignal("");
  const [approved, setApproved] = createSignal<string>();
  const st = createStatus();
  return (
    <Show when={pairing()}>
      {(p) => {
        const screenName = () => name() ?? deviceGuess(p().userAgent);
        const continuedName = () => devices()?.find((d) => d.id === continued())?.name;
        return (
          <Show
            when={approved() === undefined}
            fallback={
              <Page title="Approved">
                <p>{approved()}</p>
                <AdminLink tab="screens">Paired screens</AdminLink>
              </Page>
            }
          >
            <Page title="Pair a screen">
              <p>Check the screen shows this code:</p>
              <p class={ui.code}>{p().code}</p>
              <p class={ui.muted}>{p().userAgent || "unknown browser"}</p>
              <Show when={devices()?.length}>
                <label class={ui.label}>
                  This screen is
                  <select class={ui.select} disabled={st.busy()} onChange={(e) => setContinued(e.currentTarget.value)}>
                    <option value="" selected={!continued()}>
                      A new screen
                    </option>
                    <For each={devices()}>
                      {(d) => (
                        <option value={d.id} selected={d.id === continued()}>
                          {d.name}, continued
                        </option>
                      )}
                    </For>
                  </select>
                </label>
              </Show>
              <Show
                when={!continued()}
                fallback={
                  <p class={ui.muted}>
                    It'll show the same board as {continuedName()}, with its settings, and both keep working. Unpairing{" "}
                    {continuedName()} signs out both.
                  </p>
                }
              >
                <label class="block">
                  Name{" "}
                  <input
                    class={ui.input}
                    value={screenName()}
                    onInput={(e) => setName(e.currentTarget.value)}
                    maxLength={64}
                    aria-label="Screen name"
                  />
                </label>
              </Show>
              <button
                class={ui.btn.primary}
                disabled={st.busy()}
                onClick={st.run(async () => {
                  const continueDeviceId = continued();
                  if (continueDeviceId) {
                    const n = continuedName() ?? "The screen";
                    await auth.approvePairing({ pairingId: props.pairingId, continueDeviceId });
                    setApproved(`The new screen now shows the same board as ${n}.`);
                    return;
                  }
                  const deviceName = screenName().trim();
                  await auth.approvePairing({ pairingId: props.pairingId, deviceName });
                  setApproved(`${deviceName} can now show widgets.`);
                })}
              >
                Approve this screen
              </button>
              <st.View />
            </Page>
          </Show>
        );
      }}
    </Show>
  );
}

// ---- configure an instance ----

/** A QR ticket from a screen, or an instance id from the admin page. */
type ConfigTarget = { ticket: string } | { instanceId: string };

/**
 * A widget's settings. On its own (a QR ticket) it's a whole page that ends
 * in "Saved"; with onSaved, it's a section of the instance page and stays.
 */
function Configure(props: { target: ConfigTarget; onSaved?: () => Promise<void> }) {
  const [form] = createResource(() => admin.getConfigForm(props.target));
  return <Show when={form()}>{(f) => <ConfigEditor target={props.target} form={f()} onSaved={props.onSaved} />}</Show>;
}

function ConfigEditor(props: { target: ConfigTarget; form: GetConfigFormResponse; onSaved?: () => Promise<void> }) {
  const { target, form, onSaved } = props;
  const w = form.widget!;
  const [label, setLabel] = createSignal(form.label);
  const [saved, setSaved] = createSignal(false);
  const st = createStatus();
  const secrets: Secrets = { fields: new Set(form.secretFields), set: new Set(form.secretsSet), clear: new Set() };

  let config: { form: ConfigForm; bytes: () => Uint8Array } | undefined;
  if (form.configType) {
    const registry = createFileRegistry(fromBinary(FileDescriptorSetSchema, form.schema));
    const desc = registry.getMessage(form.configType);
    if (!desc) throw new Error(`the schema has no ${form.configType}`);
    const msg = form.config.length ? fromBinary(desc, form.config) : create(desc);
    // The widget may offer pick-lists for some fields (a league's teams),
    // which depend on what's been picked so far: ask again after each edit
    // settles. Only a field whose list changed is redrawn.
    const [choices, setChoices] = createSignal<Choices>(new Map());
    let asked = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refreshChoices = async () => {
      const mine = ++asked;
      try {
        const res = await admin.getConfigChoices({ ...target, config: toBinary(desc, msg) });
        if (mine !== asked) return; // a newer edit already asked
        const next: Choices = new Map(
          res.fields.map((f) => [f.field, f.choices.map((c) => ({ value: c.value, label: c.label }))]),
        );
        if (!sameChoices(choices(), next)) setChoices(next);
      } catch {
        // Choices are a convenience: without them, fields stay free text.
      }
    };
    const configForm = createConfigForm(desc, msg, {
      choices,
      secrets,
      onChange: () => {
        configForm.validate();
        clearTimeout(timer);
        timer = setTimeout(() => void refreshChoices(), 400);
      },
    });
    onCleanup(() => clearTimeout(timer));
    void refreshChoices();
    config = { form: configForm, bytes: () => toBinary(desc, msg) };
  }

  const save = st.run(async (say) => {
    if (config && !config.form.validate()) {
      say("Fix the highlighted fields first.");
      return;
    }
    const res = await admin.saveConfig({
      ...target,
      label: label().trim(),
      config: config ? config.bytes() : new Uint8Array(),
      clearSecrets: [...secrets.clear],
    });
    if (res.violations.length) {
      config?.form.validate(res.violations.map((v) => ({ field: v.field, message: v.message })));
      say("The widget didn't accept that; see the fields above.");
      return;
    }
    if (onSaved) {
      say("Saved.");
      await onSaved();
      return;
    }
    setSaved(true);
  });

  const fields = (
    <>
      <label class="block">
        <span class={ui.label}>Name</span>
        <input
          class={ui.input}
          value={label()}
          onInput={(e) => setLabel(e.currentTarget.value)}
          maxLength={64}
          placeholder={w.title}
          aria-label="Name"
        />
      </label>
      <div>{config ? config.form.view : <p class={ui.muted}>This widget has no settings.</p>}</div>
      <button class={ui.btn.primary} disabled={st.busy()} onClick={save}>
        {form.status?.state === InstanceStatus_State.NEEDS_CONFIG ? "Add to screens" : "Save"}
      </button>
      <st.View />
    </>
  );
  // eslint-disable-next-line solid/components-return-once -- onSaved is fixed for the editor's life
  if (onSaved) return fields;
  return (
    <Show
      when={!saved()}
      fallback={
        <Page title="Saved">
          <p>{label().trim() || w.title} is running. It'll appear on every paired screen.</p>
          <AdminLink>Manage widgets</AdminLink>
        </Page>
      }
    >
      <Page title={w.title} icon={w.icon}>
        <NetworkNote w={w} />
        {fields}
      </Page>
    </Show>
  );
}

function NetworkNote(props: { w: WidgetInfo }) {
  const text = () => {
    const w = props.w;
    if (!w.requestedHosts.length) return "This widget doesn't use the network.";
    const hosts = w.grantSet ? w.grantedHosts : w.requestedHosts;
    const reach = w.networkDisabled
      ? "Network access is turned off for this widget."
      : `Reaches: ${hosts.join(", ") || "nothing"}.`;
    return `${reach} ${w.networkWhy}`;
  };
  return <p class={ui.muted}>{text()}</p>;
}

// ---- admin app: data ----

interface HomeData {
  instances: InstanceInfo[];
  widgets: WidgetInfo[];
  devices: Device[];
  accounts: Account[];
  providers: ProviderInfo[];
}

async function loadHome(): Promise<HomeData> {
  const [{ instances }, { widgets }, { devices }, { accounts, providers }] = await Promise.all([
    admin.listInstances({}),
    admin.listWidgets({}),
    auth.listDevices({}),
    admin.listAccounts({}),
  ]);
  return { instances, widgets, devices, accounts, providers };
}

const accountName = (a: Account, providers: ProviderInfo[]) =>
  `${providers.find((p) => p.name === a.provider)?.title ?? a.provider}: ${a.label || "(unnamed)"}`;

// ---- admin app: navigation ----

type Tab = "widgets" | "screens" | "access" | "settings";

const TABS: Array<{ id: Tab; label: string; icon: IconNode }> = [
  { id: "widgets", label: "Widgets", icon: LayoutGrid },
  { id: "screens", label: "Screens", icon: Monitor },
  { id: "access", label: "Access", icon: ShieldCheck },
  { id: "settings", label: "Settings", icon: SettingsIcon },
];

function adminHref(tab: Tab, instanceId?: string): string {
  return `${base}admin/${tab}${instanceId ? `/${encodeURIComponent(instanceId)}` : ""}`;
}

/** Where the admin app is: admin/<tab>[/<instance id>]; an unknown tab is Widgets. */
function adminRoute(path: string): { tab: Tab; instanceId?: string } {
  const [, tab, id] = path.slice(base.length).split("/");
  const known = TABS.find((t) => t.id === tab)?.id ?? "widgets";
  return { tab: known, instanceId: known === "widgets" && id ? decodeURIComponent(id) : undefined };
}

// The admin app moves between its pages without reloading, so its data
// (and anything typed) stays; the URL still names the page, for the back
// button and links.
const [path, setPath] = createSignal(location.pathname);
addEventListener("popstate", () => setPath(location.pathname));

function navigate(href: string) {
  history.pushState(null, "", href);
  setPath(location.pathname);
  scrollTo(0, 0);
}

/** A link within the admin app: followed in place, unless it's being opened elsewhere. */
function Link(props: {
  href: string;
  class?: string;
  current?: boolean;
  onFollow?: () => void;
  children: JSX.Element;
}) {
  return (
    <a
      href={props.href}
      class={props.class ?? ui.link}
      aria-current={props.current ? "page" : undefined}
      onClick={(e) => {
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        props.onFollow?.();
        navigate(props.href);
      }}
    >
      {props.children}
    </a>
  );
}

/** A Lucide icon for widgetd's own UI, drawn as plain SVG elements. */
function LucideIcon(props: { icon: IconNode; size?: number }) {
  return (
    <>
      {createElement(props.icon, {
        width: props.size ?? 20,
        height: props.size ?? 20,
        "aria-hidden": "true",
        class: "shrink-0",
      })}
    </>
  );
}

// ---- admin app ----

/** Whether an account wants a sign-in it doesn't have (or more than it was granted). */
const accountNeedsSignIn = (a: Account) =>
  a.wanted.length > 0 && (!a.signedIn || a.wanted.some((s) => !a.granted.includes(s)));

/** What each tab's badge counts: things there that need the admin. */
function attention(d: HomeData, tab: Tab): number {
  switch (tab) {
    case "widgets":
      return d.instances.filter((i) => i.status?.state !== InstanceStatus_State.RUNNING).length;
    case "access":
      return d.accounts.filter((a) => a.label && accountNeedsSignIn(a)).length;
    case "settings":
      // A provider some widget uses but whose app isn't registered.
      return d.providers.filter(
        (p) => !p.clientSet && d.instances.some((i) => i.bindings.some((b) => b.provider === p.name)),
      ).length;
    default:
      return 0;
  }
}

function AdminApp() {
  const [data, { mutate }] = createResource(loadHome);
  // After a change, load again in place (a failure shows on the button
  // that asked, not as a broken page).
  const reload = async () => {
    mutate(await loadHome());
  };
  const route = createMemo(() => adminRoute(path()));
  // The search finds a card by the short id its screen's set-up code shows,
  // a screen by the one in its footer, or either by name, whatever tab is
  // open; ?card=<id> fills it in.
  const [find, setFind] = createSignal(new URLSearchParams(location.search).get("card") ?? "");
  const query = () => normalizeQuery(find());
  return (
    <Show when={data()}>
      {(d) => (
        <div class="md:flex md:min-h-dvh">
          <nav
            aria-label="Admin"
            class="fixed inset-x-0 bottom-0 z-10 flex border-t border-gray-500/20 bg-[color:Canvas] pb-[env(safe-area-inset-bottom)] md:sticky md:top-0 md:h-dvh md:w-56 md:shrink-0 md:flex-col md:gap-1 md:border-t-0 md:border-r md:p-4"
          >
            <p class="hidden px-3 pb-4 text-lg font-bold md:block">widgetd</p>
            <For each={TABS}>
              {(t) => (
                <Link
                  href={adminHref(t.id)}
                  current={route().tab === t.id}
                  onFollow={() => setFind("")}
                  class="flex flex-1 flex-col items-center gap-0.5 py-2 text-xs font-medium opacity-70 aria-[current=page]:text-blue-600 aria-[current=page]:opacity-100 md:flex-none md:flex-row md:gap-3 md:rounded-lg md:px-3 md:text-sm md:aria-[current=page]:bg-blue-600/10 dark:aria-[current=page]:text-blue-400"
                >
                  <span class="relative">
                    <LucideIcon icon={t.icon} />
                    <Show when={attention(d(), t.id)}>
                      {(n) => (
                        <span
                          class="absolute -top-1.5 -right-2.5 min-w-4 rounded-full bg-amber-500 px-1 text-center text-[10px] leading-4 font-bold text-white"
                          aria-label={`${n()} need attention`}
                        >
                          {n()}
                        </span>
                      )}
                    </Show>
                  </span>
                  {t.label}
                </Link>
              )}
            </For>
          </nav>
          <main class="mx-auto flex w-full max-w-2xl min-w-0 flex-col gap-4 px-5 pt-6 pb-28 md:px-8 md:pb-10">
            <For each={d().accounts.filter((a) => !a.label)}>
              {(a) => <NameAccount a={a} d={d()} reload={reload} />}
            </For>
            <Show
              when={route().instanceId}
              fallback={
                <>
                  <input
                    class={ui.input}
                    type="search"
                    placeholder="Find a widget or screen by id or name"
                    aria-label="Find a widget or screen by id or name"
                    autocapitalize="off"
                    autocomplete="off"
                    spellcheck={false}
                    value={find()}
                    onInput={(e) => setFind(e.currentTarget.value)}
                  />
                  <Show when={!query()} fallback={<SearchResults d={d()} query={query()} reload={reload} />}>
                    <Switch>
                      <Match when={route().tab === "widgets"}>
                        <WidgetsTab d={d()} />
                      </Match>
                      <Match when={route().tab === "screens"}>
                        <ScreensTab d={d()} reload={reload} />
                      </Match>
                      <Match when={route().tab === "access"}>
                        <AccessTab d={d()} reload={reload} />
                      </Match>
                      <Match when={route().tab === "settings"}>
                        <SettingsTab d={d()} reload={reload} />
                      </Match>
                    </Switch>
                  </Show>
                </>
              }
            >
              {(id) => <InstancePage id={id()} d={d()} reload={reload} />}
            </Show>
          </main>
        </div>
      )}
    </Show>
  );
}

function SearchResults(props: { d: HomeData; query: string; reload: () => Promise<void> }) {
  const instances = () =>
    props.d.instances.filter((inst) =>
      instanceMatches(inst, instanceTitle(inst, widgetOf(props.d, inst)), props.query),
    );
  const devices = () => props.d.devices.filter((dev) => deviceMatches(dev, props.query));
  return (
    <>
      <Show when={!instances().length && !devices().length}>
        <p class={ui.muted}>Nothing matches.</p>
      </Show>
      <Show when={instances().length}>
        <h2 class={H2}>Widgets</h2>
        <ul class={ui.list}>
          <For each={instances()}>
            {(inst) => <InstanceRow inst={inst} widget={widgetOf(props.d, inst)} query={props.query} />}
          </For>
        </ul>
      </Show>
      <Show when={devices().length}>
        <h2 class={H2}>Screens</h2>
        <ul class={ui.list}>
          <For each={devices()}>{(dev) => <ScreenItem device={dev} query={props.query} reload={props.reload} />}</For>
        </ul>
      </Show>
    </>
  );
}

const widgetOf = (d: HomeData, inst: InstanceInfo) => d.widgets.find((w) => w.name === inst.widget);

// ---- widgets tab ----

function WidgetsTab(props: { d: HomeData }) {
  return (
    <Page title="Widgets">
      <p class={ui.muted}>Each widget shows on every paired screen. Add one from a screen.</p>
      <ul class={ui.list}>
        <For each={props.d.instances} fallback={<li class={`py-3 ${ui.muted}`}>No widgets yet.</li>}>
          {(inst) => <InstanceRow inst={inst} widget={widgetOf(props.d, inst)} query="" />}
        </For>
      </ul>
    </Page>
  );
}

/** A dot for an instance's state: green running, red broken, amber waiting on the admin. */
function StateDot(props: { state: InstanceStatus_State | undefined }) {
  const color = () => {
    switch (props.state) {
      case InstanceStatus_State.RUNNING:
        return "bg-green-500";
      case InstanceStatus_State.FAULTED:
      case InstanceStatus_State.INVALID_CONFIG:
        return "bg-red-500";
      default:
        return "bg-amber-500";
    }
  };
  return <span class={`inline-block size-2 shrink-0 rounded-full ${color()}`} aria-hidden="true" />;
}

/** One instance in a list: a link to its page. */
function InstanceRow(props: {
  inst: InstanceInfo;
  widget: WidgetInfo | undefined;
  /** The admin's search, normalized: a card whose short id it starts is highlighted. */
  query: string;
}) {
  return (
    <li class="border-b border-gray-500/20">
      <Link
        href={adminHref("widgets", props.inst.id)}
        class="-mx-2 flex items-center gap-3 rounded-lg px-2 py-3 hover:bg-gray-500/10"
      >
        <div class="min-w-0 flex-1">
          <strong class="inline-flex items-center gap-2 align-middle">
            <WidgetIcon icon={props.widget?.icon} />
            {instanceTitle(props.inst, props.widget)}
          </strong>
          <p class={`flex items-center gap-1.5 ${ui.muted}`}>
            <StateDot state={props.inst.status?.state} />
            {props.inst.widget} · {stateText(props.inst.status?.state, props.inst.status?.message)}
          </p>
          <Show when={props.inst.status?.warning}>
            <p class="text-sm text-amber-600">⚠ {props.inst.status?.warning}</p>
          </Show>
          <Show when={props.inst.cards.length}>
            <div class="mt-1 flex flex-wrap gap-1.5">
              <For each={props.inst.cards}>
                {(c) => <ShortId id={c.shortId} query={props.query} title={c.title ? `${c.title} (${c.id})` : c.id} />}
              </For>
            </div>
          </Show>
        </div>
        <span class="opacity-50">
          <LucideIcon icon={ChevronRight} />
        </span>
      </Link>
    </li>
  );
}

/** Everything about one instance: its settings, accounts, network and deletion. */
function InstancePage(props: { id: string; d: HomeData; reload: () => Promise<void> }) {
  const inst = () => props.d.instances.find((i) => i.id === props.id);
  return (
    <>
      <p>
        <Link href={adminHref("widgets")}>← Widgets</Link>
      </p>
      <Show
        when={inst()}
        fallback={
          <Page title="Not found">
            <p>That widget isn't here any more.</p>
          </Page>
        }
      >
        {(i) => <InstanceDetail inst={i()} widget={widgetOf(props.d, i())} d={props.d} reload={props.reload} />}
      </Show>
    </>
  );
}

function InstanceDetail(props: {
  inst: InstanceInfo;
  widget: WidgetInfo | undefined;
  d: HomeData;
  reload: () => Promise<void>;
}) {
  const st = createStatus();
  const title = () => instanceTitle(props.inst, props.widget);
  return (
    <Page title={title()} icon={props.widget?.icon}>
      <div>
        <p class={`flex items-center gap-1.5 ${ui.muted}`}>
          <StateDot state={props.inst.status?.state} />
          {props.inst.widget} · {stateText(props.inst.status?.state, props.inst.status?.message)}
        </p>
        <Show when={props.inst.status?.warning}>
          <p class="text-sm text-amber-600">⚠ {props.inst.status?.warning}</p>
        </Show>
        <Show when={props.inst.cards.length}>
          <div class="mt-1 flex flex-wrap items-center gap-1.5">
            <span class={ui.muted}>Cards:</span>
            <For each={props.inst.cards}>
              {(c) => <ShortId id={c.shortId} query="" title={c.title ? `${c.title} (${c.id})` : c.id} />}
            </For>
          </div>
        </Show>
      </div>

      <h2 class={H2}>Settings</h2>
      <Suspense fallback={<p class={ui.muted}>Loading…</p>}>
        <Configure target={{ instanceId: props.inst.id }} onSaved={props.reload} />
      </Suspense>

      <Show when={props.inst.bindings.length}>
        <h2 class={H2}>Accounts</h2>
        <For each={props.inst.bindings}>
          {(b) => (
            <BindingPicker
              inst={props.inst}
              provider={b.provider}
              accountId={b.accountId}
              d={props.d}
              reload={props.reload}
            />
          )}
        </For>
      </Show>

      <h2 class={H2}>Network</h2>
      <Show when={props.widget}>{(w) => <NetworkNote w={w()} />}</Show>
      <p>
        <Link href={adminHref("access")}>Change network access in Access ›</Link>
      </p>
      <NetworkLog instanceId={props.inst.id} />

      <h2 class={H2}>Delete</h2>
      <p class={ui.muted}>It goes from every screen, and its saved data goes too.</p>
      <div>
        <button
          class={ui.btn.danger}
          disabled={st.busy()}
          onClick={st.run(async () => {
            if (!confirm(`Delete ${title()}? Its saved data goes too.`)) return;
            await admin.deleteInstance({ instanceId: props.inst.id });
            navigate(adminHref("widgets"));
            await props.reload();
          })}
        >
          Delete {title()}
        </button>
      </div>
      <st.View />
    </Page>
  );
}

type Fetch = Awaited<ReturnType<typeof admin.getInstanceActivity>>["fetches"][number];

/** An instance's recent requests, newest first. */
function NetworkLog(props: { instanceId: string }) {
  const st = createStatus();
  const [log, setLog] = createSignal<Fetch[]>();
  const load = st.run(async () => {
    const { fetches } = await admin.getInstanceActivity({ instanceId: props.instanceId });
    setLog(fetches.slice().reverse());
  });
  void load();
  return (
    <details class={`${ui.fieldset} w-full`}>
      <summary class="cursor-pointer font-semibold">Recent requests{log() ? ` (${log()?.length})` : ""}</summary>
      <Show when={log()}>
        {(l) => (
          <Show when={l().length} fallback={<p class="mt-2 text-sm">No requests yet.</p>}>
            <ul class="mt-2 list-disc pl-4 font-mono text-xs/snug break-all">
              <For each={l()}>
                {(f) => (
                  <li>{`${f.method} ${f.host}${f.path} → ${f.denied ? `refused: ${f.denied}` : f.error ? f.error : f.status}`}</li>
                )}
              </For>
            </ul>
          </Show>
        )}
      </Show>
      <button class={`${ui.btn.secondary} mt-2`} disabled={st.busy()} onClick={load}>
        Refresh
      </button>
      <st.View />
    </details>
  );
}

// ---- screens tab ----

function ScreensTab(props: { d: HomeData; reload: () => Promise<void> }) {
  return (
    <Page title="Screens">
      <p class={ui.muted}>To add a screen, open the board on it and scan the code it shows with this phone.</p>
      <ul class={ui.list}>
        <For each={props.d.devices} fallback={<li class={`py-3 ${ui.muted}`}>No screens paired yet.</li>}>
          {(dev) => <ScreenItem device={dev} query="" reload={props.reload} />}
        </For>
      </ul>
    </Page>
  );
}

// ---- access tab ----

function AccessTab(props: { d: HomeData; reload: () => Promise<void> }) {
  const networked = () => props.d.widgets.filter((w) => w.requestedHosts.length);
  return (
    <Page title="Access">
      <p class={ui.muted}>
        What widgets can reach outside their sandbox: the accounts they sign in with, and the hosts they may call.
      </p>

      <Show when={props.d.providers.length}>
        <h2 class={H2}>Accounts</h2>
        <p class={ui.muted}>
          Sign an account in from a widget's page, and choose there which account it uses. Each account is signed in for
          exactly what the widgets using it need.
        </p>
        <For each={props.d.providers}>
          {(p) => (
            <section>
              <h3 class="mt-2 font-semibold">{p.title}</h3>
              <Show when={!p.clientSet}>
                <p class="text-sm text-amber-600">
                  ⚠ Its app isn't registered yet, so nothing can sign in.{" "}
                  <Link href={adminHref("settings")}>Register it in Settings</Link>
                </p>
              </Show>
              <ul class={ui.list}>
                <For
                  each={props.d.accounts.filter((a) => a.provider === p.name && a.label)}
                  fallback={<li class={`py-3 ${ui.muted}`}>No {p.title} accounts yet.</li>}
                >
                  {(a) => <AccountItem a={a} p={p} d={props.d} reload={props.reload} />}
                </For>
              </ul>
            </section>
          )}
        </For>
      </Show>

      <h2 class={H2}>Network</h2>
      <p class={ui.muted}>Which hosts each kind of widget may reach. It applies to every widget of that kind.</p>
      <ul class={ui.list}>
        <For each={networked()} fallback={<li class={`py-3 ${ui.muted}`}>No widget uses the network.</li>}>
          {(w) => <NetworkGrant w={w} />}
        </For>
      </ul>
    </Page>
  );
}

// ---- settings tab ----

function SettingsTab(props: { d: HomeData; reload: () => Promise<void> }) {
  const passkey = createStatus();
  return (
    <Page title="Settings">
      <h2 class={H2}>Passkeys</h2>
      <p class={ui.muted}>A passkey signs you in as an admin. Add one on each phone or computer you use.</p>
      <button
        class={ui.btn.primary}
        disabled={passkey.busy()}
        onClick={passkey.run(async (say) => {
          await createPasskey("", deviceGuess(navigator.userAgent));
          say("Passkey added.");
        })}
      >
        Add a passkey on this device
      </button>
      <passkey.View />

      <Show when={props.d.providers.length}>
        <h2 class={H2}>Provider apps</h2>
        <p class={ui.muted}>
          Each provider's app is registered once, by whoever runs this server, and shared by all its accounts.
        </p>
        <For each={props.d.providers}>{(p) => <ProviderItem p={p} reload={props.reload} />}</For>
      </Show>
    </Page>
  );
}

/** A card's or screen's short id, highlighted when the admin's search finds it. */
function ShortId(props: { id: string; query: string; title: string }) {
  return (
    <code
      class={`rounded px-1.5 py-0.5 font-mono text-xs tracking-wider ${
        shortIdMatches(props.id, props.query) ? "bg-amber-400/40 font-semibold" : "bg-gray-500/15"
      }`}
      title={props.title}
    >
      {props.id}
    </code>
  );
}

const instanceTitle = (inst: InstanceInfo, widget: WidgetInfo | undefined) =>
  inst.label || widget?.title || inst.widget;

function NetworkGrant(props: { w: WidgetInfo }) {
  const w = props.w;
  const granted = new Set(w.grantSet ? w.grantedHosts : w.requestedHosts);
  const on = new Set(w.requestedHosts.filter((host) => !w.networkDisabled && granted.has(host)));
  const st = createStatus();
  return (
    <li class={ui.item}>
      <strong class="inline-flex items-center gap-2">
        <WidgetIcon icon={w.icon} />
        {w.title}
      </strong>
      <p class={ui.muted}>{w.networkWhy}</p>
      <For each={w.requestedHosts}>
        {(host) => (
          <label class="flex items-center gap-2">
            <input
              type="checkbox"
              checked={on.has(host)}
              onChange={(e) => (e.currentTarget.checked ? on.add(host) : on.delete(host))}
            />
            <span>{host}</span>
          </label>
        )}
      </For>
      <button
        class={ui.btn.small}
        disabled={st.busy()}
        onClick={st.run(async (say) => {
          const hosts = w.requestedHosts.filter((host) => on.has(host));
          await admin.setNetworkGrant({ widget: w.name, hosts, disabled: hosts.length === 0 });
          say("Saved. Running instances use it from their next request.");
        })}
      >
        Save access
      </button>
      <st.View />
    </li>
  );
}

function ScreenItem(props: { device: Device; query: string; reload: () => Promise<void> }) {
  const d = props.device;
  const st = createStatus();
  return (
    <li class={ui.item}>
      <div>
        <strong>{d.name}</strong>
        <Show when={d.screens > 1}>
          <span class={ui.muted}> · {d.screens} screens</span>
        </Show>
        <span class={ui.muted}> last seen {d.lastSeen ? timestampDate(d.lastSeen).toLocaleString() : "never"}</span>
        <div class="mt-1">
          <ShortId id={d.shortId} query={props.query} title="The id this screen shows in its footer" />
        </div>
      </div>
      <button
        class={ui.btn.danger}
        disabled={st.busy()}
        onClick={st.run(async () => {
          await auth.revokeDevice({ id: d.id });
          await props.reload();
        })}
      >
        Unpair
      </button>
      <st.View />
    </li>
  );
}

/**
 * Starting a sign-in from this page: a browser sign-in goes to the
 * provider's consent page right here (and comes back to name the account,
 * if it's new); a device sign-in shows the code to enter, and polls until
 * done(accounts) says it has arrived.
 */
function createSignIn(st: ReturnType<typeof createStatus>, reload: () => Promise<void>) {
  const [code, setCode] = createSignal<{ userCode: string; url: string }>();
  let poll: ReturnType<typeof setTimeout> | undefined;
  onCleanup(() => clearTimeout(poll));
  const start = (
    req: { provider: string; accountId?: string; instanceId?: string },
    done: (accounts: Account[]) => boolean,
  ) =>
    st.run(async () => {
      const s = await admin.beginSignIn(req);
      if (!s.userCode) {
        location.assign(s.url);
        return;
      }
      setCode({ userCode: s.userCode, url: s.url });
      const until = Date.now() + 10 * 60_000;
      const check = async () => {
        const { accounts } = await admin.listAccounts({});
        if (done(accounts)) {
          setCode(undefined);
          return reload();
        }
        if (Date.now() < until) poll = setTimeout(() => void check(), 3000);
      };
      poll = setTimeout(() => void check(), 3000);
    });
  const View = () => (
    <Show when={code()}>
      {(c) => (
        <p class="text-sm">
          Enter{" "}
          <strong class="font-mono text-[2rem] leading-tight font-semibold tracking-widest">{c().userCode}</strong> at{" "}
          <a class={ui.link} href={c().url} target="_blank" rel="noopener">
            {c()
              .url.replace(/^https:\/\//, "")
              .replace(/\?.*/, "")}
          </a>
          . This page updates once you've approved it.
        </p>
      )}
    </Show>
  );
  return { start, View };
}

/** Which account an instance uses a provider with; or a new one. */
function BindingPicker(props: {
  inst: InstanceInfo;
  provider: string;
  accountId: string;
  d: HomeData;
  reload: () => Promise<void>;
}) {
  const st = createStatus();
  const signIn = createSignIn(st, props.reload);
  const p = () => props.d.providers.find((x) => x.name === props.provider);
  const title = () => p()?.title ?? props.provider;
  const accounts = () => props.d.accounts.filter((a) => a.provider === props.provider && a.label);
  // Not a NUL-prefixed sentinel: Solid puts this value into its HTML template, and the
  // HTML parser turns NUL into U+FFFD there, so the change handler would never match it.
  const NEW = "+new";
  const before = new Set(props.d.accounts.map((a) => a.id));
  return (
    <div class="w-full">
      <label class={ui.label}>
        {title()} account
        <select
          class={ui.select}
          disabled={st.busy()}
          onChange={(ev) => {
            const v = ev.currentTarget.value;
            if (v === NEW) {
              // A new account arrives unnamed; the page then asks for its name.
              void signIn.start({ provider: props.provider, instanceId: props.inst.id }, (all) =>
                all.some((a) => !before.has(a.id)),
              )();
              return;
            }
            void st.run(async () => {
              await admin.bindAccount({ instanceId: props.inst.id, provider: props.provider, accountId: v });
              await props.reload();
            })();
          }}
        >
          <option value="" selected={!props.accountId}>
            None chosen
          </option>
          <For each={accounts()}>
            {(a) => (
              <option value={a.id} selected={a.id === props.accountId}>
                {a.label}
                {a.identity ? ` (${a.identity})` : ""}
              </option>
            )}
          </For>
          <option value={NEW} disabled={!p()?.clientSet}>
            + Sign in to another {title()} account{p()?.clientSet ? "" : " (register its app in Settings first)"}
          </option>
        </select>
      </label>
      <signIn.View />
      <st.View />
    </div>
  );
}

/** A new account can't be used until it has a name. */
function NameAccount(props: { a: Account; d: HomeData; reload: () => Promise<void> }) {
  const [name, setName] = createSignal("");
  const st = createStatus();
  const title = () => props.d.providers.find((p) => p.name === props.a.provider)?.title ?? props.a.provider;
  const forWhat = () =>
    props.a.instances
      .map((id) => {
        const inst = props.d.instances.find((i) => i.id === id);
        return inst ? inst.label || props.d.widgets.find((w) => w.name === inst.widget)?.title || inst.widget : id;
      })
      .join(", ");
  return (
    <form
      class={`${ui.fieldset} w-full`}
      onSubmit={(ev) => {
        ev.preventDefault();
        void st.run(async () => {
          await admin.labelAccount({ accountId: props.a.id, label: name() });
          history.replaceState(null, "", location.pathname);
          await props.reload();
        })();
      }}
    >
      <p>
        <strong>Name this {title()} account</strong>
        {props.a.identity ? ` (${props.a.identity})` : ""}.{" "}
        {forWhat() ? `It's for ${forWhat()}, which starts once it has a name.` : ""}
      </p>
      <input
        class={ui.input}
        value={name()}
        onInput={(e) => setName(e.currentTarget.value)}
        maxLength={60}
        placeholder="Work, Personal…"
        required
        autofocus
        aria-label="Account name"
      />
      <div class="mt-2 flex gap-2">
        <button class={ui.btn.small} disabled={st.busy() || !name().trim()}>
          Save name
        </button>
        <button
          type="button"
          class={ui.btn.secondary}
          disabled={st.busy()}
          onClick={st.run(async () => {
            if (!confirm("Discard this sign-in? It's revoked with the provider.")) return;
            await admin.deleteAccount({ accountId: props.a.id });
            await props.reload();
          })}
        >
          Discard
        </button>
      </div>
      <st.View />
    </form>
  );
}

/** One of the display's accounts: its sign-in and the widgets using it. */
function AccountItem(props: { a: Account; p: ProviderInfo; d: HomeData; reload: () => Promise<void> }) {
  const { a, p } = props;
  const st = createStatus();
  const signIn = createSignIn(st, props.reload);
  const needs = accountNeedsSignIn(a);
  const users = a.instances
    .map((id) => {
      const inst = props.d.instances.find((i) => i.id === id);
      return inst ? inst.label || props.d.widgets.find((w) => w.name === inst.widget)?.title || inst.widget : id;
    })
    .join(", ");
  return (
    <li class={ui.item}>
      <div>
        <strong>{accountName(a, props.d.providers)}</strong>
        <span class={ui.muted}>
          {a.identity ? ` ${a.identity} ·` : ""}{" "}
          {a.signedIn
            ? needs
              ? `signed in, but ${a.message.toLowerCase()}`
              : "signed in"
            : (a.message || "signed out").toLowerCase()}
        </span>
      </div>
      <p class={ui.muted}>Used by: {users || "nothing"}</p>
      <Show when={a.wanted.length || a.granted.length}>
        <p class="font-mono text-xs/snug break-all">
          <Show when={a.granted.length}>
            granted: {a.granted.join(" ")}
            <br />
          </Show>
          needs: {a.wanted.join(" ") || "nothing"}
        </p>
      </Show>
      <Show when={a.error}>
        <p class={`text-sm ${ui.error}`}>{a.error}</p>
      </Show>
      <div class="flex flex-wrap gap-2">
        <Show when={needs}>
          <button
            class={ui.btn.small}
            disabled={st.busy() || !p.clientSet}
            onClick={signIn.start({ provider: a.provider, accountId: a.id }, (all) => {
              const me = all.find((x) => x.id === a.id);
              return !!me?.signedIn && me.wanted.every((s) => me.granted.includes(s));
            })}
          >
            Sign in{a.identity ? ` as ${a.identity}` : ""}
          </button>
        </Show>
        <button
          class={ui.btn.secondary}
          disabled={st.busy()}
          onClick={st.run(async () => {
            const name = prompt("Name", a.label);
            if (!name) return;
            await admin.labelAccount({ accountId: a.id, label: name });
            await props.reload();
          })}
        >
          Rename
        </button>
        <Show when={a.signedIn}>
          <button
            class={ui.btn.danger}
            disabled={st.busy()}
            onClick={st.run(async () => {
              if (
                !confirm(
                  `Sign ${accountName(a, props.d.providers)} out? Widgets using it stop until it's signed in again.`,
                )
              )
                return;
              await admin.signOut({ accountId: a.id });
              await props.reload();
            })}
          >
            Sign out
          </button>
        </Show>
        <button
          class={ui.btn.danger}
          disabled={st.busy()}
          onClick={st.run(async () => {
            if (
              !confirm(
                `Delete ${accountName(a, props.d.providers)}? It's revoked, and widgets using it wait for another account.`,
              )
            )
              return;
            await admin.deleteAccount({ accountId: a.id });
            await props.reload();
          })}
        >
          Delete
        </button>
      </div>
      <signIn.View />
      <st.View />
    </li>
  );
}

/**
 * A provider's OAuth client: registered once, by whoever runs this, with the
 * provider, and shared by all its accounts here.
 */
function ProviderItem(props: { p: ProviderInfo; reload: () => Promise<void> }) {
  const p = props.p;
  const [id, setId] = createSignal("");
  const [secret, setSecret] = createSignal("");
  const st = createStatus();
  return (
    <details class={`${ui.fieldset} w-full`} open={!p.clientSet}>
      <summary class="cursor-pointer font-semibold">
        {p.title} app {p.clientSet ? (p.secretSet ? "· registered" : "· client ID only") : "· not registered yet"}
      </summary>
      <Show when={p.error}>
        <p class={`text-sm ${ui.error}`}>{p.error}</p>
      </Show>
      <Show when={p.clientHelp}>
        <p class={ui.muted}>{p.clientHelp}</p>
      </Show>
      <Show when={p.redirectUri}>
        <p class={ui.muted}>
          Redirect URI to register with {p.title}: <code class="break-all">{p.redirectUri}</code>
        </p>
      </Show>
      <input
        class={ui.input}
        value={id()}
        onInput={(e) => setId(e.currentTarget.value)}
        placeholder={p.clientSet ? "Client ID registered. Enter a new one to replace it." : "Client ID"}
        autocomplete="off"
        aria-label={`${p.title} client ID`}
      />
      <input
        class={ui.input}
        type="password"
        value={secret()}
        onInput={(e) => setSecret(e.currentTarget.value)}
        autocomplete="new-password"
        placeholder={p.secretSet ? "Client secret stored. Enter a new one to replace it." : "Client secret"}
        aria-label={`${p.title} client secret`}
      />
      <button
        class={`${ui.btn.small} mt-2`}
        disabled={st.busy()}
        onClick={st.run(async (say) => {
          await admin.setProviderClient({ provider: p.name, clientId: id(), clientSecret: secret() });
          setId("");
          setSecret("");
          say("Saved.");
          await props.reload();
        })}
      >
        Save client
      </button>
      <st.View />
    </details>
  );
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
    case InstanceStatus_State.NEEDS_SIGN_IN:
      return message ? `needs sign-in: ${message}` : "needs sign-in";
    default:
      return "unknown";
  }
}

function deviceGuess(ua: string): string {
  if (ua.includes("iPhone")) return "iPhone";
  if (ua.includes("iPad")) return "iPad";
  if (ua.includes("Android")) return ua.includes("Mobile") ? "Android phone" : "Android tablet";
  if (ua.includes("Macintosh")) return "Mac";
  if (ua.includes("Windows")) return "Windows PC";
  if (ua.includes("CrOS")) return "Chromebook";
  if (ua.includes("Linux")) return "Linux";
  return "Screen";
}

render(() => <App />, root);
