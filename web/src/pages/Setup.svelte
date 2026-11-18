<script>
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { navigate, settings, toast } from '../lib/state.svelte.js';

  let status = $state(null);
  let loadError = $state('');
  let f = $state(null);
  let dnsInput = $state('');
  let busy = $state(false);
  let error = $state('');
  let result = $state(null);
  let running = $state(false);
  let samePort = $state(true);

  const ipv4 = /^(\d{1,3}\.){3}\d{1,3}$/;
  const host = /^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$|^[0-9a-fA-F:]+$/;

  onMount(async () => {
    try {
      status = await api('/setup');
      const d = status.defaults;
      const guess = ['localhost', '127.0.0.1', '::1'].includes(location.hostname) ? '' : location.hostname;
      f = { ...d, public_host: guess, public_port: d.public_port || d.port, dns: d.dns ?? [] };
      samePort = f.public_port === f.port;
    } catch (e) {
      loadError = e.message;
    }
  });

  const port = $derived(f ? (samePort ? f.port : f.public_port) : 0);
  const clients = $derived.by(() => {
    const m = /\/(\d+)$/.exec(f?.subnet ?? '');
    return m ? Math.max(0, 2 ** (32 - Number(m[1])) - 3) : 0;
  });
  const hostOk = $derived(f && host.test(f.public_host.trim()));
  const subnetOk = $derived(f && /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/.test(f.subnet));
  const canSubmit = $derived(f && hostOk && subnetOk && !busy);

  const tlsLabel = {
    'tls-crypt-v2': 'tls-crypt-v2, a key per client',
    'tls-crypt': 'tls-crypt, one shared key',
    'tls-auth': 'tls-auth, one shared key (signing only)',
    none: 'none',
  };
  const keyLabel = { ec256: 'EC P-256', ec384: 'EC P-384', rsa2048: 'RSA 2048', rsa3072: 'RSA 3072', rsa4096: 'RSA 4096' };

  function addDNS() {
    const v = dnsInput.trim();
    if (!v) return;
    if (!ipv4.test(v) && !v.includes(':')) {
      error = `${v} is not an IP address`;
      return;
    }
    error = '';
    if (!f.dns.includes(v)) f.dns = [...f.dns, v];
    dnsInput = '';
  }

  async function submit(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const body = { ...f, public_host: f.public_host.trim(), public_port: samePort ? f.port : Number(f.public_port), port: Number(f.port) };
      result = await api('/setup', { method: 'POST', body });
      running = result.running;
      settings.setupNeeded = false;
      if (!running) waitForOpenVPN();
    } catch (e) {
      error = e.message;
    } finally {
      busy = false;
    }
  }

  async function waitForOpenVPN() {
    for (let i = 0; i < 60 && !running; i++) {
      await new Promise((r) => setTimeout(r, 3000));
      try {
        const st = await api('/status');
        if (st.agent?.openvpn_version) {
          running = true;
          toast('OpenVPN is running.');
        }
      } catch {}
    }
  }
</script>

<header class="head">
  <h1>Set up the VPN server</h1>
  <p class="muted">Answer three questions. tunnelward creates the certificate authority, the server certificate and keys, <code>server.conf</code> and the client profile template, then starts OpenVPN.</p>
</header>

