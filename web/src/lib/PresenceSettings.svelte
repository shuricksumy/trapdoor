<script>
  import { api } from './api.js';
  import { toast } from './state.svelte.js';

  // Where clients' online / offline changes go; which clients is chosen per client.
  let f = $state(null);
  let loadError = $state('');
  let busy = $state(false);
  let error = $state('');

  async function load() {
    try {
      const p = await api('/presence');
      f = { emails: (p.emails ?? []).join(', '), chats: (p.chats ?? []).join(', '), grace: p.grace_minutes || 2 };
      loadError = '';
    } catch (err) {
      loadError = err.message;
    }
  }
  $effect(() => {
    load();
  });

  async function save(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      await api('/presence', {
        method: 'PUT',
        body: { emails: f.emails.split(/[,\s;]+/).filter(Boolean), chats: [f.chats], grace_minutes: Number(f.grace) },
      });
      toast('Saved.');
      await load();
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<section class="panel">
  <div class="panel-head"><h2>Client online / offline</h2></div>
  {#if loadError}
    <div class="panel-body"><div class="banner bad" role="alert">{loadError}</div></div>
  {:else if !f}
    <p class="panel-body muted">Loading…</p>
  {:else}
    <form class="panel-body body" onsubmit={save}>
      <p class="muted small">
        A message when a client connects or disconnects, for the clients you pick on the Clients page (bell: email and / or
        Telegram). It goes through the email and Telegram settings above.
      </p>
      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      <div class="grid">
        <label class="field">
          <span>Emails</span>
          <input bind:value={f.emails} placeholder="the admin emails" autocomplete="off" spellcheck="false" />
          <span class="hint">Empty: the admin emails.</span>
        </label>
        <label class="field">
          <span>Telegram chats</span>
          <input bind:value={f.chats} placeholder="the Telegram chats" autocomplete="off" spellcheck="false" />
          <span class="hint">Empty: the chats in the Telegram settings.</span>
        </label>
        <label class="field">
          <span>Offline after (minutes)</span>
          <input type="number" min="1" max="120" bind:value={f.grace} required />
          <span class="hint">A reconnect within this time stays quiet.</span>
        </label>
      </div>
      <div class="actions"><button class="btn primary" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button></div>
    </form>
  {/if}
</section>

<style>
  .body { display: grid; gap: var(--space-4); }
  .grid { display: grid; grid-template-columns: 1fr 1fr 180px; gap: var(--space-4); align-items: start; }
  .actions { display: flex; justify-content: flex-end; }
  @media (max-width: 700px) {
    .grid { grid-template-columns: 1fr; }
  }
</style>
