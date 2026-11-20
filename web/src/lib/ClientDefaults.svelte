<script>
  import { api, reloadServers } from './api.js';
  import { toast } from './state.svelte.js';

  // A WireGuard / AmneziaWG server's name, endpoint and client defaults.
  let set = $state(null);
  let f = $state(null);
  let error = $state('');
  let busy = $state(false);
  const list = (v) => v.split(/[\s,]+/).filter(Boolean);

  async function load() {
    try {
      set = await api('/settings');
      f = { ...set, dns: set.dns.join(', '), allowed_ips: set.allowed_ips.join(', '), mtu: set.mtu || '' };
    } catch (e) {
      error = e.message;
    }
  }
  $effect(() => {
    load();
  });

  async function save(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      set = await api('/settings', {
        method: 'PUT',
        body: { ...f, public_port: Number(f.public_port), mtu: Number(f.mtu) || 0, keepalive: Number(f.keepalive) || 0,
          dns: list(f.dns), allowed_ips: set.allowed_ips },
      });
      f = { ...set, dns: set.dns.join(', '), allowed_ips: set.allowed_ips.join(', '), mtu: set.mtu || '' };
      await reloadServers();
      toast('Saved. Every client needs its new config (QR code) for the changes to apply on the device.');
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<section class="panel cd">
  <div class="panel-head"><h2>Settings and client defaults</h2></div>
  {#if !f}
    <p class="panel-body muted">{error || 'Loading…'}</p>
  {:else}
    <form class="panel-body cd-form" onsubmit={save}>
      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      <div class="grid3">
        <label class="field">
          <span>Name</span>
          <input bind:value={f.name} maxlength="64" required />
        </label>
        <label class="field">
          <span>Public address</span>
          <input bind:value={f.public_host} placeholder="vpn.example.com" autocomplete="off" spellcheck="false" required />
        </label>
        <label class="field">
          <span>Public port</span>
          <input type="number" bind:value={f.public_port} min="1" max="65535" required />
        </label>
      </div>
      <p class="hint">Defaults for every client's config; a client can have its own (Clients → Edit).</p>
      <div class="grid3">
        <label class="field">
          <span>DNS</span>
          <input bind:value={f.dns} placeholder="none" autocomplete="off" spellcheck="false" />
        </label>
        <div class="field">
          <span>Allowed IPs</span>
          <span class="mono small">{set.allowed_ips.join(', ') || '–'}</span>
          <span class="hint">Routing, all traffic through the VPN: on the <a href="/network?tab=routing">Network</a> page.</span>
        </div>
        <label class="field">
          <span>MTU</span>
          <input type="number" bind:value={f.mtu} min="1280" max="9000" placeholder="automatic" />
        </label>
        <label class="field">
          <span>Keepalive (seconds)</span>
          <input type="number" bind:value={f.keepalive} min="0" max="600" />
          <span class="hint">0 turns it off.</span>
        </label>
        <label class="check">
          <input type="checkbox" bind:checked={f.psk} />
          <span>Preshared key for new clients</span>
        </label>
      </div>
      <div class="banner warn small">Changes apply to every client's config and QR code: devices keep working with their old config until they import the new one, except after a new address or port.</div>
      <div><button class="btn primary" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button></div>
    </form>
  {/if}
</section>

<style>
  .cd { margin-top: var(--space-5); }
  .cd-form { display: grid; gap: var(--space-3); }
  .grid3 { display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--space-3); align-items: start; }
  .check { display: flex; gap: var(--space-2); align-items: center; padding-top: var(--space-5); }
  @media (max-width: 760px) { .grid3 { grid-template-columns: 1fr; } .check { padding-top: 0; } }
</style>
