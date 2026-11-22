<script>
  import Fold from '../lib/Fold.svelte';
  import { api } from '../lib/api.js';
  import AdminNav from '../lib/AdminNav.svelte';
  import { session, navigate, toast, settings, repoURL, versionURL, shortVersion } from '../lib/state.svelte.js';
  import ThemeSwitch from '../lib/ThemeSwitch.svelte';

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

  async function logout() {
    try {
      await api('/auth/logout', { method: 'POST' });
    } finally {
      session.user = null;
      navigate('/login', { replace: true });
    }
  }
</script>

<AdminNav />
<header class="head">
  <div>
    <h1>My account</h1>
    <p class="muted">Signed in as <strong>{session.user?.username}</strong>{session.user?.role === 'viewer' ? ' (read-only)' : ''}{session.user?.via === 'oidc' ? ' with single sign-on' : ''}.</p>
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
  {#if session.user?.source === 'oidc'}
    <p class="muted sso-note">You sign in with single sign-on: your password, its reset and your second factor are managed there. An admin can also give this account a local password (Admins), for when the sign-on is down.</p>
  {/if}
  <Fold id="acct-password" title="Change password" summary="your local password; you are logged out everywhere afterwards">
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



</div>

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); flex-wrap: wrap; margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .form { display: grid; gap: var(--space-4); }
  .sections { max-width: 860px; }
  .sso-note { margin: 0 0 var(--space-4); max-width: 72ch; }
  .quick { display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
</style>
