<script>
  // The controls under a paged table: "16–30 of 42", rows per page and the
  // page buttons. Nothing is shown while everything fits on the smallest page.
  import { pageList, pageSizes, saveSize } from './pager.js';

  let { page = $bindable(1), size = $bindable(15), info, label = 'Pages' } = $props();
  // info: paginate()'s answer for the current list

  function setSize(n) {
    size = n;
    page = 1;
    saveSize(n);
  }
</script>

{#if info.total > pageSizes[0]}
  <nav class="pager" aria-label={label}>
    <span class="faint small">{info.from}–{info.to} of {info.total}</span>
    <div class="right">
      {#if info.pages > 1}
        <div class="pages">
          <button class="btn small ghost" disabled={info.page <= 1} onclick={() => (page = info.page - 1)} aria-label="Previous page">←</button>
          {#each pageList(info.page, info.pages) as p, i (i)}
            {#if p === '…'}<span class="gap" aria-hidden="true">…</span>
            {:else}<button class="num" class:on={p === info.page} aria-current={p === info.page ? 'page' : undefined} onclick={() => (page = p)}>{p}</button>{/if}
          {/each}
          <button class="btn small ghost" disabled={info.page >= info.pages} onclick={() => (page = info.page + 1)} aria-label="Next page">→</button>
        </div>
      {/if}
      <label class="size"><span class="faint small">Rows</span>
        <select value={size} onchange={(e) => setSize(Number(e.currentTarget.value))}>
          {#each pageSizes as n (n)}<option value={n}>{n}</option>{/each}
        </select>
      </label>
    </div>
  </nav>
{/if}

<style>
  .pager { display: flex; justify-content: space-between; align-items: center; gap: var(--space-3); flex-wrap: wrap; padding: var(--space-3) var(--space-5); border-top: 1px solid var(--line); }
  .right { display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap; }
  .pages { display: flex; align-items: center; gap: 2px; flex-wrap: wrap; }
  .num { min-width: 30px; height: 30px; padding: 0 6px; border: 1px solid transparent; border-radius: var(--radius-s); background: none; color: var(--ink-2); font: inherit; font-size: var(--step--1); cursor: pointer; }
  .num:hover { background: var(--sunken); color: var(--ink); }
  .num.on { border-color: var(--line); background: var(--sunken); color: var(--ink); font-weight: 600; }
  .gap { padding: 0 4px; color: var(--ink-3); }
  .size { display: flex; align-items: center; gap: var(--space-2); }
  .size select { height: 30px; width: auto; padding: 0 var(--space-2); }
  .small { font-size: var(--step--1); }
  @media (max-width: 640px) {
    .pager { padding: var(--space-3) var(--space-4); }
  }
</style>
