<script>
  import qrcode from 'qrcode-generator';
  import { api } from './api.js';
  import { toast } from './state.svelte.js';
  import Dialog from './Dialog.svelte';

  let { open = $bindable(false), name } = $props();

  let st = $state(null);
  let error = $state('');
  let busy = $state(false);
  let enrol = $state(null); // { secret, uri } right after a key was created
  let staticPw = $state('');

  $effect(() => {
    if (!open || !name) return;
    st = null;
    enrol = null;
    error = '';
    staticPw = '';
    api(`/certs/${name}/auth`)
      .then((r) => (st = r))
      .catch((e) => (error = e.message));
  });

  async function update(body, done) {
    busy = true;
    error = '';
    try {
      const r = await api(`/certs/${name}/auth`, { method: 'PUT', body });
      st = r;
      if (r.secret) enrol = { secret: r.secret, uri: r.otpauth_uri };
      if (done) toast(done);
      staticPw = '';
    } catch (e) {
      error = e.message;
    } finally {
      busy = false;
    }
  }

  const qrSvg = $derived.by(() => {
    if (!enrol) return '';
    const qr = qrcode(0, 'M');
    qr.addData(enrol.uri);
    qr.make();
    return qr.createSvgTag({ cellSize: 4, margin: 2, scalable: true });
  });
  const grouped = $derived(enrol ? enrol.secret.match(/.{1,4}/g).join(' ') : '');
</script>

<Dialog bind:open title="Two-factor login for {name}">
  {#if error}<div class="banner bad" role="alert">{error}</div>{/if}

  {#if !st && !error}
    <p class="muted">Loading…</p>
  {:else if st}
    <p class="muted small">
      When this is on, the device asks for a username and password on connect.
      Username: <strong>{name}</strong>. Password: the static password (if set) followed by the 6-digit code.
    </p>

    <section class="factor">
      <div class="row">
        <div>
          <h3>Authenticator app</h3>
          <span class="pill {st.totp ? 'ok' : 'off'}">{st.totp ? 'On' : 'Off'}</span>
        </div>
        <div class="btns">
          {#if st.totp}
            <button class="btn small" disabled={busy} onclick={() => update({ totp: 'reset' }, 'New key created. Scan it on the device.')}>New key</button>
            <button class="btn small danger" disabled={busy} onclick={() => update({ totp: 'disable' }, 'Authenticator turned off.')}>Turn off</button>
          {:else}
            <button class="btn small primary" disabled={busy} onclick={() => update({ totp: 'enable' })}>Turn on</button>
          {/if}
        </div>
      </div>

      {#if enrol}
        <div class="enrol">
          <div class="qr" aria-label="QR code for the authenticator app">{@html qrSvg}</div>
          <div class="enrol-text">
            <p>Scan this with an authenticator app that supports SHA-256 codes, such as Aegis. It is shown only now.</p>
            <p class="small muted">Or enter the key by hand (SHA-256, 6 digits, 30 seconds):</p>
            <code class="secret">{grouped}</code>
          </div>
        </div>
      {/if}
    </section>

    <section class="factor">
      <div class="row">
        <div>
          <h3>Static password</h3>
          <span class="pill {st.static_password ? 'ok' : 'off'}">{st.static_password ? 'Set' : 'Not set'}</span>
          {#if st.legacy_static}<span class="hint">stored in plain text by openvpn-ui; set a new one to store it hashed</span>{/if}
        </div>
        {#if st.static_password}
          <button class="btn small danger" disabled={busy} onclick={() => update({ remove_static: true }, 'Static password removed.')}>Remove</button>
        {/if}
      </div>
      <form class="set" onsubmit={(e) => { e.preventDefault(); update({ static_password: staticPw }, 'Static password saved.'); }}>
        <input type="password" bind:value={staticPw} minlength="4" maxlength="128" placeholder={st.static_password ? 'New static password' : 'Static password'} autocomplete="new-password" aria-label="Static password" />
        <button class="btn small" type="submit" disabled={busy || staticPw.length < 4}>Save</button>
      </form>
    </section>

    <p class="hint">
      The server must check logins: <code>auth-user-pass-verify "/usr/local/bin/trapdoor auth-verify" via-file</code>
      with <code>script-security 2</code> in server.conf, and <code>auth-user-pass</code> in client-template.txt.
    </p>
  {/if}

  {#snippet actions()}
    <button class="btn" onclick={() => (open = false)}>Done</button>
  {/snippet}
</Dialog>

<style>
  .factor { display: grid; gap: var(--space-3); padding-top: var(--space-3); border-top: 1px solid var(--line); }
  .row { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--space-3); }
  .row > div:first-child { display: grid; gap: 4px; justify-items: start; }
  .btns { display: flex; gap: var(--space-2); }
  .enrol { display: grid; grid-template-columns: 148px 1fr; gap: var(--space-4); align-items: center; }
  .qr { width: 148px; padding: 6px; background: #fff; border-radius: var(--radius-s); line-height: 0; }
  .qr :global(svg) { width: 100%; height: auto; }
  .enrol-text { display: grid; gap: var(--space-2); }
  .secret { font-family: var(--mono); font-size: 13px; word-break: break-all; padding: 6px 8px; background: var(--sunken); border-radius: var(--radius-s); user-select: all; }
  .set { display: flex; gap: var(--space-2); }
  .set input { flex: 1; }
  code { font-family: var(--mono); font-size: 12px; }
  @media (max-width: 480px) { .enrol { grid-template-columns: 1fr; justify-items: start; } }
</style>
