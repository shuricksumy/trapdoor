<script>
  import { api, reloadServers } from '../lib/api.js';
  import { toast } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';

  let hosts = $state(null);
  let loadError = $state('');
  let f = $state({ name: '', url: '', token: '', fingerprint: '' });
  let busy = $state(false);
  let error = $state('');
  let removing = $state({ open: false, name: '', busy: false });

  async function load() {
    try {
      hosts = await api('/hosts');
      loadError = '';
    } catch (e) {
      loadError = e.message;
    }
  }
  $effect(() => {
    load();
  });

  const nameOk = $derived(/^[a-z][a-z0-9-]{0,30}$/.test(f.name));

  async function add(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const st = await api('/hosts', { method: 'POST', body: { ...f, name: f.name.trim(), url: f.url.trim(), fingerprint: f.fingerprint.trim() } });
      toast(`Added ${st.name}${st.hostname ? ` (${st.hostname})` : ''}. Its servers are in the server list.`);
      f = { name: '', url: '', token: '', fingerprint: '' };
      await Promise.all([load(), reloadServers()]);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  async function remove() {
    removing.busy = true;
    try {
      await api(`/hosts/${encodeURIComponent(removing.name)}`, { method: 'DELETE' });
      toast(`Removed ${removing.name}. Its servers keep running there.`);
      removing.open = false;
      await Promise.all([load(), reloadServers()]);
    } catch (err) {
      toast(err.message, 'bad');
    } finally {
      removing.busy = false;
    }
  }
</script>

<header class="head">
  <h1>Hosts</h1>
  <p class="muted">Other machines with a trapdoor agent. Their servers appear in the server list next to this machine's.</p>
</header>

{#if loadError}<div class="banner bad" role="alert">Could not load hosts: {loadError}</div>{/if}

<section class="panel">
  <div class="panel-head"><h2>Hosts</h2></div>
  {#if hosts && hosts.length === 0}
    <p class="panel-body muted">Only this machine so far.</p>
  {:else if hosts}
    <ul class="list">
      {#each hosts as h (h.name)}
        <li>
          <div class="what">
            <strong>{h.name}</strong>
            <span class="mono small">{h.url}</span>
            {#if h.error}
              <span class="bad small">Not reachable: {h.error}</span>
            {:else}
              <span class="faint small">{h.hostname} · trapdoor {h.version}</span>
            {/if}
          </div>
          <button class="btn small danger" onclick={() => (removing = { open: true, name: h.name, busy: false })}>Remove</button>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<form class="panel form" onsubmit={add}>
  <div class="panel-head"><h2>Add a host</h2></div>
  <div class="panel-body grid-form">
    <p class="muted small">
      On the other machine, run trapdoor with <code>docker-compose.remote.yml</code> (the VPN and the agent, no web UI). Its log and
      <code>docker compose exec agent trapdoor agent-cert</code> show the fingerprint. Allow the agent port only from this machine.
    </p>
    {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
    <div class="grid">
      <label class="field">
        <span>Name</span>
        <input bind:value={f.name} placeholder="e.g. vps" maxlength="31" autocomplete="off" autocapitalize="none" spellcheck="false" required />
        <span class="hint" class:bad={f.name && !nameOk}>Lower-case letters, digits and dashes. Part of its servers' ids.</span>
      </label>
      <label class="field">
        <span>Agent address</span>
        <input bind:value={f.url} placeholder="https://203.0.113.5:9443" class="mono" autocomplete="off" autocapitalize="none" spellcheck="false" required />
      </label>
    </div>
    <label class="field">
      <span>Agent token</span>
      <input type="password" bind:value={f.token} autocomplete="off" required />
      <span class="hint">TD_AGENT_TOKEN from that machine's .env.</span>
    </label>
    <label class="field">
      <span>Certificate fingerprint</span>
      <input bind:value={f.fingerprint} placeholder="sha256:…" class="mono" autocomplete="off" spellcheck="false" required />
      <span class="hint">Only an agent with exactly this certificate is trusted.</span>
    </label>
    <div class="actions">
      <button class="btn primary" type="submit" disabled={busy || !nameOk}>{busy ? 'Connecting…' : 'Add host'}</button>
    </div>
  </div>
</form>

<Dialog bind:open={removing.open} title="Remove {removing.name}?">
  <p>trapdoor stops managing it. Its VPN servers keep running there, and their traffic history stays here.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (removing.open = false)}>Cancel</button>
    <button class="btn danger solid" disabled={removing.busy} onclick={remove}>{removing.busy ? 'Removing…' : 'Remove host'}</button>
  {/snippet}
</Dialog>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .panel { margin-bottom: var(--space-5); max-width: 720px; }
  .list { list-style: none; margin: 0; padding: var(--space-3) var(--space-4); display: grid; gap: var(--space-2); }
  .list li { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); padding: var(--space-3); border: 1px solid var(--line); border-radius: 10px; }
  .what { display: grid; gap: 2px; min-width: 0; }
  .what span { overflow-wrap: anywhere; }
  .bad { color: var(--bad); }
  .grid-form { display: grid; gap: var(--space-4); }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-4); align-items: start; }
  .hint.bad { color: var(--bad); }
  .actions { display: flex; justify-content: flex-end; }
  @media (max-width: 600px) {
    .grid { grid-template-columns: 1fr; }
    .list li { flex-direction: column; align-items: stretch; }
  }
</style>
