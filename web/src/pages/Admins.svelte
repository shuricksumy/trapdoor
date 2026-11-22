<script>
  // Accounts: who can sign in, as admin or read-only, how and when they last
  // did. People signing in with single sign-on get their account at the first
  // sign-in; local accounts (a password) are added here.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { toast, session, navigate } from '../lib/state.svelte.js';
  import { relative } from '../lib/format.js';
  import AdminNav from '../lib/AdminNav.svelte';
  import Dialog from '../lib/Dialog.svelte';

  let list = $state(null);
  let error = $state('');
  let busy = $state('');
  let now = $state(Date.now());

  async function load() {
    try {
      list = await api('/admins');
      error = '';
    } catch (e) {
      error = e.message;
    }
    now = Date.now();
  }
  onMount(load);

  async function change(a, body, done) {
    busy = a.username;
    try {
      list = await api(`/admins/${encodeURIComponent(a.username)}`, { method: 'PUT', body });
      toast(done);
    } catch (e) {
      toast(e.message, 'bad');
      await load();
    } finally {
      busy = '';
    }
  }
  async function logoutAll(a) {
    busy = a.username;
    try {
      const r = await api(`/admins/${encodeURIComponent(a.username)}/logout`, { method: 'POST' });
      toast(`${a.username}: ${r.ended} session${r.ended === 1 ? '' : 's'} ended`);
      if (a.you) {
        session.user = null;
        navigate('/login', { replace: true });
        return;
      }
      await load();
    } catch (e) {
      toast(e.message, 'bad');
    } finally {
      busy = '';
    }
  }

  // Add a local account
  let add = $state({ open: false, username: '', password: '', role: 'admin', error: '', busy: false });
  async function addAccount(e) {
    e.preventDefault();
    add.busy = true;
    try {
      await api('/admins', { method: 'POST', body: { username: add.username.trim(), password: add.password, role: add.role } });
      toast(`${add.username.trim()} added`);
      add.open = false;
      await load();
    } catch (err) {
      add.error = err.message;
    } finally {
      add.busy = false;
    }
  }

  // Set a password (also gives a single sign-on account a local one)
  let pw = $state({ open: false, name: '', password: '', error: '', busy: false });
  async function setPassword(e) {
    e.preventDefault();
    pw.busy = true;
    try {
      await api(`/admins/${encodeURIComponent(pw.name)}/password`, { method: 'POST', body: { password: pw.password } });
      const mine = list.find((a) => a.username === pw.name)?.you;
      pw.open = false;
      toast(`New password for ${pw.name}; they are logged out everywhere`);
      if (mine) {
        session.user = null;
        navigate('/login', { replace: true });
        return;
      }
      await load();
    } catch (err) {
      pw.error = err.message;
    } finally {
      pw.busy = false;
    }
  }

  // Delete
  let del = $state({ open: false, name: '', busy: false });
  async function remove() {
    del.busy = true;
    try {
      await api(`/admins/${encodeURIComponent(del.name)}`, { method: 'DELETE' });
      toast(`${del.name} deleted`);
      del.open = false;
      await load();
    } catch (err) {
      toast(err.message, 'bad');
    } finally {
      del.busy = false;
    }
  }

  const via = { oidc: 'single sign-on', password: 'password' };
</script>

<AdminNav />
<header class="head">
  <div>
    <h1>Admins</h1>
    <p class="muted">Who can sign in. <strong>Admins</strong> change things; <strong>read-only</strong> accounts see everything but change nothing and download no keys. People signing in with single sign-on get their account at their first sign-in, its role from their groups.</p>
  </div>
  <button class="btn primary" onclick={() => (add = { open: true, username: '', password: '', role: 'admin', error: '', busy: false })}>Add an account</button>
</header>

