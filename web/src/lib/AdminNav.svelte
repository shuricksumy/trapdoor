<script>
  // The Admin area's tabs (opened from your name at the bottom of the menu):
  // everything about running trapdoor itself, apart from the pages of one server.
  import { route, session } from './state.svelte.js';

  const isAdmin = $derived(session.user?.role !== 'viewer');
  const tabs = $derived(
    [
      ['/account', 'My account', true],
      ['/admins', 'Admins', isAdmin],
      ['/servers', 'Servers', true],
      ['/nodes', 'Nodes', true],
      ['/backups', 'Backups', isAdmin],
      ['/notifications', 'Notifications', true],
    ].filter(([, , show]) => show),
  );
  const active = (p) => route.path === p || (p === '/servers' && route.path === '/servers/new') || (p === '/nodes' && route.path === '/hosts');
</script>

<nav class="admin-nav" aria-label="Admin">
  {#each tabs as [path, label] (path)}
    <a href={path} class:on={active(path)} aria-current={active(path) ? 'page' : undefined}>{label}</a>
  {/each}
</nav>

<style>
  .admin-nav { display: flex; gap: 2px; flex-wrap: wrap; margin-bottom: var(--space-5); padding-bottom: var(--space-2); border-bottom: 1px solid var(--line); }
  a { display: inline-flex; align-items: center; height: 34px; padding: 0 var(--space-3); border-radius: var(--radius-s); color: var(--ink-2); font-weight: 500; text-decoration: none; }
  a:hover { background: var(--sunken); color: var(--ink); }
  a.on { background: var(--surface); color: var(--ink); box-shadow: inset 0 0 0 1px var(--line); }
  @media (max-width: 640px) {
    .admin-nav { flex-wrap: nowrap; overflow-x: auto; scrollbar-width: none; }
    a { flex: none; }
  }
</style>
