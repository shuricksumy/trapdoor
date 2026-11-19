<script>
  import ServerBackup from '../lib/ServerBackup.svelte';
  import { onMount } from 'svelte';
  import { api, poll, reloadServers } from '../lib/api.js';
  import { bytes } from '../lib/format.js';
  import { currentServer, navigate, selectServer, servers, toast } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';

  let status = $state(null);
  let peers = $state([]);
  let today = $state(null);

  const srv = $derived(status?.agent?.servers?.find((x) => x.id === servers.current) ?? currentServer());
  const online = $derived(peers.filter((p) => p.connected && !p.disabled).length);

  async function refresh() {
    const [st, ps, rep] = await Promise.allSettled([api('/status'), api('/peers'), api('/stats?preset=today')]);
    if (st.status === 'fulfilled') status = st.value;
    if (ps.status === 'fulfilled') peers = ps.value;
    if (rep.status === 'fulfilled') today = rep.value;
  }
  onMount(() => poll(refresh, 15000));

  let copied = $state(false);
  async function copyKey() {
    try {
      await navigator.clipboard.writeText(srv.public_key);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      toast('Could not copy. Select the key and copy it by hand.', 'bad');
    }
  }

  let remove = $state({ open: false, busy: false });
  async function doRemove() {
    const id = servers.current;
    remove.busy = true;
    try {
      await api('/servers/' + encodeURIComponent(id), { method: 'DELETE' });
      await reloadServers();
      const next = servers.list.find((s) => s.id !== id);
      if (next) selectServer(next.id);
      toast('Server removed.');
      remove.open = false;
      navigate('/');
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      remove.busy = false;
    }
  }
</script>

