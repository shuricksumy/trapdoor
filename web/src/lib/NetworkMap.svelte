<script>
  // A VPN network as a picture: clients on the left, the server in the middle,
  // the networks on the right (through the server or behind a client) with who
  // reaches them. Half-done routes are orange. Built by networkMap.js (buildMap
  // for OpenVPN, buildWGMap for WireGuard / AmneziaWG).
  import { clientName } from './names.svelte.js';

  // server: { title, ip, subnet, line }; onclient(name), onnetwork(node): open the editors.
  let { map, server, onclient, onnetwork } = $props();
  const anyWarn = $derived(map.networks.some((n) => n.missingRoute?.length));

  const MAX = 14; // clients drawn; the rest is "+N more"
  const shown = $derived(map.clients.slice(0, MAX));
  const hidden = $derived(map.clients.length - shown.length);
  const leftRows = $derived(shown.length + (hidden ? 1 : 0));

  const internet = $derived({
    key: 'internet',
    name: 'Internet',
    line2: map.internet === 'all' ? 'through the VPN for every client' : map.internet.length ? 'through the VPN for' : 'stays on the devices (split tunnel)',
    reached: map.internet === 'all' ? '' : map.internet,
    ok: true,
  });
  const rightRows = $derived(map.networks.length + 1);

  const CH = 40; // client row
  const NH = 66; // network row
  const top = 16;
  const height = $derived(Math.max(leftRows * CH, rightRows * NH, 170) + top * 2);
  const cy = (i) => top + i * CH + 16;
  const ny = (i) => top + i * NH + 28;
  const sTop = top;
  const sBottom = $derived(height - top);
  const clamp = (y) => Math.min(Math.max(y, sTop + 24), sBottom - 24);

  const trunc = (s, n) => (s.length > n ? s.slice(0, n - 1) + '…' : s);
  const who = (r, n = 30) => {
    if (r === 'all') return 'every client';
    if (!r.length) return 'nobody yet';
    const names = r.map(clientName);
    let out = names[0];
    let i = 1;
    for (; i < names.length && (out + ', ' + names[i]).length <= n; i++) out += ', ' + names[i];
    return i < names.length ? `${out} +${names.length - i}` : out;
  };
  const where = (n) => (n.via ? `behind ${clientName(n.via)}` : 'through the server');
  const key = (e, f) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), f());
</script>

