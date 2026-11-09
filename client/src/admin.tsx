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
import { createResource, createSignal, ErrorBoundary, For, Match, onCleanup, Show, Suspense, Switch, type JSX } from "solid-js";
import { render } from "solid-js/web";

import { AuthService, Role, type Device } from "./gen/widgetd/v1/auth_pb.ts";
import { WidgetAdminService, type GetConfigFormResponse, type InstanceInfo, type WidgetInfo } from "./gen/widgetd/v1/admin_pb.ts";
import { InstanceStatus_State } from "./gen/widgetd/v1/stream_pb.ts";
import { createConfigForm, type ConfigForm } from "./lib/ConfigForm.tsx";
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
  if (!window.isSecureContext || !window.PublicKeyCredential) {
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

function Page(props: { title: string; children: JSX.Element }) {
  return (
    <>
      <h1 class="text-2xl font-bold">{props.title}</h1>
      {props.children}
    </>
  );
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
              Name <input class={ui.input} value={name()} onInput={(e) => setName(e.currentTarget.value)} maxLength={64} aria-label="Passkey name" />
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
                Name <input class={ui.input} value={screenName()} onInput={(e) => setName(e.currentTarget.value)} maxLength={64} aria-label="Screen name" />
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
        const next: Choices = new Map(res.fields.map((f) => [f.field, f.choices.map((c) => ({ value: c.value, label: c.label }))]));
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
        timer = setTimeout(refreshChoices, 400);
      },
    });
    onCleanup(() => clearTimeout(timer));
    void refreshChoices();
    config = { form: configForm, bytes: () => toBinary(desc, msg) };
  }

  const save = st.run(async (say) => {
    if (config && !config.form.validate()) return say("Fix the highlighted fields first.");
    const res = await admin.saveConfig({
      ...target,
      label: label().trim(),
      config: config ? config.bytes() : new Uint8Array(),
      clearSecrets: [...secrets.clear],
    });
    if (res.violations.length) {
      config?.form.validate(res.violations.map((v) => ({ field: v.field, message: v.message })));
      return say("The widget didn't accept that; see the fields above.");
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
      <Page title={w.title}>
        {"instanceId" in target && (
          <p>
            <AdminLink>← All widgets</AdminLink>
          </p>
        )}
        <NetworkNote w={w} />
        <label class="block">
          <span class={ui.label}>Name</span>
          <input class={ui.input} value={label()} onInput={(e) => setLabel(e.currentTarget.value)} maxLength={64} placeholder={w.title} aria-label="Name" />
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
    const reach = w.networkDisabled ? "Network access is turned off for this widget." : `Reaches: ${hosts.join(", ") || "nothing"}.`;
    return `${reach} ${w.networkWhy}`;
  };
  return <p class={ui.muted}>{text()}</p>;
}

// ---- admin home ----

type HomeData = { instances: InstanceInfo[]; widgets: WidgetInfo[]; devices: Device[] };

async function loadHome(): Promise<HomeData> {
  const [{ instances }, { widgets }, { devices }] = await Promise.all([admin.listInstances({}), admin.listWidgets({}), auth.listDevices({})]);
  return { instances, widgets, devices };
}

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
          <ul class={ui.list}>
            <For each={d().instances} fallback={<li class={`py-3 ${ui.muted}`}>No widgets yet. Add one from a screen.</li>}>
              {(inst) => <InstanceItem inst={inst} widget={d().widgets.find((w) => w.name === inst.widget)} reload={reload} />}
            </For>
          </ul>

          <Show when={d().widgets.some((w) => w.signInProvider)}>
            <h2 class={H2}>Sign-in apps</h2>
            <ul class={ui.list}>
              <For each={d().widgets.filter((w) => w.signInProvider)}>{(w) => <SignInApp w={w} />}</For>
            </ul>
          </Show>

          <h2 class={H2}>Network access</h2>
          <ul class={ui.list}>
            <For each={d().widgets.filter((w) => w.requestedHosts.length)}>{(w) => <NetworkGrant w={w} />}</For>
          </ul>

          <h2 class={H2}>Screens</h2>
          <ul class={ui.list}>
            <For each={d().devices} fallback={<li class={`py-3 ${ui.muted}`}>No screens paired yet. Open a board on a screen and scan its code.</li>}>
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
type SignInCode = { userCode: string; link: string; shown: string };

function InstanceItem(props: { inst: InstanceInfo; widget: WidgetInfo | undefined; reload: () => Promise<void> }) {
  const { inst, widget, reload } = props;
  const title = inst.label || widget?.title || inst.widget;
  const provider = widget?.signInProvider;
  const state = inst.status?.state;
  const needsSignIn = state === InstanceStatus_State.NEEDS_SIGN_IN;
  const st = createStatus();
  const [log, setLog] = createSignal<Fetch[]>();
  const [code, setCode] = createSignal<SignInCode>();

  // Signing in from this phone: show the provider's code, then watch for
  // the instance to leave NEEDS_SIGN_IN.
  let poll: ReturnType<typeof setTimeout> | undefined;
  onCleanup(() => clearTimeout(poll));
  const signInOrOut = st.run(async () => {
    if (!needsSignIn) {
      if (!confirm(`Sign ${title} out of ${provider}?`)) return;
      await admin.signOut({ instanceId: inst.id });
      return reload();
    }
    const s = await admin.beginSignIn({ instanceId: inst.id });
    setCode({ userCode: s.userCode, link: s.verificationUriComplete || s.verificationUri, shown: s.verificationUri.replace(/^https:\/\//, "") });
    const until = Date.now() + 10 * 60_000;
    const check = async () => {
      const { instances } = await admin.listInstances({});
      const me = instances.find((i) => i.id === inst.id);
      if (me && me.status?.state !== InstanceStatus_State.NEEDS_SIGN_IN) return reload();
      if (Date.now() < until) poll = setTimeout(check, 3000);
    };
    poll = setTimeout(check, 3000);
  });

  return (
    <li class={ui.item}>
      <div>
        <strong>{title}</strong>
        <span class={ui.muted}>
          {" "}
          {inst.widget} · {stateText(state, inst.status?.message)}
        </span>
      </div>
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
        <Show when={provider}>
          <button class={ui.btn.small} disabled={st.busy()} onClick={signInOrOut}>
            {needsSignIn ? `Sign in to ${provider}` : "Sign out"}
          </button>
        </Show>
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
      <Show when={code()}>
        {(c) => (
          <p class="text-sm">
            Enter <strong class="font-mono text-[2rem] leading-tight font-semibold tracking-widest">{c().userCode}</strong> at{" "}
            <a class={ui.link} href={c().link} target="_blank" rel="noopener">
              {c().shown}
            </a>
            . This page updates once you've approved it.
          </p>
        )}
      </Show>
      <Show when={log()}>
        {(l) => (
          <Show when={l().length} fallback={<p class="text-sm">No requests yet.</p>}>
            <ul class="list-disc pl-4 font-mono text-xs/snug break-all">
              <For each={l()}>{(f) => <li>{`${f.method} ${f.host}${f.path} → ${f.denied ? `refused: ${f.denied}` : f.error ? f.error : f.status}`}</li>}</For>
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
      <strong>{w.title}</strong>
      <p class={ui.muted}>{w.networkWhy}</p>
      <For each={w.requestedHosts}>
        {(host) => (
          <label class="flex items-center gap-2">
            <input type="checkbox" checked={on.has(host)} onChange={(e) => (e.currentTarget.checked ? on.add(host) : on.delete(host))} />
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
 * The OAuth app a widget signs in with: registered once per widget, by
 * whoever runs this, with the provider.
 */
function SignInApp(props: { w: WidgetInfo }) {
  const w = props.w;
  const [id, setId] = createSignal("");
  const [secret, setSecret] = createSignal("");
  const st = createStatus();
  return (
    <li class={ui.item}>
      <strong>{w.title}</strong>
      <p class={ui.muted}>
        Signs in to {w.signInProvider}. Register an OAuth app there (with device flow enabled) and enter its client ID. If its tokens expire, add its client secret too, so they can be renewed without signing in
        again.
      </p>
      <input
        class={ui.input}
        value={id()}
        onInput={(e) => setId(e.currentTarget.value)}
        placeholder={w.signInClientSet ? "Registered. Enter a new one to replace it." : "Client ID"}
        autocomplete="off"
        aria-label={`${w.signInProvider} client ID`}
      />
      <input
        class={ui.input}
        type="password"
        value={secret()}
        onInput={(e) => setSecret(e.currentTarget.value)}
        autocomplete="new-password"
        placeholder={w.signInSecretSet ? "Stored. Enter a new one to replace it." : "Client secret (to renew expiring tokens)"}
        aria-label={`${w.signInProvider} client secret`}
      />
      <button
        class={ui.btn.small}
        disabled={st.busy()}
        onClick={st.run(async (say) => {
          await admin.setSignInClient({ widget: w.name, clientId: id(), clientSecret: secret() });
          setId("");
          setSecret("");
          say("Saved.");
        })}
      >
        Save
      </button>
      <st.View />
    </li>
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
  if (/iPhone/.test(ua)) return "iPhone";
  if (/iPad/.test(ua)) return "iPad";
  if (/Android/.test(ua)) return /Mobile/.test(ua) ? "Android phone" : "Android tablet";
  if (/Macintosh/.test(ua)) return "Mac";
  if (/Windows/.test(ua)) return "Windows PC";
  if (/CrOS/.test(ua)) return "Chromebook";
  if (/Linux/.test(ua)) return "Linux";
  return "Screen";
}
