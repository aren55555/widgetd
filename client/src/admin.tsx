// The admin pages a phone opens from a QR code:
//
//   <base>enroll/<token>   create the first passkey
//   <base>pair/<id>        approve a screen
//   <base>configure/<t>    set up or change one widget instance (QR ticket)
//   <base>edit/<id>        the same, from the admin page
//   <base>admin            instances, network access, screens, passkeys
//
// Solid components, styled with Tailwind (lib/ui.ts). The settings form is
// generated from the widget's schema (lib/ConfigForm.tsx).

import { ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { create, createFileRegistry, fromBinary, toBinary } from "@bufbuild/protobuf";
import { FileDescriptorSetSchema } from "@bufbuild/protobuf/wkt";
import {
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
import { widgetIcon } from "./lib/icon.ts";
import { sameChoices, type Choices, type Secrets } from "./lib/form.ts";
import * as ui from "./lib/ui.ts";

const base = new URL(".", import.meta.url).pathname; // the bundle sits at <base>admin.js
const transport = createConnectTransport({ baseUrl: location.origin, useBinaryFormat: true });
const auth = createClient(AuthService, transport);
const admin = createClient(WidgetAdminService, transport);

const root = document.getElementById("app")!;
root.className = "mx-auto flex max-w-md flex-col gap-4 px-5 py-8";
render(() => <App />, root);

function App() {
  if (!window.isSecureContext || !("PublicKeyCredential" in window)) {
    // eslint-disable-next-line solid/components-return-once -- the condition never changes
    return (
      <Page title="Can't use passkeys here">
        <p>Open this page over HTTPS, in a browser that supports passkeys.</p>
      </Page>
    );
  }
  const [section, arg = ""] = location.pathname.slice(base.length).split("/");
  return (
    <ErrorBoundary
      fallback={(e) => (
        <Page title="Something went wrong">
          <p class={ui.error}>{errorText(e)}</p>
        </Page>
      )}
    >
      <Suspense>
        <Switch
          fallback={
            <RequireAdmin>
              <AdminHome />
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
              <Configure target={{ instanceId: decodeURIComponent(arg) }} />
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

function AdminLink(props: { children: JSX.Element }) {
  return (
    <a class={ui.link} href={`${base}admin`}>
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
  const [name, setName] = createSignal<string>();
  const [approved, setApproved] = createSignal<string>();
  const st = createStatus();
  return (
    <Show when={pairing()}>
      {(p) => {
        const screenName = () => name() ?? deviceGuess(p().userAgent);
        return (
          <Show
            when={approved() === undefined}
            fallback={
              <Page title="Approved">
                <p>{approved()} can now show widgets.</p>
                <AdminLink>Paired screens</AdminLink>
              </Page>
            }
          >
            <Page title="Pair a screen">
              <p>Check the screen shows this code:</p>
              <p class={ui.code}>{p().code}</p>
              <p class={ui.muted}>{p().userAgent || "unknown browser"}</p>
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
              <button
                class={ui.btn.primary}
                disabled={st.busy()}
                onClick={st.run(async () => {
                  const deviceName = screenName().trim();
                  await auth.approvePairing({ pairingId: props.pairingId, deviceName });
                  setApproved(deviceName);
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

function Configure(props: { target: ConfigTarget }) {
  const [form] = createResource(() => admin.getConfigForm(props.target));
  return <Show when={form()}>{(f) => <ConfigEditor target={props.target} form={f()} />}</Show>;
}

function ConfigEditor(props: { target: ConfigTarget; form: GetConfigFormResponse }) {
  const { target, form } = props;
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
    setSaved(true);
  });

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
        {"instanceId" in target && (
          <p>
            <AdminLink>← All widgets</AdminLink>
          </p>
        )}
        <NetworkNote w={w} />
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

// ---- admin home ----

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

function AdminHome() {
  const [data, { mutate }] = createResource(loadHome);
  // After a change, load again in place (a failure shows on the button
  // that asked, not as a broken page).
  const reload = async () => {
    mutate(await loadHome());
  };
  const passkey = createStatus();
  return (
    <Show when={data()}>
      {(d) => (
        <Page title="Widgets">
          <For each={d().accounts.filter((a) => !a.label)}>{(a) => <NameAccount a={a} d={d()} reload={reload} />}</For>
          <ul class={ui.list}>
            <For
              each={d().instances}
              fallback={<li class={`py-3 ${ui.muted}`}>No widgets yet. Add one from a screen.</li>}
            >
              {(inst) => (
                <InstanceItem
                  inst={inst}
                  widget={d().widgets.find((w) => w.name === inst.widget)}
                  d={d()}
                  reload={reload}
                />
              )}
            </For>
          </ul>

          <Show when={d().providers.length}>
            <h2 class={H2}>Accounts</h2>
            <p class={ui.muted}>
              Sign an account in from a widget above, and choose for each widget which account it uses. Each account is
              signed in for exactly what the widgets using it need.
            </p>
            <For each={d().providers}>
              {(p) => (
                <>
                  <ul class={ui.list}>
                    <For each={d().accounts.filter((a) => a.provider === p.name && a.label)}>
                      {(a) => <AccountItem a={a} p={p} d={d()} reload={reload} />}
                    </For>
                  </ul>
                  <ProviderItem p={p} reload={reload} />
                </>
              )}
            </For>
          </Show>

          <h2 class={H2}>Network access</h2>
          <ul class={ui.list}>
            <For each={d().widgets.filter((w) => w.requestedHosts.length)}>{(w) => <NetworkGrant w={w} />}</For>
          </ul>

          <h2 class={H2}>Screens</h2>
          <ul class={ui.list}>
            <For
              each={d().devices}
              fallback={
                <li class={`py-3 ${ui.muted}`}>No screens paired yet. Open a board on a screen and scan its code.</li>
              }
            >
              {(dev) => <ScreenItem device={dev} reload={reload} />}
            </For>
          </ul>

          <h2 class={H2}>Passkeys</h2>
          <button
            class={ui.btn.primary}
            disabled={passkey.busy()}
            onClick={passkey.run(async (say) => {
              await createPasskey("", deviceGuess(navigator.userAgent));
              say("Passkey added.");
            })}
          >
            Add another passkey
          </button>
          <passkey.View />
        </Page>
      )}
    </Show>
  );
}

type Fetch = Awaited<ReturnType<typeof admin.getInstanceActivity>>["fetches"][number];

function InstanceItem(props: {
  inst: InstanceInfo;
  widget: WidgetInfo | undefined;
  d: HomeData;
  reload: () => Promise<void>;
}) {
  const { inst, widget, reload } = props;
  const title = inst.label || widget?.title || inst.widget;
  const state = inst.status?.state;
  const st = createStatus();
  const [log, setLog] = createSignal<Fetch[]>();

  return (
    <li class={ui.item}>
      <div>
        <strong class="inline-flex items-center gap-2 align-middle">
          <WidgetIcon icon={widget?.icon} />
          {title}
        </strong>
        <span class={ui.muted}>
          {" "}
          {inst.widget} · {stateText(state, inst.status?.message)}
        </span>
        <Show when={inst.status?.warning}>
          <p class="text-sm text-amber-600">⚠ {inst.status?.warning}</p>
        </Show>
      </div>
      <For each={inst.bindings}>
        {(b) => <BindingPicker inst={inst} provider={b.provider} accountId={b.accountId} d={props.d} reload={reload} />}
      </For>
      <div class="flex flex-wrap gap-2">
        <button class={ui.btn.small} onClick={() => location.assign(`${base}edit/${encodeURIComponent(inst.id)}`)}>
          {state === InstanceStatus_State.NEEDS_CONFIG ? "Set up" : "Settings"}
        </button>
        <button
          class={ui.btn.small}
          disabled={st.busy()}
          onClick={st.run(async () => {
            const name = prompt("Name", inst.label);
            if (name === null) return;
            await admin.renameInstance({ instanceId: inst.id, label: name });
            await reload();
          })}
        >
          Rename
        </button>
        <button
          class={ui.btn.small}
          disabled={st.busy()}
          onClick={st.run(async () => {
            const { fetches } = await admin.getInstanceActivity({ instanceId: inst.id });
            setLog(fetches.slice().reverse());
          })}
        >
          Network log
        </button>
        <button
          class={ui.btn.danger}
          disabled={st.busy()}
          onClick={st.run(async () => {
            if (!confirm(`Delete ${title}? Its saved data goes too.`)) return;
            await admin.deleteInstance({ instanceId: inst.id });
            await reload();
          })}
        >
          Delete
        </button>
      </div>
      <Show when={log()}>
        {(l) => (
          <Show when={l().length} fallback={<p class="text-sm">No requests yet.</p>}>
            <ul class="list-disc pl-4 font-mono text-xs/snug break-all">
              <For each={l()}>
                {(f) => (
                  <li>{`${f.method} ${f.host}${f.path} → ${f.denied ? `refused: ${f.denied}` : f.error ? f.error : f.status}`}</li>
                )}
              </For>
            </ul>
          </Show>
        )}
      </Show>
      <st.View />
    </li>
  );
}

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

function ScreenItem(props: { device: Device; reload: () => Promise<void> }) {
  const d = props.device;
  const st = createStatus();
  return (
    <li class={ui.item}>
      <div>
        <strong>{d.name}</strong>
        <span class={ui.muted}> last seen {d.lastSeen ? timestampDate(d.lastSeen).toLocaleString() : "never"}</span>
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
  const NEW = "\u0000new";
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
            + Sign in to another {title()} account{p()?.clientSet ? "" : " (register a client below first)"}
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
  const needs = a.wanted.length > 0 && (!a.signedIn || a.wanted.some((s) => !a.granted.includes(s)));
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
