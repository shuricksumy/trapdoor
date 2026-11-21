<script>
  import { api } from '../lib/api.js';
  import { session, navigate, route } from '../lib/state.svelte.js';
  import ThemeSwitch from '../lib/ThemeSwitch.svelte';

  let username = $state('');
  let password = $state('');
  // A sign-in with the provider that went wrong comes back as ?error=.
  const back = route.query.get('error') ?? '';
  let error = $state(back && back[0].toUpperCase() + back.slice(1) + (back.endsWith('.') ? '' : '.'));
  let busy = $state(false);
  let sso = $state(''); // the provider's name when sign-in with it is set up
  let showPassword = $state(false);
  let pw = $state(true); // the password login is allowed from this address
  $effect(() => {
    fetch('/api/v1/auth/methods')
      .then((r) => (r.ok ? r.json() : {}))
      .then((m) => {
        sso = m.oidc ?? '';
        pw = m.password !== false;
      })
      .catch(() => {});
  });

  async function submit(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      session.user = await api('/auth/login', { method: 'POST', body: { username, password } });
      const next = route.query.get('next');
      // Only same-site paths, never back to the login page itself.
      const safe = next && next.startsWith('/') && !next.startsWith('//') && !next.startsWith('/login');
      navigate(safe ? next : '/', { replace: true });
    } catch (err) {
      error =
        err.status === 429
          ? 'Too many failed attempts. Wait a few minutes, then try again.'
          : err.status === 401
            ? 'Wrong username or password.'
            : err.message;
      password = '';
    } finally {
      busy = false;
    }
  }
</script>

<div class="wrap">
  <form class="card" onsubmit={submit}>
    <div class="mark" aria-hidden="true">
      <svg viewBox="0 0 64 40" width="64" height="40">
        <path d="M6 36c0-14.4 11.6-26 26-26s26 11.6 26 26" fill="none" stroke="var(--accent)" stroke-width="5" stroke-linecap="round" />
        <path d="M19 36c0-7.2 5.8-13 13-13s13 5.8 13 13" fill="none" stroke="var(--upload)" stroke-width="5" stroke-linecap="round" />
      </svg>
    </div>
    <h1>Trapdoor</h1>
    <p class="muted">Log in to manage your VPN server.</p>

    {#if error}<div class="banner bad" role="alert">{error}</div>{/if}

    {#if sso}
      <a class="btn primary sso" href="/api/v1/auth/oidc">Sign in with {sso}</a>
      {#if !pw}
        <p class="note">The admin password works only from the VPN or the local network.</p>
      {:else if !showPassword}
        <button type="button" class="link" onclick={() => (showPassword = true)}>Use the admin password</button>
      {:else}
        <div class="or"><span>or with the admin password</span></div>
      {/if}
    {/if}

    {#if !pw && !sso}
      <div class="banner warn">The password login is not allowed from this address. Connect to the VPN or the local network.</div>
    {:else if pw && (!sso || showPassword)}
    <label class="field">
      <span>Username</span>
      <input bind:value={username} autocomplete="username" autocapitalize="none" spellcheck="false" required />
    </label>
    <label class="field">
      <span>Password</span>
      <input type="password" bind:value={password} autocomplete="current-password" required />
    </label>
    <button class="btn" class:primary={!sso} type="submit" disabled={busy}>{busy ? 'Logging in…' : 'Log in'}</button>
    {/if}
  </form>
  <div class="theme-slot"><ThemeSwitch compact /></div>
</div>

<style>
  .wrap {
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: var(--space-5);
    background:
      radial-gradient(120% 60% at 50% 110%, var(--accent-soft), transparent 70%),
      var(--bg);
  }
  .card {
    width: min(360px, 100%);
    display: grid;
    gap: var(--space-4);
    padding: var(--space-6);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--radius-m);
    box-shadow: var(--shadow);
  }
  h1 { font-size: var(--step-2); }
  .mark { margin-bottom: calc(-1 * var(--space-2)); }
  .btn { justify-content: center; height: 40px; margin-top: var(--space-2); }
  .sso { text-decoration: none; }
  .link { justify-self: center; padding: 0; border: 0; background: none; color: var(--ink-2); font: inherit; font-size: var(--step--1); text-decoration: underline; cursor: pointer; }
  .note { margin: 0; text-align: center; color: var(--ink-3); font-size: var(--step--1); }
  .or { display: flex; align-items: center; gap: var(--space-3); color: var(--ink-3); font-size: var(--step--1); }
  .or::before, .or::after { content: ''; flex: 1; border-top: 1px solid var(--line); }
  .theme-slot { position: fixed; top: var(--space-4); right: var(--space-4); width: 120px; }
</style>
