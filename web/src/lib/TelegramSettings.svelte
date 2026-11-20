<script>
  import { api } from './api.js';
  import { toast } from './state.svelte.js';

  // A Telegram bot that sends the admins' notifications; a test message.
  let f = $state(null);
  let last = $state(null);
  let loadError = $state('');
  let busy = $state(false);
  let testing = $state(false);
  let error = $state('');

  async function load() {
    try {
      const t = await api('/telegram');
      last = t.last ?? null;
      f = {
        on: !!t.enabled,
        token: '',
        tokenSet: !!t.token_set,
        clearToken: false,
        chats: (t.chats ?? []).join(', '),
        notify: { backup_failed: true, node_down: true, cert_expiry: true, ...(t.notify ?? {}) },
      };
      loadError = '';
    } catch (err) {
      loadError = err.message;
    }
  }
  $effect(() => {
    load();
  });

  async function save(e) {
    e?.preventDefault();
    busy = true;
    error = '';
    try {
      await api('/telegram', {
        method: 'PUT',
        body: {
          enabled: f.on,
          token: f.token.trim(),
          clear_token: f.clearToken,
          chats: [f.chats],
          notify: f.notify,
        },
      });
      toast(f.on ? 'Telegram settings saved.' : 'Telegram is off.');
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
      const r = await api('/telegram/test', { method: 'POST' });
      toast(`Test message sent to ${r.sent_to.join(', ')}.`);
    } catch (err) {
      error = err.message;
    } finally {
      testing = false;
      await load();
    }
  }
  const fmt = (t) => new Date(t).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
</script>

<section class="panel">
  <div class="panel-head"><h2>Telegram</h2></div>
  {#if loadError}
    <div class="panel-body"><div class="banner bad" role="alert">Could not load the Telegram settings: {loadError}</div></div>
  {:else if !f}
    <p class="panel-body muted">Loading…</p>
  {:else}
    <form class="panel-body body" onsubmit={save}>
      <p class="muted small">
        The same notifications as email, from your own bot. Create one with <span class="mono">@BotFather</span>, send it a message, then
        put your chat id below (e.g. from <span class="mono">@userinfobot</span>). For a group: add the bot and use the group's id (starts with -100).
      </p>
      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      {#if last && !last.ok}
        <div class="banner bad" role="status">The last message ({fmt(last.time)}, “{last.subject}”) was not sent: {last.error}</div>
      {/if}
      <label class="check"><input type="checkbox" bind:checked={f.on} /><span>Send Telegram messages</span></label>

      {#if f.on}
        <div class="grid">
          <label class="field">
            <span>Bot token</span>
            <input type="password" bind:value={f.token} placeholder={f.tokenSet ? 'Saved; type to change' : '123456789:AAE…'} autocomplete="new-password" spellcheck="false" disabled={f.clearToken} />
            {#if f.tokenSet}<label class="check small"><input type="checkbox" bind:checked={f.clearToken} /><span>Remove the saved token</span></label>{/if}
          </label>
          <label class="field">
            <span>Chat ids</span>
            <input bind:value={f.chats} placeholder="123456789, -1001234567890" autocomplete="off" spellcheck="false" />
            <span class="hint">Get the notifications. Several: separate with commas. A channel: @name (the bot must be an admin there).</span>
          </label>
        </div>

        <fieldset class="notify">
          <legend>Tell the admins when</legend>
          <label class="check"><input type="checkbox" bind:checked={f.notify.backup_failed} /><span>a scheduled backup fails</span></label>
          <label class="check"><input type="checkbox" bind:checked={f.notify.node_down} /><span>a node does not answer for 5 minutes, and when it is back</span></label>
          <label class="check"><input type="checkbox" bind:checked={f.notify.cert_expiry} /><span>certificates expire soon (once per certificate)</span></label>
        </fieldset>
      {/if}

      <div class="actions">
        {#if f.on}<button class="btn" type="button" disabled={testing || busy} onclick={test}>{testing ? 'Sending…' : 'Save and send a test'}</button>{/if}
        <button class="btn primary" type="submit" disabled={busy}>{busy && !testing ? 'Saving…' : 'Save'}</button>
      </div>
    </form>
  {/if}
</section>

<style>
  .body { display: grid; gap: var(--space-4); }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-4); align-items: start; }
  .check { display: flex; gap: var(--space-2); align-items: center; }
  .field .check { margin-top: var(--space-1); }
  .notify { border: 1px solid var(--line); border-radius: 10px; padding: var(--space-3) var(--space-4); display: grid; gap: var(--space-2); margin: 0; }
  .notify legend { padding: 0 var(--space-1); font-weight: 600; font-size: var(--step--1); color: var(--ink-2); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); flex-wrap: wrap; }
  @media (max-width: 700px) {
    .grid { grid-template-columns: 1fr; }
  }
</style>
