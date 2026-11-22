<script>
  import Fold from './Fold.svelte';
  import { api, poll } from './api.js';
  import { servers, toast } from './state.svelte.js';
  import { nodeOf } from './location.js';
  import { bytes } from './format.js';
  import Dialog from './Dialog.svelte';

  // Scheduled backups: when, how many to keep, the files, run now.
  let info = $state(null);
  let off = $state(false); // TD_BACKUP_DIR=-
  let loadError = $state('');
  let f = $state(null); // the form, a copy of info.schedule
  let saving = $state(false);
  let running = $state(false);
  let deleting = $state({ open: false, name: '' });

  async function load() {
    try {
      info = await api('/backups');
      if (!f) f = { ...info.schedule };
      loadError = '';
    } catch (e) {
      if (e.status === 501) off = true;
      else loadError = e.message;
    }
  }
  $effect(() => poll(load, 15_000));

  const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  const hours = Array.from({ length: 24 }, (_, h) => h);
  const changed = $derived(
    !!f && !!info && ['every', 'weekday', 'hour', 'keep', 'servers'].some((k) => f[k] !== info.schedule[k]),
  );
  const fmt = (t) => new Date(t).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });

  async function save(e) {
    e.preventDefault();
    saving = true;
    try {
      await api('/backups/schedule', { method: 'PUT', body: { ...f, keep: Number(f.keep), hour: Number(f.hour), weekday: Number(f.weekday) } });
      toast(f.every === 'off' ? 'Scheduled backups are off.' : 'Schedule saved.');
      f = null;
      await load();
    } catch (err) {
      toast(err.message, 'bad');
    } finally {
      saving = false;
    }
  }

  async function runNow() {
    running = true;
    try {
      const st = await api('/backups/run', { method: 'POST' });
      toast(`Backup done: ${st.files?.length ?? 0} ${st.files?.length === 1 ? 'file' : 'files'}.`);
    } catch (err) {
      toast(err.message, 'bad');
    } finally {
      running = false;
      await load();
    }
  }

  async function remove() {
    try {
      await api(`/backups/files/${encodeURIComponent(deleting.name)}`, { method: 'DELETE' });
      toast(`Deleted ${deleting.name}.`);
      deleting.open = false;
      await load();
    } catch (err) {
      toast(err.message, 'bad');
    }
  }

  // Newest run first, "Everything" first within a run.
  const sorted = $derived(
    [...(info?.files ?? [])].sort((a, b) => stamp(b.name).localeCompare(stamp(a.name)) || allFirst(a, b) || a.name.localeCompare(b.name)),
  );
  const stamp = (n) => /-(\d{8}-\d{6})\./.exec(n)?.[1] ?? '';
  const allFirst = (a, b) => (b.name.startsWith('trapdoor-all-') ? 1 : 0) - (a.name.startsWith('trapdoor-all-') ? 1 : 0);

  const kind = (name) => {
    const m = /^trapdoor-(all|server-(.+))-\d{8}-\d{6}\./.exec(name);
    if (!m) return name;
    if (m[1] === 'all') return 'Everything (all nodes)';
    const sv = servers.list.find((x) => x.id === m[2]);
    return sv ? `${sv.name} · node ${nodeOf(sv).name}` : `Server ${m[2]}`;
  };
  // For the folded header.
  const summary = $derived.by(() => {
    if (off) return 'off (TD_BACKUP_DIR=-)';
    if (!info) return '';
    const sc = info.schedule;
    const when = sc.every === 'off' ? 'off' : `${sc.every === 'weekly' ? `weekly on ${days[sc.weekday]}` : 'daily'} at ${String(sc.hour).padStart(2, '0')}:00 · keep ${sc.keep}`;
    const last = info.last ? (info.last.ok ? ` · last ${fmt(info.last.time)}` : ' · last one failed') : '';
    return when + last;
  });
</script>