<header class="page-head">
  <h1>Overview</h1>
  {#if srv}<p class="muted">{srv.name} · {srv.type === 'amneziawg' ? 'AmneziaWG' : 'WireGuard'} interface <span class="mono">{srv.id}</span></p>{/if}
</header>

{#if status && !status.agent_ok}
  <div class="banner bad" role="alert">The agent is not reachable, so nothing can be changed right now.</div>
{:else if srv?.error}
  <div class="banner bad" role="alert">The server has a problem: {srv.error}</div>
{/if}

<section class="summary" aria-label="Clients">
  <div>
    <span class="k">Status</span>
    <span class="v"><span class="pill {srv?.running ? 'ok' : 'off'}">{srv?.running ? 'Running' : 'Not running'}</span></span>
  </div>
  <div>
    <span class="k">Clients</span>
    <span class="v">{peers.length}</span>
  </div>
  <div>
    <span class="k">Connected now</span>
    <span class="v">{online}</span>
  </div>
  <div>
    <span class="k">Traffic today</span>
    <span class="v">{today ? bytes(today.total.download + today.total.upload) : '–'}</span>
  </div>
</section>

<section class="panel">
  <div class="panel-head"><h2>Server</h2><a class="small" href="/clients">Clients</a></div>
  <dl class="info">
    <div><dt>Address clients connect to</dt><dd class="mono">{srv?.endpoint || '–'}</dd></div>
    <div><dt>Listening on</dt><dd class="mono">UDP {srv?.listen_port ?? '–'}</dd></div>
    <div><dt>VPN network</dt><dd class="mono">{srv?.subnet || '–'}</dd></div>
    <div class="key">
      <dt>Public key</dt>
      <dd>
        <code class="mono">{srv?.public_key || '–'}</code>
        {#if srv?.public_key}<button class="btn small" onclick={copyKey}>{copied ? 'Copied' : 'Copy'}</button>{/if}
      </dd>
    </div>
  </dl>
</section>

{#if srv?.obfuscation}
  {@const o = srv.obfuscation}
  <section class="panel">
    <div class="panel-head"><h2>Hiding</h2><span class="small muted">AmneziaWG {o.level}</span></div>
    <p class="panel-body muted small obf-note">
      {o.level === '3.1'
        ? 'Newest (3.1): also hides packet headers and sizes. Every device needs an AmneziaWG app that supports 3.1.'
        : o.level === '2.0'
          ? 'Strongest hiding: needs an up-to-date AmneziaWG app on every device.'
          : 'Most compatible: works with older AmneziaWG apps too.'}
      Every client config carries these values; they cannot change without new configs for all clients.
    </p>
    <dl class="info">
      <div><dt>Junk packets (Jc, Jmin–Jmax)</dt><dd class="mono">{o.jc} × {o.jmin}–{o.jmax} bytes</dd></div>
      <div><dt>Padding (S1, S2{o.s3 || o.s4 ? ', S3, S4' : ''})</dt><dd class="mono">{[o.s1, o.s2, o.s3, o.s4].filter((v, i) => i < 2 || v).join(', ')}</dd></div>
      <div><dt>Header values (H1–H4)</dt><dd class="mono hs">{o.h1}<br />{o.h2}<br />{o.h3}<br />{o.h4}</dd></div>
      {#if o.level === '3.1'}
        <div><dt>Header protection</dt><dd>{o.header_protection_key ? 'On (key in every client config)' : 'Off'}</dd></div>
        <div><dt>Random packet sizes</dt><dd>{o.random_trailers ? 'On' : 'Off'}{o.content_padding ? `, data padding ${o.content_padding} bytes` : ''}</dd></div>
        <div><dt>Cookie replies</dt><dd>{o.disable_cookies ? 'Off (harder to probe, no handshake flood protection)' : 'On'}</dd></div>
        {#if o.rekey_after_time || o.keepalive_timeout}
          <div><dt>Client timings (seconds)</dt><dd class="mono">rekey {o.rekey_after_time || '120'}, reject {o.reject_after_time || '180'}, retry {o.rekey_timeout || '5'}, keepalive {o.keepalive_timeout || '10'}</dd></div>
        {/if}
      {/if}
      {#each ['i1', 'i2', 'i3', 'i4', 'i5'].filter((k) => o[k]) as k (k)}
        <div><dt>Imitation packet {k.toUpperCase()}</dt><dd><code class="mono">{o[k]}</code></dd></div>
      {/each}
    </dl>
  </section>
{/if}

<ServerBackup />

<section class="panel">
  <div class="panel-head"><h2>Remove server</h2></div>
  <div class="panel-body row">
    <p class="muted">Stops this server and deletes its clients and keys. Their devices can no longer connect. Traffic history stays.</p>
    <button class="btn danger" onclick={() => (remove = { open: true, busy: false })}>Remove server</button>
  </div>
</section>

<Dialog bind:open={remove.open} title="Remove this server?">
  <p><strong>{srv?.name}</strong> and its {peers.length} {peers.length === 1 ? 'client' : 'clients'} are deleted. Devices using it can no longer connect. This cannot be undone.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (remove.open = false)}>Cancel</button>
    <button class="btn danger solid" disabled={remove.busy} onclick={doRemove}>{remove.busy ? 'Removing…' : 'Remove server'}</button>
  {/snippet}
</Dialog>

<style>
  .page-head { margin-bottom: var(--space-5); }
  .page-head p { margin-top: var(--space-1); }
  .banner { margin-bottom: var(--space-4); }
  .summary {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    margin-bottom: var(--space-5);
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }
  .summary > div { display: grid; gap: 2px; padding: var(--space-4) var(--space-4) var(--space-4) 0; align-content: start; }
  .summary > div + div { padding-left: var(--space-4); border-left: 1px solid var(--line); }
  .k { font-size: var(--step--1); color: var(--ink-2); }
  .v { font-size: var(--step-2); font-weight: 500; letter-spacing: -0.01em; }
  .v .pill { font-size: var(--step-0); }

  .panel { margin-bottom: var(--space-5); }
  .info { margin: 0; padding: var(--space-2) var(--space-5) var(--space-4); display: grid; }
  .info > div { display: grid; grid-template-columns: 220px 1fr; gap: var(--space-3); padding: 10px 0; border-bottom: 1px solid var(--line); align-items: center; }
  .info > div:last-child { border-bottom: 0; }
  dt { color: var(--ink-2); font-size: var(--step--1); }
  dd { margin: 0; display: flex; align-items: center; gap: var(--space-3); min-width: 0; }
  dd code { word-break: break-all; font-size: 13px; }
  .obf-note { margin: 0; padding: 0 var(--space-5) var(--space-2); }
  .hs { display: block; line-height: 1.6; }
  .row { display: flex; align-items: center; justify-content: space-between; gap: var(--space-4); }
  .row p { margin: 0; }
  .row .btn { flex: none; }

  @media (max-width: 760px) {
    .summary { grid-template-columns: 1fr 1fr; }
    .summary > div:nth-child(3) { padding-left: 0; border-left: 0; }
    .summary > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
    .info > div { grid-template-columns: 1fr; gap: 2px; }
    .row { flex-direction: column; align-items: flex-start; }
  }
</style>
