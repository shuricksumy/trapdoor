<script>
  import { onMount } from 'svelte';
  import { api } from './api.js';
  import { currentServer, toast } from './state.svelte.js';

  // Presets and own start / stop scripts of the selected server, run by its
  // vpn container while the server's interface is up.
  let s = $state(null); // as loaded
  let f = $state(null); // the form
  let loadError = $state('');
  let busy = $state(false);
  let error = $state('');
  const isOVPN = $derived(currentServer()?.type === 'openvpn');

  async function load() {
    try {
      s = await api('/scripts');
      f = { dnsOn: !!s.dns_redirect, dns: s.dns_redirect || '', block_dot: s.block_dot, isolate: s.isolate, up: s.up, down: s.down };
      loadError = '';
    } catch (e) {
      loadError = e.message;
    }
  }
  onMount(load);

  const changed = $derived(
    !!f && !!s &&
      ((f.dnsOn ? f.dns.trim() : '') !== (s.dns_redirect || '') || f.block_dot !== s.block_dot || f.isolate !== s.isolate ||
        f.up !== s.up || f.down !== s.down),
  );

  async function save(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      s = await api('/scripts', {
        method: 'PUT',
        body: { dns_redirect: f.dnsOn ? f.dns.trim() : '', block_dot: f.block_dot, isolate: f.isolate, up: f.up, down: f.down },
      });
      f = { ...f, up: s.up, down: s.down };
      toast('Saved. The vpn container applies it within a few seconds.');
      setTimeout(load, 4000);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  const stateText = {
    ok: ['ok', 'Running'],
    error: ['bad', 'Failed'],
    pending: ['warn', 'Being applied…'],
    inactive: ['off', 'Server not running'],
  };
  const vars = ['IFACE', 'SUBNET', 'SERVER_ID', 'TYPE', 'NAT_IFACE'];
</script>

<section class="panel">
  <div class="panel-head">
    <h2>Scripts and extra rules</h2>
    {#if s?.state && stateText[s.state]}<span class="pill {stateText[s.state][0]}">{stateText[s.state][1]}</span>{/if}
  </div>
  {#if loadError}
    <div class="panel-body"><div class="banner bad" role="alert">{loadError}</div></div>
  {:else if !s}
    <p class="panel-body muted">Loading…</p>
  {:else if !s.supported}
    <p class="panel-body muted">Run by a vpn container: this node's agent does not use one (TD_FIREWALL / TD_WIREGUARD=file).</p>
  {:else}
    <form class="panel-body body" onsubmit={save}>
      <p class="muted small">
        Run by the vpn container as root while the server is up (interface <code>{s.iface}</code>), undone when it stops. NAT for the
        VPN network is always on. Who may reach what: the Access page.
      </p>
      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      {#if s.state === 'error'}<div class="banner bad" role="status">{s.message}</div>{/if}

      <fieldset>
        <legend>Presets</legend>
        <label class="check"><input type="checkbox" bind:checked={f.dnsOn} /><span>Send the clients' DNS to</span>
          <input class="dns mono" bind:value={f.dns} placeholder="172.30.0.53" disabled={!f.dnsOn} aria-label="DNS server" />
        </label>
        <span class="hint">Plain DNS (port 53) to any server goes there instead, e.g. your AdGuard or Pi-hole.</span>
        <label class="check"><input type="checkbox" bind:checked={f.block_dot} /><span>Block DNS-over-TLS and DNS-over-QUIC (port 853)</span></label>
        <span class="hint">Apps fall back to plain DNS, which the setting above redirects.</span>
        <label class="check"><input type="checkbox" bind:checked={f.isolate} /><span>Clients cannot reach each other</span></label>
        {#if isOVPN}<span class="hint">With <code>client-to-client</code> in server.conf, OpenVPN passes that traffic itself: remove it there for this to work.</span>{/if}
      </fieldset>

      <fieldset>
        <legend>Own scripts</legend>
        {#if !s.editable}
          <p class="small muted">
            Read-only here: edit <code>{s.files[0].split('/').pop()}</code> and <code>{s.files[1].split('/').pop()}</code> in this server's
            folder on the host (<code>{s.files[0].slice(0, s.files[0].lastIndexOf('/'))}</code> in the containers); they are picked up within a
            second. Or set <code>TD_SCRIPTS=on</code> for this node's agent to edit them here.
          </p>
        {/if}
        <label class="field">
          <span>On start</span>
          <textarea class="mono" rows="6" bind:value={f.up} readonly={!s.editable} spellcheck="false"
            placeholder={s.editable ? '#!/bin/sh\n# e.g.\niptables -A FORWARD -i "$IFACE" -d 192.168.1.0/24 -j ACCEPT' : '(none)'}></textarea>
        </label>
        <label class="field">
          <span>On stop</span>
          <textarea class="mono" rows="4" bind:value={f.down} readonly={!s.editable} spellcheck="false"
            placeholder={s.editable ? '#!/bin/sh\niptables -D FORWARD -i "$IFACE" -d 192.168.1.0/24 -j ACCEPT' : '(none)'}></textarea>
        </label>
        <span class="hint">sh (not bash), 30 s at most, as root in the vpn container. Variables: {#each vars as v, i (v)}{i ? ', ' : ''}<code>${v}</code>{/each}. A change runs the old stop script, then the new start script.</span>
      </fieldset>

      <div class="actions"><button class="btn primary" type="submit" disabled={busy || !changed}>{busy ? 'Saving…' : 'Save'}</button></div>
    </form>
  {/if}
</section>

<style>
  .body { display: grid; gap: var(--space-4); }
  fieldset { border: 1px solid var(--line); border-radius: 10px; padding: var(--space-3) var(--space-4); display: grid; gap: var(--space-2); margin: 0; min-width: 0; }
  legend { padding: 0 var(--space-1); font-weight: 600; font-size: var(--step--1); color: var(--ink-2); }
  .check { flex-wrap: wrap; }
  .dns { width: 14ch; }
  textarea { width: 100%; font-size: var(--step--1); }
  .hint code { font-size: inherit; }
  .actions { display: flex; justify-content: flex-end; }
</style>