{#if loadError}
  <div class="banner bad" role="alert">Could not reach the agent: {loadError}</div>
{:else if status && !status.needed && !result}
  <div class="panel done">
    <h2>This server is already set up</h2>
    <p class="muted">Change its settings on the Server page. Every change is checked and backed up.</p>
    <div><a class="btn primary" href="/server">Open server settings</a></div>
  </div>
{:else if result}
  <div class="panel done">
    {#if running}
      <h2><i class="dot ok" aria-hidden="true"></i> OpenVPN is running</h2>
      <p class="muted">Clients connect to <span class="mono">{f.public_host.trim()}:{port}</span> over {f.proto.toUpperCase()}. Make sure that port is open in your router or firewall and forwarded to this server.</p>
      <div class="row">
        <button class="btn primary" onclick={() => navigate('/certificates')}>Create the first client</button>
        <a class="btn ghost" href="/server">View server.conf</a>
      </div>
    {:else}
      <h2><i class="dot wait" aria-hidden="true"></i> Waiting for OpenVPN</h2>
      <p class="muted">{result.message}</p>
      <div class="row"><a class="btn ghost" href="/server">View server.conf</a></div>
    {/if}
    {#if result.warnings?.length}
      <ul class="warnings">
        {#each result.warnings as w (w.message)}<li>{w.message}</li>{/each}
      </ul>
    {/if}
  </div>
{:else if f}
  <div class="layout">
    <form class="steps" onsubmit={submit}>
      <section class="panel step">
        <h2><span class="n" aria-hidden="true">1</span> Where clients connect</h2>
        <label class="field">
          <span>Public address</span>
          <input bind:value={f.public_host} placeholder="vpn.example.com" autocomplete="off" autocapitalize="none" spellcheck="false" required aria-invalid={f.public_host !== '' && !hostOk} />
          <span class="hint">The hostname or IP address of this server as seen from the internet. Use a DNS name if your IP changes.</span>
        </label>
        <div class="grid">
          <label class="field">
            <span>Protocol</span>
            <select bind:value={f.proto} disabled={!!status.fixed_proto}>
              <option value="udp">UDP (recommended)</option>
              <option value="tcp">TCP (for networks that block UDP)</option>
            </select>
          </label>
          <label class="field">
            <span>Port</span>
            <input type="number" min="1" max="65535" bind:value={f.port} disabled={!!status.fixed_port} />
          </label>
        </div>
        {#if status.fixed_port}
          <p class="hint">Port and protocol come from the container setup (<code>VPN_PORT</code> and <code>VPN_PROTO</code> in <code>.env</code>); OpenVPN listens on {status.fixed_port} inside the container.</p>
        {/if}
        <label class="check">
          <input type="checkbox" bind:checked={samePort} />
          <span>Clients use the same port <span class="hint">Turn off when your router or Docker publishes a different port.</span></span>
        </label>
        {#if !samePort}
          <label class="field narrow">
            <span>Public port</span>
            <input type="number" min="1" max="65535" bind:value={f.public_port} />
          </label>
        {/if}
      </section>

      <section class="panel step">
        <h2><span class="n" aria-hidden="true">2</span> VPN network</h2>
        <label class="field narrow">
          <span>Subnet</span>
          <input bind:value={f.subnet} placeholder="10.8.0.0/24" spellcheck="false" aria-invalid={!subnetOk} />
          <span class="hint">{clients ? `Room for ${clients} devices.` : 'A private IPv4 network.'} Pick one that is not used at home or work.</span>
        </label>
        <label class="check">
          <input type="checkbox" bind:checked={f.full_tunnel} />
          <span>Send all internet traffic through the VPN <span class="hint">Otherwise devices use the VPN only for the VPN network.</span></span>
        </label>
        <label class="check">
          <input type="checkbox" bind:checked={f.client_to_client} />
          <span>Devices can reach each other</span>
        </label>
        <div class="field">
          <span>DNS servers for devices</span>
          {#if f.dns.length}
            <div class="chips">
              {#each f.dns as d (d)}
                <span class="chip mono">{d}<button type="button" aria-label="Remove {d}" onclick={() => (f.dns = f.dns.filter((x) => x !== d))}>×</button></span>
              {/each}
            </div>
          {/if}
          <div class="add">
            <input bind:value={dnsInput} placeholder="1.1.1.1" aria-label="Add a DNS server" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), addDNS())} />
            <button type="button" class="btn small" onclick={addDNS}>Add</button>
          </div>
          <span class="hint">Optional. With all traffic through the VPN, set one so names resolve.</span>
        </div>
      </section>

      <section class="panel step">
        <h2><span class="n" aria-hidden="true">3</span> Security</h2>
        <label class="check">
          <input type="checkbox" bind:checked={f.two_factor} />
          <span>Ask for a one-time code at every connect <span class="hint">Each client then needs an authenticator app set up on the Clients page before it can connect.</span></span>
        </label>
        <details class="advanced">
          <summary>Certificates and encryption</summary>
          <div class="grid">
            <label class="field">
              <span>Certificate authority name</span>
              <input bind:value={f.ca_name} maxlength="64" disabled={status.has_pki} />
              {#if status.has_pki}<span class="hint">The existing authority is kept.</span>{/if}
            </label>
            <label class="field">
              <span>Key type</span>
              <select bind:value={f.key_type} disabled={status.has_pki}>
                {#each Object.entries(keyLabel) as [k, label] (k)}<option value={k}>{label}{k === 'ec256' ? ' (recommended)' : ''}</option>{/each}
              </select>
            </label>
            <label class="field">
              <span>Control channel protection</span>
              <select bind:value={f.tls}>
                {#each Object.entries(tlsLabel) as [k, label] (k)}<option value={k}>{label}{k === 'tls-crypt-v2' ? ' (recommended)' : ''}</option>{/each}
              </select>
            </label>
          </div>
          {#if f.tls === 'none'}<p class="banner warn">Without control channel protection the server answers anyone who probes the port.</p>{/if}
        </details>
      </section>

      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      <div class="submit">
        <button class="btn primary" type="submit" disabled={!canSubmit}>{busy ? 'Setting up… this can take a minute' : 'Create the server'}</button>
      </div>
    </form>

    <aside class="panel preview" aria-label="Summary">
      <h2>Devices will get</h2>
      <pre class="mono">remote {f.public_host.trim() || '…'} {port}
proto {f.proto}{f.two_factor ? '\nauth-user-pass' : ''}</pre>
      <dl>
        <dt>Network</dt><dd class="mono">{f.subnet}</dd>
        <dt>Traffic</dt><dd>{f.full_tunnel ? 'Everything' : 'VPN network only'}</dd>
        <dt>DNS</dt><dd>{f.dns.length ? f.dns.join(', ') : 'Their own'}</dd>
        <dt>Login</dt><dd>{f.two_factor ? 'Certificate + code' : 'Certificate'}</dd>
        <dt>Keys</dt><dd>{keyLabel[f.key_type]}, {f.tls}</dd>
      </dl>
      <p class="hint">Nothing is written until you create the server.</p>
    </aside>
  </div>
{/if}

<style>
  .head { margin-bottom: var(--space-5); max-width: 68ch; }
  .head p { margin-top: var(--space-1); }
  .layout { display: grid; grid-template-columns: minmax(0, 680px) 300px; gap: var(--space-5); align-items: start; }
  .steps { display: grid; gap: var(--space-4); max-width: 680px; }
  .step { display: grid; gap: var(--space-3); padding: var(--space-5); }
  .step h2 { display: flex; align-items: center; gap: var(--space-3); font-size: var(--step-1); }
  .n { display: inline-grid; place-items: center; width: 26px; height: 26px; border-radius: 50%; background: var(--sunken); color: var(--ink-2); font-size: var(--step--1); font-weight: 600; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: var(--space-3); }
  .narrow { max-width: 280px; }
  .check { display: flex; gap: var(--space-2); align-items: baseline; }
  .check input { height: auto; }
  .check .hint { display: block; }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; }
  .chip { display: inline-flex; align-items: center; gap: 4px; padding: 2px 4px 2px 8px; border-radius: var(--radius-s); background: var(--sunken); font-size: 13px; }
  .chip button { width: 20px; height: 20px; border: 0; border-radius: 3px; background: none; color: var(--ink-2); font-size: 15px; line-height: 1; cursor: pointer; }
  .add { display: flex; gap: var(--space-2); max-width: 360px; }
  .add input { flex: 1; }
  .advanced summary { cursor: pointer; color: var(--ink-2); font-weight: 500; }
  .advanced[open] summary { margin-bottom: var(--space-3); }
  .advanced .banner { margin-top: var(--space-3); }
  .submit { display: flex; justify-content: flex-end; }
  input[aria-invalid='true'] { border-color: var(--bad); }

  .preview { position: sticky; top: var(--space-5); display: grid; gap: var(--space-3); padding: var(--space-5); }
  .preview h2 { font-size: var(--step-0); }
  pre { margin: 0; padding: var(--space-3); border-radius: var(--radius-s); background: var(--sunken); font-size: 13px; line-height: 1.6; white-space: pre-wrap; word-break: break-all; }
  dl { display: grid; grid-template-columns: auto 1fr; gap: 6px var(--space-3); margin: 0; font-size: var(--step--1); }
  dt { color: var(--ink-3); }
  dd { margin: 0; }

  .done { display: grid; gap: var(--space-3); padding: var(--space-5); max-width: 680px; }
  .done h2 { display: flex; align-items: center; gap: var(--space-2); }
  .row { display: flex; gap: var(--space-2); flex-wrap: wrap; }
  .dot { width: 10px; height: 10px; border-radius: 50%; display: inline-block; }
  .dot.ok { background: var(--ok); }
  .dot.wait { background: var(--warn); animation: pulse 1.4s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.35; } }
  @media (prefers-reduced-motion: reduce) { .dot.wait { animation: none; } }
  .warnings { margin: 0; padding-left: var(--space-4); font-size: var(--step--1); color: var(--warn); }
  code { font-family: var(--mono); font-size: 0.92em; }

  @media (max-width: 960px) {
    .layout { grid-template-columns: 1fr; }
    .preview { position: static; order: -1; }
  }
</style>
