<script>
  import { onMount } from 'svelte';
  import qrcode from 'qrcode-generator';
  import { api, apiURL, poll } from '../lib/api.js';
  import { bytes, relative } from '../lib/format.js';
  import { toast } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';

  let peers = $state([]);
  let loadError = $state('');
  let loaded = $state(false);

  async function load() {
    try {
      peers = await api('/peers');
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
    if (p.connected) return { tone: 'ok', label: 'Connected' };
    if (p.last_handshake && !p.last_handshake.startsWith('0001')) return { tone: 'warn', label: `Seen ${relative(p.last_handshake)}` };
    return { tone: 'off', label: 'Never connected' };
  }

  // Add client
  let createOpen = $state(false);
  let form = $state({ name: '', lan: '' });
  let busy = $state(false);
  let formError = $state('');
  const nameOk = $derived(/^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$/.test(form.name));

  function openCreate() {
    form = { name: '', lan: '' };
    formError = '';
    createOpen = true;
  }

  async function create(e) {
    e.preventDefault();
    busy = true;
    formError = '';
    try {
      const body = { name: form.name };
      const lan = form.lan.split(/[\s,]+/).filter(Boolean);
      if (lan.length) body.lan = lan;
      const p = await api('/peers', { method: 'POST', body });
      createOpen = false;
      toast(`Added ${p.name}. Scan the QR code or download the config to set up the device.`);
      await load();
      showConfig(p.name);
    } catch (err) {
      formError = err.message;
    } finally {
      busy = false;
    }
  }

  // Config and QR code
  let conf = $state({ open: false, name: '', text: '', error: '' });
  async function showConfig(name) {
    conf = { open: true, name, text: '', error: '' };
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
    <p class="muted">One entry per device. Scan its QR code in the WireGuard app, or download the config.</p>
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
                  <span class="name">{p.name}</span>
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
                <button class="btn small ghost" onclick={() => setDisabled(p, !p.disabled)}>{p.disabled ? 'Enable' : 'Disable'}</button>
                <button class="btn small ghost danger" onclick={() => (confirm = { open: true, name: p.name })}>Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<Dialog bind:open={createOpen} title="Add client">
  <form id="create" class="form" onsubmit={create}>
    {#if formError}<div class="banner bad" role="alert">{formError}</div>{/if}
    <label class="field">
      <span>Name</span>
      <input bind:value={form.name} placeholder="e.g. alice-phone" autocomplete="off" autocapitalize="none" spellcheck="false" required />
      <span class="hint" class:bad={form.name && !nameOk}>Letters, digits, dot, dash, underscore or @. Starts with a letter or digit.</span>
    </label>
    <label class="field">
      <span>Networks behind this client (optional)</span>
      <input bind:value={form.lan} placeholder="e.g. 192.168.50.0/24" autocomplete="off" spellcheck="false" />
      <span class="hint">For a router or site-to-site link. Separate several networks with commas.</span>
    </label>
  </form>
  {#snippet actions()}
    <button class="btn" onclick={() => (createOpen = false)}>Cancel</button>
    <button class="btn primary" form="create" type="submit" disabled={busy || !nameOk}>{busy ? 'Adding…' : 'Add client'}</button>
  {/snippet}
</Dialog>

<Dialog bind:open={conf.open} title="Config for {conf.name}">
  {#if conf.error}
    <div class="banner bad" role="alert">{conf.error}</div>
  {:else if !conf.text}
    <p class="muted">Loading…</p>
  {:else}
    <div class="conf-wrap">
      {#if qrSvg}<div class="qr" role="img" aria-label="QR code with the WireGuard config">{@html qrSvg}</div>{/if}
      <div class="conf-side">
        <p class="small muted">Open the WireGuard app, choose “Add a tunnel” and scan the code. On a computer, download the file and import it.</p>
        <p class="small muted">The config holds the client's private key. Share it only with the person who uses this device.</p>
      </div>
    </div>
    <pre class="conf">{conf.text}</pre>
  {/if}
  {#snippet actions()}
    {#if conf.text}
      <a class="btn" href={apiURL(`/peers/${encodeURIComponent(conf.name)}/conf`)} download="{conf.name}.conf">Download</a>
    {/if}
    <button class="btn primary" onclick={() => (conf.open = false)}>Done</button>
  {/snippet}
</Dialog>

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
  .nm { display: grid; gap: 2px; }
  .c-traffic span + span { margin-left: var(--space-3); }
  tr.old td { color: var(--ink-3); }
  tr.old .name { font-weight: 400; }
  td.actions { text-align: right; white-space: nowrap; }
  td.actions > * { vertical-align: middle; }

  .form { display: grid; gap: var(--space-4); }
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
  }
</style>
