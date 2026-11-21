<script>
  import { api, reloadServers } from '../lib/api.js';
  import { currentServer, toast, route, setQuery } from '../lib/state.svelte.js';
  import NetworkDialog from '../lib/NetworkDialog.svelte';
  import NamedNetworks from '../lib/NamedNetworks.svelte';
  import { loadClientNames, clientName, clientTitle } from '../lib/names.svelte.js';
  import Dialog from '../lib/Dialog.svelte';
  import NetworkMap from '../lib/NetworkMap.svelte';
  import RoutingPanel from '../lib/RoutingPanel.svelte';
  import WGRoutingPanel from '../lib/WGRoutingPanel.svelte';
  import WGNetworkDialog from '../lib/WGNetworkDialog.svelte';
  import Pager from '../lib/Pager.svelte';
  import { paginate, savedSize } from '../lib/pager.js';
  import { buildMap, buildWGMap, fold } from '../lib/networkMap.js';

  // Routing at a glance, the same for every server type: a map of who reaches
  // what, what needs attention (with a fix), and tabs for the clients, the
  // routing and (OpenVPN) the named networks.
  const server = currentServer();
  let wgEdit = $state({ open: false, peer: null });
  const isWG = ['wireguard', 'amneziawg'].includes(server?.type);
  const kind = server?.type === 'amneziawg' ? 'AmneziaWG' : isWG ? 'WireGuard' : 'OpenVPN';

  // OpenVPN
  let srv = $state(null); // GET /network
  let clients = $state([]); // GET /network/clients
  let nets = $state([]); // named networks
  let cfg = $state(null); // GET /server/config
  let live = $state(new Map()); // name → VPN address while connected
  // WireGuard / AmneziaWG
  let peers = $state([]);
  let settings = $state(null); // GET /settings: client defaults
  let isolate = $state(false);
  // both
  let rules = $state([]); // access rules (to count them per client)
  let loadError = $state('');
  let loaded = $state(false);

  async function load() {
    try {
      if (isWG) {
        const [p, st, sc, fw] = await Promise.all([api('/peers'), api('/settings'), api('/scripts').catch(() => null), api('/firewall').catch(() => null)]);
        peers = p;
        settings = st;
        isolate = !!sc?.isolate;
        rules = fw?.policy?.enabled ? fw.policy.rules : [];
      } else {
        const [n, c, nn, sc, ses, fw] = await Promise.all([
          api('/network'),
          api('/network/clients'),
          api('/named-networks'),
          api('/server/config').catch(() => null),
          api('/sessions').catch(() => null),
          api('/firewall').catch(() => null),
        ]);
        srv = n;
        clients = c;
        nets = nn.networks;
        cfg = sc;
        live = new Map((ses?.sessions ?? []).map((x) => [x.name, x.virtual_address]));
        rules = fw?.policy?.enabled ? fw.policy.rules : [];
        loadClientNames();
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

  const pushed = $derived(cfg?.settings?.push_routes ?? []);
  const subnet = $derived(isWG ? server?.subnet ?? '' : srv?.subnet ?? '');
  const serverIp = $derived(isWG ? (subnet ? subnet.replace(/\.\d+\/\d+$/, '.1') : '') : srv?.server_ip ?? '');
  const map = $derived(
    isWG
      ? settings ? buildWGMap({ subnet, serverIp, peers, defaults: settings }) : null
      : srv ? buildMap({ srv, clients, nets, pushed, online: live, c2c: !!cfg?.settings?.client_to_client }) : null,
  );
  const box = $derived({
    title: `${kind} server`,
    ip: serverIp,
    subnet,
    line: isWG ? `port ${server?.listen_port ?? ''}` : `${srv?.routes?.length ?? 0} route${srv?.routes?.length === 1 ? '' : 's'} into the VPN`,
  });
  const order = $derived(new Map((map?.clients ?? []).map((c, i) => [c.name, i])));
  // the missing-route warnings are in "Needs attention", with a button
  const warnings = $derived([...new Set(clients.flatMap((c) => c.warnings ?? []))].filter((w) => !w.startsWith('server.conf needs "route')));
  const netOf = (n) => nets.find((x) => x.name === n);
  // Tags per cell, one per line; long lists fold to 4 with "+N more".
  let unfolded = $state(new Set());
  const unfold = (key) => (unfolded = new Set([...unfolded, key]));
  const reachTags = (c) => [
    ...(c.named ?? []).filter((u) => u.as === 'route').map((u) => ({ t: u.name, cls: 'named', title: netOf(u.name)?.networks.join(', ') })),
    ...c.routes.map((r) => ({ t: r, cls: 'mono' })),
    ...pushed.filter((r) => !c.routes.includes(r)).map((r) => ({ t: r, cls: 'mono all', title: 'Sent to every client' })),
  ];
  const lanTags = (c) => [
    ...(c.named ?? []).filter((u) => u.as === 'lan').map((u) => ({ t: u.name, cls: 'named lan', title: netOf(u.name)?.networks.join(', ') })),
    ...c.lan.map((r) => ({ t: r, cls: 'mono lan' })),
  ];
  const rulesOf = (name) => rules.filter((r) => r.client === name).length;

  // Tabs, in the URL as ?tab=
  const tabs = isWG ? [['clients', 'Clients'], ['routing', 'Routing']] : [['clients', 'Clients'], ['routing', 'Routing'], ['named', 'Named networks']];
  let tab = $state(tabs.some(([k]) => k === route.query.get('tab')) ? route.query.get('tab') : 'clients');
  $effect(() => setQuery({ tab: tab === 'clients' ? '' : tab }));

  // Editors
  let edit = $state({ open: false, name: '' });
  let namedOpen = $state(false);
  function editClient(name) {
    if (isWG) wgEdit = { open: true, peer: peers.find((p) => p.name === name) ?? null };
    else edit = { open: true, name };
  }
  function openNetwork(n) {
    if (n.named) {
      tab = 'named';
      namedOpen = true;
    } else if (n.via) editClient(n.via);
    else if (isWG) tab = 'routing';
    else {
      const c = clients.find((x) => (x.routes ?? []).includes(n.cidrs[0]));
      if (c) editClient(c.name);
      else tab = 'routing';
    }
  }

  // Search (the clients tab)
  let q = $state('');
  const match = (name, ...more) => {
    const s = q.trim().toLowerCase();
    return !s || [name, clientName(name), ...more.flat()].some((x) => String(x ?? '').toLowerCase().includes(s));
  };
  const sorted = (list) => [...list].sort((a, b) => (order.get(a.name) ?? 1e9) - (order.get(b.name) ?? 1e9));
  const ovpnRows = $derived(sorted(clients).filter((c) => match(c.name, c.static_ip, live.get(c.name), c.routes, c.lan)));
  const wgRows = $derived(sorted(peers).filter((p) => match(p.name, p.address, p.lan, p.notes)));
  const isRouterOVPN = (c) => c.lan.length > 0 || (c.named ?? []).some((u) => u.as === 'lan');
  // Pages of the clients table: back to the first when the search changes.
  let page = $state(1);
  let size = $state(savedSize());
  $effect(() => {
    void q;
    page = 1;
  });
  const ovpnShown = $derived(paginate(ovpnRows, page, size));
  const wgShown = $derived(paginate(wgRows, page, size));

  // Fixes
  let busy = $state('');
  async function addRoute(cidr) {
    if (!cfg) return toast('server.conf could not be read', 'bad');
    busy = cidr;
    try {
      const st = { ...cfg.settings, routes: [...(cfg.settings.routes ?? []), cidr] };
      cfg = await api('/server/config', { method: 'PUT', body: { settings: st, base_version: cfg.version } });
      toast(`Route for ${cidr} saved in server.conf. OpenVPN uses it after a restart.`);
    } catch (e) {
      toast(e.status === 409 ? 'server.conf was changed meanwhile. Try again.' : `${e.message}. The Server page shows what is wrong.`, 'bad');
    } finally {
      busy = '';
      await load();
    }
  }
  async function giveToEveryone(cidr) {
    busy = cidr;
    try {
      settings = await api('/settings', { method: 'PUT', body: { ...settings, allowed_ips: [...settings.allowed_ips, cidr] } });
      await reloadServers();
      toast(`${cidr} added to every client's allowed IPs. Devices get it with their new config (QR code or file).`);
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      busy = '';
      await load();
    }
  }
  async function makeFixed(name) {
    busy = name;
    try {
      await api('/firewall/addresses', { method: 'POST', body: { names: [name] } });
      toast(`${clientName(name)} has a fixed VPN address now. It gets it when it reconnects.`);
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      busy = '';
      await load();
    }
  }
  async function reconnect(name) {
    busy = name;
    try {
      await api(`/certs/${encodeURIComponent(name)}/sessions`, { method: 'DELETE' });
      toast(`Disconnected ${clientName(name)}; it reconnects by itself with its current settings.`);
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      busy = '';
      setTimeout(load, 1500);
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
</script>

{#snippet tags(key, list, empty)}
  {@const f = fold(list, unfolded.has(key))}
  {#if list.length}
    <div class="tags">
      {#each f.shown as x (x.t)}<span class="tag {x.cls}" title={x.title}>{x.t}</span>{/each}
      {#if f.more}<button class="tag more" onclick={() => unfold(key)}>+{f.more} more</button>{/if}
    </div>
  {:else}<span class="faint">{empty}</span>{/if}
{/snippet}

{#snippet wgTable(list)}
        <div class="table-wrap">
          <table class="cards">
            <thead><tr><th>Client</th><th>Address</th><th>Behind it</th><th>Sends through the VPN</th><th>DNS</th><th>Access</th><th><span class="sr">Edit</span></th></tr></thead>
            <tbody>
              {#each list as p (p.name)}
                <tr class:off={p.disabled || p.expired}>
                  <td data-label="Client">
                    <span class="who"><i class="dot" class:on={p.connected} title={p.connected ? 'Connected' : 'Not connected'}></i><strong>{p.name}</strong>{#if p.lan?.length}<span class="router">router</span>{/if}</span>
                    {#if p.disabled}<span class="faint small block">disabled</span>{:else if p.expired}<span class="faint small block">expired</span>{:else if p.notes}<span class="faint small block">{p.notes}</span>{/if}
                  </td>
                  <td class="mono" data-label="Address">{p.address}</td>
                  <td data-label="Behind it" class="tagcell">{@render tags('l:' + p.name, (p.lan ?? []).map((r) => ({ t: r, cls: 'mono lan' })), '–')}</td>
                  <td data-label="Sends" class="tagcell">
                    {@render tags('s:' + p.name, (p.allowed_ips?.length ? p.allowed_ips : settings?.allowed_ips ?? []).map((r) => ({ t: r, cls: p.allowed_ips?.length ? 'mono' : 'mono all' })), '–')}
                    {#if !p.allowed_ips?.length}<span class="faint small">default</span>{/if}
                  </td>
                  <td class="mono" data-label="DNS">{(p.dns?.length ? p.dns : settings?.dns ?? []).join(', ') || '–'}{#if !p.dns?.length && settings?.dns?.length}<span class="faint small dflt">default</span>{/if}</td>
                  <td data-label="Access">{#if rulesOf(p.name)}<a href="/access">{rulesOf(p.name)} rule{rulesOf(p.name) === 1 ? '' : 's'}</a>{:else}<span class="faint">–</span>{/if}</td>
                  <td class="actions"><button class="btn small ghost" onclick={() => editClient(p.name)}>Edit</button></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
{/snippet}

{#snippet ovpnTable(list)}
      <div class="table-wrap">
        <table class="cards">
          <thead><tr><th>Client</th><th>VPN address</th><th>All traffic</th><th>Reaches</th><th>Behind it</th><th>Access</th><th><span class="sr">Actions</span></th></tr></thead>
          <tbody>
            {#each list as c (c.name)}
              {@const now = live.get(c.name)}
              {@const isRouter = isRouterOVPN(c)}
              <tr>
                <td data-label="Client">
                  <span class="who">
                    <i class="dot" class:on={!!now} title={now ? 'Connected' : 'Not connected'}></i>
                    <strong title={clientTitle(c.name)}>{clientName(c.name)}</strong>
                    {#if isRouter}<span class="router">router</span>{/if}
                  </span>
                  {#each c.warnings?.filter((w) => !w.startsWith('server.conf needs "route')) ?? [] as w (w)}<span class="warn-text small block">{w}</span>{/each}
                </td>
                <td data-label="VPN address">
                  {#if c.static_ip}<span class="mono">{c.static_ip}</span>
                  {:else}
                    <span class="faint">automatic</span>{#if now}<span class="mono small block">now {now}</span>{/if}
                    <button class="link small block" disabled={!!busy} onclick={() => makeFixed(c.name)} title="Give it a fixed address (needed for access rules about this client)">{busy === c.name ? 'Fixing…' : 'Make fixed'}</button>
                  {/if}
                </td>
                <td data-label="All traffic">{c.full_tunnel || srv?.pushes_full_tunnel ? 'Yes' : '–'}</td>
                <td data-label="Reaches" class="tagcell">{@render tags('r:' + c.name, reachTags(c), 'VPN network only')}</td>
                <td data-label="Behind it" class="tagcell">{@render tags('l:' + c.name, lanTags(c), '–')}</td>
                <td data-label="Access">{#if rulesOf(c.name)}<a href="/access">{rulesOf(c.name)} rule{rulesOf(c.name) === 1 ? '' : 's'}</a>{:else}<span class="faint">–</span>{/if}</td>
                <td class="actions">
                  {#if now}<button class="btn small ghost" disabled={!!busy} onclick={() => reconnect(c.name)} title="Disconnect it so it reconnects with its current settings">Reconnect</button>{/if}
                  <button class="btn small ghost" onclick={() => editClient(c.name)}>Edit</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
{/snippet}



<header class="head">
  <div>
    <h1>Network</h1>
    <p class="muted">Who reaches what through the VPN, the networks behind clients, and the server's routing.</p>
  </div>
</header>

{#if loadError}<div class="banner bad" role="alert">Could not load the network: {loadError}</div>{/if}

{#if cfg?.restart_pending}
  <div class="banner warn pending" role="status">
    <span>Saved changes to server.conf are not active yet: OpenVPN still runs the previous config.</span>
    <button class="btn small primary" onclick={() => (restart = { open: true, busy: false })}>Apply and restart</button>
  </div>
{/if}

{#if map && (map.clients.length || map.networks.length)}
  <section class="panel">
    <div class="panel-head"><h2>Map</h2></div>
    <div class="panel-body"><NetworkMap {map} server={box} onclient={editClient} onnetwork={openNetwork} /></div>
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
            <button class="btn small primary" disabled={!!busy || !cfg} onclick={() => addRoute(p.cidr)}>{busy === p.cidr ? 'Adding…' : 'Add the route'}</button>
          {:else if p.kind === 'unreached' && isWG}
            <span>No client sends traffic for <strong class="mono">{p.cidr}</strong> (behind {clientName(p.via)}) into the VPN.</span>
            <button class="btn small" disabled={!!busy} onclick={() => giveToEveryone(p.cidr)}>{busy === p.cidr ? 'Adding…' : 'Give it to every client'}</button>
          {:else if p.kind === 'unreached'}
            <span>No client reaches <strong>{p.name}</strong> (behind {clientName(p.via)}) yet.</span>
            <button class="btn small" onclick={() => ((tab = 'named'), (namedOpen = true))}>Give it to clients</button>
          {:else if p.kind === 'overlap'}
            <span><strong>{p.name}</strong> (behind {clientName(p.via)}) and <strong>{p.otherName}</strong> (behind {clientName(p.otherVia)}) overlap: traffic for the shared addresses only reaches one of them.</span>
            <button class="btn small" onclick={() => editClient(p.via)}>Edit {clientName(p.via)}</button>
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
      {label}
      {#if key === 'clients'}<span class="count">{isWG ? peers.length : clients.length}</span>
      {:else if key === 'named'}<span class="count">{nets.length}</span>
      {:else if key === 'routing' && map?.problems.some((p) => p.kind === 'route' || p.kind === 'orphan')}<span class="count warn">!</span>{/if}
    </button>
  {/each}
</div>

{#if tab === 'clients'}
  <section class="panel">
    <div class="panel-head">
      <h2>Clients</h2>
      {#if (isWG ? peers.length : clients.length) > 6}
        <input class="search" type="search" bind:value={q} placeholder="Find a client, address or network" aria-label="Find a client" />
      {/if}
    </div>
    {#if !loaded}
      <p class="panel-body muted">Loading…</p>
    {:else if isWG}
      {#if peers.length === 0}
        <p class="panel-body muted">No clients yet.</p>
      {:else}
        {@render wgTable(wgShown.rows)}
        <Pager bind:page bind:size info={wgShown} label="Pages of clients" />
        <p class="panel-body hint"><span class="tag mono all">10.0.0.0/8</span> from the defaults (Routing tab) · a client's own list replaces them</p>
      {/if}
    {:else if clients.length === 0}
      <p class="panel-body muted">No clients yet.</p>
    {:else}
      {@render ovpnTable(ovpnShown.rows)}
      <Pager bind:page bind:size info={ovpnShown} label="Pages of clients" />
      <p class="panel-body hint"><span class="tag named">Name</span> named network · <span class="tag named lan">LAN</span> network behind the client · <span class="tag mono all">10.0.0.0/8</span> sent to every client (Routing tab) · Access: rules for this client alone (Access page)</p>
    {/if}
  </section>
{:else if tab === 'routing'}
  {#if isWG && settings && map}
    <WGRoutingPanel {settings} {map} {subnet} {isolate} own={peers.filter((p) => p.allowed_ips?.length).map((p) => p.name)} onsaved={(s) => ((settings = s), load())} />
  {:else if !isWG && cfg && map}
    <RoutingPanel {cfg} {srv} {map} onsaved={(c) => ((cfg = c), load())} />
  {:else if loaded}
    <p class="muted">{isWG ? 'The server settings' : 'server.conf'} could not be read.</p>
  {/if}
{:else if tab === 'named'}
  <section class="panel">
    <div class="panel-head"><h2>Named networks</h2><button class="btn small" onclick={() => (namedOpen = true)}>Manage</button></div>
    <p class="panel-body hint intro">Give a network a name once (e.g. <em>Office LAN</em>), put it behind the client that serves it, and give it to the clients that should reach it. Changing it changes every one of them.</p>
    {#if nets.length === 0}
      <p class="panel-body muted">None yet.</p>
    {:else}
      <div class="table-wrap">
        <table class="cards">
          <thead><tr><th>Name</th><th>Networks</th><th>Where</th><th>Clients that reach it</th></tr></thead>
          <tbody>
            {#each nets as n (n.name)}
              <tr>
                <td data-label="Name"><strong>{n.name}</strong>{#if n.description}<span class="faint small block">{n.description}</span>{/if}</td>
                <td class="mono" data-label="Networks">{n.networks.join(', ')}</td>
                <td data-label="Where">{n.via ? `Behind ${clientName(n.via)}` : 'Through the server'}</td>
                <td data-label="Reach it">{n.clients.length ? n.clients.map(clientName).join(', ') : '–'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
{/if}

{#if isWG}
  <WGNetworkDialog bind:open={wgEdit.open} peer={wgEdit.peer} defaults={settings} {subnet} onsaved={load} />
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
  .panel-head { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
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
  .search { max-width: 280px; height: 32px; }
  .intro { margin: 0; padding-top: 0; }
  .block { display: block; }
  .dflt { margin-left: var(--space-2); font-family: var(--sans, inherit); }
  /* one tag per line; a column's tags share its width */
  .tags { display: grid; gap: 4px; }
  .tag { display: flex; align-items: center; gap: 5px; padding: 2px 7px; border: 1px solid transparent; border-radius: 6px; background: var(--sunken); color: var(--ink-2); font-size: 12px; line-height: 1.35; white-space: nowrap; }
  .tags > .tag { margin: 0; }
  .hint .tag { display: inline-flex; margin: 0 2px; vertical-align: 1px; }
  .tag.named { background: var(--accent-soft); color: var(--ink); }
  .tag.lan { border-color: color-mix(in srgb, var(--ok) 45%, transparent); background: var(--ok-soft); color: var(--ink); font-weight: 500; }
  .tag.lan::before { content: ''; width: 6px; height: 6px; border-radius: 2px; background: var(--ok); flex: none; }
  .tag.all { border: 1px dashed var(--line); background: none; }
  .tag.more { border: 1px solid var(--line); background: none; color: var(--accent); font: inherit; font-size: 12px; cursor: pointer; }
  .tag.more:hover { border-color: var(--accent); }
  .tagcell { min-width: 140px; }
  .who { display: inline-flex; align-items: center; gap: 8px; white-space: nowrap; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ink-3); opacity: 0.6; flex: none; }
  .dot.on { background: var(--ok); opacity: 1; }
  .router { padding: 0 6px; border-radius: 999px; border: 1px solid var(--accent); color: var(--accent); font-size: 11px; font-weight: 600; }
  .warn-text { color: var(--warn); }
  .link { padding: 0; border: 0; background: none; color: var(--accent); cursor: pointer; font: inherit; text-decoration: underline; }
  .link:disabled { opacity: 0.5; cursor: default; }
  tr.off td { color: var(--ink-3); }
  .actions { text-align: right; white-space: nowrap; }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
  @media (max-width: 640px) {
    .problems li { flex-direction: column; align-items: flex-start; gap: var(--space-2); }
    .search { max-width: none; width: 100%; }
    /* tables become one card per row */
    .cards thead { display: none; }
    .cards, .cards tbody, .cards tr, .cards td { display: block; width: 100%; }
    .cards tr { padding: var(--space-3) var(--space-4); border-top: 1px solid var(--line); }
    .cards td { padding: 3px 0; border: 0; }
    .cards td[data-label]:not(:first-child)::before { content: attr(data-label); display: inline-block; min-width: 104px; color: var(--ink-3); font-size: var(--step--1); }
    .cards .actions { text-align: left; padding-top: var(--space-2); }
    /* label | value, every value in the second column */
    .cards td[data-label]:not(:first-child) { display: grid; grid-template-columns: 104px minmax(0, 1fr); align-items: start; column-gap: var(--space-2); }
    .cards td[data-label]:not(:first-child)::before { min-width: 0; padding-top: 2px; }
    .cards td[data-label]:not(:first-child) > * { grid-column: 2; justify-self: start; text-align: left; }
    .cards td.tagcell .tags { padding-top: 1px; width: max-content; max-width: 100%; }
  }
</style>
