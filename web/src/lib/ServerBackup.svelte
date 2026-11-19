<script>
  import { download } from './api.js';
  import { servers, toast } from './state.svelte.js';

  // Backup of the selected server: its files and traffic history.
  let pass = $state('');
  let busy = $state(false);
  let error = $state('');

  async function save(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const f = await download(`/api/v1/servers/${encodeURIComponent(servers.current)}/backup`, { passphrase: pass }, 'trapdoor-server-backup');
      toast(`Saved ${f.name}. Restore it with Add a server → From a backup.`);
      pass = '';
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<section class="panel sb">
  <div class="panel-head"><h2>Backup</h2></div>
  <form class="panel-body sb-form" onsubmit={save}>
    <p class="muted small">
      This server only: its keys and certificates, clients, settings, access rules and traffic history. Restore it on this or another
      machine with <em>Add a server → From a backup</em>.
    </p>
    {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
    <div class="sb-row">
      <label class="field">
        <span>Passphrase (recommended)</span>
        <input type="password" bind:value={pass} autocomplete="off" />
      </label>
      <button class="btn" type="submit" disabled={busy}>{busy ? 'Preparing…' : 'Download backup'}</button>
    </div>
  </form>
</section>

<style>
  .sb { margin-top: var(--space-5); }
  .sb-form { display: grid; gap: var(--space-3); }
  .sb-row { display: flex; gap: var(--space-3); align-items: end; flex-wrap: wrap; }
  .sb-row .field { flex: 1 1 240px; }
</style>
