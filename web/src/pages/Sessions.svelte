<script>
  import { onMount } from 'svelte';
  import { api, poll } from '../lib/api.js';
  import { bytes, duration, dateTime } from '../lib/format.js';
  import { toast, currentServer } from '../lib/state.svelte.js';
  import { loadClientNames, clientName, clientTitle } from '../lib/names.svelte.js';

  const kickable = $derived(!['wireguard', 'amneziawg'].includes(currentServer()?.type));
  let live = $state(null);
  let error = $state('');
  let recent = $state([]);
  let now = $state(new Date());

  async function load() {
    now = new Date();
    try {
      live = await api('/sessions');
      error = '';
    } catch (e) {
      error = e.message;
    }
    try {
      recent = (await api('/history/sessions?preset=last_30_days&limit=50')).filter((s) => s.disconnected_at);
    } catch {
      /* history is optional here */
    }
  }
  onMount(() => {
    loadClientNames();
    return poll(load, 10000);
  });

  async function kick(s) {
    try {
      await api(`/sessions/${s.client_id}`, { method: 'DELETE' });
      toast(`Disconnected ${clientName(s.name)}. Their client reconnects on its own unless you revoke it.`);
      await load();
    } catch (e) {
      toast(e.message, 'bad');
    }
  }
</script>

<header class="head">
  <h1>Connected</h1>
  <p class="muted">Live from {kickable ? 'OpenVPN' : (currentServer()?.type === 'amneziawg' ? 'AmneziaWG' : 'WireGuard') + ' (a client counts as connected while its last handshake is recent)'}, refreshed every 10 seconds.</p>
</header>

{#if error}<div class="banner bad" role="alert">Live sessions are unavailable: {error}</div>{/if}

<section class="panel">
  {#if live && live.sessions.length === 0}
    <p class="empty">No one is connected right now.</p>
  {:else if live}
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Client</th>
            <th>VPN address</th>
            <th class="hide-s">Connected from</th>
            <th>Online for</th>
            <th class="num">Down</th>
            <th class="num">Up</th>
            {#if kickable}<th class="hide-s">Cipher</th>{/if}
            <th><span class="sr">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each live.sessions as s (s.client_id)}
            <tr>
              <td><span class="pill ok" title={clientTitle(s.name)}>{clientName(s.name)}</span></td>
              <td class="mono">{s.virtual_address}</td>
              <td class="mono hide-s faint">{s.real_address}</td>
              <td title={dateTime(s.connected_since)}>{duration((now - new Date(s.connected_since)) / 1000)}</td>
              <td class="num">{bytes(s.bytes_sent)}</td>
              <td class="num">{bytes(s.bytes_received)}</td>
              {#if kickable}<td class="mono hide-s faint">{s.cipher}</td>{/if}
              <td class="act">{#if kickable}<button class="btn small" onclick={() => kick(s)}>Disconnect</button>{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="foot small faint">
      Server total since start: {bytes(live.bytes_out)} sent, {bytes(live.bytes_in)} received.
    </p>
  {/if}
</section>

{#if recent.length}
  <section class="panel recent">
    <div class="panel-head"><h2>Recently disconnected</h2></div>
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Client</th>
            <th>From</th>
            <th>To</th>
            <th>Duration</th>
            <th class="num">Down</th>
            <th class="num">Up</th>
          </tr>
        </thead>
        <tbody>
          {#each recent.slice(0, 20) as s, i (i)}
            <tr>
              <td title={clientTitle(s.name)}>{clientName(s.name)}</td>
              <td>{dateTime(s.connected_at)}</td>
              <td>{dateTime(s.disconnected_at)}</td>
              <td>{duration(s.duration_seconds)}</td>
              <td class="num">{bytes(s.download)}</td>
              <td class="num">{bytes(s.upload)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>
{/if}

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .banner { margin-bottom: var(--space-3); }
  .act { text-align: right; }
  .foot { padding: var(--space-3) var(--space-3) var(--space-4); }
  .recent { margin-top: var(--space-5); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
  @media (max-width: 760px) { .hide-s { display: none; } }
</style>
