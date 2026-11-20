<script>
  // A WireGuard / AmneziaWG client's network, edited on the Network page:
  // its address, what its device sends through the VPN, its DNS and the
  // networks behind it. Its other settings (notes, MTU, keepalive, expiry)
  // are sent back unchanged. The device needs its new config afterwards.
  import { api } from './api.js';
  import { toast } from './state.svelte.js';
  import { checkNetwork } from './networkMap.js';
  import Dialog from './Dialog.svelte';

  // peer: from GET /peers; defaults: GET /settings; subnet: the VPN network
  let { open = $bindable(false), peer = null, defaults = null, subnet = '', onsaved } = $props();

  const ALL = ['0.0.0.0/0', '::/0'];
  let f = $state(null);
  let error = $state('');
  let busy = $state(false);
  let input = $state({ allowed: '', lan: '', dns: '' });
  $effect(() => {
    if (!open || !peer) return;
    f = {
      address: peer.address,
      ownAllowed: !!peer.allowed_ips?.length,
      allowed: [...(peer.allowed_ips?.length ? peer.allowed_ips : defaults?.allowed_ips ?? [])],
      ownDNS: !!peer.dns?.length,
      dns: [...(peer.dns?.length ? peer.dns : defaults?.dns ?? [])],
      lan: [...(peer.lan ?? [])],
    };
    error = '';
    input = { allowed: '', lan: '', dns: '' };
  });
  const full = $derived(f?.allowed.includes('0.0.0.0/0'));
  function setFull(on) {
    f.ownAllowed = true;
    f.allowed = on ? [...f.allowed.filter((x) => !ALL.includes(x)), ...ALL] : f.allowed.filter((x) => !ALL.includes(x));
  }

  function add(kind) {
    const raw = input[kind].trim();
    if (!raw) return;
    if (kind === 'dns') {
      if (!/^[0-9a-fA-F:.]+$/.test(raw)) return (error = `${raw} is not an IP address`);
      f.ownDNS = true;
      if (!f.dns.includes(raw)) f.dns = [...f.dns, raw];
    } else {
      const r = checkNetwork(raw, { list: f[kind], subnet, allowSubnet: kind === 'allowed' });
      if (!r.value) return (error = r.error ?? '');
      if (kind === 'allowed') f.ownAllowed = true;
      f[kind] = [...f[kind], r.value];
    }
    input[kind] = '';
    error = '';
  }
  function remove(kind, v) {
    if (kind === 'allowed') f.ownAllowed = true;
    if (kind === 'dns') f.ownDNS = true;
    f[kind] = f[kind].filter((x) => x !== v);
  }
  function useDefaults(kind) {
    if (kind === 'allowed') {
      f.ownAllowed = false;
      f.allowed = [...(defaults?.allowed_ips ?? [])];
    } else {
      f.ownDNS = false;
      f.dns = [...(defaults?.dns ?? [])];
    }
  }
  const key = (e, kind) => e.key === 'Enter' && (e.preventDefault(), add(kind));

  async function save(e) {
    e.preventDefault();
    if (f.ownAllowed && !f.allowed.length) return (error = 'The client must send something through the VPN, at least the VPN network');
    busy = true;
    try {
      const settings = {
        notes: peer.notes ?? '',
        mtu: peer.mtu || undefined,
        keepalive: peer.keepalive ?? undefined,
        expires_at: peer.expires_at ?? undefined,
        allowed_ips: f.ownAllowed ? f.allowed : [],
        dns: f.ownDNS ? f.dns : [],
      };
      const body = { lan: f.lan, settings };
      if (f.address.trim() && f.address.trim() !== peer.address) body.address = f.address.trim();
      await api(`/peers/${encodeURIComponent(peer.name)}`, { method: 'PUT', body });
      toast(`Saved. ${peer.name} needs its new config (QR code or file on the Clients page) for the device side.`);
      open = false;
      onsaved?.();
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Network for {peer?.name ?? ''}">
  {#if f}
    <form id="wgnetform" class="form" onsubmit={save}>
      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      <label class="field">
        <span>VPN address</span>
        <input bind:value={f.address} inputmode="decimal" autocomplete="off" />
        <span class="hint">In {subnet || 'the VPN network'}; another free address moves the client there.</span>
      </label>

      <label class="check">
        <input type="checkbox" checked={full} onchange={(e) => setFull(e.currentTarget.checked)} />
        <span>Send all internet traffic through the VPN</span>
      </label>

      <fieldset>
        <legend>→ Sends through the VPN <span class="faint small">allowed IPs</span></legend>
        <span class="hint">
          {f.ownAllowed ? 'Its own list.' : 'The server defaults (Network → Routing).'}
          {#if f.ownAllowed}<button type="button" class="link" onclick={() => useDefaults('allowed')}>Use the defaults</button>{/if}
        </span>
        <div class="chips">
          {#each f.allowed as r (r)}<span class="chip mono" class:dflt={!f.ownAllowed}>{r}<button type="button" aria-label="Remove {r}" onclick={() => remove('allowed', r)}>×</button></span>{/each}
        </div>
        <div class="add">
          <input bind:value={input.allowed} placeholder="192.168.1.0/24" aria-label="Add a network it sends through the VPN" onkeydown={(e) => key(e, 'allowed')} />
          <button type="button" class="btn small" onclick={() => add('allowed')}>Add</button>
        </div>
      </fieldset>

      <fieldset>
        <legend>⌂ Behind this client</legend>
        <span class="hint">Only when this device is a router (site-to-site): its own LAN. The server sends traffic for it to this client.</span>
        <div class="chips">
          {#each f.lan as r (r)}<span class="chip mono">{r}<button type="button" aria-label="Remove {r}" onclick={() => remove('lan', r)}>×</button></span>{/each}
        </div>
        <div class="add">
          <input bind:value={input.lan} placeholder="192.168.50.0/24" aria-label="Add a network behind this client" onkeydown={(e) => key(e, 'lan')} />
          <button type="button" class="btn small" onclick={() => add('lan')}>Add</button>
        </div>
      </fieldset>

      <fieldset>
        <legend>DNS</legend>
        <span class="hint">
          {f.ownDNS ? 'Its own servers.' : 'The server defaults.'}
          {#if f.ownDNS}<button type="button" class="link" onclick={() => useDefaults('dns')}>Use the defaults</button>{/if}
        </span>
        <div class="chips">
          {#each f.dns as d (d)}<span class="chip mono" class:dflt={!f.ownDNS}>{d}<button type="button" aria-label="Remove {d}" onclick={() => remove('dns', d)}>×</button></span>{/each}
        </div>
        <div class="add">
          <input bind:value={input.dns} placeholder="1.1.1.1" aria-label="Add a DNS server" onkeydown={(e) => key(e, 'dns')} />
          <button type="button" class="btn small" onclick={() => add('dns')}>Add</button>
        </div>
      </fieldset>
      <p class="hint">The server side applies at once; the device needs its new config (QR code or file on the Clients page).</p>
    </form>
  {/if}
  {#snippet actions()}
    <button class="btn" type="button" onclick={() => (open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="wgnetform" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<style>
  .form { display: grid; gap: var(--space-4); }
  fieldset { border: 0; margin: 0; padding: 0; display: grid; gap: var(--space-2); }
  legend { padding: 0; margin-bottom: var(--space-1); font-weight: 600; }
  .check { display: flex; gap: var(--space-2); align-items: center; }
  .check input { height: auto; }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; }
  .chip { display: inline-flex; align-items: center; gap: 4px; padding: 2px 4px 2px 10px; border-radius: 999px; background: var(--sunken); font-size: 13px; }
  .chip.dflt { border: 1px dashed var(--line); background: none; }
  .chip button { width: 22px; height: 22px; border: 0; border-radius: 50%; background: none; color: var(--ink-2); cursor: pointer; }
  .chip button:hover { color: var(--bad); }
  .add { display: flex; gap: var(--space-2); max-width: 360px; }
  .add input { flex: 1; }
  .link { padding: 0; border: 0; background: none; color: var(--accent); cursor: pointer; font: inherit; text-decoration: underline; }
</style>
