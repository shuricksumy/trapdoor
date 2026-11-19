<script>
  import { onMount } from 'svelte';
  import qrcode from 'qrcode-generator';
  import { api, apiURL, poll } from '../lib/api.js';
  import { bytes, relative } from '../lib/format.js';
  import { toast, currentServer } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';
  import { placeMenu } from '../lib/menu.js';
  import SendProfile from '../lib/SendProfile.svelte';

  const app = currentServer()?.type === 'amneziawg' ? 'AmneziaWG' : 'WireGuard';

  let peers = $state([]);
  let loadError = $state('');
  let loaded = $state(false);

  // Clients' email addresses (to send them their config).
  let emails = $state({});
  async function load() {
    try {
      peers = await api('/peers');
      api('/emails').then((m) => (emails = m)).catch(() => {});
      loadError = '';
    } catch (e) {
      loadError = e.message;
    } finally {
      loaded = true;
    }
  }
  onMount(() => poll(load, 8000));

  const rows = $derived([...peers].sort((a, b) => a.name.localeCompare(b.name)));

  function status(p) {
    if (p.disabled) return { tone: 'off', label: 'Disabled' };
    if (p.expired) return { tone: 'bad', label: 'Expired' };
    if (p.connected) return { tone: 'ok', label: 'Connected' };
    if (p.last_handshake && !p.last_handshake.startsWith('0001')) return { tone: 'warn', label: `Seen ${relative(p.last_handshake)}` };
    return { tone: 'off', label: 'Never connected' };
  }

  // Add (one or several) and edit: one form. Empty settings use the
  // server's client defaults (shown as placeholders).
  let defaults = $state(null);
  let formOpen = $state(false);
  let mode = $state('add'); // add | bulk | edit
  let editing = $state('');
  let form = $state({});
  let busy = $state(false);
  let formError = $state('');
  const list = (v) => v.split(/[\s,]+/).filter(Boolean);
  const nameOk = $derived(/^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$/.test(mode === 'bulk' ? `${form.prefix}-1` : form.name ?? ''));

  // Free addresses of the subnet for the address field's suggestions.
  const freeAddrs = $derived.by(() => {
    const sub = currentServer()?.subnet;
    if (!sub) return [];
    const [base, bits] = sub.split('/');
    const n = base.split('.').reduce((a, o) => a * 256 + Number(o), 0);
    const size = 2 ** (32 - Number(bits));
    const used = new Set(peers.map((p) => p.address));
    const out = [];
    for (let i = 2; i < size - 1 && out.length < 30; i++) {
      const v = n + i;
      const ip = [v >>> 24, (v >>> 16) & 255, (v >>> 8) & 255, v & 255].join('.');
      if (!used.has(ip)) out.push(ip);
    }
    return out;
  });

  async function loadDefaults() {
    try {
      defaults = await api('/settings');
    } catch {
      defaults = null;
    }
  }

  function blank() {
    return { name: '', prefix: 'guest', count: 5, notes: '', address: '', ownKey: false, publicKey: '', psk: defaults?.psk ?? true,
      lan: '', dns: '', allowed: '', mtu: '', keepalive: '', until: '' };
  }

  async function openCreate() {
    await loadDefaults();
    mode = 'add';
    form = blank();
    formError = '';
    formOpen = true;
  }

  async function openEdit(p) {
    await loadDefaults();
    mode = 'edit';
    editing = p.name;
    form = { ...blank(), notes: p.notes ?? '', address: p.address, psk: p.preshared_key, lan: (p.lan ?? []).join(', '),
      dns: (p.dns ?? []).join(', '), allowed: (p.allowed_ips ?? []).join(', '), mtu: p.mtu || '',
      keepalive: p.keepalive ?? '', until: p.expires_at ? localDate(p.expires_at) : '' };
    formError = '';
    formOpen = true;
  }

  // The end date is the end of that day, in this browser's time zone.
  const localDate = (t) => {
    const d = new Date(t);
    return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 10);
  };
  const today = localDate(new Date());
  const until = (t) => new Date(t).toLocaleDateString([], { dateStyle: 'medium' });

  function settingsBody() {
    const b = { notes: form.notes.trim() };
    if (form.until) b.expires_at = new Date(form.until + 'T23:59:59').toISOString();
    if (form.dns.trim()) b.dns = list(form.dns);
    if (form.allowed.trim()) b.allowed_ips = list(form.allowed);
    if (form.mtu) b.mtu = Number(form.mtu);
    if (form.keepalive !== '' && form.keepalive !== null) b.keepalive = Number(form.keepalive);
    return b;
  }

  async function submit(e) {
    e.preventDefault();
    busy = true;
    formError = '';
    try {
      if (mode === 'edit') {
        const body = { settings: settingsBody(), psk: form.psk, lan: list(form.lan) };
        if (form.address.trim()) body.address = form.address.trim();
        await api(`/peers/${encodeURIComponent(editing)}`, { method: 'PUT', body });
        formOpen = false;
        toast(`Saved ${editing}. Its device needs the new config if its settings changed.`);
        await load();
        return;
      }
      if (mode === 'bulk') {
        const added = await api('/peers/bulk', { method: 'POST', body: { prefix: form.prefix.trim(), count: Number(form.count), psk: form.psk, ...settingsBody() } });
        formOpen = false;
        toast(`Added ${added.length} clients: ${added.map((p) => p.name).join(', ')}.`);
        await load();
        return;
      }
      const body = { name: form.name, psk: form.psk, ...settingsBody() };
      if (form.address.trim()) body.address = form.address.trim();
      if (form.ownKey) body.public_key = form.publicKey.trim();
      const lan = list(form.lan);
      if (lan.length) body.lan = lan;
      const p = await api('/peers', { method: 'POST', body });
      formOpen = false;
      toast(`Added ${p.name}. ${p.own_key ? 'Put its private key into the config on the device.' : 'Scan the QR code or download the config to set up the device.'}`);
      await load();
      showConfig(p.name, p.own_key);
    } catch (err) {
      formError = err.message;
    } finally {
      busy = false;
    }
  }

  // Config and QR code
  let conf = $state({ open: false, name: '', text: '', error: '', ownKey: false });
  async function showConfig(name, ownKey = peers.find((p) => p.name === name)?.own_key ?? false) {
    conf = { open: true, name, text: '', error: '', ownKey };
    try {
      conf.text = await api(`/peers/${encodeURIComponent(name)}/conf`);
    } catch (e) {
      conf.error = e.message;
    }
  }
  const qrSvg = $derived.by(() => {
    if (!conf.text) return '';
    try {
      const qr = qrcode(0, 'L');
      qr.addData(conf.text);
      qr.make();
      return qr.createSvgTag({ cellSize: 4, margin: 2, scalable: true });
    } catch {
      return '';
    }
  });

  // Email the config (with its QR code as a GIF).
  let mailing = $state({ open: false, name: '', qr: '' });
  async function emailConfig(name) {
    let qr = '';
    try {
      const text = conf.name === name && conf.text ? conf.text : await api(`/peers/${encodeURIComponent(name)}/conf`);
      const code = qrcode(0, 'L');
      code.addData(text);
      code.make();
      qr = code.createDataURL(5, 20).split(',')[1] ?? '';
    } catch {
      /* without the QR code then */
    }
    conf.open = false;
    mailing = { open: true, name, qr };
  }

  // Disable / enable / delete
  async function setDisabled(p, disabled) {
    try {
      await api(`/peers/${encodeURIComponent(p.name)}`, { method: 'PUT', body: { disabled } });
      toast(disabled ? `Disabled ${p.name}. It is disconnected and cannot connect.` : `Enabled ${p.name}.`);
      await load();
    } catch (e) {
      toast(e.message, 'bad');
    }
  }

  let confirm = $state({ open: false, name: '' });
  async function remove() {
    try {
      await api(`/peers/${encodeURIComponent(confirm.name)}`, { method: 'DELETE' });
      toast(`Deleted ${confirm.name}.`);
      confirm.open = false;
      await load();
    } catch (e) {
      toast(e.message, 'bad');
    }
  }
