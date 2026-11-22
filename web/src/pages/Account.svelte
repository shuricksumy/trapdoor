<script>
  import Fold from '../lib/Fold.svelte';
  import { api, download } from '../lib/api.js';
  import { session, navigate, toast, settings, repoURL, versionURL, shortVersion } from '../lib/state.svelte.js';
  import ThemeSwitch from '../lib/ThemeSwitch.svelte';
  import ScheduledBackups from '../lib/ScheduledBackups.svelte';
  import EmailSettings from '../lib/EmailSettings.svelte';
  import TelegramSettings from '../lib/TelegramSettings.svelte';
  import PresenceSettings from '../lib/PresenceSettings.svelte';

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
  <div>
    <h1>Account</h1>
    <p class="muted">Signed in as <strong>{session.user?.username}</strong>.</p>
    <p class="muted small">
      Trapdoor {#if settings.version}<a href={versionURL(settings.version)} target="_blank" rel="noopener noreferrer" title={settings.version}>{shortVersion(settings.version)}</a>{/if}
      · <a href={repoURL} target="_blank" rel="noopener noreferrer">GitHub</a>
    </p>
  </div>
  <div class="quick">
    <ThemeSwitch />
    <button class="btn" onclick={logout}>Log out</button>
  </div>
</header>

<div class="sections">
  <Fold id="acct-password" title="Change password" summary="the admin password; you are logged out everywhere afterwards">
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
  </Fold>

  <Fold id="acct-backup" title="Backup" summary="download one file with everything, now">
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
  <EmailSettings />
  <TelegramSettings />
  <PresenceSettings />
</div>

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); flex-wrap: wrap; margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .form { display: grid; gap: var(--space-4); }
  .sections { max-width: 860px; }
  .quick { display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
</style>
