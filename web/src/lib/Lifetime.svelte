<script>
  // How long a certificate is valid: a preset or until a date. days is
  // bound; defaultDays is the server's lifetime (marked in the list).
  let { days = $bindable(825), defaultDays = 825, label = 'Valid for', preset = $bindable(true) } = $props();

  const presets = [
    [30, '30 days'],
    [90, '90 days'],
    [365, '1 year'],
    [825, '825 days (about 2¼ years)'],
    [1825, '5 years'],
    [3650, '10 years'],
  ];
  const day = 24 * 3600 * 1000;
  const iso = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 10);
  const today = iso(new Date());
  const maxDate = iso(new Date(Date.now() + 3650 * day));

  let choice = $state(presets.some(([d]) => d === days) ? String(days) : 'date');
  let until = $state(iso(new Date(Date.now() + days * day)));

  // Days from now to the end of the chosen date (at least 1).
  function fromDate(v) {
    const end = new Date(v + 'T23:59:59');
    return Math.min(3650, Math.max(1, Math.ceil((end.getTime() - Date.now()) / day)));
  }
  $effect(() => {
    days = choice === 'date' ? fromDate(until) : Number(choice);
    preset = choice !== 'date';
  });
  const fmt = (d) => d.toLocaleDateString([], { dateStyle: 'medium' });
  const ends = $derived(choice === 'date' ? fmt(new Date(until + 'T12:00:00')) : fmt(new Date(Date.now() + days * day)));
</script>

<label class="field">
  <span>{label}</span>
  <select bind:value={choice}>
    {#each presets as [d, text] (d)}
      <option value={String(d)}>{text}{d === defaultDays ? ' (this server’s default)' : ''}</option>
    {/each}
    <option value="date">Until a date…</option>
  </select>
  {#if choice === 'date'}
    <input type="date" bind:value={until} min={today} max={maxDate} required />
  {/if}
  <span class="hint">Works until {choice === 'date' ? '' : 'about '}{ends}; renew it before then to keep it working.</span>
</label>
