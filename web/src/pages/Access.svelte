<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { toast, currentServer } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';
  import { setClientNames, clientName, clientTitle } from '../lib/names.svelte.js';

  let state = $state(null); // last answer from the agent
  let policy = $state(null); // edited copy
  let saved = $state(''); // JSON of the saved policy
  let loadError = $state('');
  let busy = $state(false);
  let clients = $state([]);
  let net = $state(null);
  let showRules = $state(false);

  const isWG = ['wireguard', 'amneziawg'].includes(currentServer()?.type);

  // WireGuard: every client has a fixed address; build the same shape as
  // OpenVPN's /network from the client list.
  function wgNetwork(peers, fw) {
    const n = { static_ips: {}, client_lans: {}, routes: [] };
    if (fw?.subnet) n.server_ip = fw.subnet.replace(/\.\d+\/\d+$/, '.1');
    for (const p of peers) {
      n.static_ips[p.name] = p.address;
      if (p.lan?.length) n.client_lans[p.name] = p.lan;
    }
    return n;
  }

  async function loadNetwork(fw) {
    if (!isWG) return api('/network').catch(() => null);
    return wgNetwork(await api('/peers').catch(() => []), fw);
  }

  async function load() {
    try {
      if (isWG) {
        const [fw, peers] = await Promise.all([api('/firewall'), api('/peers').catch(() => [])]);
        take(fw);
        setClientNames([]);
        clients = peers.filter((p) => !p.disabled).map((p) => p.name).sort();
        net = wgNetwork(peers, fw);
      } else {
        const [fw, cs, n] = await Promise.all([api('/firewall'), api('/certs').catch(() => []), api('/network').catch(() => null)]);
        take(fw);
        setClientNames(cs);
        clients = cs.filter((c) => c.current && c.kind !== 'server' && c.status !== 'revoked').map((c) => c.name).sort((a, b) => clientName(a).localeCompare(clientName(b)));
        net = n;
      }
      loadError = '';
    } catch (e) {
      loadError = e.message;
    }
  }
  function take(fw) {
    state = fw;
    policy = structuredClone(fw.policy);
    saved = JSON.stringify(fw.policy);
  }
  onMount(load);

  const dirty = $derived(policy && JSON.stringify(policy) !== saved);
  const status = $derived.by(() => {
    if (!state) return null;
    if (state.mode === 'off') return { cls: 'off', text: 'Not managed by trapdoor (TD_FIREWALL=off)' };
    if (state.error) return { cls: 'bad', text: state.error };
    if (!state.policy.enabled) return { cls: 'off', text: 'Off: clients can reach everything the server routes' };
    return state.applied ? { cls: 'ok', text: 'Active' } : { cls: 'warn', text: 'Waiting to be applied' };
  });

  async function save() {
    busy = true;
    try {
      const fw = await api('/firewall', { method: 'PUT', body: policy });
      take(fw);
      if (fw.error) toast(fw.error, 'bad');
      else toast(fw.policy.enabled ? 'Access rules saved and active.' : 'Access rules saved. They are off.');
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      busy = false;
    }
  }

  async function assign() {
    busy = true;
    try {
      const names = state.missing;
      take(await api('/firewall/addresses', { method: 'POST', body: { names } }));
      toast(`Fixed addresses given to ${names.join(', ')}. The rules apply when ${names.length === 1 ? 'it reconnects' : 'they reconnect'}.`);
      net = (await loadNetwork(state)) ?? net;
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      busy = false;
    }
  }

  // Rule editor
  const blank = () => ({ client: '', to: '', proto: 'any', ports: '', action: 'allow', note: '' });
  let edit = $state({ open: false, index: -1, rule: blank(), error: '' });
  function openRule(i) {
    edit = { open: true, index: i, rule: i < 0 ? blank() : { ...blank(), ...policy.rules[i] }, error: '' };
  }
  const netRe = /^(\d{1,3}\.){3}\d{1,3}(\/\d{1,2})?$/;
  const portRe = /^\d{1,5}(-\d{1,5})?(,\d{1,5}(-\d{1,5})?)*$/;
  const usesPorts = $derived(['tcp', 'udp', 'tcp+udp'].includes(edit.rule.proto));
  function saveRule(e) {
    e.preventDefault();
    const r = { ...edit.rule, to: edit.rule.to.trim(), ports: usesPorts ? edit.rule.ports.replace(/\s/g, '') : '', note: edit.rule.note.trim() };
    if (r.to && !netRe.test(r.to)) return (edit.error = `${r.to} is not an IPv4 network like 192.168.1.0/24 or an address`);
    if (r.ports && !portRe.test(r.ports)) return (edit.error = 'Ports: numbers, ranges like 8000-8100, separated by commas');
    for (const k of ['client', 'to', 'ports', 'note']) if (!r[k]) delete r[k];
    if (edit.index < 0) policy.rules = [...policy.rules, r];
    else policy.rules[edit.index] = r;
    edit.open = false;
  }
  function move(i, d) {
    const rs = [...policy.rules];
    [rs[i], rs[i + d]] = [rs[i + d], rs[i]];
    policy.rules = rs;
  }
  const remove = (i) => (policy.rules = policy.rules.filter((_, j) => j !== i));

  const suggestions = $derived.by(() => {
    const out = [];
    if (state?.subnet) out.push({ label: 'VPN network', value: state.subnet });
    if (net?.server_ip) out.push({ label: 'The server', value: net.server_ip });
    for (const r of net?.routes ?? []) out.push({ label: r, value: r });
    for (const [c, lans] of Object.entries(net?.client_lans ?? {})) for (const l of lans) out.push({ label: `${l} (behind ${c})`, value: l });
    // a client LAN usually also has a server "route": show it once, with its client
    const seen = new Map();
    for (const s of out) seen.set(s.value, s);
    return [...seen.values()];
  });

  const what = (r) => {
    if (!r.proto || r.proto === 'any') return 'all traffic';
    if (r.proto === 'icmp') return 'ping (ICMP)';
    const p = r.proto === 'tcp+udp' ? 'TCP/UDP' : r.proto.toUpperCase();
    return r.ports ? `${p} ${r.ports.replaceAll(',', ', ')}` : `all ${p}`;
  };
  const ipOf = (name) => net?.static_ips?.[name];
