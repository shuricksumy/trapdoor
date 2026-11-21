<script>
  // Network tools run in the selected server's network (its agent shares the
  // vpn container's network): ping, traceroute, DNS, TCP ports, HTTP(S) and
  // routes. Results stay on the page while it is open.
  import { api } from '../lib/api.js';
  import { currentServer, route, setQuery } from '../lib/state.svelte.js';
  import { clientName, loadClientNames } from '../lib/names.svelte.js';
  import { where } from '../lib/location.js';

  const server = currentServer();
  const isWG = ['wireguard', 'amneziawg'].includes(server?.type);
  const tools = [
    ['ping', 'Ping', 'Is it reachable, how fast, any loss.'],
    ['traceroute', 'Traceroute', 'The path, hop by hop (up to 20).'],
    ['dns', 'DNS', 'What a name resolves to, from here or from a chosen DNS server.'],
    ['tcp', 'TCP port', 'Is a port open (several at once: 22, 80,443 or 8000-8005).'],
    ['http', 'HTTP(S)', 'Status, timing and certificate of a web address.'],
    ['route', 'Route', 'Which interface and gateway a destination takes; empty: the routing table.'],
  ];
  let tool = $state(tools.some(([k]) => k === route.query.get('tool')) ? route.query.get('tool') : 'ping');
  $effect(() => setQuery({ tool: tool === 'ping' ? '' : tool }));

  let f = $state({ target: '', count: 4, ports: '22, 80, 443', kind: '', server: '', url: 'https://' });
  let busy = $state(false);
  let runs = $state([]); // newest first: { id, tool, target, at, res | error, open }
  let seq = 0;

  // Suggestions from the server: its own address, connected clients, their LANs, DNS servers.
  let picks = $state([]);
  let dnsPicks = $state([]);
  $effect(() => {
    loadClientNames();
    const subnet = server?.subnet ?? '';
    const own = subnet ? [{ v: subnet.replace(/\.\d+\/\d+$/, '.1'), l: 'this server' }] : [];
    Promise.all([
      api('/sessions').catch(() => null),
      isWG ? api('/peers').catch(() => []) : api('/network').catch(() => null),
      isWG ? api('/settings').catch(() => null) : api('/server/config').catch(() => null),
    ]).then(([ses, net, cfg]) => {
      const out = [...own];
      if (!isWG && net?.server_ip) out[0] = { v: net.server_ip, l: 'this server' };
      for (const s of ses?.sessions ?? []) if (s.virtual_address) out.push({ v: s.virtual_address, l: `${clientName(s.name)} (connected)` });
      const lans = isWG ? (net ?? []).flatMap((p) => (p.lan ?? []).map((l) => [l, p.name])) : Object.entries(net?.client_lans ?? {}).flatMap(([c, ls]) => ls.map((l) => [l, c]));
      for (const [l, c] of lans) out.push({ v: l.replace(/\.0\/\d+$/, '.1'), l: `gateway of ${l}, behind ${clientName(c)}` });
      const dns = isWG ? cfg?.dns ?? [] : cfg?.settings?.dns ?? [];
      dnsPicks = dns.map((d) => ({ v: d, l: 'DNS for clients' }));
      for (const d of dns) out.push({ v: d, l: 'DNS for clients' });
      out.push({ v: '1.1.1.1', l: 'internet (Cloudflare)' });
      const seen = new Set();
      picks = out.filter((p) => !seen.has(p.v) && seen.add(p.v));
    });
  });

  async function runTool(e) {
    e?.preventDefault();
    if (busy) return;
    const body = { target: f.target.trim() };
    if (tool === 'ping') body.count = Number(f.count);
    if (tool === 'tcp') body.ports = f.ports;
    if (tool === 'dns') Object.assign(body, { kind: f.kind, server: f.server.trim() });
    if (tool === 'http') Object.assign(body, { url: f.url.trim(), target: undefined });
    const run = { id: ++seq, tool, target: tool === 'http' ? body.url : body.target || 'routing table', at: new Date(), res: null, error: '', open: true };
    runs = [run, ...runs.map((r) => ({ ...r, open: false }))].slice(0, 20);
    busy = true;
    try {
      const res = await api(`/tools/${tool}`, { method: 'POST', body });
      runs = runs.map((r) => (r.id === run.id ? { ...r, res } : r));
    } catch (err) {
      runs = runs.map((r) => (r.id === run.id ? { ...r, error: err.message } : r));
    } finally {
      busy = false;
    }
  }
  const label = (k) => tools.find(([t]) => t === k)?.[1] ?? k;
  const toggle = (id) => (runs = runs.map((r) => (r.id === id ? { ...r, open: !r.open } : r)));
</script>

<header class="head">
  <div>
    <h1>Tools</h1>
    <p class="muted">Run from the network of <strong>{server?.name ?? 'this server'}</strong>{where(server) ? ` (${where(server)})` : ''}: what the VPN server reaches, you test here.</p>
  </div>
</header>

