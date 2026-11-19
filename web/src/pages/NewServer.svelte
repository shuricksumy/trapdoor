<script>
  import { api, reloadServers, upload } from '../lib/api.js';
  import { canRun, containerFor, machineName, typeNames } from '../lib/location.js';
  import { navigate, selectServer, servers, toast } from '../lib/state.svelte.js';

  import { route } from '../lib/state.svelte.js';

  // OpenVPN (set up with the wizard afterwards), WireGuard, or AmneziaWG:
  // WireGuard with traffic that is hard to detect.
  const kinds = {
    openvpn: { label: 'OpenVPN', prefix: 'ovpn', port: 1195, net: 0, app: 'OpenVPN' },
    wireguard: { label: 'WireGuard', prefix: 'wg', port: 51820, net: 9, app: 'WireGuard' },
    amneziawg: { label: 'AmneziaWG', prefix: 'awg', port: 51830, net: 50, app: 'AmneziaWG' },
  };
  const asked = route.query.get('type');
  const initial = kinds[asked] || asked === 'restore' ? asked : 'wireguard';
  let type = $state(initial);
  const isOVPN = $derived(type === 'openvpn');
  // Restore: the containers of the chosen machine, by type.
  const containerList = $derived(
    ['openvpn', 'wireguard', 'amneziawg']
      .filter((t) => containerFor(t, f.machine))
      .map((t) => `${typeNames[t]}: ${containerFor(t, f.machine)}`)
      .join(', '),
  );
  // 'restore': a server from its backup file (Server page → Backup).
  const isRestore = $derived(type === 'restore');
  let backupFile = $state(null);
  let backupPass = $state('');
  let replace = $state(false);

  async function restoreBackup() {
    const form = new FormData();
    form.append('passphrase', backupPass);
    form.append('host', f.machine);
    form.append('replace', replace ? '1' : '0');
    form.append('file', backupFile);
    const sv = await upload('/api/v1/servers/restore', form);
    await reloadServers();
    selectServer(sv.id);
    toast(`Restored ${sv.name}.`);
    navigate('/');
  }

  // Ids, ports and subnets only have to differ on the same machine.
  const onMachine = (machine) => servers.list.filter((s) => (s.host ?? '') === machine);
  const localID = (s) => (s.host ? s.id.slice(s.host.length + 1) : s.id);

  function freeID(prefix, machine) {
    const ids = onMachine(machine).map(localID);
    for (let i = 0; i < 100; i++) {
      if (!ids.includes(`${prefix}${i}`)) return `${prefix}${i}`;
    }
    return '';
  }

  // A second server needs its own port and subnet, so suggest the next ones.
  function defaults(t, machine = '') {
    const k = kinds[t];
    let taken = onMachine(machine).filter((s) => s.type === t).length;
    if (t === 'openvpn') taken = Math.max(0, taken - 1); // the first one has its own port (VPN_PORT)
    return { id: freeID(k.prefix, machine), port: k.port + taken, subnet: `10.${k.net + taken}.0.0/24` };
  }
  let f = $state({
    ...defaults(initial === 'restore' ? 'wireguard' : initial),
    name: '',
    host: '',
    level: '2.0',
    imitation: '',
    dns: '',
    publicPort: '',
    proto: 'tcp',
    machine: '', // '' = this machine, else a host name
    mtu: '',
    allowed: '0.0.0.0/0, ::/0',
    keepalive: 25,
  });
  let busy = $state(false);
  let error = $state('');

  function pick(t) {
    if (t === type) return;
    type = t;
    if (t === 'restore') return;
    Object.assign(f, defaults(t, f.machine));
  }

  // OpenVPN: "tun…" names belong to the first server's device.
  const idOk = $derived(
    /^[a-z][a-z0-9-]{0,14}$/.test(f.id) &&
      !servers.list.some((s) => s.id === (f.machine ? `${f.machine}.${f.id}` : f.id)) &&
      !(isOVPN && f.id.startsWith('tun')),
  );
  const list = (s) => s.split(/[\s,]+/).filter(Boolean);

  async function create(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      if (isRestore) {
        await restoreBackup();
        return;
      }
      if (type === 'openvpn') {
        const sv = await api('/servers', {
          method: 'POST',
          body: { type, host: f.machine, id: f.id, name: f.name.trim() || f.id, port: Number(f.port), proto: f.proto },
        });
        await reloadServers();
        selectServer(sv.id);
        toast(`Added ${sv.name}. Finish its setup.`);
        navigate('/setup');
        return;
      }
      const body = {
        type,
        host: f.machine,
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
      if (type === 'amneziawg') {
        body.awg_level = f.level;
        const packets = f.imitation.split('\n').map((l) => l.trim()).filter(Boolean);
        if (packets.length) {
          body.obfuscation = {};
          packets.slice(0, 5).forEach((p, i) => (body.obfuscation[`i${i + 1}`] = p));
        }
      }
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
  <h1>Add a server</h1>
  {#if isRestore}
    <p class="muted">Bring a server back from its backup file, on this machine or another one: with its keys, clients, settings and traffic history.</p>
  {:else if isOVPN}
    <p class="muted">A second OpenVPN server next to your other servers, for example on TCP 443 for networks that block other ports. It gets its own certificates and clients; the setup comes next.</p>
  {:else}
    <p class="muted">A new {kinds[type].label} network next to your other servers. Clients scan a QR code in the {kinds[type].app} app to join.</p>
  {/if}
</header>

<form class="panel form" onsubmit={create}>
  {#if error}<div class="banner bad" role="alert">{error}</div>{/if}

  <div class="kinds" role="radiogroup" aria-label="Type">
    <button type="button" role="radio" aria-checked={type === 'openvpn'} class:on={type === 'openvpn'} disabled={!canRun('openvpn', f.machine)} title={canRun('openvpn', f.machine) ? '' : `OpenVPN is turned off on ${machineName(f.machine)}`} onclick={() => pick('openvpn')}>
      <strong>OpenVPN</strong>
      <span>Works almost everywhere, also over TCP. Uses the OpenVPN apps.</span>
    </button>
    <button type="button" role="radio" aria-checked={type === 'wireguard'} class:on={type === 'wireguard'} disabled={!canRun('wireguard', f.machine)} title={canRun('wireguard', f.machine) ? '' : `WireGuard is turned off on ${machineName(f.machine)}`} onclick={() => pick('wireguard')}>
      <strong>WireGuard</strong>
      <span>Fast and simple. Works with the official WireGuard apps.</span>
    </button>
    <button type="button" role="radio" aria-checked={type === 'amneziawg'} class:on={type === 'amneziawg'} disabled={!canRun('amneziawg', f.machine)} title={canRun('amneziawg', f.machine) ? '' : `AmneziaWG is turned off on ${machineName(f.machine)}`} onclick={() => pick('amneziawg')}>
      <strong>AmneziaWG</strong>
      <span>WireGuard that is hard to detect and block. Needs the AmneziaWG app.</span>
    </button>
    <button type="button" role="radio" aria-checked={isRestore} class:on={isRestore} onclick={() => pick('restore')}>
      <strong>From a backup</strong>
      <span>A server saved with Backup on its page, with its clients and history.</span>
    </button>
  </div>

  {#if servers.hosts.length}
    <label class="field">
      <span>Machine</span>
      <select bind:value={f.machine} onchange={() => isRestore || Object.assign(f, defaults(type, f.machine))}>
        <option value="">This machine</option>
        {#each servers.hosts as h (h.name)}<option value={h.name} disabled={!!h.error}>{h.name}{h.error ? ' (not reachable)' : ''}</option>{/each}
      </select>
    </label>
  {/if}

  {#if isRestore}
    <label class="field">
      <span>Backup file</span>
      <input type="file" accept=".tdbackup,.gz,application/gzip,application/octet-stream" onchange={(e) => (backupFile = e.currentTarget.files?.[0] ?? null)} required />
      <span class="hint">A .tdbackup (encrypted) or .tar.gz file of one server.</span>
    </label>
    <label class="field">
      <span>Passphrase</span>
      <input type="password" bind:value={backupPass} autocomplete="off" />
      <span class="hint">The one given when the backup was made; empty for a .tar.gz.</span>
    </label>
    <label class="check">
      <input type="checkbox" bind:checked={replace} />
      <span>Replace a server with the same id on that machine (its current clients and keys are swapped for the backup's)</span>
    </label>
  {:else}
  <div class="grid">
    <label class="field">
      <span>Name</span>
      <input bind:value={f.name} placeholder="e.g. Home" autocomplete="off" />
      <span class="hint">Shown in the server list. Optional.</span>
    </label>
    <label class="field">
      <span>Interface name</span>
      <input bind:value={f.id} class="mono" autocomplete="off" autocapitalize="none" spellcheck="false" maxlength="15" required />
      <span class="hint" class:bad={f.id && !idOk}>Lower-case letters, digits and dashes, at most 15. Must not exist yet{isOVPN ? ' or start with "tun"' : ''}.</span>
    </label>
  </div>

  {#if isOVPN}
    <div class="grid">
      <label class="field">
        <span>Port</span>
        <input type="number" bind:value={f.port} min="1" max="65535" required />
        <span class="hint">In Docker, the port the vpn container publishes for it (VPN2_PORT).</span>
      </label>
      <label class="field">
        <span>Protocol</span>
        <select bind:value={f.proto}>
          <option value="udp">UDP (faster)</option>
          <option value="tcp">TCP (gets through more firewalls)</option>
        </select>
      </label>
    </div>
  {:else}
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

  {#if type === 'amneziawg'}
    <fieldset class="obf">
      <legend>Hiding</legend>
      <label class="choice">
        <input type="radio" bind:group={f.level} value="2.0" />
        <span><strong>Strongest</strong> (AmneziaWG 2.0): hardest to detect. Every device needs an up-to-date AmneziaWG app.</span>
      </label>
      <label class="choice">
        <input type="radio" bind:group={f.level} value="3.1" />
        <span><strong>Newest</strong> (3.1): also hides packet headers, sizes and timing. Only for AmneziaWG apps that support 3.1.</span>
      </label>
      <label class="choice">
        <input type="radio" bind:group={f.level} value="1.5" />
        <span><strong>Most compatible</strong> (1.5): also works with older AmneziaWG apps and the Linux kernel module.</span>
      </label>
      <p class="hint">Random values are chosen for this server; all its clients get the same ones. You see them on the server's overview.</p>
      <details>
        <summary>Imitation packets (optional)</summary>
        <label class="field">
          <span>I1–I5, one per line</span>
          <textarea bind:value={f.imitation} class="mono" rows="3" spellcheck="false" placeholder="<b 0xc700000001><r 16><t>"></textarea>
          <span class="hint">Packets the client sends before connecting, to look like another protocol. Tags: &lt;b 0xHEX&gt; fixed bytes, &lt;r N&gt; random bytes, &lt;rc N&gt; letters, &lt;rd N&gt; digits, &lt;t&gt; time. Leave empty if unsure.</span>
        </label>
      </details>
    </fieldset>
  {/if}

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

  {/if}
  {/if}

  <!-- What exactly happens, and where. -->
  <div class="summary" role="status">
    {#if isRestore}
      Restores the server in the backup on <strong>{machineName(f.machine)}</strong>{#if containerList}, in the container for its type ({containerList}){/if}.
    {:else}
      Adds <strong>{typeNames[type]} {f.id || '…'}</strong> on <strong>{machineName(f.machine)}</strong>{#if containerFor(type, f.machine)}, in container <strong class="mono">{containerFor(type, f.machine)}</strong>{/if}, {isOVPN ? (f.proto || 'udp').toUpperCase() : 'UDP'} port {f.port || '…'}.
      {#if !canRun(type, f.machine)}<span class="bad">{typeNames[type]} is turned off on that machine.</span>{/if}
    {/if}
  </div>

  <div class="actions">
    <a class="btn" href="/">Cancel</a>
    {#if isRestore}
      <button class="btn primary" type="submit" disabled={busy || !backupFile}>{busy ? 'Restoring…' : 'Restore server'}</button>
    {:else}
      <button class="btn primary" type="submit" disabled={busy || !idOk || !canRun(type, f.machine) || (!isOVPN && !f.host.trim())}>{busy ? 'Adding…' : 'Add server'}</button>
    {/if}
  </div>
</form>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .form { display: grid; gap: var(--space-4); padding: var(--space-5); max-width: 720px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-4); align-items: start; }
  .hint.bad { color: var(--bad); }
  .kinds { display: grid; grid-template-columns: repeat(2, 1fr); gap: var(--space-3); }
  .kinds button {
    display: grid; gap: 2px; text-align: left; padding: var(--space-3) var(--space-4);
    border: 1px solid var(--line); border-radius: 10px; background: var(--surface); color: var(--ink); cursor: pointer; font: inherit;
  }
  .kinds button span { color: var(--ink-2); font-size: 13px; }
  .kinds button:disabled { opacity: 0.45; cursor: not-allowed; }
  .summary { padding: var(--space-3) var(--space-4); border-radius: 10px; background: var(--sunken); color: var(--ink-2); }
  .summary strong { color: var(--ink); }
  .summary .bad { display: block; color: var(--bad); }
  .kinds button.on { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
  .obf { border: 1px solid var(--line); border-radius: 10px; padding: var(--space-3) var(--space-4); display: grid; gap: var(--space-2); margin: 0; }
  .obf legend { padding: 0 var(--space-1); font-weight: 600; }
  .obf .choice { display: grid; grid-template-columns: auto 1fr; gap: var(--space-2); align-items: start; margin: 0; padding: 2px 0; }
  .obf .choice input { margin: 4px 0 0; }
  .choice span { color: var(--ink-2); }
  .check { display: flex; gap: var(--space-2); align-items: flex-start; }
  .check input { margin-top: 3px; }
  .choice strong { color: var(--ink); }
  .obf details summary { cursor: pointer; color: var(--ink-2); font-weight: 500; }
  .obf textarea { width: 100%; }
  .adv summary { cursor: pointer; color: var(--ink-2); font-weight: 500; margin-bottom: var(--space-3); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); }
  @media (max-width: 600px) { .grid, .kinds { grid-template-columns: 1fr; } .form { padding: var(--space-4); } }
</style>
