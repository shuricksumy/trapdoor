<script>
  // One client's online / offline messages: by email and / or Telegram. The
  // channels and recipients are on the admin page.
  import { api } from './api.js';
  import { toast } from './state.svelte.js';
  import Dialog from './Dialog.svelte';

  let { open = $bindable(false), name = '', label = '', flags = null, onsaved } = $props();

  let f = $state({ email: false, telegram: false });
  let channels = $state(null); // { email, telegram }: switched on on the admin page
  let busy = $state(false);
  $effect(() => {
    if (!open) return;
    f = { email: !!flags?.email, telegram: !!flags?.telegram };
    Promise.all([api('/email').catch(() => null), api('/telegram').catch(() => null)]).then(([e, t]) => {
      channels = { email: !!(e?.enabled && e?.host), telegram: !!(t?.enabled && t?.token_set) };
    });
  });

  async function save(e) {
    e.preventDefault();
    busy = true;
    try {
      const saved = await api(`/notify/${encodeURIComponent(name)}`, { method: 'PUT', body: f });
      toast(saved.email || saved.telegram ? `You get a message when ${label || name} goes online or offline.` : `No messages for ${label || name}.`);
      open = false;
      onsaved?.(saved);
    } catch (err) {
      toast(err.message, 'bad');
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Online / offline messages">
  <form id="notifyform" class="form" onsubmit={save}>
    <p class="muted">When <strong>{label || name}</strong> connects or disconnects, send a message by:</p>
    <label class="check"><input type="checkbox" bind:checked={f.email} /><span>Email</span>
      {#if channels && !channels.email}<span class="hint warn">email is off on the admin page</span>{/if}</label>
    <label class="check"><input type="checkbox" bind:checked={f.telegram} /><span>Telegram</span>
      {#if channels && !channels.telegram}<span class="hint warn">Telegram is off on the admin page</span>{/if}</label>
    <p class="hint">Recipients and the quiet time for quick reconnects: admin page → Client online / offline.</p>
  </form>
  {#snippet actions()}
    <button class="btn" type="button" onclick={() => (open = false)}>Cancel</button>
    <button class="btn primary" type="submit" form="notifyform" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>

<style>
  .form { display: grid; gap: var(--space-3); }
  .check { display: flex; gap: var(--space-2); align-items: center; }
  .check input { height: auto; }
  .warn { color: var(--warn); margin-left: var(--space-2); }
</style>
