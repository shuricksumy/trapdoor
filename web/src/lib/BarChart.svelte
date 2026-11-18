<script>
  // Stacked bars per period: download (accent) under upload (light accent).
  // Periods flagged "estimated" get a hatch so gaps in recording are visible.
  import { bytes } from './format.js';

  let { series = [], group = 'day' } = $props();
  let width = $state(600);
  const height = 220;
  const pad = { top: 12, right: 8, bottom: 26, left: 56 };

  let hover = $state(-1);

  const max = $derived(Math.max(1, ...series.map((p) => p.upload + p.download)));
  // A "nice" axis maximum in powers of 1, 2, 2.5, 5 × 10^n.
  const top = $derived.by(() => {
    const p = 10 ** Math.floor(Math.log10(max));
    for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= max) return m * p;
    return 10 * p;
  });
  const ticks = $derived([0, 0.25, 0.5, 0.75, 1].map((f) => f * top));

  const inner = $derived({ w: Math.max(50, width - pad.left - pad.right), h: height - pad.top - pad.bottom });
  const step = $derived(inner.w / Math.max(1, series.length));
  const barW = $derived(Math.max(2, Math.min(36, step * 0.7)));
  const y = (v) => pad.top + inner.h - (v / top) * inner.h;

  function label(period) {
    if (group === 'day') {
      const d = new Date(period + 'T00:00:00');
      return series.length > 14 ? String(d.getDate()) : d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
    }
    if (group === 'month') return new Date(period + '-01T00:00:00').toLocaleDateString(undefined, { month: 'short', year: series.length > 12 ? '2-digit' : undefined });
    return period;
  }
  // Show at most ~12 x labels.
  const every = $derived(Math.max(1, Math.ceil(series.length / 12)));
  const hovered = $derived(hover >= 0 ? series[hover] : null);
  const anyEstimated = $derived(series.some((p) => p.estimated));
</script>

<div class="chart" bind:clientWidth={width}>
  <div class="readout" aria-live="polite">
    {#if hovered}
      <strong>{label(hovered.period)}{group === 'day' && series.length > 14 ? ' ' + new Date(hovered.period + 'T00:00:00').toLocaleDateString(undefined, { month: 'short' }) : ''}</strong>
      <span><i class="sw down"></i>{bytes(hovered.download)} down</span>
      <span><i class="sw up"></i>{bytes(hovered.upload)} up</span>
      {#if hovered.estimated}<span class="est">estimated</span>{/if}
    {:else}
      <span><i class="sw down"></i>Download</span>
      <span><i class="sw up"></i>Upload</span>
      {#if anyEstimated}<span class="est"><i class="sw hatch"></i>Estimated: recording had a gap</span>{/if}
    {/if}
  </div>

  <svg {width} {height} role="img" aria-label="Traffic per {group}">
    <defs>
      <pattern id="hatch" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
        <line x1="0" y1="0" x2="0" y2="5" stroke="var(--surface)" stroke-width="2" opacity="0.7" />
      </pattern>
    </defs>

    {#each ticks as t (t)}
      <line x1={pad.left} x2={width - pad.right} y1={y(t)} y2={y(t)} class="grid" />
      <text x={pad.left - 8} y={y(t) + 4} class="ylab" text-anchor="end">{bytes(t)}</text>
    {/each}

    {#each series as p, i (p.period)}
      {@const x = pad.left + i * step + (step - barW) / 2}
      {@const yd = y(p.download)}
      {@const yu = y(p.download + p.upload)}
      <g
        class="bar"
        class:dim={hover >= 0 && hover !== i}
        role="presentation"
        onpointerenter={() => (hover = i)}
        onpointerleave={() => (hover = -1)}
      >
        <rect x={pad.left + i * step} y={pad.top} width={step} height={inner.h} fill="transparent" />
        {#if p.download > 0}<rect {x} y={yd} width={barW} height={y(0) - yd} rx="1.5" class="down" />{/if}
        {#if p.upload > 0}<rect {x} y={yu} width={barW} height={yd - yu} rx="1.5" class="up" />{/if}
        {#if p.estimated && p.upload + p.download > 0}
          <rect {x} y={yu} width={barW} height={y(0) - yu} fill="url(#hatch)" />
        {/if}
        <title>{p.period}: {bytes(p.download)} down, {bytes(p.upload)} up{p.estimated ? ' (estimated)' : ''}</title>
      </g>
      {#if i % every === 0}
        <text x={pad.left + i * step + step / 2} y={height - 8} class="xlab" text-anchor="middle">{label(p.period)}</text>
      {/if}
    {/each}
    <line x1={pad.left} x2={width - pad.right} y1={y(0)} y2={y(0)} class="base" />
  </svg>
</div>

<style>
  .chart { width: 100%; }
  .readout {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-4);
    min-height: 24px;
    margin-bottom: var(--space-2);
    font-size: var(--step--1);
    color: var(--ink-2);
  }
  .readout strong { color: var(--ink); font-weight: 600; }
  .readout span { display: inline-flex; align-items: center; gap: 6px; }
  .sw { width: 10px; height: 10px; border-radius: 2px; display: inline-block; }
  .sw.down { background: var(--accent); }
  .sw.up { background: var(--upload); }
  .sw.hatch {
    background: repeating-linear-gradient(45deg, var(--accent) 0 3px, var(--surface) 3px 5px);
  }
  .est { color: var(--warn); }
  svg { display: block; overflow: visible; }
  .grid { stroke: var(--line); stroke-dasharray: 2 3; }
  .base { stroke: var(--ink-3); }
  .ylab, .xlab { fill: var(--ink-3); font-size: 11px; font-family: var(--mono); }
  .down { fill: var(--accent); }
  .up { fill: var(--upload); }
  .bar { transition: opacity 120ms; }
  .bar.dim { opacity: 0.45; }
</style>