</script>

<header class="head">
  <div>
    <h1>Access</h1>
    <p class="muted">Which networks VPN clients can reach. Rules are checked from the top; the first that matches decides.</p>
  </div>
</header>

{#if loadError}
  <div class="banner bad" role="alert">Could not load the access rules: {loadError}</div>
{:else if policy}
  <section class="panel top">
    <div class="state">
      <label class="switch">
        <input type="checkbox" bind:checked={policy.enabled} disabled={state.mode === 'off'} />
        <span>Use access rules</span>
      </label>
      <span class="pill {status.cls}">{status.text}</span>
    </div>
    <fieldset class="defaults" disabled={!policy.enabled}>
      <legend>When no rule matches</legend>
      <label class:chosen={policy.default === 'allow'}>
        <input type="radio" bind:group={policy.default} value="allow" />
        <span><strong>Allow</strong><span class="hint">Clients reach everything except what a rule blocks.</span></span>
      </label>
      <label class:chosen={policy.default === 'deny'}>
        <input type="radio" bind:group={policy.default} value="deny" />
        <span><strong>Block</strong><span class="hint">Clients reach only what a rule allows. Replies to allowed traffic always pass.</span></span>
      </label>
    </fieldset>
    {#if policy.enabled && policy.default === 'deny' && saved && JSON.parse(saved).default !== 'deny'}
      <p class="banner warn">If you open this page through the VPN, add a rule that allows it first, or you lock yourself out.</p>
    {/if}
  </section>

  {#if state.missing.length}
    <div class="banner warn missing">
      <span>
        {state.missing.join(', ')}
        {state.missing.length === 1 ? 'has' : 'have'} no fixed VPN address, so {state.missing.length === 1 ? 'its rules are' : 'their rules are'} not active.
      </span>
      <button class="btn small" onclick={assign} disabled={busy || dirty} title={dirty ? 'Save your changes first' : ''}>Give fixed addresses</button>
    </div>
  {/if}
  {#each state.warnings.filter((w) => !w.includes('no fixed VPN address')) as w (w)}
    <p class="banner warn">{w}</p>
  {/each}

  <section class="panel rules" class:off={!policy.enabled}>
    <div class="panel-head">
      <h2>Rules</h2>
      <button class="btn small" onclick={() => openRule(-1)}>Add rule</button>
    </div>
    <ol>
      {#each policy.rules as r, i (i)}
        <li class="rule">
          <span class="n">{i + 1}</span>
          <span class="action {r.action}">{r.action === 'allow' ? 'Allow' : 'Block'}</span>
          <span class="desc">
            <span class="who">
              {#if r.client}<strong title={clientTitle(r.client)}>{clientName(r.client)}</strong>{#if ipOf(r.client)}<span class="tag mono faint">{ipOf(r.client)}</span>{:else}<span class="tag warn-text">no fixed address</span>{/if}{:else}<strong>All clients</strong>{/if}
            </span>
            <span class="muted">to <span class="mono">{r.to ? r.to.replace(/\/32$/, '') : 'anywhere'}</span>, {what(r)}</span>
            {#if r.note}<span class="note">{r.note}</span>{/if}
          </span>
          <span class="tools">
            <button class="icon" aria-label="Move rule {i + 1} up" disabled={i === 0} onclick={() => move(i, -1)}>↑</button>
            <button class="icon" aria-label="Move rule {i + 1} down" disabled={i === policy.rules.length - 1} onclick={() => move(i, 1)}>↓</button>
            <button class="btn small ghost" onclick={() => openRule(i)}>Edit</button>
            <button class="btn small ghost danger" onclick={() => remove(i)}>Delete</button>
          </span>
        </li>
      {/each}
      <li class="rule last">
        <span class="n"></span>
        <span class="action {policy.default}">{policy.default === 'allow' ? 'Allow' : 'Block'}</span>
        <span class="desc"><span class="muted">everything else</span></span>
      </li>
    </ol>
    {#if !policy.rules.length}<p class="empty-hint">No rules yet. Add one, for example: allow all clients to reach the server's LAN, block one client from the internet.</p>{/if}
  </section>

  <details class="generated" bind:open={showRules}>
    <summary>Generated iptables rules</summary>
    <pre class="mono">{state.rules}</pre>
    <p class="hint">
      {#if isWG}Chain TD-{currentServer()?.id}, used for traffic coming from this server's clients (INPUT and FORWARD on its interface).{:else}Chain TD-FWD, used for traffic coming from VPN clients (INPUT and FORWARD on tun devices).{/if}
      Shown as last saved.
    </p>
  </details>

  {#if dirty}
    <div class="savebar" role="region" aria-label="Unsaved changes">
      <span>Unsaved changes</span>
      <button class="btn" onclick={() => (policy = JSON.parse(saved))} disabled={busy}>Discard</button>
      <button class="btn primary" onclick={save} disabled={busy}>{busy ? 'Applying…' : 'Save and apply'}</button>
    </div>
  {/if}
{/if}

<Dialog bind:open={edit.open} title={edit.index < 0 ? 'Add rule' : `Edit rule ${edit.index + 1}`}>
  <form id="ruleform" class="form" onsubmit={saveRule}>
    {#if edit.error}<div class="banner bad" role="alert">{edit.error}</div>{/if}
    <div class="seg" role="radiogroup" aria-label="Action">
      <label class:chosen={edit.rule.action === 'allow'}><input type="radio" bind:group={edit.rule.action} value="allow" /> Allow</label>
      <label class:chosen={edit.rule.action === 'deny'}><input type="radio" bind:group={edit.rule.action} value="deny" /> Block</label>
    </div>
    <label class="field">
      <span>From</span>
      <select bind:value={edit.rule.client}>
        <option value="">All clients</option>
        {#each clients as c (c)}<option value={c}>{clientName(c)}</option>{/each}
        {#if edit.rule.client && !clients.includes(edit.rule.client)}<option value={edit.rule.client}>{clientName(edit.rule.client)}</option>{/if}
      </select>
      {#if edit.rule.client && !ipOf(edit.rule.client)}<span class="hint">This client needs a fixed VPN address; you can give it one after saving.</span>{/if}
    </label>
    <label class="field">
      <span>To</span>
      <input bind:value={edit.rule.to} placeholder="Anywhere" spellcheck="false" autocomplete="off" />
      {#if suggestions.length}
        <span class="sugg">
          <button type="button" class="chipbtn" onclick={() => (edit.rule.to = '')}>Anywhere</button>
          {#each suggestions as s (s.value)}<button type="button" class="chipbtn" onclick={() => (edit.rule.to = s.value)}>{s.label}</button>{/each}
        </span>
      {/if}
    </label>
    <div class="row2">
      <label class="field">
        <span>Traffic</span>
        <select bind:value={edit.rule.proto}>
          <option value="any">All traffic</option>
          <option value="tcp">TCP</option>
          <option value="udp">UDP</option>
          <option value="tcp+udp">TCP and UDP</option>
          <option value="icmp">Ping (ICMP)</option>
        </select>
      </label>
      <label class="field">
        <span>Ports</span>
        <input bind:value={edit.rule.ports} placeholder={usesPorts ? 'All, or 22, 80, 8000-8100' : '—'} disabled={!usesPorts} inputmode="numeric" />
      </label>
    </div>
    <label class="field">
      <span>Note (optional)</span>
      <input bind:value={edit.rule.note} maxlength="200" placeholder="e.g. NAS web interface" />
    </label>
  </form>
  {#snippet actions()}
    <button class="btn" onclick={() => (edit.open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="ruleform">{edit.index < 0 ? 'Add rule' : 'Keep changes'}</button>
  {/snippet}
</Dialog>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); max-width: 68ch; }
  .banner { margin-bottom: var(--space-3); }
  .top { display: grid; gap: var(--space-4); padding: var(--space-5); margin-bottom: var(--space-4); }
  .top .banner { margin: 0; }
  .state { display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap; }
  .switch { display: flex; align-items: center; gap: var(--space-2); font-weight: 500; }
  .switch input, .defaults input, .seg input { height: auto; }
  .defaults { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); border: 0; margin: 0; padding: 0; }
  .defaults legend { padding: 0; margin-bottom: var(--space-2); font-size: var(--step--1); font-weight: 500; color: var(--ink-2); }
  .defaults label { display: flex; gap: var(--space-3); align-items: baseline; padding: var(--space-3) var(--space-4); border: 1px solid var(--line); border-radius: var(--radius-s); cursor: pointer; }
  .defaults label.chosen { border-color: var(--accent); box-shadow: inset 0 0 0 1px var(--accent); }
  .defaults label > span { display: grid; gap: 2px; }
  .defaults:disabled { opacity: 0.55; }
  .missing { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }

  .rules.off ol { opacity: 0.55; }
  ol { list-style: none; margin: 0; padding: 0; }
  .rule { display: grid; grid-template-columns: 28px 64px minmax(0, 1fr) auto; gap: var(--space-3); align-items: center; padding: var(--space-3) var(--space-5); border-top: 1px solid var(--line); }
  .rule.last { background: var(--sunken); border-radius: 0 0 var(--radius-m) var(--radius-m); }
  .n { color: var(--ink-3); font-variant-numeric: tabular-nums; font-size: var(--step--1); text-align: right; }
  .action { justify-self: start; padding: 1px 8px; border-radius: var(--radius-s); font-size: var(--step--1); font-weight: 600; }
  .action.allow { color: var(--ok); background: var(--ok-soft); }
  .action.deny { color: var(--bad); background: var(--bad-soft); }
  .desc { display: grid; gap: 2px; min-width: 0; }
  .desc .muted { overflow-wrap: anywhere; }
  .note { font-size: var(--step--1); color: var(--ink-3); }
  .warn-text { color: var(--warn); font-size: var(--step--1); }
  .tag { margin-left: var(--space-2); font-size: var(--step--1); }
  .tools { display: flex; align-items: center; gap: 2px; }
  .icon { width: 28px; height: 28px; border: 0; border-radius: var(--radius-s); background: none; color: var(--ink-2); cursor: pointer; font-size: 15px; }
  .icon:hover:not(:disabled) { background: var(--sunken); color: var(--ink); }
  .icon:disabled { opacity: 0.3; cursor: default; }
  .empty-hint { padding: 0 var(--space-5) var(--space-4); color: var(--ink-3); font-size: var(--step--1); }

  .generated { margin-top: var(--space-4); }
  .generated summary { cursor: pointer; color: var(--ink-2); font-weight: 500; }
  .generated pre { margin: var(--space-3) 0 var(--space-2); padding: var(--space-3); border-radius: var(--radius-s); background: var(--sunken); font-size: 12.5px; line-height: 1.55; overflow-x: auto; }

  .savebar { position: sticky; bottom: var(--space-4); display: flex; align-items: center; justify-content: flex-end; gap: var(--space-2); margin-top: var(--space-4); padding: var(--space-3) var(--space-4); border: 1px solid var(--line); border-radius: var(--radius-m); background: var(--surface); box-shadow: var(--shadow); }
  .savebar span { margin-right: auto; font-weight: 500; }

  .form { display: grid; gap: var(--space-4); }
  .seg { display: inline-flex; justify-self: start; padding: 2px; border-radius: var(--radius-s); background: var(--sunken); }
  .seg label { display: flex; gap: 6px; align-items: center; padding: 4px 14px; border-radius: var(--radius-s); cursor: pointer; font-weight: 500; color: var(--ink-2); }
  .seg label.chosen { background: var(--surface); color: var(--ink); box-shadow: inset 0 0 0 1px var(--line); }
  .seg input { position: absolute; opacity: 0; pointer-events: none; }
  .seg label:has(input:focus-visible) { outline: 2px solid var(--accent); }
  .sugg { display: flex; flex-wrap: wrap; gap: 6px; }
  .chipbtn { padding: 2px 8px; border: 1px solid var(--line); border-radius: var(--radius-s); background: var(--surface); color: var(--ink-2); font: inherit; font-size: 12.5px; cursor: pointer; }
  .chipbtn:hover { border-color: var(--ink-3); color: var(--ink); }
  .row2 { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }

  @media (max-width: 720px) {
    .defaults, .row2 { grid-template-columns: 1fr; }
    .rule { grid-template-columns: 56px minmax(0, 1fr); padding: var(--space-3) var(--space-4); }
    .n { display: none; }
    .tools { grid-column: 1 / -1; justify-content: flex-end; }
    .missing { flex-direction: column; align-items: flex-start; }
  }
</style>
