<script>
  import { api } from '../lib/api.js';
  import { currentServer } from '../lib/state.svelte.js';
  import NetworkDialog from '../lib/NetworkDialog.svelte';
  import NamedNetworks from '../lib/NamedNetworks.svelte';
  import { loadClientNames, clientName, clientTitle } from '../lib/names.svelte.js';

  // Routing at a glance: the server's network, named networks and what every
  // client can reach (OpenVPN), or every peer's addresses (WireGuard).
  const isWG = ['wireguard', 'amneziawg'].includes(currentServer()?.type);

  let srv = $state(null); // OpenVPN: GET /network
  let pushed = $state([]); // routes pushed to every client
  let clients = $state([]); // OpenVPN: GET /network/clients
  let nets = $state([]); // named networks
  let peers = $state([]); // WireGuard
  let defaults = $state(null); // WireGuard client defaults
  let loadError = $state('');
  let loaded = $state(false);

  async function load() {
    try {
      if (isWG) {
        [peers, defaults] = await Promise.all([api('/peers'), api('/settings')]);
      } else {
        const [n, c, nn, cfg] = await Promise.all([api('/network'), api('/network/clients'), api('/named-networks'), api('/server/config').catch(() => null)]);
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
  const warnings = $derived([...new Set(clients.flatMap((c) => c.warnings ?? []))]);

  let edit = $state({ open: false, name: '' });
  let namedOpen = $state(false);
</script>

<header class="head">
  <div>
    <h1>Network</h1>
    <p class="muted">{isWG ? "Every client's address, the networks behind it and what it sends through the VPN." : 'What every client can reach through the VPN, and the networks behind clients.'}</p>
  </div>
  {#if !isWG}
    <button class="btn" onclick={() => (namedOpen = true)}>Named networks</button>
  {/if}
</header>

{#if loadError}<div class="banner bad" role="alert">Could not load the network: {loadError}</div>{/if}

{#if !isWG && srv}
  <section class="panel">
    <div class="panel-head"><h2>Server</h2><a class="small" href="/server">Change</a></div>
    <dl class="info">
      <div><dt>VPN network</dt><dd class="mono">{srv.subnet || '–'}{srv.server_ip ? ` (server ${srv.server_ip})` : ''}</dd></div>
      <div><dt>Topology</dt><dd>{srv.topology}</dd></div>
      <div><dt>All traffic of every client</dt><dd>{srv.pushes_full_tunnel ? 'Through the VPN' : 'Only the networks below'}</dd></div>
      <div><dt>Every client reaches</dt><dd class="mono">{pushed.length ? pushed.join(', ') : '–'}</dd></div>
      <div><dt>Networks behind clients (route)</dt><dd class="mono">{srv.routes.length ? srv.routes.join(', ') : '–'}</dd></div>
    </dl>
  </section>

  {#if warnings.length}
    <div class="banner warn" role="status">{#each warnings as w (w)}<p>{w}</p>{/each}</div>
  {/if}

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
                {#each pushed as r (r)}<span class="tag mono all" title="Sent to every client">{r}</span>{/each}
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
    <p class="panel-body hint"><span class="tag named">Name</span> named network · <span class="tag mono all">10.0.0.0/8</span> sent to every client (Server page)</p>
  {/if}
</section>

{#if !isWG}
  <NetworkDialog bind:open={edit.open} name={edit.name} onsaved={load} />
  <NamedNetworks bind:open={namedOpen} clients={clients.map((c) => c.name)} onsaved={load} />
{/if}

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-3); flex-wrap: wrap; margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .panel { margin-bottom: var(--space-5); }
  .panel-head { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); }
  .banner { margin-bottom: var(--space-4); }
  .block { display: block; }
  .dflt { margin-left: var(--space-2); font-family: var(--sans, inherit); }
  .tag { display: inline-block; margin: 2px 4px 2px 0; padding: 1px 8px; border-radius: 999px; background: var(--sunken); font-size: 12px; }
  .tag.named { background: var(--accent-soft); color: var(--ink); }
  .tag.all { border: 1px dashed var(--line); background: none; }
  .warn-dot { display: inline-grid; place-items: center; width: 18px; height: 18px; margin-left: 6px; border-radius: 50%; background: var(--warn-soft); color: var(--warn); font-size: 12px; font-weight: 700; cursor: help; }
  tr.off td { color: var(--ink-3); }
  .actions { text-align: right; }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
  .info { margin: 0; padding: var(--space-2) var(--space-5) var(--space-4); display: grid; }
  .info > div { display: grid; grid-template-columns: 240px 1fr; gap: var(--space-3); padding: 10px 0; border-bottom: 1px solid var(--line); }
  .info > div:last-child { border-bottom: 0; }
  dt { color: var(--ink-2); font-size: var(--step--1); }
  dd { margin: 0; overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    .info > div { grid-template-columns: 1fr; gap: 2px; }
    .info { padding: var(--space-2) var(--space-4) var(--space-3); }
  }
</style>
