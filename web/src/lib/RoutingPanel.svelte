<script>
  // OpenVPN routing in one place, next to the map: all traffic, client-to-client,
  // routes sent to every client (push route) and networks the server sends into
  // the VPN (route). Saved to server.conf like the Server page; OpenVPN uses it
  // after a restart.
  import { untrack } from 'svelte';
  import { api } from './api.js';
  import { toast } from './state.svelte.js';
  import { clientName } from './names.svelte.js';
  import { normalize, overlaps, covers } from './networkMap.js';

  // cfg: GET /server/config; srv: GET /network; map: buildMap(); onsaved(cfg)
  let { cfg, srv, map, onsaved } = $props();

  const pick = (st) => ({ full_tunnel: !!st.full_tunnel, client_to_client: !!st.client_to_client, push_routes: [...(st.push_routes ?? [])], routes: [...(st.routes ?? [])] });
  // the editable copy starts from the loaded file and follows new versions below
  let draft = $state(untrack(() => pick(cfg.settings)));
  let base = $state(untrack(() => cfg.version));
  $effect(() => {
    // a new version from outside (saved here or elsewhere): start from it
    if (cfg.version !== base) {
      base = cfg.version;
      draft = pick(cfg.settings);
    }
  });
  const dirty = $derived(JSON.stringify(draft) !== JSON.stringify(pick(cfg.settings)));

  let input = $state({ push_routes: '', routes: '' });
  let error = $state({ push_routes: '', routes: '' });
  let note = $state({ push_routes: '', routes: '' }); // e.g. "Added as …" when the host bits were cleared
  function add(kind) {
    const raw = input[kind].trim();
    if (!raw) return;
    note[kind] = '';
    const n = normalize(raw);
    if (!n) return (error[kind] = `${raw} is not an IPv4 network like 192.168.1.0/24`);
    if (n === '0.0.0.0/0') return (error[kind] = 'For all traffic use "Send all internet traffic" above');
    if (srv?.subnet && overlaps(n, srv.subnet)) return (error[kind] = `${n} overlaps the VPN network ${srv.subnet}`);
    if (draft[kind].includes(n)) return (error[kind] = `${n} is already there`);
    draft[kind] = [...draft[kind], n];
    input[kind] = '';
    error[kind] = '';
    note[kind] = raw.includes('/') && n !== raw ? `Added as ${n}` : '';
  }
  const remove = (kind, r) => (draft[kind] = draft[kind].filter((x) => x !== r));
  const keydown = (e, kind) => e.key === 'Enter' && (e.preventDefault(), add(kind));

  // where a network is: behind a client (iroute), or not
  const lanOwners = $derived(map.networks.filter((n) => n.via).flatMap((n) => n.cidrs.map((c) => [c, n.via])));
  const behind = (r) => lanOwners.filter(([c]) => covers(r, c) || covers(c, r)).map(([, v]) => v);

  let busy = $state(false);
  async function save() {
    busy = true;
    try {
      const settings = { ...cfg.settings, ...draft };
      const next = await api('/server/config', { method: 'PUT', body: { settings, base_version: cfg.version } });
      toast('Routing saved in server.conf. OpenVPN uses it after a restart.');
      onsaved?.(next);
    } catch (e) {
      toast(e.status === 409 ? 'server.conf was changed meanwhile. Reload the page.' : `${e.message}. The Server page shows what is wrong.`, 'bad');
    } finally {
      busy = false;
    }
  }
</script>

