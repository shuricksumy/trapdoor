<script>
  // Backups: everything in one file now, and on a schedule.
  import Fold from '../lib/Fold.svelte';
  import AdminNav from '../lib/AdminNav.svelte';
  import ScheduledBackups from '../lib/ScheduledBackups.svelte';
  import { download } from '../lib/api.js';
  import { toast } from '../lib/state.svelte.js';

  // Backup: everything in one file, encrypted with a passphrase if given.
  let bpass = $state('');
  let bpass2 = $state('');
  let bbusy = $state(false);
  let berror = $state('');
  async function makeBackup(e) {
    e.preventDefault();
    berror = '';
    if (bpass !== bpass2) {
      berror = 'The passphrases do not match.';
      return;
    }
    bbusy = true;
    try {
      const f = await download('/backup', { passphrase: bpass }, 'trapdoor-backup.tar.gz');
      toast(`Saved ${f.name} (${(f.size / 1024 / 1024).toFixed(1)} MB). Keep it somewhere safe${bpass ? ' with its passphrase' : ''}.`);
      bpass = bpass2 = '';
    } catch (err) {
      berror = err.message;
    } finally {
      bbusy = false;
    }
  }

</script>

<AdminNav />
<header class="head">
  <h1>Backups</h1>
  <p class="muted">Certificates and keys, server settings, clients, access rules, admins, traffic history and the other nodes: now, or on a schedule.</p>
</header>
<div class="sections">
  <Fold id="backup-now" title="Download a backup" summary="one file with everything, now">
    <form class="panel-body form" onsubmit={makeBackup}>
      <p class="muted small">
        One file with everything: certificates and keys, server settings, clients, WireGuard and AmneziaWG servers, access
        rules, admins, traffic history and the other nodes. Restore it with <code>trapdoor restore</code> (see the guide).
      </p>
      {#if berror}<div class="banner bad" role="alert">{berror}</div>{/if}
      <label class="field">
        <span>Passphrase</span>
        <input type="password" bind:value={bpass} autocomplete="new-password" />
        <span class="hint">Encrypts the file. Strongly recommended: it holds your CA keys. Without the passphrase it cannot be restored.</span>
      </label>
      {#if bpass}
        <label class="field">
          <span>Passphrase again</span>
          <input type="password" bind:value={bpass2} autocomplete="new-password" />
        </label>
      {/if}
      <div><button class="btn primary" type="submit" disabled={bbusy}>{bbusy ? 'Preparing…' : 'Download a backup'}</button></div>
    </form>
  </Fold>

  <ScheduledBackups />
</div>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); max-width: 72ch; }
  .sections { max-width: 860px; }
  .form { display: grid; gap: var(--space-4); }
</style>
