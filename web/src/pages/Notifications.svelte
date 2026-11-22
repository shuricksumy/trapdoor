<script>
  // Where trapdoor sends messages: email, Telegram, and clients going online / offline.
  import AdminNav from '../lib/AdminNav.svelte';
  import EmailSettings from '../lib/EmailSettings.svelte';
  import TelegramSettings from '../lib/TelegramSettings.svelte';
  import PresenceSettings from '../lib/PresenceSettings.svelte';
</script>

<AdminNav />
<header class="head">
  <h1>Notifications</h1>
  <p class="muted">Email and Telegram for the admins' messages (backups, nodes, certificates, usage limits, traffic alarms), and where clients' online / offline messages go.</p>
</header>
<div class="sections">
  <EmailSettings />
  <TelegramSettings />
  <PresenceSettings />
</div>

<style>
  .head { margin-bottom: var(--space-5); }
  .head p { margin-top: var(--space-1); max-width: 72ch; }
  .sections { max-width: 860px; }
</style>
