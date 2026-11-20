<script>
  import { api, download } from '../lib/api.js';
  import { session, navigate, toast, settings, repoURL, versionURL, shortVersion } from '../lib/state.svelte.js';
  import ThemeSwitch from '../lib/ThemeSwitch.svelte';
  import ScheduledBackups from '../lib/ScheduledBackups.svelte';
  import EmailSettings from '../lib/EmailSettings.svelte';

  let current = $state('');
  let next = $state('');
  let repeat = $state('');
  let error = $state('');
  let busy = $state(false);

  async function change(e) {
    e.preventDefault();
    error = '';
    if (next !== repeat) {
      error = 'The new passwords do not match.';
      return;
    }
    busy = true;
    try {
      await api('/auth/password', { method: 'POST', body: { current_password: current, new_password: next } });
      session.user = null;
      toast('Password changed. Log in with the new password.');
      navigate('/login', { replace: true });
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

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

  async function logout() {
    try {
      await api('/auth/logout', { method: 'POST' });
    } finally {
      session.user = null;
      navigate('/login', { replace: true });
    }
  }
</script>

<header class="head">
  <h1>Account</h1>
  <p class="muted">Signed in as <strong>{session.user?.username}</strong>.</p>
  <p class="muted small">
    Trapdoor {#if settings.version}<a href={versionURL(settings.version)} target="_blank" rel="noopener noreferrer" title={settings.version}>{shortVersion(settings.version)}</a>{/if}
    · <a href={repoURL} target="_blank" rel="noopener noreferrer">GitHub</a>
  </p>
</header>

<div class="grid">
  <section class="panel">
    <div class="panel-head"><h2>Change password</h2></div>
    <form class="panel-body form" onsubmit={change}>
      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      <label class="field">
        <span>Current password</span>
        <input type="password" bind:value={current} autocomplete="current-password" required />
      </label>
      <label class="field">
        <span>New password</span>
        <input type="password" bind:value={next} minlength="10" autocomplete="new-password" required />
        <span class="hint">At least 10 characters. You are logged out everywhere afterwards.</span>
      </label>
      <label class="field">
        <span>Repeat new password</span>
        <input type="password" bind:value={repeat} minlength="10" autocomplete="new-password" required />
      </label>
      <div><button class="btn primary" type="submit" disabled={busy}>{busy ? 'Changing…' : 'Change password'}</button></div>
    </form>
  </section>

  <section class="panel">
    <div class="panel-head"><h2>Appearance</h2></div>
    <div class="panel-body">
      <ThemeSwitch />
      <p class="hint" style="margin-top: var(--space-2)">Auto follows your device's light or dark setting.</p>
    </div>
    <div class="panel-head"><h2>Session</h2></div>
    <div class="panel-body">
      <button class="btn" onclick={logout}>Log out</button>
    </div>
  </section>

  <section class="panel">
    <div class="panel-head"><h2>Backup</h2></div>
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
  </section>
</div>

<div class="scheduled">
  <ScheduledBackups />
  <EmailSettings />
</div>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .grid { display: grid; grid-template-columns: minmax(0, 420px) minmax(0, 320px); gap: var(--space-5); align-items: start; }
  .form { display: grid; gap: var(--space-4); }
  .scheduled { margin-top: var(--space-5); max-width: 760px; display: grid; gap: var(--space-5); }
  @media (max-width: 900px) { .grid { grid-template-columns: 1fr; } }
</style>
