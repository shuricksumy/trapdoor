<script>
  import ServerBackup from '../lib/ServerBackup.svelte';
  import Fold from '../lib/Fold.svelte';
  import ServerScripts from '../lib/ServerScripts.svelte';
  import ClientDefaults from '../lib/ClientDefaults.svelte';
  import { where } from '../lib/location.js';
  import { onMount } from 'svelte';
  import { api, poll, reloadServers } from '../lib/api.js';
  import { currentServer, navigate, selectServer, servers, toast } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';

  let status = $state(null);
  let peers = $state([]);

  const srv = $derived(status?.agent?.servers?.find((x) => x.id === servers.current) ?? currentServer());

  async function refresh() {
    const [st, ps] = await Promise.allSettled([api('/status'), api('/peers')]);
    if (st.status === 'fulfilled') status = st.value;
    if (ps.status === 'fulfilled') peers = ps.value;
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
  <h1>Server</h1>
  {#if srv}<p class="muted">{srv.name} · {srv.type === 'amneziawg' ? 'AmneziaWG' : 'WireGuard'} interface <span class="mono">{srv.id}</span></p>{/if}
</header>

{#if status && !status.agent_ok}
  <div class="banner bad" role="alert">The agent is not reachable, so nothing can be changed right now.</div>
{:else if srv?.error}
  <div class="banner bad" role="alert">The server has a problem: {srv.error}</div>
{/if}

<Fold id="wg-connection" title="Connection" summary={srv ? `${srv.endpoint || '–'} · UDP ${srv.listen_port ?? '–'} · ${srv.subnet || '–'}` : ''}>
  {#snippet badge()}<span class="pill {srv?.running ? 'ok' : 'off'}">{srv?.running ? 'Running' : 'Not running'}</span>{/snippet}
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
</Fold>

{#if srv?.obfuscation}
  {@const o = srv.obfuscation}
  <Fold id="wg-hiding" title="Hiding" summary="AmneziaWG {o.level}: the values every client config carries">
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
  </Fold>
{/if}

<ClientDefaults />

<ServerScripts />

<ServerBackup />

<Fold id="remove" title="Remove server" summary="stops the server and deletes its clients and keys">
  <div class="panel-body row">
    <p class="muted">Stops this server and deletes its clients and keys. Their devices can no longer connect. Traffic history stays.</p>
    <button class="btn danger" onclick={() => (remove = { open: true, busy: false })}>Remove server</button>
  </div>
</Fold>

<Dialog bind:open={remove.open} title="Remove this server?">
  <p><strong>{srv?.name}</strong> ({srv?.id}) on <strong>{where(srv)}</strong> and its {peers.length} {peers.length === 1 ? 'client' : 'clients'} are deleted. Devices using it can no longer connect. This cannot be undone.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (remove.open = false)}>Cancel</button>
    <button class="btn danger solid" disabled={remove.busy} onclick={doRemove}>{remove.busy ? 'Removing…' : 'Remove server'}</button>
  {/snippet}
</Dialog>

<style>
  .page-head { margin-bottom: var(--space-5); }
  .page-head p { margin-top: var(--space-1); }
  .banner { margin-bottom: var(--space-4); }
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
    .info > div { grid-template-columns: 1fr; gap: 2px; }
    .row { flex-direction: column; align-items: flex-start; }
  }
</style>
