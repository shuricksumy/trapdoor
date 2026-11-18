<script>
  // A 24-hour ruler with one row per user and a bar for each time they were
  // connected today, like a logic-analyser trace.
  import { time, duration, bytes } from './format.js';

  let { sessions = [], now = new Date() } = $props();

  const dayStart = $derived(new Date(now.getFullYear(), now.getMonth(), now.getDate()));
  const dayMs = 86400000;
  const pct = (t) => Math.min(100, Math.max(0, ((t - dayStart) / dayMs) * 100));

  const rows = $derived.by(() => {
    const byName = new Map();
    for (const s of sessions) {
      const start = new Date(s.connected_at);
      const end = s.disconnected_at ? new Date(s.disconnected_at) : now;
      if (end < dayStart) continue;
      if (!byName.has(s.name)) byName.set(s.name, { name: s.name, bars: [], first: start, live: false, total: 0 });
      const r = byName.get(s.name);
      r.bars.push({ left: pct(start), width: Math.max(0.4, pct(end) - pct(start)), live: !s.disconnected_at, s });
      r.live ||= !s.disconnected_at;
      r.total += s.upload + s.download;
      if (start < r.first) r.first = start;
    }
    return [...byName.values()].sort((a, b) => b.live - a.live || a.first - b.first);
  });

  const ticks = [0, 3, 6, 9, 12, 15, 18, 21, 24];
  const nowPct = $derived(pct(now));
</script>

{#if rows.length === 0}
  <p class="empty">Nobody has connected today.</p>
{:else}
  <div class="trace" role="img" aria-label="Connections today by user">
    <div class="axis" aria-hidden="true">
      <span></span>
      <div class="ticks">
        {#each ticks as h (h)}
          <span style="left: {(h / 24) * 100}%">{String(h).padStart(2, '0')}</span>
        {/each}
      </div>
      <span></span>
    </div>
    {#each rows as r (r.name)}
      <div class="row">
        <span class="name" title={r.name}>
          {#if r.live}<i class="dot" aria-label="online"></i>{/if}{r.name}
        </span>
        <div class="lane">
          {#each ticks.slice(1, -1) as h (h)}<i class="grid" style="left: {(h / 24) * 100}%"></i>{/each}
          {#each r.bars as b, i (i)}
            <span class="bar" class:live={b.live} style="left: {b.left}%; width: {b.width}%">
              <title>
                {time(b.s.connected_at)}–{b.live ? 'now' : time(b.s.disconnected_at)} ({duration(b.s.duration_seconds)}), {bytes(b.s.download)} down, {bytes(b.s.upload)} up
              </title>
            </span>
          {/each}
          <i class="now" style="left: {nowPct}%"></i>
        </div>
        <span class="total faint small">{bytes(r.total)}</span>
      </div>
    {/each}
  </div>
{/if}

<style>
  .trace { display: grid; gap: 6px; }
  .row, .axis {
    display: grid;
    grid-template-columns: 120px 1fr 64px;
    align-items: center;
    gap: var(--space-3);
  }
  .ticks { position: relative; height: 16px; }
  .ticks span {
    position: absolute;
    transform: translateX(-50%);
    font-family: var(--mono);
    font-size: 11px;
    color: var(--ink-3);
  }
  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--ok); flex: none; }
  .lane {
    position: relative;
    height: 22px;
    border-radius: 3px;
    background: var(--sunken);
  }
  .grid { position: absolute; top: 0; bottom: 0; width: 1px; background: var(--line); }
  .bar {
    position: absolute;
    top: 5px;
    bottom: 5px;
    border-radius: 2px;
    background: var(--upload);
  }
  .bar.live { background: var(--accent); top: 3px; bottom: 3px; }
  .now {
    position: absolute;
    top: -3px;
    bottom: -3px;
    width: 2px;
    margin-left: -1px;
    background: var(--warn);
  }
  .total { text-align: right; white-space: nowrap; }
  .empty { padding: var(--space-4) 0; color: var(--ink-2); }

  @media (max-width: 760px) {
    .row, .axis { grid-template-columns: 92px 1fr; gap: var(--space-2); }
    .total, .axis > span:last-child { display: none; }
  }
</style>
