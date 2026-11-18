<script>
  import { api } from '../lib/api.js';
  import { session, navigate, toast } from '../lib/state.svelte.js';
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

<header class="head">
  <h1>Account</h1>
  <p class="muted">Signed in as <strong>{session.user?.username}</strong>.</p>
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
</div>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); }
  .grid { display: grid; grid-template-columns: minmax(0, 420px) minmax(0, 320px); gap: var(--space-5); align-items: start; }
  .form { display: grid; gap: var(--space-4); }
  @media (max-width: 900px) { .grid { grid-template-columns: 1fr; } }
</style>