<section class="panel">
  <div class="seg" role="tablist" aria-label="Tool">
    {#each tools as [k, l] (k)}<button role="tab" aria-selected={tool === k} onclick={() => (tool = k)}>{l}</button>{/each}
  </div>
  <form class="panel-body form" onsubmit={runTool}>
    <p class="hint">{tools.find(([k]) => k === tool)[2]}</p>
    <div class="row">
      {#if tool === 'http'}
        <label class="field grow"><span>Address</span><input bind:value={f.url} placeholder="https://photos.example.com/" autocomplete="off" spellcheck="false" required /></label>
      {:else}
        <label class="field grow">
          <span>{tool === 'dns' ? 'Name or IP' : tool === 'route' ? 'IP address (optional)' : 'Host or IP'}</span>
          <input bind:value={f.target} list="tool-picks" placeholder={tool === 'dns' ? 'vpn.example.com' : tool === 'route' ? 'empty: the whole routing table' : '10.8.0.6 or vpn.example.com'}
            autocomplete="off" spellcheck="false" required={tool !== 'route'} />
        </label>
      {/if}
      {#if tool === 'ping'}
        <label class="field small"><span>Packets</span>
          <select bind:value={f.count}>{#each [1, 4, 10] as n (n)}<option value={n}>{n}</option>{/each}</select>
        </label>
      {:else if tool === 'tcp'}
        <label class="field"><span>Ports</span><input bind:value={f.ports} placeholder="22, 80, 443" autocomplete="off" required /></label>
      {:else if tool === 'dns'}
        <label class="field small"><span>Record</span>
          <select bind:value={f.kind}>{#each [['', 'A / AAAA'], ['CNAME', 'CNAME'], ['MX', 'MX'], ['TXT', 'TXT'], ['NS', 'NS'], ['PTR', 'PTR (reverse)']] as [v, l] (v)}<option value={v}>{l}</option>{/each}</select>
        </label>
        <label class="field"><span>Ask DNS server (optional)</span><input bind:value={f.server} list="dns-picks" placeholder="the system's" autocomplete="off" /></label>
      {/if}
      <button class="btn primary run" type="submit" disabled={busy}>{busy ? 'Running…' : 'Run'}</button>
    </div>
    {#if picks.length && tool !== 'http'}
      <div class="chips" aria-label="Suggestions">
        {#each picks.slice(0, 10) as p (p.v)}<button type="button" class="chip" onclick={() => (f.target = p.v)} title={p.l}><span class="mono">{p.v}</span> <span class="faint">{p.l}</span></button>{/each}
      </div>
    {/if}
  </form>
  <datalist id="tool-picks">{#each picks as p (p.v)}<option value={p.v}>{p.l}</option>{/each}</datalist>
  <datalist id="dns-picks">{#each dnsPicks as p (p.v)}<option value={p.v}>{p.l}</option>{/each}</datalist>
</section>

{#each runs as r (r.id)}
  <section class="panel run-card">
    <button class="run-head" onclick={() => toggle(r.id)} aria-expanded={r.open}>
      <span class="what"><strong>{label(r.tool)}</strong> <span class="mono">{r.target}</span></span>
      {#if r.error}<span class="pill bad">{r.error}</span>
      {:else if !r.res}<span class="pill">running…</span>
      {:else}<span class="pill {r.res.ok ? 'ok' : 'bad'}">{r.res.summary}</span>{/if}
      <span class="faint small when">{r.at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}{r.res ? ` · ${(r.res.duration_ms / 1000).toFixed(1)} s` : ''}</span>
    </button>
    {#if r.open && r.res}
      <pre class="out mono">{r.res.output || '(no output)'}{r.res.truncated ? '\n… (cut)' : ''}</pre>
    {/if}
  </section>
{:else}
  <p class="muted empty">Results show up here; the newest on top. One tool runs at a time per node, for up to 30 seconds.</p>
{/each}

<style>
  .head { margin-bottom: var(--space-4); }
  .head p { margin-top: var(--space-1); }
  .panel { margin-bottom: var(--space-4); }
  .seg { display: flex; flex-wrap: wrap; gap: 2px; padding: var(--space-3) var(--space-5) 0; }
  .seg button { height: 32px; padding: 0 var(--space-3); border: 0; border-radius: var(--radius-s); background: transparent; color: var(--ink-2); font: inherit; font-weight: 500; cursor: pointer; }
  .seg button:hover { background: var(--sunken); color: var(--ink); }
  .seg button[aria-selected='true'] { background: var(--sunken); color: var(--ink); box-shadow: inset 0 0 0 1px var(--line); }
  .form { display: grid; gap: var(--space-3); }
  .form .hint { margin: 0; }
  .row { display: flex; gap: var(--space-3); align-items: flex-end; flex-wrap: wrap; }
  .grow { flex: 1 1 260px; }
  .field.small { width: 140px; }
  .run { height: 38px; min-width: 96px; }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; }
  .chip { display: inline-flex; gap: 6px; align-items: baseline; padding: 3px 10px; border: 1px solid var(--line); border-radius: 999px; background: var(--surface); color: var(--ink); font: inherit; font-size: var(--step--1); cursor: pointer; }
  .chip:hover { border-color: var(--accent); }
  .run-card { overflow: hidden; }
  .run-head { display: flex; align-items: center; gap: var(--space-3); width: 100%; padding: var(--space-3) var(--space-5); border: 0; background: none; color: var(--ink); font: inherit; text-align: left; cursor: pointer; flex-wrap: wrap; }
  .what { min-width: 0; overflow-wrap: anywhere; }
  .when { margin-left: auto; }
  .pill { overflow-wrap: anywhere; white-space: normal; text-align: left; }
  .out { margin: 0; padding: var(--space-3) var(--space-5) var(--space-4); border-top: 1px solid var(--line); background: var(--sunken); font-size: 12.5px; line-height: 1.55; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 420px; overflow: auto; }
  .empty { padding: var(--space-3) 0; }
  @media (max-width: 640px) {
    .field.small { width: auto; flex: 1 1 120px; }
    .run { width: 100%; }
    .when { margin-left: 0; width: 100%; }
  }
</style>