</script>

<header class="head">
  <div>
    <h1>Clients</h1>
    <p class="muted">One entry per device. Scan its QR code in the {app} app, or download the config.</p>
  </div>
  <button class="btn primary" onclick={openCreate}>Add client</button>
</header>

{#if loadError}<div class="banner bad" role="alert">Could not load clients: {loadError}</div>{/if}

<section class="panel">
  {#if loaded && rows.length === 0}
    <div class="empty"><p>No clients yet. Add the first one to get a config for a phone or laptop.</p></div>
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Address</th>
            <th>Status</th>
            <th class="num hide-s">Traffic</th>
            <th><span class="sr">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each rows as p (p.name)}
            {@const st = status(p)}
            <tr class:old={p.disabled}>
              <td class="c-name">
                <div class="nm">
                  <span class="name">{p.name}{#if emails[p.name]}<span class="faint small email">{emails[p.name]}</span>{/if}</span>
                  {#if p.notes}<span class="faint small">{p.notes}</span>{/if}
                  {#if p.expires_at}<span class="small" class:faint={!p.expired} class:bad-text={p.expired}>{p.expired ? 'Ended' : 'Until'} {until(p.expires_at)}</span>{/if}

                  {#if p.lan?.length}<span class="faint small mono">LAN {p.lan.join(', ')}</span>{/if}
                </div>
              </td>
              <td class="mono c-addr">{p.address}</td>
              <td class="c-status"><span class="pill {st.tone}">{st.label}</span></td>
              <td class="num hide-s c-traffic">
                {#if p.bytes_received || p.bytes_sent}
                  <span title="Downloaded by the client">↓ {bytes(p.bytes_sent)}</span>
                  <span title="Uploaded by the client">↑ {bytes(p.bytes_received)}</span>
                {:else}
                  <span class="faint">–</span>
                {/if}
              </td>
              <td class="actions">
                {#if !p.disabled}<button class="btn small" onclick={() => showConfig(p.name)}>Show config</button>{/if}
                <button class="btn small ghost" class:warn-btn={!p.disabled} onclick={() => setDisabled(p, !p.disabled)}>{p.disabled ? 'Enable' : 'Disable'}</button>
                <details class="menu" ontoggle={placeMenu}>
                  <summary class="btn small ghost" aria-label="More actions for {p.name}">More</summary>
                  <div class="menu-list">
                    {#if !p.disabled}
                      <a class="menu-item" href={apiURL(`/peers/${encodeURIComponent(p.name)}/conf`)} download="{p.name}.conf">Download config</a>
                      {#if !p.own_key}<button onclick={(e) => { e.currentTarget.closest('details').open = false; emailConfig(p.name); }}>Email the config</button>{/if}
                    {/if}
                    <button onclick={(e) => { e.currentTarget.closest('details').open = false; openEdit(p); }}>Edit</button>
                    <button class="danger" onclick={(e) => { e.currentTarget.closest('details').open = false; confirm = { open: true, name: p.name }; }}>Delete</button>
                  </div>
                </details>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<Dialog bind:open={formOpen} title={mode === 'edit' ? `Edit ${editing}` : 'Add clients'}>
  <form id="peerform" class="form" onsubmit={submit}>
    {#if formError}<div class="banner bad" role="alert">{formError}</div>{/if}
    {#if mode !== 'edit'}
      <div class="seg" role="radiogroup" aria-label="How many">
        <button type="button" role="radio" aria-checked={mode === 'add'} class:on={mode === 'add'} onclick={() => (mode = 'add')}>One client</button>
        <button type="button" role="radio" aria-checked={mode === 'bulk'} class:on={mode === 'bulk'} onclick={() => (mode = 'bulk')}>Several at once</button>
      </div>
    {/if}

    {#if mode === 'bulk'}
      <div class="grid2">
        <label class="field">
          <span>Name prefix</span>
          <input bind:value={form.prefix} autocomplete="off" autocapitalize="none" spellcheck="false" required />
          <span class="hint" class:bad={form.prefix && !nameOk}>Named {form.prefix || 'guest'}-1, -2, … on the next free addresses.</span>
        </label>
        <label class="field">
          <span>How many</span>
          <input type="number" bind:value={form.count} min="1" max="250" required />
        </label>
      </div>
    {:else if mode === 'add'}
      <label class="field">
        <span>Name</span>
        <input bind:value={form.name} placeholder="e.g. alice-phone" autocomplete="off" autocapitalize="none" spellcheck="false" required />
        <span class="hint" class:bad={form.name && !nameOk}>Letters, digits, dot, dash, underscore or @. Starts with a letter or digit.</span>
      </label>
    {/if}

    <label class="field">
      <span>Notes (optional)</span>
      <input bind:value={form.notes} placeholder="e.g. Alice's work phone" maxlength="500" autocomplete="off" />
    </label>

    <label class="field">
      <span>Works until (optional)</span>
      <span class="until">
        <input type="date" bind:value={form.until} min={mode === 'edit' ? undefined : today} />
        {#if form.until}<button type="button" class="btn small ghost" onclick={() => (form.until = '')}>No end date</button>{/if}
      </span>
      <span class="hint">E.g. for a guest: after that day {mode === 'bulk' ? 'these clients are' : 'the client is'} taken off the server and cannot connect. Empty: no end date.</span>
    </label>

    {#if mode !== 'bulk'}
      <label class="field">
        <span>VPN address</span>
        <input bind:value={form.address} list="free-addrs" class="mono" placeholder={mode === 'edit' ? '' : `next free (${freeAddrs[0] ?? '–'})`} autocomplete="off" spellcheck="false" />
        <datalist id="free-addrs">{#each freeAddrs as a (a)}<option value={a}></option>{/each}</datalist>
        <span class="hint">{mode === 'edit' ? 'Change it to move the client; its device needs the new config.' : 'Empty: the next free one. Type or pick a free address.'}</span>
      </label>
    {/if}

    {#if mode === 'add'}
      <fieldset class="box">
        <legend>Keys</legend>
        <label class="choice"><input type="radio" bind:group={form.ownKey} value={false} /><span>Make a key pair here: the config and QR code are ready to use</span></label>
        <label class="choice"><input type="radio" bind:group={form.ownKey} value={true} /><span>Use the device's own public key: its private key never leaves the device</span></label>
        {#if form.ownKey}
          <input bind:value={form.publicKey} class="mono" placeholder="public key (wg pubkey)" autocomplete="off" spellcheck="false" required />
        {/if}
      </fieldset>
    {/if}

    <label class="check">
      <input type="checkbox" bind:checked={form.psk} />
      <span>Preshared key <span class="hint">an extra layer of encryption (against future quantum computers)</span></span>
    </label>

    {#if mode !== 'bulk'}
      <label class="field">
        <span>Networks behind this client (optional)</span>
        <input bind:value={form.lan} placeholder="e.g. 192.168.50.0/24" autocomplete="off" spellcheck="false" />
        <span class="hint">For a router or site-to-site link. Separate several with commas.</span>
      </label>
    {/if}

    <details class="adv" open={mode === 'edit' && !!(form.dns || form.allowed || form.mtu || form.keepalive !== '')}>
      <summary>Config settings for this client</summary>
      <p class="hint">Empty: the server's defaults (change those on the Overview page).</p>
      <div class="grid2">
        <label class="field">
          <span>DNS</span>
          <input bind:value={form.dns} placeholder={defaults?.dns?.join(', ') || 'none'} autocomplete="off" spellcheck="false" />
        </label>
        <label class="field">
          <span>Allowed IPs</span>
          <input bind:value={form.allowed} placeholder={defaults?.allowed_ips?.join(', ') ?? ''} autocomplete="off" spellcheck="false" />
          <span class="hint">What the device sends through the VPN.</span>
        </label>
        <label class="field">
          <span>MTU</span>
          <input type="number" bind:value={form.mtu} min="1280" max="9000" placeholder={defaults?.mtu ? String(defaults.mtu) : 'automatic'} />
        </label>
        <label class="field">
          <span>Keepalive (seconds)</span>
          <input type="number" bind:value={form.keepalive} min="0" max="600" placeholder={String(defaults?.keepalive ?? 25)} />
          <span class="hint">0 turns it off.</span>
        </label>
      </div>
    </details>
  </form>
  {#snippet actions()}
    <button class="btn" onclick={() => (formOpen = false)}>Cancel</button>
    <button class="btn primary" form="peerform" type="submit" disabled={busy || (mode !== 'edit' && !nameOk) || (mode === 'add' && form.ownKey && !form.publicKey.trim())}>
      {busy ? 'Saving…' : mode === 'edit' ? 'Save' : mode === 'bulk' ? `Add ${form.count || ''} clients` : 'Add client'}
    </button>
  {/snippet}
</Dialog>

<Dialog bind:open={conf.open} title="Config for {conf.name}">
  {#if conf.error}
    <div class="banner bad" role="alert">{conf.error}</div>
  {:else if !conf.text}
    <p class="muted">Loading…</p>
  {:else}
    <div class="conf-wrap">
      {#if qrSvg}<div class="qr" role="img" aria-label="QR code with the {app} config">{@html qrSvg}</div>{/if}
      <div class="conf-side">
        <p class="small muted">Open the {app} app, choose “Add a tunnel” and scan the code. On a computer, download the file and import it.</p>
        {#if conf.ownKey}
          <p class="small warn-text">This client uses its own key: replace the PrivateKey line with the device's private key before importing (the QR code has the placeholder).</p>
        {:else}
          <p class="small muted">The config holds the client's private key. Share it only with the person who uses this device.</p>
        {/if}
      </div>
    </div>
    <pre class="conf">{conf.text}</pre>
  {/if}
  {#snippet actions()}
    {#if conf.text}
      <a class="btn" href={apiURL(`/peers/${encodeURIComponent(conf.name)}/conf`)} download="{conf.name}.conf">Download</a>
      {#if !conf.ownKey}<button class="btn" onclick={() => emailConfig(conf.name)}>Email…</button>{/if}
    {/if}
    <button class="btn primary" onclick={() => (conf.open = false)}>Done</button>
  {/snippet}
</Dialog>

<SendProfile bind:open={mailing.open} name={mailing.name} qr={mailing.qr} email={emails[mailing.name] ?? ''} onsent={(to) => (emails = { ...emails, [mailing.name]: to })} />

<Dialog bind:open={confirm.open} title="Delete this client?">
  <p><strong>{confirm.name}</strong> can no longer connect, and its address is freed. The device needs a new client and a new config to connect again. This cannot be undone.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (confirm.open = false)}>Cancel</button>
    <button class="btn danger solid" onclick={remove}>Delete</button>
  {/snippet}
</Dialog>

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .banner { margin-bottom: var(--space-3); }
  .name { font-weight: 500; }
  .until { display: flex; gap: var(--space-2); align-items: center; }
  .bad-text { color: var(--bad); }
  .email { font-weight: 400; margin-left: var(--space-2); }
  .nm { display: grid; gap: 2px; }
  .c-traffic span + span { margin-left: var(--space-3); }
  tr.old td { color: var(--ink-3); }
  tr.old .name { font-weight: 400; }
  td.actions { text-align: right; white-space: nowrap; }
  td.actions > * { vertical-align: middle; }

  .form { display: grid; gap: var(--space-4); }
  .grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); align-items: start; }
  .seg { display: inline-flex; gap: 2px; padding: 2px; background: var(--sunken); border-radius: 8px; justify-self: start; }
  .seg button { border: 0; background: none; padding: 6px 12px; border-radius: 6px; font: inherit; color: var(--ink-2); cursor: pointer; }
  .seg button.on { background: var(--surface); color: var(--ink); box-shadow: inset 0 0 0 1px var(--line); }
  .box { border: 1px solid var(--line); border-radius: 10px; padding: var(--space-3); margin: 0; display: grid; gap: var(--space-2); }
  .box legend { padding: 0 var(--space-1); font-weight: 600; }
  .choice, .check { display: flex; gap: var(--space-2); align-items: flex-start; }
  .choice input, .check input { margin-top: 3px; }
  .adv summary { cursor: pointer; color: var(--ink-2); font-weight: 500; }
  .adv[open] summary { margin-bottom: var(--space-2); }
  .warn-text { color: var(--warn); }
  .hint.bad { color: var(--bad); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }

  .conf-wrap { display: grid; grid-template-columns: 180px 1fr; gap: var(--space-4); align-items: center; }
  .qr { width: 180px; padding: 6px; background: #fff; border-radius: var(--radius-s); line-height: 0; }
  .qr :global(svg) { width: 100%; height: auto; }
  .conf-side { display: grid; gap: var(--space-2); }
  .conf {
    margin: 0;
    max-height: 200px;
    overflow: auto;
    padding: var(--space-3);
    background: var(--sunken);
    border-radius: var(--radius-s);
    font-family: var(--mono);
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-all;
    user-select: all;
  }

  @media (max-width: 760px) {
    .hide-s { display: none; }
    thead { display: none; }
    tbody tr {
      display: grid;
      grid-template-columns: 1fr auto;
      grid-template-areas: 'name status' 'addr addr' 'act act';
      gap: 6px var(--space-3);
      padding: var(--space-3) var(--space-4);
      border-bottom: 1px solid var(--line);
    }
    tbody tr:last-child { border-bottom: 0; }
    tbody td { display: block; padding: 0; border: 0; }
    tbody tr:hover td { background: none; }
    .c-name { grid-area: name; }
    .c-status { grid-area: status; }
    .c-addr { grid-area: addr; font-size: var(--step--1); }
    td.actions { grid-area: act; text-align: left; }
    .conf-wrap { grid-template-columns: 1fr; justify-items: start; }
    .grid2 { grid-template-columns: 1fr; }
  }
</style>