<Fold id="acct-schedule" title="Scheduled backups" summary={summary}>
  {#if off}
    <p class="panel-body muted">Turned off (<code>TD_BACKUP_DIR=-</code>).</p>
  {:else if loadError}
    <div class="panel-body"><div class="banner bad" role="alert">Could not load the backups: {loadError}</div></div>
  {:else if !info || !f}
    <p class="panel-body muted">Loading…</p>
  {:else}
    <div class="panel-body body">
      <p class="muted small">
        Everything (and, if you like, each server on its own) is saved on a schedule to <code>{info.dir}</code> on the web UI's machine,
        including the other nodes. Mount that folder somewhere safe, e.g. a NAS.
      </p>
      {#if !info.encrypted}
        <div class="banner warn" role="status">
          Not encrypted: set <code>TD_BACKUP_PASSPHRASE</code> for the web service. The files hold your CA keys.
        </div>
      {/if}
      {#if info.last}
        <div class="banner {info.last.ok ? 'ok' : 'bad'}" role="status">
          {#if info.last.ok}
            Last backup {fmt(info.last.time)}{info.last.manual ? ' (run now)' : ''}: {info.last.files?.length ?? 0} files, {Math.max(1, Math.round(info.last.duration_seconds))} s.
          {:else}
            Last backup {fmt(info.last.time)} failed: {info.last.error}
          {/if}
        </div>
      {/if}

      <form class="grid" onsubmit={save}>
        <label class="field">
          <span>Run</span>
          <select bind:value={f.every}>
            <option value="off">Off</option>
            <option value="daily">Every day</option>
            <option value="weekly">Every week</option>
          </select>
        </label>
        {#if f.every === 'weekly'}
          <label class="field">
            <span>On</span>
            <select bind:value={f.weekday}>{#each days as d, i (d)}<option value={i}>{d}</option>{/each}</select>
          </label>
        {/if}
        {#if f.every !== 'off'}
          <label class="field">
            <span>At</span>
            <select bind:value={f.hour}>{#each hours as h (h)}<option value={h}>{String(h).padStart(2, '0')}:00</option>{/each}</select>
          </label>
          <label class="field">
            <span>Keep</span>
            <input type="number" min="1" max="365" bind:value={f.keep} />
            <span class="hint">Newest files of each kind; older ones are deleted.</span>
          </label>
          <label class="check wide">
            <input type="checkbox" bind:checked={f.servers} />
            <span>Also one file per server (to restore or move a single server)</span>
          </label>
        {/if}
        <div class="actions wide">
          <span class="muted small">{#if info.next}Next: {fmt(info.next)}{/if}</span>
          <span class="btns">
            <button class="btn" type="button" disabled={running || info.running} onclick={runNow}>{running || info.running ? 'Backing up…' : 'Back up now'}</button>
            <button class="btn primary" type="submit" disabled={saving || !changed}>{saving ? 'Saving…' : 'Save schedule'}</button>
          </span>
        </div>
      </form>

      {#if info.files.length}
        <div class="table-wrap">
          <table class="files">
            <thead><tr><th>Backup of</th><th>Made</th><th class="num">Size</th><th><span class="sr">Actions</span></th></tr></thead>
            <tbody>
              {#each sorted as file (file.name)}
                <tr>
                  <td title={file.name}>{kind(file.name)}</td>
                  <td class="small nowrap">{fmt(file.time)}</td>
                  <td class="num mono small nowrap">{bytes(file.size)}</td>
                  <td class="acts">
                    <a class="btn small" href="/api/v1/backups/files/{encodeURIComponent(file.name)}" download>Download</a>
                    <button class="btn small ghost danger" onclick={() => (deleting = { open: true, name: file.name })}>Delete</button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else}
        <p class="muted small">No backup files yet.</p>
      {/if}
    </div>
  {/if}
</Fold>

<Dialog bind:open={deleting.open} title="Delete this backup?">
  <p class="mono small">{deleting.name}</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (deleting.open = false)}>Cancel</button>
    <button class="btn danger solid" onclick={remove}>Delete</button>
  {/snippet}
</Dialog>

<style>
  .body { display: grid; gap: var(--space-4); }
  .grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--space-4); align-items: start; }
  .wide { grid-column: 1 / -1; }
  .check { display: flex; gap: var(--space-2); align-items: center; }
  .actions { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
  .btns { display: flex; gap: var(--space-2); }
  .files .num { text-align: right; }
  .nowrap { white-space: nowrap; }
  .acts { white-space: nowrap; text-align: right; }
  .acts .btn { margin-left: var(--space-1); }
  .btn.danger { color: var(--bad); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
  @media (max-width: 700px) {
    .grid { grid-template-columns: 1fr 1fr; }
    .files th:nth-child(3), .files td:nth-child(3) { display: none; }
  }
</style>
