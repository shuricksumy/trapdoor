<script>
  import { api } from './api.js';
  import { toast } from './state.svelte.js';
  import Dialog from './Dialog.svelte';

  // clients: names of the clients that can use a network.
  let { open = $bindable(false), clients = [], onsaved } = $props();

  let nets = $state(null);
  let error = $state('');
  let warnings = $state([]);
  let busy = $state(false);
  let edit = $state(null); // { original, name, description, networks, via, clients: Set }

  $effect(() => {
    if (!open) return;
    nets = null;
    edit = null;
    error = '';
    warnings = [];
    load();
  });

  async function load() {
    try {
      nets = (await api('/named-networks')).networks;
    } catch (e) {
      error = e.message;
    }
  }

  function start(n) {
    error = '';
    warnings = [];
    edit = n
      ? { original: n.name, name: n.name, description: n.description ?? '', networks: n.networks.join(', '), via: n.via ?? '', clients: new Set(n.clients) }
      : { original: '', name: '', description: '', networks: '', via: '', clients: new Set() };
  }

  function toggle(c) {
    const s = new Set(edit.clients);
    s.has(c) ? s.delete(c) : s.add(c);
    edit.clients = s;
  }

  async function save(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const body = {
        name: edit.name.trim(),
        description: edit.description.trim(),
        networks: edit.networks.split(/[\s,]+/).filter(Boolean),
        via: edit.via,
        clients: [...edit.clients].filter((c) => c !== edit.via),
      };
      const r = await api(`/named-networks/${encodeURIComponent(edit.original || body.name)}`, { method: 'PUT', body });
      nets = r.networks;
      warnings = r.warnings ?? [];
      edit = null;
      onsaved?.();
      toast(`Saved ${body.name}. Clients get the change when they reconnect.`);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  async function remove(n) {
    if (!confirm(`Remove ${n.name}? Its routes are taken off its clients.`)) return;
    try {
      const r = await api(`/named-networks/${encodeURIComponent(n.name)}`, { method: 'DELETE' });
      nets = r.networks;
      warnings = r.warnings ?? [];
      onsaved?.();
      toast(`Removed ${n.name}.`);
    } catch (err) {
      error = err.message;
    }
  }
</script>

<Dialog bind:open title="Named networks">
  {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
  {#if warnings.length}
    <div class="banner warn" role="status">{#each warnings as w (w)}<p>{w}</p>{/each}</div>
  {/if}

  {#if edit}
    <form id="namedform" class="form" onsubmit={save}>
      <div class="grid">
        <label class="field">
          <span>Name</span>
          <input bind:value={edit.name} placeholder="e.g. Office LAN" maxlength="40" required autocomplete="off" />
        </label>
        <label class="field">
          <span>Description (optional)</span>
          <input bind:value={edit.description} maxlength="200" autocomplete="off" />
        </label>
      </div>
      <label class="field">
        <span>Networks</span>
        <input bind:value={edit.networks} class="mono" placeholder="192.168.1.0/24, 192.168.2.0/24" required autocomplete="off" spellcheck="false" />
        <span class="hint">Separate with commas.</span>
      </label>
      <label class="field">
        <span>Where they are</span>
        <select bind:value={edit.via}>
          <option value="">Reached through the server (e.g. its own LAN)</option>
          {#each clients as c (c)}<option value={c}>Behind the client {c} (site-to-site)</option>{/each}
        </select>
        {#if edit.via}<span class="hint">The server also needs a route to them: add them under Routes on the Server page.</span>{/if}
      </label>
      <fieldset>
        <legend>Clients that can reach them</legend>
        {#if clients.length === 0}
          <p class="hint">No clients yet.</p>
        {:else}
          <div class="clients">
            {#each clients.filter((c) => c !== edit.via) as c (c)}
              <label class="check"><input type="checkbox" checked={edit.clients.has(c)} onchange={() => toggle(c)} /><span>{c}</span></label>
            {/each}
          </div>
        {/if}
      </fieldset>
    </form>
  {:else if !nets && !error}
    <p class="muted">Loading…</p>
  {:else if nets}
    <p class="muted intro">Networks with a name that you give to several clients at once, e.g. an office LAN. Changing one updates every client that has it.</p>
    {#if nets.length === 0}
      <p class="muted">None yet.</p>
    {:else}
      <ul class="list">
        {#each nets as n (n.name)}
          <li>
            <div class="what">
              <strong>{n.name}</strong>
              <span class="mono small">{n.networks.join(', ')}</span>
              <span class="faint small">
                {n.via ? `Behind ${n.via}` : 'Through the server'} · {n.clients.length === 0 ? 'no clients' : n.clients.length === 1 ? n.clients[0] : `${n.clients.length} clients`}
                {#if n.description}· {n.description}{/if}
              </span>
            </div>
            <div class="row-actions">
              <button class="btn small" onclick={() => start(n)}>Edit</button>
              <button class="btn small danger" onclick={() => remove(n)}>Remove</button>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}

  {#snippet actions()}
    {#if edit}
      <button class="btn" onclick={() => (edit = null)}>Back</button>
      <button class="btn primary" type="submit" form="namedform" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
    {:else}
      <button class="btn" onclick={() => (open = false)}>Close</button>
      <button class="btn primary" onclick={() => start(null)} disabled={!nets}>Add a network</button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .form { display: grid; gap: var(--space-4); }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
  .intro { margin-bottom: var(--space-3); }
  fieldset { border: 1px solid var(--line); border-radius: 10px; padding: var(--space-3); margin: 0; }
  legend { padding: 0 var(--space-1); font-weight: 600; }
  .clients { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: var(--space-1) var(--space-3); }
  .check { display: flex; gap: var(--space-2); align-items: center; }
  .list { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--space-2); }
  .list li { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); padding: var(--space-3); border: 1px solid var(--line); border-radius: 10px; }
  .what { display: grid; gap: 2px; min-width: 0; }
  .what span { overflow-wrap: anywhere; }
  .row-actions { display: flex; gap: var(--space-2); flex-shrink: 0; }
  @media (max-width: 600px) {
    .grid { grid-template-columns: 1fr; }
    .list li { flex-direction: column; align-items: stretch; }
  }
</style>
