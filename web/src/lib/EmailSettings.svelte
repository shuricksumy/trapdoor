<script>
  import Fold from './Fold.svelte';
  import { api } from './api.js';
  import { toast } from './state.svelte.js';

  // SMTP server, who gets notifications, which ones; a test email.
  let f = $state(null);
  let last = $state(null);
  let loadError = $state('');
  let busy = $state(false);
  let testing = $state(false);
  let error = $state('');

  const ports = { tls: 465, starttls: 587, none: 25 };

  async function load() {
    try {
      const e = await api('/email');
      last = e.last ?? null;
      f = {
        on: !!e.enabled,
        host: e.host ?? '',
        port: e.port || 587,
        security: e.security || 'starttls',
        username: e.username ?? '',
        password: '',
        passwordSet: !!e.password_set,
        clearPassword: false,
        from: e.from ?? '',
        admins: (e.admins ?? []).join(', '),
        insecure: !!e.insecure_skip_verify,
        notify: { backup_failed: true, node_down: true, cert_expiry: true, usage_limit: true, ...(e.notify ?? {}) },
      };
      loadError = '';
    } catch (err) {
      loadError = err.message;
    }
  }
  $effect(() => {
    load();
  });

  function security(v) {
    if (ports[f.security] === Number(f.port)) f.port = ports[v];
    f.security = v;
  }

  async function save(e) {
    e?.preventDefault();
    busy = true;
    error = '';
    try {
      await api('/email', {
        method: 'PUT',
        body: {
              enabled: f.on,
              host: f.host.trim(),
              port: Number(f.port),
              security: f.security,
              username: f.username.trim(),
              password: f.password,
              clear_password: f.clearPassword,
              from: f.from.trim(),
              insecure_skip_verify: f.insecure,
              admins: f.admins.split(/[,\s;]+/).filter(Boolean),
              notify: f.notify,
            },
      });
      toast(f.on ? 'Email settings saved.' : 'Email is off.');
      await load();
      return true;
    } catch (err) {
      error = err.message;
      return false;
    } finally {
      busy = false;
    }
  }

  async function test() {
    testing = true;
    error = '';
    try {
      if (!(await save())) return;
      const r = await api('/email/test', { method: 'POST' });
      toast(`Test email sent to ${r.sent_to.join(', ')}.`);
    } catch (err) {
      error = err.message;
    } finally {
      testing = false;
      await load();
    }
  }
  const fmt = (t) => new Date(t).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
  // For the folded header.
  const summary = $derived.by(() => {
    if (!f) return '';
    if (!f.on) return 'off';
    const n = f.admins.split(',').map((a) => a.trim()).filter(Boolean).length;
    return `on · ${f.host || 'no SMTP server'} · ${n} admin${n === 1 ? '' : 's'}`;
  });
</script>

<Fold id="acct-email" title="Email" summary={summary}>
  {#if loadError}
    <div class="panel-body"><div class="banner bad" role="alert">Could not load the email settings: {loadError}</div></div>
  {:else if !f}
    <p class="panel-body muted">Loading…</p>
  {:else}
    <form class="panel-body body" onsubmit={save}>
      <p class="muted small">
        To send clients their profile or config with instructions, and to tell you when a backup fails, a node stops answering or
        certificates are about to expire.
      </p>
      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      {#if last && !last.ok}
        <div class="banner bad" role="status">The last email ({fmt(last.time)}, “{last.subject}”) was not sent: {last.error}</div>
      {/if}
      <label class="check"><input type="checkbox" bind:checked={f.on} /><span>Send emails</span></label>

      {#if f.on}
        <div class="grid">
          <label class="field span2">
            <span>SMTP server</span>
            <input bind:value={f.host} placeholder="smtp.example.com" autocomplete="off" autocapitalize="none" spellcheck="false" required />
          </label>
          <label class="field">
            <span>Security</span>
            <select value={f.security} onchange={(e) => security(e.currentTarget.value)}>
              <option value="starttls">STARTTLS</option>
              <option value="tls">TLS</option>
              <option value="none">None (local relay)</option>
            </select>
          </label>
          <label class="field">
            <span>Port</span>
            <input type="number" min="1" max="65535" bind:value={f.port} required />
          </label>
          <label class="field span2">
            <span>Username</span>
            <input bind:value={f.username} autocomplete="off" autocapitalize="none" spellcheck="false" />
          </label>
          <label class="field span2">
            <span>Password</span>
            <input type="password" bind:value={f.password} placeholder={f.passwordSet ? 'Saved; type to change' : ''} autocomplete="new-password" disabled={f.clearPassword} />
            {#if f.passwordSet}
              <label class="check small"><input type="checkbox" bind:checked={f.clearPassword} /><span>Remove the saved password</span></label>
            {:else}
              <span class="hint">For Gmail and similar: an app password.</span>
            {/if}
          </label>
          <label class="field span2">
            <span>From</span>
            <input bind:value={f.from} placeholder="trapdoor <vpn@example.com>" autocomplete="off" spellcheck="false" required />
          </label>
          <label class="field span2">
            <span>Admin emails</span>
            <input bind:value={f.admins} placeholder="you@example.com" autocomplete="off" spellcheck="false" />
            <span class="hint">Get the notifications. Several: separate with commas.</span>
          </label>
        </div>
        {#if f.security !== 'none'}
          <label class="check small"><input type="checkbox" bind:checked={f.insecure} /><span>Accept a self-signed certificate of the SMTP server</span></label>
        {/if}

        <fieldset class="notify">
          <legend>Tell the admins when</legend>
          <label class="check"><input type="checkbox" bind:checked={f.notify.backup_failed} /><span>a scheduled backup fails</span></label>
          <label class="check"><input type="checkbox" bind:checked={f.notify.node_down} /><span>a node does not answer for 5 minutes, and when it is back</span></label>
          <label class="check"><input type="checkbox" bind:checked={f.notify.cert_expiry} /><span>certificates expire soon (once per certificate)</span></label>
          <label class="check"><input type="checkbox" bind:checked={f.notify.usage_limit} /><span>a client reaches 80% or 100% of its usage limit, or a server its traffic alarm (Limits page)</span></label>
        </fieldset>
      {/if}

      <div class="actions">
        {#if f.on}<button class="btn" type="button" disabled={testing || busy} onclick={test}>{testing ? 'Sending…' : 'Save and send a test'}</button>{/if}
        <button class="btn primary" type="submit" disabled={busy}>{busy && !testing ? 'Saving…' : 'Save'}</button>
      </div>
    </form>
  {/if}
</Fold>

<style>
  .body { display: grid; gap: var(--space-4); }
  .grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--space-4); align-items: start; }
  .span2 { grid-column: span 2; }
  .check { display: flex; gap: var(--space-2); align-items: center; }
  .field .check { margin-top: var(--space-1); }
  .notify { border: 1px solid var(--line); border-radius: 10px; padding: var(--space-3) var(--space-4); display: grid; gap: var(--space-2); margin: 0; }
  .notify legend { padding: 0 var(--space-1); font-weight: 600; font-size: var(--step--1); color: var(--ink-2); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); flex-wrap: wrap; }
  @media (max-width: 700px) {
    .grid { grid-template-columns: 1fr 1fr; }
  }
</style>