<section class="panel routing">
  <div class="panel-head">
    <h2>Routing</h2>
    <a class="small" href="/server">All of server.conf</a>
  </div>
  <div class="body">
    <p class="line">
      <span class="k">VPN network</span>
      <span class="mono">{srv?.subnet || '–'}</span>{#if srv?.server_ip}<span class="faint"> · server <span class="mono">{srv.server_ip}</span></span>{/if}
      <span class="faint"> · topology {srv?.topology}</span>
    </p>

    <label class="check">
      <input type="checkbox" bind:checked={draft.full_tunnel} />
      <span>
        Send all internet traffic of every client through the VPN
        <span class="hint">{draft.full_tunnel ? 'Every client uses the server as its gateway (redirect-gateway).' : 'Split tunnel: only the networks below go through the VPN. Single clients can still get all traffic under Edit.'}</span>
      </span>
    </label>
    <label class="check">
      <input type="checkbox" bind:checked={draft.client_to_client} />
      <span>
        Clients can reach each other inside OpenVPN (client-to-client)
        <span class="hint">{draft.client_to_client ? 'OpenVPN passes traffic between clients itself, so access rules cannot filter it.' : 'Traffic between clients goes through the server, where access rules apply.'}</span>
      </span>
    </label>

    <fieldset>
      <legend>Every client reaches <span class="faint small">push route</span></legend>
      <span class="hint">Sent to every device as a route, e.g. the LAN next to the server or behind a site router.</span>
      <ul class="routes">
        {#each draft.push_routes as r (r)}
          {@const via = behind(r)}
          <li>
            <span class="mono">{r}</span>
            <span class="faint small">{via.length ? `behind ${via.map(clientName).join(', ')}` : 'through the server'}</span>
            <button type="button" class="x" aria-label="Remove {r}" onclick={() => remove('push_routes', r)}>×</button>
          </li>
        {:else}
          <li class="none faint small">None: clients only reach the VPN network and what they get under Edit.</li>
        {/each}
      </ul>
      <div class="add">
        <input bind:value={input.push_routes} placeholder="10.20.0.0/16" aria-label="Add a network every client reaches" onkeydown={(e) => keydown(e, 'push_routes')} />
        <button type="button" class="btn small" onclick={() => add('push_routes')}>Add</button>
      </div>
      {#if error.push_routes}<span class="hint err">{error.push_routes}</span>{:else if note.push_routes}<span class="hint">{note.push_routes}</span>{/if}
    </fieldset>

    <fieldset>
      <legend>The server sends into the VPN <span class="faint small">route</span></legend>
      <span class="hint">
        Networks behind clients (site-to-site). Each needs a client that has it behind it: Edit → Networks behind this client.
        {#if draft.client_to_client}With client-to-client on, clients reach those networks without a route here; a route is only needed for traffic from the server itself.{/if}
      </span>
      <ul class="routes">
        {#each draft.routes as r (r)}
          {@const via = behind(r)}
          <li class:warn={!via.length}>
            <span class="mono">{r}</span>
            <span class="small {via.length ? 'faint' : 'warn-t'}">{via.length ? `behind ${via.map(clientName).join(', ')}` : 'no client has it behind it'}</span>
            <button type="button" class="x" aria-label="Remove {r}" onclick={() => remove('routes', r)}>×</button>
          </li>
        {:else}
          <li class="none faint small">None.</li>
        {/each}
      </ul>
      <div class="add">
        <input bind:value={input.routes} placeholder="192.168.50.0/24" aria-label="Add a network the server sends into the VPN" onkeydown={(e) => keydown(e, 'routes')} />
        <button type="button" class="btn small" onclick={() => add('routes')}>Add</button>
      </div>
      {#if error.routes}<span class="hint err">{error.routes}</span>{:else if note.routes}<span class="hint">{note.routes}</span>{/if}
    </fieldset>

    {#if dirty}
      <div class="bar" role="region" aria-label="Unsaved routing changes">
        <span>Unsaved changes. OpenVPN uses them after a restart.</span>
        <button class="btn" disabled={busy} onclick={() => (draft = pick(cfg.settings))}>Discard</button>
        <button class="btn primary" disabled={busy} onclick={save}>{busy ? 'Saving…' : 'Save'}</button>
      </div>
    {/if}
  </div>
</section>

<style>
  .body { display: grid; gap: var(--space-4); padding: var(--space-2) var(--space-5) var(--space-5); }
  .line { margin: 0; }
  .k { color: var(--ink-2); font-size: var(--step--1); margin-right: var(--space-2); }
  .check { display: flex; gap: var(--space-3); align-items: baseline; }
  .check input { height: auto; }
  .check > span { display: grid; gap: 2px; }
  fieldset { border: 0; margin: 0; padding: 0; display: grid; gap: var(--space-2); }
  legend { padding: 0; margin-bottom: var(--space-1); font-weight: 600; }
  .routes { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }
  .routes li { display: flex; align-items: center; gap: var(--space-3); padding: 6px 10px; border: 1px solid var(--line); border-radius: var(--radius-s); background: var(--sunken); }
  .routes li.warn { border-color: var(--warn); border-style: dashed; }
  .routes li.none { border: 0; background: none; padding: 0; }
  .x { margin-left: auto; width: 26px; height: 26px; border: 0; border-radius: var(--radius-s); background: none; color: var(--ink-2); cursor: pointer; font-size: 16px; }
  .x:hover { background: var(--surface); color: var(--bad); }
  .add { display: flex; gap: var(--space-2); max-width: 360px; }
  .add input { flex: 1; }
  .warn-t { color: var(--warn); }
  .err { color: var(--bad); }
  .bar { display: flex; align-items: center; justify-content: flex-end; gap: var(--space-2); padding: var(--space-3) var(--space-4); border: 1px solid var(--line); border-radius: var(--radius-m); background: var(--surface); box-shadow: var(--shadow); position: sticky; bottom: var(--space-4); }
  .bar span { margin-right: auto; }
  @media (max-width: 640px) { .bar { flex-wrap: wrap; } }
</style>
