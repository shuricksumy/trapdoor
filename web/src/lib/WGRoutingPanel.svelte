<script>
  // WireGuard / AmneziaWG routing next to the map: the allowed IPs every client
  // gets unless it has its own (what its device sends through the VPN), and
  // whether clients reach each other. Devices get changes with a new config.
  import { untrack } from 'svelte';
  import { api, reloadServers } from './api.js';
  import { toast } from './state.svelte.js';
  import { clientName } from './names.svelte.js';
  import { checkNetwork, overlaps, covers } from './networkMap.js';

  // settings: GET /settings; map: buildWGMap(); subnet; isolate: scripts preset; own: clients with their own allowed IPs
  let { settings, map, subnet = '', isolate = false, own = [], onsaved } = $props();

  const ALL = ['0.0.0.0/0', '::/0'];
  const pick = (st) => [...(st.allowed_ips ?? [])];
  let draft = $state(untrack(() => pick(settings)));
  let seen = $state(untrack(() => JSON.stringify(settings.allowed_ips)));
  $effect(() => {
    const now = JSON.stringify(settings.allowed_ips);
    if (now !== seen) {
      seen = now;
      draft = pick(settings);
    }
  });
  const dirty = $derived(JSON.stringify(draft) !== JSON.stringify(pick(settings)));
  const full = $derived(draft.includes('0.0.0.0/0'));
  function setFull(on) {
    draft = on ? [...draft.filter((x) => !ALL.includes(x)), ...ALL] : draft.filter((x) => !ALL.includes(x));
  }

  let input = $state('');
  let error = $state('');
  let note = $state('');
  function add() {
    const r = checkNetwork(input, { list: draft, subnet, allowSubnet: true });
    error = r.error ?? '';
    note = r.note ?? '';
    if (!r.value) return;
    draft = [...draft, r.value];
    input = '';
  }
  const remove = (r) => (draft = draft.filter((x) => x !== r));

  const lanOwners = $derived(map.networks.filter((n) => n.via).map((n) => [n.cidrs[0], n.via]));
  const where = (r) => {
    if (subnet && overlaps(r, subnet)) return 'the VPN network: other clients and the server';
    const v = lanOwners.filter(([c]) => covers(r, c) || covers(c, r)).map(([, x]) => clientName(x));
    return v.length ? `behind ${v.join(', ')}` : 'through the server';
  };
  const listed = $derived(draft.filter((x) => !ALL.includes(x)));

  let busy = $state(false);
  async function save() {
    busy = true;
    try {
      const next = await api('/settings', { method: 'PUT', body: { ...settings, allowed_ips: draft } });
      await reloadServers();
      toast('Saved. Clients get it with their new config (QR code or file).');
      onsaved?.(next);
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      busy = false;
    }
  }
</script>

<section class="panel routing">
  <div class="panel-head">
    <h2>Routing</h2>
    <a class="small" href="/server">Server settings</a>
  </div>
  <div class="body">
    <p class="line"><span class="k">VPN network</span><span class="mono">{subnet || '–'}</span></p>

    <label class="check">
      <input type="checkbox" checked={full} onchange={(e) => setFull(e.currentTarget.checked)} />
      <span>
        Send all internet traffic of every client through the VPN
        <span class="hint">{full ? 'Clients send everything (0.0.0.0/0, ::/0) through the server.' : 'Split tunnel: only the networks below go through the VPN.'}</span>
      </span>
    </label>
    <p class="line">
      <span class="k">Clients reach each other</span>
      {isolate ? 'No: blocked by the server' : 'Yes, through the server'}
      <a class="small" href="/server">change under Scripts</a>
    </p>

    <fieldset>
      <legend>Every client sends through the VPN <span class="faint small">allowed IPs</span></legend>
      <span class="hint">The networks a device sends to the server, unless the client has its own list (Clients → Edit).</span>
      <ul class="routes">
        {#each listed as r (r)}
          <li>
            <span class="mono">{r}</span>
            <span class="faint small">{where(r)}</span>
            <button type="button" class="x" aria-label="Remove {r}" onclick={() => remove(r)}>×</button>
          </li>
        {:else}
          <li class="none faint small">{full ? 'Everything, see above.' : 'None: clients only reach the server.'}</li>
        {/each}
      </ul>
      <div class="add">
        <input bind:value={input} placeholder="192.168.1.0/24" aria-label="Add a network every client sends through the VPN"
          onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add())} />
        <button type="button" class="btn small" onclick={add}>Add</button>
      </div>
      {#if error}<span class="hint err">{error}</span>{:else if note}<span class="hint">{note}</span>{/if}
    </fieldset>

    {#if own.length}
      <p class="hint">Own allowed IPs (these defaults do not apply): {own.map(clientName).join(', ')}.</p>
    {/if}

    {#if dirty}
      <div class="bar" role="region" aria-label="Unsaved routing changes">
        <span>Unsaved changes. Devices get them with their new config.</span>
        <button class="btn" disabled={busy} onclick={() => (draft = pick(settings))}>Discard</button>
        <button class="btn primary" disabled={busy} onclick={save}>{busy ? 'Saving…' : 'Save'}</button>
      </div>
    {/if}
  </div>
</section>

<style>
  .body { display: grid; gap: var(--space-4); padding: var(--space-2) var(--space-5) var(--space-5); }
  .line { margin: 0; display: flex; gap: var(--space-2); align-items: baseline; flex-wrap: wrap; }
  .k { color: var(--ink-2); font-size: var(--step--1); }
  .check { display: flex; gap: var(--space-3); align-items: baseline; }
  .check input { height: auto; }
  .check > span { display: grid; gap: 2px; }
  fieldset { border: 0; margin: 0; padding: 0; display: grid; gap: var(--space-2); }
  legend { padding: 0; margin-bottom: var(--space-1); font-weight: 600; }
  .routes { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }
  .routes li { display: flex; align-items: center; gap: var(--space-3); padding: 6px 10px; border: 1px solid var(--line); border-radius: var(--radius-s); background: var(--sunken); }
  .routes li.none { border: 0; background: none; padding: 0; }
  .x { margin-left: auto; width: 26px; height: 26px; border: 0; border-radius: var(--radius-s); background: none; color: var(--ink-2); cursor: pointer; font-size: 16px; }
  .x:hover { background: var(--surface); color: var(--bad); }
  .add { display: flex; gap: var(--space-2); max-width: 360px; }
  .add input { flex: 1; }
  .err { color: var(--bad); }
  .bar { display: flex; align-items: center; justify-content: flex-end; gap: var(--space-2); padding: var(--space-3) var(--space-4); border: 1px solid var(--line); border-radius: var(--radius-m); background: var(--surface); box-shadow: var(--shadow); position: sticky; bottom: var(--space-4); }
  .bar span { margin-right: auto; }
  @media (max-width: 640px) { .bar { flex-wrap: wrap; } }
</style>
