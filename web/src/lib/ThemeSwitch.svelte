<script>
  import { theme, setTheme } from './state.svelte.js';

  const options = [
    { value: 'light', label: 'Light', icon: 'M12 4V2M12 22v-2M4 12H2M22 12h-2M5.6 5.6 4.2 4.2M19.8 19.8l-1.4-1.4M5.6 18.4l-1.4 1.4M19.8 4.2l-1.4 1.4M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10z' },
    { value: 'dark', label: 'Dark', icon: 'M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z' },
    { value: 'auto', label: 'Auto', icon: 'M12 3a9 9 0 1 0 0 18zM12 3a9 9 0 0 1 0 18' },
  ];
  let { compact = false } = $props();
</script>

<div class="theme" class:compact role="radiogroup" aria-label="Theme">
  {#each options as o (o.value)}
    <button
      type="button"
      role="radio"
      aria-checked={theme.choice === o.value}
      title={o.value === 'auto' ? 'Follow the system setting' : `${o.label} theme`}
      onclick={() => setTheme(o.value)}
    >
      <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
        <path d={o.icon} fill={o.value === 'auto' ? 'currentColor' : 'none'} fill-rule="evenodd" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
      {#if !compact}<span>{o.label}</span>{/if}
    </button>
  {/each}
</div>

<style>
  .theme {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 2px;
    padding: 2px;
    border-radius: var(--radius-s);
    background: var(--sunken);
  }
  button {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 4px;
    height: 28px;
    padding: 0 6px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--ink-2);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
  button:hover { color: var(--ink); }
  button[aria-checked='true'] {
    background: var(--surface);
    color: var(--ink);
    box-shadow: 0 1px 2px rgb(0 0 0 / 12%);
  }
</style>
