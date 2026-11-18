<script>
  import { onMount } from 'svelte';
  import { api, poll } from '../lib/api.js';
  import { date, relative, certState } from '../lib/format.js';
  import { toast, route, setQuery } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';
  import TwoFactor from '../lib/TwoFactor.svelte';

  let twoFactor = $state({ open: false, name: '' });

  let certs = $state([]);
  let online = $state(new Set());
  let loadError = $state('');
  let loaded = $state(false);

  let search = $state(route.query.get('q') ?? '');
  let filter = $state(route.query.get('show') ?? 'all');
  let showHistory = $state(false);

  async function load() {
    try {
      certs = await api('/certs');
      loadError = '';
    } catch (e) {
      loadError = e.message;
    } finally {
      loaded = true;
    }
    try {
      const live = await api('/sessions');
      online = new Set(live.sessions.map((s) => s.name));
    } catch {
      online = new Set();
    }
  }
  onMount(() => poll(load, 20000));

  $effect(() => setQuery({ q: search, show: filter === 'all' ? '' : filter }));

  const filters = [
    { key: 'all', label: 'All' },
    { key: 'valid', label: 'Valid' },
    { key: 'expiring', label: 'Expire soon' },
    { key: 'expired', label: 'Expired' },
    { key: 'revoked', label: 'Revoked' },
  ];

  // Server certificates are not clients; they are managed with the server setup.
  const clientCerts = $derived(certs.filter((c) => c.kind !== 'server'));

  const rows = $derived(
    clientCerts
      .filter((c) => showHistory || c.current)
      .map((c) => ({ ...c, st: certState(c) }))
      .filter((c) => filter === 'all' || c.st.key === filter)
      .filter((c) => !search || c.name.toLowerCase().includes(search.toLowerCase()))
      .sort((a, b) => a.name.localeCompare(b.name) || (b.current - a.current)),
  );
  const counts = $derived.by(() => {
    const n = { all: 0, valid: 0, expiring: 0, expired: 0, revoked: 0 };
    for (const c of clientCerts.filter((c) => c.current)) {
      n.all++;
      n[certState(c).key]++;
    }
    return n;
  });

  // Create
  let createOpen = $state(false);
  let form = $state({ name: '', passphrase: '', validity: 825 });
  let showPass = $state(false);
  let busy = $state(false);
  let formError = $state('');
  const nameOk = $derived(/^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$/.test(form.name) && form.name !== 'ca');

  function openCreate() {
    form = { name: '', passphrase: '', validity: 825 };
    formError = '';
    showPass = false;
    createOpen = true;
  }

  async function create(e) {
    e.preventDefault();
    busy = true;
    formError = '';
    try {
      const body = { name: form.name, validity_days: Number(form.validity) };
      if (form.passphrase) body.passphrase = form.passphrase;
      await api('/certs', { method: 'POST', body });
      createOpen = false;
      toast(`Created ${form.name}. Download the profile to set up the device.`);
      await load();
    } catch (err) {
      formError = err.message;
    } finally {
      busy = false;
    }
  }

  // Row actions
  let confirm = $state({ open: false, kind: '', name: '' });
  async function act(kind, name) {
    const calls = {
      revoke: ['POST', `/certs/${name}/revoke`, `Revoked ${name} and disconnected it.`],
      unrevoke: ['POST', `/certs/${name}/unrevoke`, `Restored ${name}.`],
      renew: ['POST', `/certs/${name}/renew`, `Renewed ${name}. Send the new profile to the device.`],
      delete: ['DELETE', `/certs/${name}`, `Deleted ${name}.`],
      disconnect: ['DELETE', `/certs/${name}/sessions`, `Disconnected ${name}.`],
    };
    const [method, path, done] = calls[kind];
    try {
      await api(path, { method });
      toast(done);
      confirm.open = false;
      await load();
    } catch (err) {
      toast(err.message, 'bad');
    }
  }
  function ask(kind, name) {
    confirm = { open: true, kind, name };
  }
  const confirmText = {
    revoke: {
      title: 'Revoke this client?',
      body: 'The device is disconnected now and cannot connect again. You can restore it later.',
      button: 'Revoke',
    },
    delete: {
      title: 'Delete this client?',
      body: 'Its keys are deleted. The certificate stays blocked until it expires, so the old profile can never connect again. This cannot be undone.',
      button: 'Delete',
    },
    renew: {
      title: 'Renew this certificate?',
      body: 'A new certificate is issued with the same key. The old profile stops working: download the new one and install it on the device.',
      button: 'Renew',
    },
  };
