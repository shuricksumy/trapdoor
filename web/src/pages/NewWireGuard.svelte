<script>
  import { api, reloadServers } from '../lib/api.js';
  import { navigate, selectServer, servers, toast } from '../lib/state.svelte.js';

  function freeID() {
    for (let i = 0; i < 100; i++) {
      if (!servers.list.some((s) => s.id === `wg${i}`)) return `wg${i}`;
    }
    return '';
  }

  // A second server needs its own port and subnet, so suggest the next ones.
  const taken = servers.list.filter((s) => s.type === 'wireguard').length;
  let f = $state({
    id: freeID(),
    name: '',
    host: '',
    port: 51820 + taken,
    subnet: `10.${9 + taken}.0.0/24`,
    dns: '',
    publicPort: '',
    mtu: '',
    allowed: '0.0.0.0/0, ::/0',
    keepalive: 25,
  });
  let busy = $state(false);
  let error = $state('');

  const idOk = $derived(/^[a-z0-9][a-z0-9-]{0,14}$/.test(f.id) && !servers.list.some((s) => s.id === f.id));
  const list = (s) => s.split(/[\s,]+/).filter(Boolean);

  async function create(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const body = {
        type: 'wireguard',
        id: f.id,
        name: f.name.trim() || f.id,
        public_host: f.host.trim(),
        port: Number(f.port),
        subnet: f.subnet.trim(),
        keepalive: Number(f.keepalive),
      };
      if (f.dns.trim()) body.dns = list(f.dns);
      if (f.publicPort) body.public_port = Number(f.publicPort);
      if (f.mtu) body.mtu = Number(f.mtu);
      if (f.allowed.trim()) body.allowed_ips = list(f.allowed);
      const sv = await api('/servers', { method: 'POST', body });
      await reloadServers();
      selectServer(sv.id);
      toast(`Added ${sv.name}. Add a client to connect a device.`);
      navigate('/clients');
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<header class="head">
  <h1>Add WireGuard server</h1>
  <p class="muted">A new WireGuard network next to your other servers. Clients scan a QR code to join.</p>
</header>

<form class="panel form" onsubmit={create}>
  {#if error}<div class="banner bad" role="alert">{error}</div>{/if}

  <div class="grid">
    <label class="field">
      <span>Name</span>
      <input bind:value={f.name} placeholder="e.g. Home" autocomplete="off" />
      <span class="hint">Shown in the server list. Optional.</span>
    </label>
    <label class="field">
      <span>Interface name</span>
      <input bind:value={f.id} class="mono" autocomplete="off" autocapitalize="none" spellcheck="false" maxlength="15" required />
      <span class="hint" class:bad={f.id && !idOk}>Lower-case letters, digits and dashes, at most 15. Must not exist yet.</span>
    </label>
  </div>

  <label class="field">
    <span>Public address</span>
    <input bind:value={f.host} placeholder="vpn.example.com" autocomplete="off" autocapitalize="none" spellcheck="false" required />
    <span class="hint">The address clients connect to, e.g. vpn.example.com.</span>
  </label>

  <div class="grid">
    <label class="field">
      <span>Port (UDP)</span>
      <input type="number" bind:value={f.port} min="1" max="65535" required />
    </label>
    <label class="field">
      <span>VPN network</span>
      <input bind:value={f.subnet} class="mono" autocomplete="off" spellcheck="false" required />
      <span class="hint">The server takes the first address.</span>
    </label>
  </div>

  <label class="field">
    <span>DNS servers (optional)</span>
    <input bind:value={f.dns} placeholder="e.g. 1.1.1.1, 9.9.9.9" autocomplete="off" spellcheck="false" />
    <span class="hint">Separate with commas. Clients use them while connected.</span>
  </label>

  <details class="adv">
    <summary>Advanced</summary>
    <div class="grid">
      <label class="field">
        <span>Public port</span>
        <input type="number" bind:value={f.publicPort} min="1" max="65535" placeholder={String(f.port)} />
        <span class="hint">Only if clients reach the server on another port, e.g. behind port forwarding.</span>
      </label>
      <label class="field">
        <span>MTU</span>
        <input type="number" bind:value={f.mtu} min="576" max="9000" placeholder="default" />
      </label>
      <label class="field">
        <span>Allowed IPs</span>
        <input bind:value={f.allowed} class="mono" autocomplete="off" spellcheck="false" />
        <span class="hint">What clients send through the VPN. The default sends everything.</span>
      </label>
      <label class="field">
        <span>Keepalive (seconds)</span>
        <input type="number" bind:value={f.keepalive} min="-1" max="65535" />
        <span class="hint">Keeps clients behind NAT reachable. -1 turns it off.</span>
      </label>
    </div>
  </details>

  <div class="actions">
    <a class="btn" href="/">Cancel</a>
    <button class="btn primary" type="submit" disabled={busy || !idOk || !f.host.trim()}>{busy ? 'Adding…' : 'Add server'}</button>
  </div>
</form>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .form { display: grid; gap: var(--space-4); padding: var(--space-5); max-width: 720px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-4); align-items: start; }
  .hint.bad { color: var(--bad); }
  .adv summary { cursor: pointer; color: var(--ink-2); font-weight: 500; margin-bottom: var(--space-3); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); }
  @media (max-width: 600px) { .grid { grid-template-columns: 1fr; } .form { padding: var(--space-4); } }
</style>
