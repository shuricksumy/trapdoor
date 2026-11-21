<script>
  // A section of the Server page: one block of settings with its own Save,
  // folded by default. The header says what is set now; the browser
  // remembers which sections were left open.
  let { id, title, summary = '', badge, children, open: initial = false } = $props();
  const key = () => `td-fold-${id}`;
  let open = $state(read());
  function read() {
    try {
      const v = localStorage.getItem(key());
      return v === null ? initial : v === '1';
    } catch {
      return initial;
    }
  }
  function toggled(e) {
    open = e.currentTarget.open;
    try {
      localStorage.setItem(key(), open ? '1' : '0');
    } catch {
      /* private mode: not remembered */
    }
  }
</script>

<details class="panel fold" {open} ontoggle={toggled}>
  <summary>
    <span class="chev" aria-hidden="true">▸</span>
    <h2>{title}</h2>
    {#if badge}{@render badge()}{/if}
    {#if summary}<span class="sum">{summary}</span>{/if}
  </summary>
  <div class="inner">{@render children()}</div>
</details>

<style>
  .fold { margin-bottom: var(--space-4); }
  summary { display: flex; align-items: center; gap: var(--space-3); padding: var(--space-4) var(--space-5); cursor: pointer; list-style: none; user-select: none; border-radius: var(--radius-m); }
  summary::-webkit-details-marker { display: none; }
  summary:hover { background: var(--sunken); }
  summary:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
  h2 { margin: 0; font-size: var(--step-1); white-space: nowrap; }
  .chev { display: inline-block; width: 1em; font-size: 1.15em; line-height: 1; color: var(--ink-2); transition: transform 0.15s; }
  .fold[open] .chev { transform: rotate(90deg); }
  .fold[open] summary { border-bottom: 1px solid var(--line); border-radius: var(--radius-m) var(--radius-m) 0 0; }
  .sum { min-width: 0; margin-left: auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: var(--step--1); }
  @media (max-width: 640px) {
    summary { flex-wrap: wrap; padding: var(--space-3) var(--space-4); }
    .sum { flex-basis: 100%; margin-left: calc(1em + var(--space-3)); white-space: normal; }
  }
</style>