</script>

<header class="head">
  <div>
    <h1>Clients</h1>
    <p class="muted">One certificate per device. Download its profile to connect.</p>
  </div>
  <button class="btn primary" onclick={openCreate}>New client</button>
</header>

<div class="toolbar">
  <div class="tabs" role="tablist" aria-label="Filter by status">
    {#each filters as f (f.key)}
      <button role="tab" aria-selected={filter === f.key} onclick={() => (filter = f.key)}>
        {f.label}<span class="count">{counts[f.key]}</span>
      </button>
    {/each}
  </div>
  <input class="search" type="search" placeholder="Search by name" aria-label="Search by name" bind:value={search} />
</div>

{#if loadError}<div class="banner bad" role="alert">Could not load clients: {loadError}</div>{/if}

<section class="panel">
  {#if loaded && rows.length === 0}
    <div class="empty">
      {#if clientCerts.length === 0}
        <p>No clients yet. Create the first one to get a profile for a phone or laptop.</p>
      {:else}
        <p>No clients match this filter.</p>
      {/if}
    </div>
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Status</th>
            <th>Valid until</th>
            <th class="hide-s">Serial</th>
            <th><span class="sr">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each rows as c (c.serial)}
            <tr class:old={!c.current}>
              <td class="c-name">
                <span class="name">
                  {#if online.has(c.name) && c.current}<i class="dot" title="Connected now"></i>{/if}
                  {c.name}
                </span>
                {#if !c.current}<span class="faint small">replaced</span>{/if}
              </td>
              <td class="c-status"><span class="pill {c.st.tone}">{c.st.label}</span></td>
              <td class="c-valid">
                {#if c.status === 'revoked'}
                  <span class="muted">Revoked {date(c.revoked_at)}</span>
                {:else}
                  {date(c.not_after)} <span class="faint small">({relative(c.not_after)})</span>
                {/if}
              </td>
              <td class="hide-s"><span class="mono faint" title={c.serial}>{c.serial.slice(0, 8)}…</span></td>
              <td class="actions">
                {#if c.current}
                  {#if c.st.key === 'valid' || c.st.key === 'expiring'}
                    <a class="btn small" href="/api/v1/certs/{c.name}/ovpn" download="{c.name}.ovpn">Download profile</a>
                  {/if}
                  <details class="menu">
                    <summary class="btn small ghost" aria-label="More actions for {c.name}">More</summary>
                    <div class="menu-list">
                      {#if online.has(c.name)}
                        <button onclick={() => act('disconnect', c.name)}>Disconnect</button>
                      {/if}
                      {#if c.status !== 'revoked'}
                        <button onclick={(e) => { e.currentTarget.closest('details').open = false; twoFactor = { open: true, name: c.name }; }}>Two-factor login</button>
                        <button onclick={() => ask('renew', c.name)}>Renew</button>
                        <button class="danger" onclick={() => ask('revoke', c.name)}>Revoke</button>
                      {:else}
                        <button onclick={() => act('unrevoke', c.name)}>Restore</button>
                      {/if}
                      <button class="danger" onclick={() => ask('delete', c.name)}>Delete</button>
                    </div>
                  </details>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<label class="history">
  <input type="checkbox" bind:checked={showHistory} />
  Show replaced certificates
</label>

<Dialog bind:open={createOpen} title="New client">
  <form id="create" class="form" onsubmit={create}>
    {#if formError}<div class="banner bad" role="alert">{formError}</div>{/if}
    <label class="field">
      <span>Name</span>
      <input bind:value={form.name} placeholder="e.g. alice-phone" autocomplete="off" autocapitalize="none" spellcheck="false" required />
      <span class="hint" class:bad={form.name && !nameOk}>Letters, digits, dot, dash, underscore or @. Starts with a letter or digit.</span>
    </label>
    <label class="field">
      <span>Valid for</span>
      <select bind:value={form.validity}>
        <option value={30}>30 days</option>
        <option value={90}>90 days</option>
        <option value={365}>1 year</option>
        <option value={825}>825 days (default)</option>
        <option value={1825}>5 years</option>
      </select>
    </label>
    <label class="field">
      <span>Key passphrase (optional)</span>
      <div class="pass">
        <input type={showPass ? 'text' : 'password'} bind:value={form.passphrase} minlength="4" maxlength="1024" autocomplete="new-password" />
        <button type="button" class="btn ghost small" onclick={() => (showPass = !showPass)}>{showPass ? 'Hide' : 'Show'}</button>
      </div>
      <span class="hint">The device asks for it on every connect. Leave empty for none.</span>
    </label>
  </form>
  {#snippet actions()}
    <button class="btn" onclick={() => (createOpen = false)}>Cancel</button>
    <button class="btn primary" form="create" type="submit" disabled={busy || !nameOk}>{busy ? 'Creating…' : 'Create client'}</button>
  {/snippet}
</Dialog>

<TwoFactor bind:open={twoFactor.open} name={twoFactor.name} />

<Dialog bind:open={confirm.open} title={confirmText[confirm.kind]?.title ?? ''}>
  <p><strong>{confirm.name}</strong>: {confirmText[confirm.kind]?.body}</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (confirm.open = false)}>Cancel</button>
    <button class="btn {confirm.kind === 'renew' ? 'primary' : 'danger solid'}" onclick={() => act(confirm.kind, confirm.name)}>
      {confirmText[confirm.kind]?.button}
    </button>
  {/snippet}
</Dialog>

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }

  .toolbar { display: flex; flex-wrap: wrap; gap: var(--space-3); align-items: center; justify-content: space-between; margin-bottom: var(--space-3); }
  .tabs { display: flex; flex-wrap: wrap; gap: 2px; }
  .tabs button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 32px;
    padding: 0 var(--space-3);
    border: 0;
    border-radius: var(--radius-s);
    background: transparent;
    color: var(--ink-2);
    font: inherit;
    font-weight: 500;
    cursor: pointer;
  }
  .tabs button:hover { color: var(--ink); background: var(--sunken); }
  .tabs button[aria-selected='true'] { color: var(--ink); background: var(--surface); box-shadow: inset 0 0 0 1px var(--line); }
  .count { font-size: 12px; color: var(--ink-3); }
  .search { width: 220px; }
  .banner { margin-bottom: var(--space-3); }

  .name { display: inline-flex; align-items: center; gap: 6px; font-weight: 500; }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--ok); }
  tr.old td { color: var(--ink-3); }
  tr.old .name { font-weight: 400; }
  td.actions { text-align: right; white-space: nowrap; }
  td.actions > * { vertical-align: middle; }

  .menu { position: relative; display: inline-block; margin-left: var(--space-1); }
  .menu summary { list-style: none; }
  .menu summary::-webkit-details-marker { display: none; }
  .menu-list {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 10;
    min-width: 160px;
    display: grid;
    padding: 4px;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius-s);
    box-shadow: var(--shadow);
  }
  .menu-list button {
    padding: 8px var(--space-3);
    border: 0;
    border-radius: 3px;
    background: none;
    color: var(--ink);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .menu-list button:hover { background: var(--sunken); }
  .menu-list button.danger { color: var(--bad); }

  .history { display: inline-flex; align-items: center; gap: var(--space-2); margin-top: var(--space-3); color: var(--ink-2); font-size: var(--step--1); }
  .history input { height: auto; }

  .form { display: grid; gap: var(--space-4); }
  .pass { display: flex; gap: var(--space-2); }
  .pass input { flex: 1; }
  .hint.bad { color: var(--bad); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }

  /* Phones: each row becomes a small card: name + status, date, actions */
  @media (max-width: 760px) {
    .search { width: 100%; }
    .hide-s { display: none; }
    thead { display: none; }
    tbody tr {
      display: grid;
      grid-template-columns: 1fr auto;
      grid-template-areas: 'name status' 'valid valid' 'act act';
      gap: 6px var(--space-3);
      padding: var(--space-3) var(--space-4);
      border-bottom: 1px solid var(--line);
    }
    tbody tr:last-child { border-bottom: 0; }
    tbody td { display: block; padding: 0; border: 0; }
    tbody tr:hover td { background: none; }
    .c-name { grid-area: name; }
    .c-status { grid-area: status; }
    .c-valid { grid-area: valid; font-size: var(--step--1); }
    td.actions { grid-area: act; text-align: left; }
  }
</style>
