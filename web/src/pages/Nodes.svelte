<script>
  import AdminNav from '../lib/AdminNav.svelte';
  import { api, poll, reloadServers } from '../lib/api.js';
  import { navigate, selectServer, servers, toast } from '../lib/state.svelte.js';
  import Dialog from '../lib/Dialog.svelte';
  import { machines, typeNames } from '../lib/location.js';

  // Machines › nodes › VPN servers, and adding a node.
  let loadError = $state('');
  async function load() {
    try {
      await reloadServers();
      loadError = '';
    } catch (e) {
      loadError = e.message;
    }
  }
  $effect(() => poll(load, 10_000));

  const groups = $derived(machines());
  const serversOn = (key) => servers.list.filter((s) => (s.host ?? '') === key);

  function open(s) {
    selectServer(s.id);
    navigate('/');
  }

  // Add a node: a join token (the node connects itself) or by hand.
  let mode = $state('token');
  let t = $state({ name: '', hours: 24, machine: '', url: '' });
  let made = $state(null); // { token, name, expires_at }
  let busy = $state(false);
  let error = $state('');
  const nameRe = /^[a-z][a-z0-9-]{0,30}$/;
  const tokenNameOk = $derived(t.name === '' || nameRe.test(t.name));

  async function makeToken(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      made = await api('/nodes/tokens', { method: 'POST', body: { name: t.name.trim(), hours: Number(t.hours) } });
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  const snippet = $derived(
    made
      ? [
          `TD_JOIN_URL=${location.origin}`,
          `TD_JOIN_TOKEN=${made.token}`,
          `TD_NODE_NAME=${made.name || t.name || 'node-name'}`,
          `TD_NODE_URL=${t.url.trim() || 'https://203.0.113.5:9443'}`,
          `TD_MACHINE=${t.machine.trim() || made.name || t.name || 'machine-name'}`,
        ].join('\n')
      : '',
  );

  async function copy() {
    try {
      await navigator.clipboard.writeText(snippet);
      toast('Copied.');
    } catch {
      toast('Select the text and copy it.', 'bad');
    }
  }

  let f = $state({ name: '', url: '', token: '', fingerprint: '' });
  const nameOk = $derived(nameRe.test(f.name));
  async function add(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const st = await api('/nodes', { method: 'POST', body: { ...f, name: f.name.trim(), url: f.url.trim(), fingerprint: f.fingerprint.trim() } });
      toast(`Added node ${st.name}. Its servers are in the server list.`);
      f = { name: '', url: '', token: '', fingerprint: '' };
      await load();
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  let removing = $state({ open: false, name: '', busy: false });
  async function remove() {
    removing.busy = true;
    try {
      await api(`/nodes/${encodeURIComponent(removing.name)}`, { method: 'DELETE' });
      toast(`Removed node ${removing.name}. Its servers keep running there.`);
      removing.open = false;
      await load();
    } catch (err) {
      toast(err.message, 'bad');
    } finally {
      removing.busy = false;
    }
  }

  const typeLabel = (s) => typeNames[s.type] ?? s.type;

  // "vpn-main: OpenVPN, WireGuard, AmneziaWG": the types each container runs.
  function byContainer(map) {
    const out = new Map();
    for (const t of ['openvpn', 'wireguard', 'amneziawg']) {
      if (!(t in map)) continue;
      const c = map[t] || '';
      out.set(c, [...(out.get(c) ?? []), typeNames[t]]);
    }
    return [...out];
  }

  function serverState(s) {
    if (s.stopped) return { key: 'off', label: 'Stopped' };
    if (s.type === 'openvpn') {
      if (!s.pki_ready) return { key: 'warn', label: 'Not set up' };
      return s.openvpn_version ? { key: 'ok', label: 'Running' } : { key: 'bad', label: 'Not running' };
    }
    return s.running ? { key: 'ok', label: 'Running' } : { key: 'bad', label: 'Not running' };
  }
</script>

<AdminNav />
<header class="head">
  <h1>Nodes</h1>
  <p class="muted">
    A <strong>node</strong> is a trapdoor agent with its VPN container: it runs VPN servers of any type. A <strong>machine</strong> can
    have several nodes, e.g. one per network. This web UI manages the servers on all of them.
  </p>
</header>

<div class="legend small" aria-hidden="true">
  <span class="lv machine">Machine</span><span class="arrow">›</span><span class="lv nd">Node · container</span><span class="arrow">›</span><span class="lv server">VPN server</span>
</div>

{#if loadError}<div class="banner bad" role="alert">Could not load the nodes: {loadError}</div>{/if}

{#each groups as m (m.machine)}
  <section class="panel machine-panel">
    <div class="panel-head">
      <h2><span class="tag">Machine</span> {m.machine}</h2>
      <span class="muted small">{m.nodes.length} {m.nodes.length === 1 ? 'node' : 'nodes'}</span>
    </div>
    <div class="nodes">
      {#each m.nodes as n (n.key)}
        {@const list = serversOn(n.key)}
        <article class="node" class:down={!!n.error}>
          <div class="node-head">
            <div class="what">
              <strong><span class="tag">Node</span> {n.name}</strong>
              {#if n.own}
                <span class="faint small">With this web UI</span>
              {:else if n.error}
                <span class="bad small">Not reachable: {n.error}</span>
              {:else}
                <span class="faint small mono">{n.url}{n.version ? ` · trapdoor ${n.version}` : ''}</span>
              {/if}
            </div>
            {#if !n.own}
              <button class="btn small ghost danger" onclick={() => (removing = { open: true, name: n.name, busy: false })}>Remove</button>
            {/if}
          </div>
          {#if n.containers}
            {#each byContainer(n.containers) as [c, types] (c)}
              <p class="small runs"><span class="tag">Container</span> <span class="mono">{c || '–'}</span>: {types.join(', ')}</p>
            {/each}
          {/if}
          {#if list.length}
            <ul class="servers">
              {#each list as s (s.id)}
                <li>
                  <button class="link" onclick={() => open(s)}>
                    <strong>{s.name}</strong>
                    <span class="faint small">{typeLabel(s)}{s.listen_port ? ` · ${(s.proto || 'udp').toUpperCase()} ${s.listen_port}` : ''}</span>
                  </button>
                  <span class="pill {serverState(s).key}">{serverState(s).label}</span>
                </li>
              {/each}
            </ul>
          {:else}
            <p class="small muted">{n.error ? 'Its servers are not shown while it does not answer.' : 'No VPN servers yet.'}</p>
          {/if}
          {#if !n.error}
            <a class="small" href="/servers/new?node={encodeURIComponent(n.key)}">Add a server on this node</a>
          {/if}
        </article>
      {/each}
    </div>
  </section>
{/each}

<section class="panel add">
  <div class="panel-head"><h2>Add a node</h2></div>
  <div class="panel-body grid-form">
    <div class="seg" role="radiogroup" aria-label="How">
      <button type="button" role="radio" aria-checked={mode === 'token'} class:on={mode === 'token'} onclick={() => ((mode = 'token'), (error = ''))}>With a join token</button>
      <button type="button" role="radio" aria-checked={mode === 'hand'} class:on={mode === 'hand'} onclick={() => ((mode = 'hand'), (error = ''))}>By hand</button>
    </div>
    {#if error}<div class="banner bad" role="alert">{error}</div>{/if}

    {#if mode === 'token'}
      <p class="muted small">
        Start the node (<code>docker-compose.remote.yml</code>: the VPN and the agent, no web UI) with these settings in its
        <code>.env</code>. It connects to this web UI by itself; the token works once. Nodes in this web UI's own compose file join with
        <code>TD_JOIN_SECRET</code> instead.
      </p>
      <form class="grid-form" onsubmit={makeToken}>
        <div class="grid">
          <label class="field">
            <span>Node name</span>
            <input bind:value={t.name} placeholder="e.g. vps" maxlength="31" autocomplete="off" autocapitalize="none" spellcheck="false" />
            <span class="hint" class:bad={!tokenNameOk}>Lower-case letters, digits and dashes. Empty: any name.</span>
          </label>
          <label class="field">
            <span>Valid for</span>
            <select bind:value={t.hours}>
              <option value={1}>1 hour</option>
              <option value={24}>1 day</option>
              <option value={168}>7 days</option>
            </select>
          </label>
          <label class="field">
            <span>Machine</span>
            <input bind:value={t.machine} placeholder="e.g. vps-frankfurt" autocomplete="off" spellcheck="false" />
            <span class="hint">Nodes with the same machine name are shown together.</span>
          </label>
          <label class="field">
            <span>Node address</span>
            <input bind:value={t.url} placeholder="https://203.0.113.5:9443" class="mono" autocomplete="off" autocapitalize="none" spellcheck="false" />
            <span class="hint">How this web UI reaches the node's agent.</span>
          </label>
        </div>
        <div class="actions">
          <button class="btn primary" type="submit" disabled={busy || !tokenNameOk}>{busy ? 'Making…' : made ? 'Make another token' : 'Make a join token'}</button>
        </div>
      </form>
      {#if made}
        <div class="made">
          <p class="small">
            For the node's <code>.env</code>, valid until {new Date(made.expires_at).toLocaleString()}. The token is shown only now.
          </p>
          <pre class="mono small">{snippet}</pre>
          <div class="actions"><button class="btn small" onclick={copy}>Copy</button></div>
          <p class="faint small">Then <code>docker compose -f docker-compose.remote.yml up -d</code> on that machine. The node shows up here within a minute.</p>
        </div>
      {/if}
    {:else}
      <p class="muted small">
        For an agent started without a join token. Its log and <code>docker compose exec agent trapdoor agent-cert</code> show the
        fingerprint. Allow the agent port only from this machine.
      </p>
      <form class="grid-form" onsubmit={add}>
        <div class="grid">
          <label class="field">
            <span>Node name</span>
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
          <span class="hint">TD_AGENT_TOKEN from that node's .env.</span>
        </label>
        <label class="field">
          <span>Certificate fingerprint</span>
          <input bind:value={f.fingerprint} placeholder="sha256:…" class="mono" autocomplete="off" spellcheck="false" required />
          <span class="hint">Only an agent with exactly this certificate is trusted.</span>
        </label>
        <div class="actions">
          <button class="btn primary" type="submit" disabled={busy || !nameOk}>{busy ? 'Connecting…' : 'Add node'}</button>
        </div>
      </form>
    {/if}
  </div>
</section>

<Dialog bind:open={removing.open} title="Remove node {removing.name}?">
  <p>trapdoor stops managing it. Its VPN servers keep running there, and their traffic history stays here. A node with a join token or secret in its settings joins again when it restarts.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (removing.open = false)}>Cancel</button>
    <button class="btn danger solid" disabled={removing.busy} onclick={remove}>{removing.busy ? 'Removing…' : 'Remove node'}</button>
  {/snippet}
</Dialog>

<style>
  .head { margin-bottom: var(--space-3); }
  .head p { margin-top: var(--space-1); max-width: 760px; }
  .legend { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; margin-bottom: var(--space-5); color: var(--ink-2); }
  .lv { white-space: nowrap; padding: 2px 10px; border-radius: 999px; border: 1px solid var(--line); }
  .lv.machine { background: var(--sunken); font-weight: 600; }
  .lv.nd { border-style: dashed; }
  .arrow { color: var(--ink-3); }
  .panel { margin-bottom: var(--space-5); }
  .add { max-width: 760px; }
  .panel-head { display: flex; justify-content: space-between; align-items: baseline; gap: var(--space-3); }
  .tag { font-size: var(--step--1); font-weight: 500; color: var(--ink-3); text-transform: uppercase; letter-spacing: 0.04em; margin-right: 4px; }
  .nodes { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: var(--space-3); padding: var(--space-3) var(--space-4) var(--space-4); }
  .node { border: 1px dashed var(--line); border-radius: 10px; padding: var(--space-3); display: grid; gap: var(--space-2); align-content: start; min-width: 0; }
  .node.down { border-color: var(--bad); }
  .node-head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-2); }
  .what { display: grid; gap: 2px; min-width: 0; }
  .what span { overflow-wrap: anywhere; }
  .runs { margin: 0; color: var(--ink-2); }
  .servers { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--space-1); }
  .servers li { display: flex; justify-content: space-between; align-items: center; gap: var(--space-2); padding: var(--space-2); border: 1px solid var(--line); border-radius: 8px; }
  .link { background: none; border: 0; padding: 0; color: var(--ink); font: inherit; cursor: pointer; text-align: left; display: grid; min-width: 0; }
  .link:hover strong { text-decoration: underline; }
  .bad { color: var(--bad); }
  .btn.danger { color: var(--bad); }
  .grid-form { display: grid; gap: var(--space-4); }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-4); align-items: start; }
  .hint.bad { color: var(--bad); }
  .actions { display: flex; justify-content: flex-end; }
  .seg { display: inline-flex; border: 1px solid var(--line); border-radius: 999px; padding: 2px; justify-self: start; }
  .seg button { background: none; border: 0; padding: 6px 14px; border-radius: 999px; color: var(--ink-2); font: inherit; cursor: pointer; }
  .seg button.on { background: var(--surface); color: var(--ink); font-weight: 600; box-shadow: 0 0 0 1px var(--accent); }
  .made { display: grid; gap: var(--space-2); }
  .made pre { margin: 0; padding: var(--space-3); border: 1px solid var(--line); border-radius: 8px; overflow-x: auto; white-space: pre; user-select: all; }
  @media (max-width: 600px) {
    .grid { grid-template-columns: 1fr; }
    .nodes { grid-template-columns: 1fr; padding: var(--space-3); }
  }
</style>
