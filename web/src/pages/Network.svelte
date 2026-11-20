<script>
  import { api } from '../lib/api.js';
  import { currentServer } from '../lib/state.svelte.js';
  import NetworkDialog from '../lib/NetworkDialog.svelte';
  import NamedNetworks from '../lib/NamedNetworks.svelte';
  import { loadClientNames, clientName, clientTitle } from '../lib/names.svelte.js';
  import { toast, route, setQuery } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';
  import NetworkMap from '../lib/NetworkMap.svelte';
  import RoutingPanel from '../lib/RoutingPanel.svelte';
  import { buildMap } from '../lib/networkMap.js';

  // Routing at a glance: the server's network, named networks and what every
  // client can reach (OpenVPN), or every peer's addresses (WireGuard).
  const isWG = ['wireguard', 'amneziawg'].includes(currentServer()?.type);

  let srv = $state(null); // OpenVPN: GET /network
  let pushed = $state([]); // routes pushed to every client
  let clients = $state([]); // OpenVPN: GET /network/clients
  let nets = $state([]); // named networks
  let peers = $state([]); // WireGuard
  let defaults = $state(null); // WireGuard client defaults
  let cfg = $state(null); // OpenVPN: GET /server/config (for the route fix)
  let online = $state(new Set());
  let loadError = $state('');
  let loaded = $state(false);

  async function load() {
    try {
      if (isWG) {
        [peers, defaults] = await Promise.all([api('/peers'), api('/settings')]);
      } else {
        const [n, c, nn, sc, live] = await Promise.all([
          api('/network'),
          api('/network/clients'),
          api('/named-networks'),
          api('/server/config').catch(() => null),
          api('/sessions').catch(() => null),
        ]);
        cfg = sc;
        online = new Set((live?.sessions ?? []).map((x) => x.name));
        srv = n;
        loadClientNames();
        clients = c;
        nets = nn.networks;
        pushed = cfg?.settings?.push_routes ?? [];
      }
      loadError = '';
    } catch (e) {
      loadError = e.message;
    } finally {
      loaded = true;
    }
  }
  $effect(() => {
    load();
  });

  const netOf = (n) => nets.find((x) => x.name === n);
  const map = $derived(srv ? buildMap({ srv, clients, nets, pushed, online, c2c: !!cfg?.settings?.client_to_client }) : null);
  // the missing-route warnings are in "Needs attention", with a button
  const warnings = $derived([...new Set(clients.flatMap((c) => c.warnings ?? []))].filter((w) => !w.startsWith('server.conf needs "route')));

  function openNetwork(n) {
    if (n.named) {
      tab = 'named';
      namedOpen = true;
    }
    else if (n.via) edit = { open: true, name: n.via };
    else {
      const c = clients.find((x) => (x.routes ?? []).includes(n.cidrs[0]));
      if (c) edit = { open: true, name: c.name };
    }
  }

  // Fixes
  let fixing = $state('');
  async function addRoute(cidr) {
    if (!cfg) return toast('server.conf could not be read', 'bad');
    fixing = cidr;
    try {
      const settings = { ...cfg.settings, routes: [...(cfg.settings.routes ?? []), cidr] };
      cfg = await api('/server/config', { method: 'PUT', body: { settings, base_version: cfg.version } });
      toast(`Route for ${cidr} saved in server.conf. OpenVPN uses it after a restart.`);
      await load();
    } catch (e) {
      toast(e.status === 409 ? 'server.conf was changed meanwhile. Try again.' : `${e.message}. The Server page shows what is wrong.`, 'bad');
      await load();
    } finally {
      fixing = '';
    }
  }
  let restart = $state({ open: false, busy: false });
  async function doRestart() {
    restart.busy = true;
    try {
      const r = await api('/server/restart', { method: 'POST', body: { rollback: true } });
      if (r.ok) toast(`OpenVPN restarted in ${Math.round(r.seconds)} s.`);
      else toast(r.error || 'OpenVPN did not come back; the previous config was restored.', 'bad');
      restart.open = false;
      await load();
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      restart.busy = false;
    }
  }

  // OpenVPN: one space each for the clients, the routing and the named networks
  const tabs = [['clients', 'Clients'], ['routing', 'Routing'], ['named', 'Named networks']];
  let tab = $state(tabs.some(([k]) => k === route.query.get('tab')) ? route.query.get('tab') : 'clients');
  $effect(() => { if (!isWG) setQuery({ tab: tab === 'clients' ? '' : tab }); });

  let edit = $state({ open: false, name: '' });
  let namedOpen = $state(false);
</script>

<header class="head">
  <div>
    <h1>Network</h1>
    <p class="muted">{isWG ? "Every client's address, the networks behind it and what it sends through the VPN." : 'What every client can reach through the VPN, and the networks behind clients.'}</p>
  </div>
</header>

