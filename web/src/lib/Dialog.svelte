<script>
  // Native <dialog>: focus trap, Esc and backdrop handling come for free.
  let { open = $bindable(false), title, children, actions } = $props();
  let el;

  $effect(() => {
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  });
</script>

<dialog bind:this={el} onclose={() => (open = false)} onclick={(e) => e.target === el && (open = false)} aria-labelledby="dlg-title">
  <div class="inner">
    <h2 id="dlg-title">{title}</h2>
    {@render children?.()}
    {#if actions}<div class="actions">{@render actions()}</div>{/if}
  </div>
</dialog>

<style>
  dialog {
    width: min(520px, calc(100vw - 32px));
    padding: 0;
    border: 1px solid var(--line);
    border-radius: var(--radius-m);
    background: var(--surface);
    color: var(--ink);
    box-shadow: var(--shadow);
  }
  dialog::backdrop { background: rgb(10 16 18 / 45%); }
  .inner { display: grid; gap: var(--space-4); padding: var(--space-5); }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); margin-top: var(--space-2); }
</style>