<figure class="map" aria-label="The VPN network">
  <svg viewBox="0 0 960 {height}" role="group" aria-label="Clients, the server and the networks they reach">
    <defs>
      <marker id="nm-ok" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 10 5 0 10z" class="f-ok" /></marker>
      <marker id="nm-warn" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 10 5 0 10z" class="f-warn" /></marker>
    </defs>

    <!-- server -->
    <rect x="330" y={sTop} width="180" height={sBottom - sTop} rx="12" class="box server" />
    <text x="420" y={(sTop + sBottom) / 2 - 22} class="t b" text-anchor="middle">{server?.title ?? 'Server'}</text>
    <text x="420" y={(sTop + sBottom) / 2} class="t s mono" text-anchor="middle">{server?.ip ?? ''}</text>
    <text x="420" y={(sTop + sBottom) / 2 + 18} class="t xs m mono" text-anchor="middle">{server?.subnet ?? ''}</text>
    <text x="420" y={(sTop + sBottom) / 2 + 38} class="t xs m" text-anchor="middle">{server?.line ?? ''}</text>

    <!-- clients -->
    {#each shown as c, i (c.name)}
      {@const y = cy(i)}
      <path d="M232 {y} C 280 {y}, 290 {clamp(y)}, 328 {clamp(y)}" class="wire" class:off={!c.online} />
      <g class="node click" role="button" tabindex="0" aria-label="{clientName(c.name)}: {c.online ? 'online' : 'offline'}{c.router.length ? `, router for ${c.router.join(', ')}` : ''}. Edit its network"
        onclick={() => onclient?.(c.name)} onkeydown={(e) => key(e, () => onclient?.(c.name))}>
        <title>{clientName(c.name)}{clientName(c.name) !== c.name ? ` (${c.name})` : ''}</title>
        <rect x="1" y={y - 16} width="230" height="32" rx="8" class="box" class:router={c.router.length} />
        <circle cx="16" cy={y} r="4" class="dot" class:on={c.online} />
        <text x="28" y={y + 4} class="t s b">{trunc(clientName(c.name), c.router.length ? 17 : 22)}</text>
        <text x="222" y={y + 4} class="t xs m mono" text-anchor="end">{c.router.length ? '' : c.ip || 'auto'}</text>
        {#if c.router.length}<text x="222" y={y + 4} class="t xs tag" text-anchor="end">router</text>{/if}
      </g>
    {/each}
    {#if hidden}
      {@const y = cy(shown.length)}
      <text x="116" y={y + 4} class="t xs m" text-anchor="middle">+{hidden} more in the table below</text>
    {/if}

    <!-- networks -->
    {#each [...map.networks, internet] as n, i (n.key)}
      {@const y = ny(i)}
      {@const warn = n.missingRoute?.length > 0}
      <path d="M512 {clamp(y)} C 550 {clamp(y)}, 560 {y}, 606 {y}" class="wire link" class:warn marker-end="url(#nm-{warn ? 'warn' : 'ok'})" />
      {#snippet body()}
        <rect x="610" y={y - 26} width="349" height="56" rx="10" class="box net" class:warn class:named={n.named} />
        <text x="624" y={y - 7} class="t s b">{trunc(n.name, warn ? 26 : 38)}</text>
        {#if n.named}<text x="944" y={y - 7} class="t xs m mono" text-anchor="end">{trunc(n.cidrs.join(', '), warn ? 0 : 22)}</text>{/if}
        {#if warn}
          <rect x="866" y={y - 21} width="82" height="20" rx="10" class="pill warn" />
          <text x="907" y={y - 7} class="t xs b warn-t" text-anchor="middle">needs route</text>
        {/if}
        {#if n.key === 'internet'}
          <text x="624" y={y + 12} class="t xs m">{n.line2}{n.reached.length ? ` ${who(n.reached, 26)}` : ''}</text>
        {:else}
          <text x="624" y={y + 12} class="t xs m">{where(n)} · reached by <tspan class:nobody={n.reachedBy !== 'all' && !n.reachedBy.length}>{who(n.reachedBy, 50 - where(n).length - 14)}</tspan></text>
        {/if}
      {/snippet}
      {#if n.key === 'internet'}
        <g class="node">{@render body()}</g>
      {:else}
        <g class="node click" role="button" tabindex="0"
          aria-label={`${n.name}: ${where(n)}, reached by ${who(n.reachedBy, 200)}${warn ? '. The server has no route for it' : ''}. Edit`}
          onclick={() => onnetwork?.(n)} onkeydown={(e) => key(e, () => onnetwork?.(n))}>{@render body()}</g>
      {/if}
    {/each}
  </svg>

  <!-- phones: the networks as a list -->
  <ul class="stack">
    {#each map.networks as n (n.key)}
      <li>
        <button type="button" class="sbox" class:warn={n.missingRoute.length} onclick={() => onnetwork?.(n)}>
          <strong>{n.name}</strong>
          {#if n.named}<span class="mono faint">{n.cidrs.join(', ')}</span>{/if}
          <span class="faint">{where(n)} · reached by {who(n.reachedBy, 60)}</span>
          {#if n.missingRoute.length}<span class="warn-t">The server has no route for it</span>{/if}
        </button>
      </li>
    {/each}
    <li class="sbox"><strong>Internet</strong><span class="faint">{internet.line2}{internet.reached.length ? ` ${who(internet.reached, 60)}` : ''}</span></li>
  </ul>
  <figcaption class="hint">Click a client or a network to change it. Grey lines: offline clients.{#if anyWarn}{' '}Orange: the server does not send that network into the VPN yet.{/if}</figcaption>
</figure>

<style>
  .map { margin: 0; display: grid; gap: var(--space-2); }
  svg { width: 100%; height: auto; }
  .box { fill: var(--sunken); stroke: var(--line); stroke-width: 1.2; }
  .box.server { stroke: var(--accent); stroke-width: 1.6; }
  .box.router { stroke: var(--accent); }
  .box.net.named { stroke-width: 1.6; }
  .box.net.warn { stroke: var(--warn); stroke-dasharray: 5 4; }
  .node.click { cursor: pointer; }
  .node.click:hover .box, .node.click:focus-visible .box { stroke: var(--accent); stroke-width: 2; }
  .node:focus { outline: none; }
  .t { fill: var(--ink); font-family: inherit; font-size: 14px; }
  .b { font-weight: 600; }
  .s { font-size: 13px; }
  .xs { font-size: 11.5px; }
  .m { fill: var(--ink-3); }
  .mono { font-family: var(--font-mono, ui-monospace, monospace); }
  .tag { fill: var(--accent); font-weight: 600; }
  .nobody { fill: var(--warn); }
  .warn-t { fill: var(--warn); color: var(--warn); }
  .pill.warn { fill: var(--warn-soft, transparent); stroke: var(--warn); }
  .dot { fill: var(--ink-3); }
  .dot.on { fill: var(--ok); }
  .wire { fill: none; stroke: var(--ok); stroke-width: 1.6; opacity: 0.8; }
  .wire.off { stroke: var(--ink-3); stroke-dasharray: 3 4; opacity: 0.6; }
  .wire.link { stroke: var(--accent); stroke-width: 2; opacity: 1; }
  .wire.link.warn { stroke: var(--warn); stroke-dasharray: 6 5; }
  .f-ok { fill: var(--accent); }
  .f-warn { fill: var(--warn); }
  figcaption { margin: 0; }

  .stack { display: none; list-style: none; margin: 0; padding: 0; gap: 6px; font-size: var(--step--1); }
  .sbox { display: grid; gap: 2px; width: 100%; padding: 8px 10px; border: 1px solid var(--line); border-radius: var(--radius-s); background: var(--sunken); color: var(--ink); font: inherit; text-align: left; }
  button.sbox { cursor: pointer; }
  .sbox.warn { border-color: var(--warn); border-style: dashed; }
  @media (max-width: 640px) {
    svg { display: none; }
    .stack { display: grid; }
  }
</style>
