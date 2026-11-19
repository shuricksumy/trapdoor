<script>
  import { api } from './api.js';
  import { toast } from './state.svelte.js';
  import Dialog from './Dialog.svelte';

  // Send a client its profile (OpenVPN) or config (WireGuard, AmneziaWG) by
  // email, with how to set it up. qr: base64 GIF of the config's QR code.
  let { open = $bindable(false), name = '', email = '', qr = '', onsent = () => {} } = $props();

  let to = $state('');
  let note = $state('');
  let busy = $state(false);
  let error = $state('');
  let ready = $state(null); // email set up?

  $effect(() => {
    if (!open) return;
    to = email;
    note = '';
    error = '';
    api('/email')
      .then((e) => (ready = !!e.enabled && !!e.host))
      .catch(() => (ready = false));
  });

  async function send(e) {
    e.preventDefault();
    busy = true;
    error = '';
    try {
      const r = await api(`/emails/${encodeURIComponent(name)}/send`, { method: 'POST', body: { email: to.trim(), note, qr_gif: qr } });
      toast(`Sent to ${r.sent_to}.`);
      open = false;
      onsent(r.sent_to);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title="Email {name}'s access">
  {#if ready === false}
    <p>Email is not set up or turned off: see <a href="/account">Account → Email</a>.</p>
  {:else}
    <form id="sendform" class="form" onsubmit={send}>
      {#if error}<div class="banner bad" role="alert">{error}</div>{/if}
      <label class="field">
        <span>Email address</span>
        <input type="email" bind:value={to} placeholder="name@example.com" autocomplete="off" required />
        <span class="hint">Kept for this client.</span>
      </label>
      <label class="field">
        <span>Note (optional)</span>
        <textarea rows="3" bind:value={note} placeholder="e.g. Call me if it does not work."></textarea>
      </label>
      <p class="small muted">
        The email has the {qr ? 'config and its QR code' : 'profile'} attached, the app to install and how to connect. The file is the
        client's private key: anyone with the email can connect as this client.
      </p>
    </form>
  {/if}
  {#snippet actions()}
    <button class="btn" onclick={() => (open = false)}>Cancel</button>
    {#if ready !== false}
      <button class="btn primary" type="submit" form="sendform" disabled={busy || !to.trim()}>{busy ? 'Sending…' : 'Send'}</button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .form { display: grid; gap: var(--space-4); }
</style>