{#if error}<div class="banner bad" role="alert">{error}</div>{/if}

{#if list}
  <section class="panel">
    <div class="table-wrap">
      <table class="cards">
        <thead><tr><th>Account</th><th>Role</th><th>Signs in with</th><th>Last sign-in</th><th class="num">Sessions</th><th><span class="sr">Actions</span></th></tr></thead>
        <tbody>
          {#each list as a (a.id)}
            <tr class:off={a.disabled}>
              <td data-label="Account">
                <strong>{a.username}</strong>
                {#if a.you}<span class="tag you">you</span>{/if}
                {#if a.disabled}<span class="pill off">off</span>{/if}
              </td>
              <td data-label="Role">
                <select value={a.role} disabled={busy === a.username} aria-label="Role of {a.username}"
                  onchange={(e) => change(a, { role: e.currentTarget.value }, `${a.username} is ${e.currentTarget.value === 'viewer' ? 'read-only' : 'an admin'} now`)}>
                  <option value="admin">Admin</option>
                  <option value="viewer">Read-only</option>
                </select>
              </td>
              <td data-label="Signs in with">
                {a.source === 'oidc' ? 'single sign-on' : 'password'}{#if a.source === 'oidc' && a.has_password}<span class="faint small block">and a local password</span>{/if}
              </td>
              <td data-label="Last sign-in">
                {#if a.last_login_at}
                  {relative(a.last_login_at, now)}
                  <span class="faint small block mono">{a.last_login_ip} · {via[a.last_login_via] ?? a.last_login_via}</span>
                {:else}<span class="faint">never</span>{/if}
              </td>
              <td data-label="Sessions" class="num">{a.sessions}</td>
              <td class="actions">
                {#if !a.you}
                  <button class="btn small ghost" disabled={busy === a.username} onclick={() => change(a, { disabled: !a.disabled }, `${a.username} ${a.disabled ? 'turned on' : 'turned off'}`)}>{a.disabled ? 'Turn on' : 'Turn off'}</button>
                {/if}
                <button class="btn small ghost" onclick={() => (pw = { open: true, name: a.username, password: '', error: '', busy: false })}>{a.has_password ? 'New password' : 'Set a password'}</button>
                {#if a.sessions}<button class="btn small ghost" disabled={busy === a.username} onclick={() => logoutAll(a)}>Log out everywhere</button>{/if}
                {#if !a.you}<button class="btn small ghost danger" onclick={() => (del = { open: true, name: a.username, busy: false })}>Delete</button>{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="panel-body hint">There is always at least one admin who is turned on: the last one cannot be made read-only, turned off or deleted. A password set here also works for a single sign-on account (e.g. when the sign-on is down); its owner is logged out everywhere.</p>
  </section>
{/if}

<Dialog bind:open={add.open} title="Add an account">
  <form class="form" id="add-form" onsubmit={addAccount}>
    <label class="field"><span>Name</span><input bind:value={add.username} autocomplete="off" spellcheck="false" required /></label>
    <label class="field"><span>Password</span><input type="password" bind:value={add.password} minlength="10" autocomplete="new-password" required /><span class="hint">At least 10 characters.</span></label>
    <label class="field"><span>Role</span>
      <select bind:value={add.role}><option value="admin">Admin: changes things</option><option value="viewer">Read-only: sees everything, changes nothing</option></select>
    </label>
    {#if add.error}<p class="err" role="alert">{add.error}</p>{/if}
  </form>
  {#snippet actions()}
    <button class="btn" onclick={() => (add.open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="add-form" disabled={add.busy}>{add.busy ? 'Adding…' : 'Add'}</button>
  {/snippet}
</Dialog>

<Dialog bind:open={pw.open} title="Password for {pw.name}">
  <form class="form" id="pw-form" onsubmit={setPassword}>
    <label class="field"><span>New password</span><input type="password" bind:value={pw.password} minlength="10" autocomplete="new-password" required /><span class="hint">At least 10 characters. {pw.name} is logged out everywhere.</span></label>
    {#if pw.error}<p class="err" role="alert">{pw.error}</p>{/if}
  </form>
  {#snippet actions()}
    <button class="btn" onclick={() => (pw.open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="pw-form" disabled={pw.busy}>{pw.busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<Dialog bind:open={del.open} title="Delete {del.name}?">
  <p>{del.name} can no longer sign in{list?.find((a) => a.username === del.name)?.source === 'oidc' ? ' with a password; signing in with single sign-on makes a new account (turn it off instead to keep them out)' : ''}. Their sessions end.</p>
  {#snippet actions()}
    <button class="btn" onclick={() => (del.open = false)}>Cancel</button>
    <button class="btn danger solid" disabled={del.busy} onclick={remove}>{del.busy ? 'Deleting…' : 'Delete'}</button>
  {/snippet}
</Dialog>

<style>
  .head { display: flex; justify-content: space-between; align-items: flex-start; gap: var(--space-4); flex-wrap: wrap; margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); max-width: 80ch; }
  .banner { margin-bottom: var(--space-4); }
  .form { display: grid; gap: var(--space-4); }
  .err { margin: 0; color: var(--bad); }
  .block { display: block; }
  .small { font-size: var(--step--1); }
  .tag.you { margin-left: 6px; padding: 1px 7px; border-radius: 6px; background: var(--accent-soft); font-size: 12px; }
  .actions { text-align: right; white-space: nowrap; }
  td select { height: 30px; width: auto; }
  tr.off td { color: var(--ink-3); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
  @media (max-width: 640px) {
    .cards thead { display: none; }
    .cards, .cards tbody, .cards tr, .cards td { display: block; width: 100%; }
    .cards tr { padding: var(--space-3) var(--space-4); border-top: 1px solid var(--line); }
    .cards td { padding: 3px 0; border: 0; }
    .cards td[data-label]:not(:first-child) { display: grid; grid-template-columns: 110px minmax(0, 1fr); column-gap: var(--space-2); }
    .cards td[data-label]:not(:first-child)::before { content: attr(data-label); color: var(--ink-3); font-size: var(--step--1); }
    .cards td[data-label]:not(:first-child) > * { grid-column: 2; justify-self: start; }
    .cards .actions { text-align: left; white-space: normal; padding-top: var(--space-2); }
    .cards td.num { text-align: left; }
  }
</style>