{#if loadError}<div class="banner bad" role="alert">Could not load the network: {loadError}</div>{/if}

{#if !isWG && srv}
  {#if cfg?.restart_pending}
    <div class="banner warn pending" role="status">
      <span>Saved changes to server.conf are not active yet: OpenVPN still runs the previous config.</span>
      <button class="btn small primary" onclick={() => (restart = { open: true, busy: false })}>Apply and restart</button>
    </div>
  {/if}

  {#if map && (map.clients.length || map.networks.length)}
    <section class="panel">
      <div class="panel-head"><h2>Map</h2></div>
      <div class="panel-body"><NetworkMap {map} {srv} onclient={(name) => (edit = { open: true, name })} onnetwork={openNetwork} /></div>
    </section>
  {/if}

  {#if map?.problems.length}
    <section class="panel">
      <div class="panel-head"><h2>Needs attention</h2></div>
      <ul class="problems">
        {#each map.problems as p, i (i)}
          <li>
            {#if p.kind === 'route'}
              <span><strong>{p.name}</strong> is behind {clientName(p.via)}, but server.conf has no route for <span class="mono">{p.cidr}</span>, so the server does not send its traffic into the VPN.</span>
              <button class="btn small primary" disabled={!!fixing || !cfg} onclick={() => addRoute(p.cidr)}>{fixing === p.cidr ? 'Adding…' : 'Add the route'}</button>
            {:else if p.kind === 'unreached'}
              <span>No client reaches <strong>{p.name}</strong> (behind {clientName(p.via)}) yet.</span>
              <button class="btn small" onclick={() => ((tab = 'named'), (namedOpen = true))}>Give it to clients</button>
            {:else}
              <span>server.conf sends <span class="mono">{p.cidr}</span> into the VPN, but no client has it behind it, so that traffic goes nowhere.</span>
              <button class="btn small" onclick={() => (tab = 'routing')}>Routing</button>
            {/if}
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if warnings.length}
    <div class="banner warn" role="status">{#each warnings as w (w)}<p>{w}</p>{/each}</div>
  {/if}

  <div class="tabs" role="tablist" aria-label="Manage">
    {#each tabs as [key, label] (key)}
      <button role="tab" aria-selected={tab === key} onclick={() => (tab = key)}>
        {label}{#if key === 'clients'}<span class="count">{clients.length}</span>{:else if key === 'named'}<span class="count">{nets.length}</span>{:else if key === 'routing' && map?.problems.some((p) => p.kind !== 'unreached')}<span class="count warn">!</span>{/if}
      </button>
    {/each}
  </div>

  {#if tab === 'routing' && cfg}
    <RoutingPanel {cfg} {srv} {map} onsaved={(c) => ((cfg = c), load())} />
  {:else if tab === 'routing'}
    <p class="muted">server.conf could not be read.</p>
  {/if}

  {#if tab === 'named'}
  <section class="panel">
    <div class="panel-head"><h2>Named networks</h2><button class="btn small" onclick={() => (namedOpen = true)}>Manage</button></div>
    {#if nets.length === 0}
      <p class="panel-body muted">None yet. A named network is given to several clients at once, e.g. an office LAN.</p>
    {:else}
      <div class="table-wrap">
        <table>
          <thead><tr><th>Name</th><th>Networks</th><th>Where</th><th>Clients that reach it</th></tr></thead>
          <tbody>
            {#each nets as n (n.name)}
              <tr>
                <td><strong>{n.name}</strong>{#if n.description}<span class="faint small block">{n.description}</span>{/if}</td>
                <td class="mono">{n.networks.join(', ')}</td>
                <td>{n.via ? `Behind ${clientName(n.via)}` : 'Through the server'}</td>
                <td>{n.clients.length ? n.clients.map(clientName).join(', ') : '–'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
  {/if}
{/if}

{#if isWG || tab === 'clients'}
<section class="panel">
  <div class="panel-head"><h2>Clients</h2></div>
  {#if !loaded}
    <p class="panel-body muted">Loading…</p>
  {:else if isWG}
    {#if peers.length === 0}
      <p class="panel-body muted">No clients yet.</p>
    {:else}
      <div class="table-wrap">
        <table>
          <thead><tr><th>Client</th><th>Address</th><th>Networks behind it</th><th>Sends through the VPN</th><th>DNS</th></tr></thead>
          <tbody>
            {#each peers as p (p.name)}
              <tr class:off={p.disabled}>
                <td><strong>{p.name}</strong>{#if p.disabled}<span class="faint small block">disabled</span>{/if}</td>
                <td class="mono">{p.address}</td>
                <td class="mono">{p.lan?.length ? p.lan.join(', ') : '–'}</td>
                <td class="mono">{(p.allowed_ips?.length ? p.allowed_ips : defaults?.allowed_ips ?? []).join(', ')}{#if !p.allowed_ips?.length}<span class="faint small dflt">default</span>{/if}</td>
                <td class="mono">{(p.dns?.length ? p.dns : defaults?.dns ?? []).join(', ') || '–'}{#if !p.dns?.length && defaults?.dns?.length}<span class="faint small dflt">default</span>{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <p class="panel-body hint">Change a client under Clients → Edit, the defaults on the Overview.</p>
    {/if}
  {:else if clients.length === 0}
    <p class="panel-body muted">No clients yet.</p>
  {:else}
    <div class="table-wrap">
      <table>
        <thead><tr><th>Client</th><th>VPN address</th><th>All traffic</th><th>Reaches</th><th>Behind it</th><th><span class="sr">Edit</span></th></tr></thead>
        <tbody>
          {#each clients as c (c.name)}
            <tr>
              <td>
                <strong title={clientTitle(c.name)}>{clientName(c.name)}</strong>
                {#if c.warnings?.length}<span class="warn-dot" title={c.warnings.join('\n')}>!</span>{/if}
              </td>
              <td class="mono">{c.static_ip || 'automatic'}</td>
              <td>{c.full_tunnel || srv?.pushes_full_tunnel ? 'Yes' : '–'}</td>
              <td>
                {#each (c.named ?? []).filter((u) => u.as === 'route') as u (u.name)}
                  <span class="tag named" title={netOf(u.name)?.networks.join(', ')}>{u.name}</span>
                {/each}
                {#each c.routes as r (r)}<span class="tag mono">{r}</span>{/each}
                {#each pushed.filter((r) => !c.routes.includes(r)) as r (r)}<span class="tag mono all" title="Sent to every client">{r}</span>{/each}
                {#if !c.routes.length && !pushed.length && !(c.named ?? []).some((u) => u.as === 'route')}<span class="faint">VPN network only</span>{/if}
              </td>
              <td>
                {#each (c.named ?? []).filter((u) => u.as === 'lan') as u (u.name)}
                  <span class="tag named" title={netOf(u.name)?.networks.join(', ')}>{u.name}</span>
                {/each}
                {#each c.lan as r (r)}<span class="tag mono">{r}</span>{/each}
                {#if !c.lan.length && !(c.named ?? []).some((u) => u.as === 'lan')}<span class="faint">–</span>{/if}
              </td>
              <td class="actions"><button class="btn small ghost" onclick={() => (edit = { open: true, name: c.name })}>Edit</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="panel-body hint"><span class="tag named">Name</span> named network · <span class="tag mono all">10.0.0.0/8</span> sent to every client (Routing tab)</p>
  {/if}
</section>
{/if}

{#if !isWG}
  <Dialog bind:open={restart.open} title="Restart OpenVPN?">
    <p>OpenVPN restarts with the saved server.conf. Every client is disconnected for a few seconds and reconnects by itself.</p>
    <p class="muted">If OpenVPN does not come back, the previous config is restored.</p>
    {#snippet actions()}
      <button class="btn" onclick={() => (restart.open = false)}>Cancel</button>
      <button class="btn primary" disabled={restart.busy} onclick={doRestart}>{restart.busy ? 'Restarting…' : 'Restart'}</button>
    {/snippet}
  </Dialog>
  <NetworkDialog bind:open={edit.open} name={edit.name} onsaved={load} />
  <NamedNetworks bind:open={namedOpen} clients={clients.map((c) => c.name)} onsaved={load} />
{/if}

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-3); flex-wrap: wrap; margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .panel { margin-bottom: var(--space-5); }
  .panel-head { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); }
  .banner { margin-bottom: var(--space-4); }
  .tabs { display: flex; gap: 2px; margin-bottom: var(--space-3); flex-wrap: wrap; }
  .tabs button { display: inline-flex; align-items: center; gap: 6px; height: 34px; padding: 0 var(--space-3); border: 0; border-radius: var(--radius-s); background: transparent; color: var(--ink-2); font: inherit; font-weight: 500; cursor: pointer; }
  .tabs button:hover { background: var(--sunken); color: var(--ink); }
  .tabs button[aria-selected='true'] { background: var(--surface); color: var(--ink); box-shadow: inset 0 0 0 1px var(--line); }
  .count { min-width: 20px; padding: 0 6px; border-radius: 999px; background: var(--sunken); color: var(--ink-2); font-size: 12px; text-align: center; }
  .count.warn { background: var(--warn-soft); color: var(--warn); font-weight: 700; }
  .pending { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); }
  .problems { list-style: none; margin: 0; padding: 0 var(--space-5) var(--space-2); }
  .problems li { display: flex; align-items: center; justify-content: space-between; gap: var(--space-4); padding: var(--space-3) 0; border-top: 1px solid var(--line); }
  .problems li:first-child { border-top: 0; }
  .problems .btn { flex: none; }
  @media (max-width: 640px) { .problems li { flex-direction: column; align-items: flex-start; gap: var(--space-2); } }
  .block { display: block; }
  .dflt { margin-left: var(--space-2); font-family: var(--sans, inherit); }
  .tag { display: inline-block; margin: 2px 4px 2px 0; padding: 1px 8px; border-radius: 999px; background: var(--sunken); font-size: 12px; }
  .tag.named { background: var(--accent-soft); color: var(--ink); }
  .tag.all { border: 1px dashed var(--line); background: none; }
  .warn-dot { display: inline-grid; place-items: center; width: 18px; height: 18px; margin-left: 6px; border-radius: 50%; background: var(--warn-soft); color: var(--warn); font-size: 12px; font-weight: 700; cursor: help; }
  tr.off td { color: var(--ink-3); }
  .actions { text-align: right; }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
</style>
