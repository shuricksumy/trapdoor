<script>
  import { onMount } from 'svelte';
  import { api, poll } from '../lib/api.js';
  import { bytes, duration, relative, certState, date } from '../lib/format.js';
  import { settings, toast } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';
  import TodayTrace from '../lib/TodayTrace.svelte';

  let status = $state(null);
  let live = $state(null);
  let liveError = $state('');
  let history = $state([]);
  let today = $state(null);
  let certs = $state([]);
  let now = $state(new Date());

  async function refresh() {
    now = new Date();
    const [st, hs, rep, cs] = await Promise.allSettled([
      api('/status'),
      api('/history/sessions?preset=today&limit=500'),
      api('/stats?preset=today'),
      api('/certs'),
    ]);
    if (st.status === 'fulfilled') status = st.value;
    if (hs.status === 'fulfilled') history = hs.value;
    if (rep.status === 'fulfilled') today = rep.value;
    if (cs.status === 'fulfilled') certs = cs.value;
    try {
      live = await api('/sessions');
      liveError = '';
    } catch (e) {
      liveError = e.message;
    }
  }
  onMount(() => poll(refresh, 15000));

  const attention = $derived(
    certs
      .filter((c) => c.current && c.status !== 'revoked')
      .map((c) => ({ c, st: certState(c, c.kind === 'server' ? 90 : settings.expiryWarnDays) }))
      .filter(({ st }) => st.key === 'expiring' || st.key === 'expired')
      .sort((a, b) => new Date(a.c.not_after) - new Date(b.c.not_after)),
  );
  const caDays = $derived(status?.agent?.ca_not_after ? (new Date(status.agent.ca_not_after) - Date.now()) / 86400000 : Infinity);

  let renewServer = $state({ open: false, name: '', busy: false });
  async function doRenewServer() {
    renewServer.busy = true;
    try {
      await api(`/certs/${renewServer.name}/renew`, { method: 'POST' });
      toast('Server certificate renewed. Restart OpenVPN to use it.');
      renewServer.open = false;
      await refresh();
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      renewServer.busy = false;
    }
  }

  const serverProblem = $derived(
    !status ? '' : !status.agent_ok ? 'The agent is not reachable, so nothing can be changed right now.' : status.agent?.mgmt_error ? 'OpenVPN does not answer on its management interface. Live sessions and kicking are unavailable.' : '',
  );
</script>

<header class="page-head">
  <div>
    <h1>Overview</h1>
    {#if status?.agent_ok}
      <p class="muted">
        {status.agent.hostname}
        {#if status.agent.openvpn_version}running {status.agent.openvpn_version.split(' ').slice(0, 2).join(' ')}{/if}
      </p>
    {/if}
  </div>
</header>

{#if serverProblem}<div class="banner bad" role="alert">{serverProblem}</div>{/if}

<section class="summary" aria-label="Today">
  <div>
    <span class="k">Connected now</span>
    <span class="v">{live ? live.sessions.length : '–'}</span>
  </div>
  <div>
    <span class="k">Downloaded today</span>
    <span class="v">{today ? bytes(today.total.download) : '–'}</span>
  </div>
  <div>
    <span class="k">Uploaded today</span>
    <span class="v">{today ? bytes(today.total.upload) : '–'}</span>
  </div>
  <div>
    <span class="k">Sessions today</span>
    <span class="v">{today ? today.total.sessions : '–'}</span>
  </div>
</section>

<section class="panel">
  <div class="panel-head">
    <h2>Today</h2>
    <a class="small" href="/statistics?preset=today">Traffic details</a>
  </div>
  <div class="panel-body">
    <TodayTrace sessions={history} {now} />
  </div>
</section>

<div class="cols">
  <section class="panel">
    <div class="panel-head">
      <h2>Connected now</h2>
      <a class="small" href="/sessions">All sessions</a>
    </div>
    {#if liveError}
      <p class="empty">Live sessions are unavailable: {liveError}</p>
    {:else if live && live.sessions.length === 0}
      <p class="empty">No one is connected.</p>
    {:else if live}
      <ul class="list">
        {#each live.sessions as s (s.client_id)}
          <li>
            <span class="pill ok">{s.name}</span>
            <span class="mono faint">{s.virtual_address}</span>
            <span class="muted small right">{duration((now - new Date(s.connected_since)) / 1000)}, {bytes(s.bytes_sent + s.bytes_received)}</span>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section class="panel">
    <div class="panel-head">
      <h2>Needs attention</h2>
      <a class="small" href="/certificates?show=expiring">Clients</a>
    </div>
    {#if attention.length === 0 && caDays >= 90}
      <p class="empty">No certificates expire in the next {settings.expiryWarnDays} days.</p>
    {:else}
      <ul class="list">
        {#if caDays < 90}
          <li>
            <span class="pill bad">CA: {status.agent.ca_name}</span>
            <span class="muted small right">Expires {relative(status.agent.ca_not_after)} ({date(status.agent.ca_not_after)}). Every client stops working then.</span>
          </li>
        {/if}
        {#each attention as { c, st } (c.serial)}
          <li>
            <span class="pill {st.tone}">{c.name}</span>
            {#if c.kind === 'server'}<span class="faint small">server certificate</span>{/if}
            <span class="muted small right">{st.key === 'expired' ? 'Expired' : 'Expires'} {relative(c.not_after)}</span>
            {#if c.kind === 'server'}
              <button class="btn small" onclick={() => (renewServer = { open: true, name: c.name, busy: false })}>Renew</button>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<Dialog bind:open={renewServer.open} title="Renew the server certificate?">
  <p>A new certificate for <strong>{renewServer.name}</strong> is issued with the same key. Clients are not affected.</p>
  <p class="muted">OpenVPN keeps using the old certificate until it is restarted, so restart it afterwards (for Docker: <code>docker compose restart openvpn</code>). If server.conf points to a copy of the certificate, update the copy first.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (renewServer.open = false)}>Cancel</button>
    <button class="btn primary" disabled={renewServer.busy} onclick={doRenewServer}>{renewServer.busy ? 'Renewing…' : 'Renew'}</button>
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
  .summary > div {
    display: grid;
    gap: 2px;
    padding: var(--space-4) var(--space-4) var(--space-4) 0;
  }
  .summary > div + div { padding-left: var(--space-4); border-left: 1px solid var(--line); }
  .k { font-size: var(--step--1); color: var(--ink-2); }
  .v { font-size: var(--step-2); font-weight: 500; letter-spacing: -0.01em; }

  .panel { margin-bottom: var(--space-5); }
  .cols { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-5); }
  .cols .panel { margin-bottom: 0; }

  .list { list-style: none; margin: 0; padding: var(--space-3) var(--space-5) var(--space-4); }
  .list li {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: 8px 0;
    border-bottom: 1px solid var(--line);
  }
  .list li:last-child { border-bottom: 0; }
  .right { margin-left: auto; text-align: right; }

  @media (max-width: 900px) {
    .cols { grid-template-columns: 1fr; }
  }
  @media (max-width: 760px) {
    .summary { grid-template-columns: 1fr 1fr; }
    .summary > div:nth-child(3) { padding-left: 0; border-left: 0; }
    .summary > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
  }
</style>
