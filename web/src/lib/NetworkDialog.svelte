<script>
  import { api } from './api.js';
  import { toast } from './state.svelte.js';
  import Dialog from './Dialog.svelte';

  let { open = $bindable(false), name, online = false, onsaved } = $props();

  let server = $state(null);
  let form = $state(null);
  let warnings = $state([]);
  let error = $state('');
  let busy = $state(false);
  let saved = $state(false);
  let newRoute = $state('');
  let newLan = $state('');

  $effect(() => {
    if (!open || !name) return;
    server = form = null;
    error = '';
    saved = false;
    warnings = [];
    newRoute = newLan = '';
    Promise.all([api('/network'), api(`/certs/${name}/network`)])
      .then(([srv, net]) => {
        server = srv;
        form = { static_ip: net.static_ip ?? '', full_tunnel: net.full_tunnel, routes: [...net.routes], lan: [...net.lan] };
        warnings = net.warnings ?? [];
      })
      .catch((e) => (error = e.message));
  });

  const cidr = /^(\d{1,3}\.){3}\d{1,3}(\/\d{1,2})?$/;
  function add(list, value, clear) {
    const v = value.trim();
    if (!v) return;
    if (!cidr.test(v)) {
      error = `${v} is not an IPv4 network like 192.168.1.0/24`;
      return;
    }
    error = '';
    if (!form[list].includes(v)) form[list] = [...form[list], v];
    clear();
  }
  const remove = (list, v) => (form[list] = form[list].filter((x) => x !== v));

  // Other clients' static addresses, to avoid collisions.
  const taken = $derived(
    server ? Object.entries(server.static_ips).filter(([n]) => n !== name).map(([n, ip]) => `${ip} (${n})`) : [],
  );

  async function save(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const r = await api(`/certs/${name}/network`, { method: 'PUT', body: form });
      warnings = r.warnings ?? [];
      form = { static_ip: r.static_ip ?? '', full_tunnel: r.full_tunnel, routes: r.routes, lan: r.lan };
      saved = true;
      onsaved?.();
      toast(`Saved network settings for ${name}. They apply when it reconnects.`);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  async function reconnect() {
    try {
      await api(`/certs/${name}/sessions`, { method: 'DELETE' });
      toast(`Disconnected ${name}; it reconnects with the new settings.`);
      open = false;
    } catch (err) {
      toast(err.message, 'bad');
    }
  }
</script>

<Dialog bind:open title="Network for {name}">
  {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
  {#if !form && !error}
    <p class="muted">Loading…</p>
  {:else if form}
    <form id="netform" class="form" onsubmit={save}>
      <label class="field">
        <span>VPN address</span>
        <input bind:value={form.static_ip} placeholder={server?.subnet ? `Automatic (from ${server.subnet})` : 'Automatic'} inputmode="decimal" autocomplete="off" />
        <span class="hint">
          Leave empty to get any free address.
          {#if server?.server_ip}The server is {server.server_ip}.{/if}
          {#if taken.length}Taken: {taken.join(', ')}.{/if}
        </span>
      </label>

      <label class="check">
        <input type="checkbox" bind:checked={form.full_tunnel} />
        <span>
          Send all internet traffic through the VPN
          <span class="hint">{server?.pushes_full_tunnel ? 'The server already does this for every client.' : 'Otherwise only the networks below use the VPN.'}</span>
        </span>
      </label>

      <fieldset>
        <legend>Networks this client can reach</legend>
        <span class="hint">For example your home LAN behind another client or the server. Sent to the device as routes.</span>
        <div class="chips">
          {#each form.routes as r (r)}
            <span class="chip mono">{r}<button type="button" aria-label="Remove {r}" onclick={() => remove('routes', r)}>×</button></span>
          {/each}
        </div>
        <div class="add">
          <input bind:value={newRoute} placeholder="192.168.1.0/24" aria-label="Add a network to reach"
            onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add('routes', newRoute, () => (newRoute = '')))} />
          <button type="button" class="btn small" onclick={() => add('routes', newRoute, () => (newRoute = ''))}>Add</button>
        </div>
      </fieldset>

      <fieldset>
        <legend>Networks behind this client</legend>
        <span class="hint">When this device is a router (site-to-site), e.g. the LAN of a NAS or a remote office.</span>
        <div class="chips">
          {#each form.lan as r (r)}
            <span class="chip mono">{r}<button type="button" aria-label="Remove {r}" onclick={() => remove('lan', r)}>×</button></span>
          {/each}
        </div>
        <div class="add">
          <input bind:value={newLan} placeholder="192.168.50.0/24" aria-label="Add a network behind this client"
            onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add('lan', newLan, () => (newLan = '')))} />
          <button type="button" class="btn small" onclick={() => add('lan', newLan, () => (newLan = ''))}>Add</button>
        </div>
      </fieldset>

      {#if warnings.length}
        <div class="banner warn" role="status">
          {#each warnings as w (w)}<p>{w}</p>{/each}
        </div>
      {/if}
    </form>
  {/if}

  {#snippet actions()}
    {#if saved && online}
      <button class="btn" onclick={reconnect}>Reconnect now</button>
    {/if}
    <button class="btn" onclick={() => (open = false)}>{saved ? 'Done' : 'Cancel'}</button>
    <button class="btn primary" type="submit" form="netform" disabled={busy || !form}>{busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<style>
  .form { display: grid; gap: var(--space-4); }
  .check { display: flex; gap: var(--space-2); align-items: flex-start; }
  .check input { height: auto; margin-top: 4px; }
  .check > span { display: grid; gap: 2px; }
  fieldset { display: grid; gap: var(--space-2); margin: 0; padding: 0; border: 0; }
  legend { padding: 0; font-size: var(--step--1); font-weight: 500; color: var(--ink-2); }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; }
  .chips:empty { display: none; }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 4px 2px 8px;
    border-radius: var(--radius-s);
    background: var(--sunken);
    font-size: 13px;
  }
  .chip button {
    width: 20px;
    height: 20px;
    border: 0;
    border-radius: 3px;
    background: none;
    color: var(--ink-2);
    font-size: 15px;
    line-height: 1;
    cursor: pointer;
  }
  .chip button:hover { background: var(--line); color: var(--ink); }
  .add { display: flex; gap: var(--space-2); }
  .add input { flex: 1; }
  .banner p + p { margin-top: var(--space-1); }
</style>
