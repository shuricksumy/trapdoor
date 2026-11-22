<script>
  import { readOnly } from './state.svelte.js';
  import { destinations, toggle as flip } from './accessRules.js';
  // What the access settings mean, drawn: a client, the server's filter and
  // where traffic goes. It follows the unsaved policy and edits it: clicking a
  // destination switches it between allowed and blocked for every client.
  //
  // How a switch turns into rules: accessRules.js.
  // type: openvpn | wireguard | amneziawg; net: the Access page's network;
  // fwMode: direct | file | off (TD_FIREWALL); c2c: OpenVPN client-to-client is on.
  let { policy, type = 'openvpn', id = '', subnet = '', net = null, names = {}, fwMode = 'direct', c2c = false } = $props();

  const wg = $derived(type === 'wireguard' || type === 'amneziawg');
  const kind = $derived(type === 'amneziawg' ? 'AmneziaWG' : wg ? 'WireGuard' : 'OpenVPN');
  const device = $derived(wg ? id : 'tun');
  const chain = $derived(wg ? `TD-${id}` : 'TD-FWD');
  const clientIp = $derived(subnet ? subnet.replace(/\.\d+\/\d+$/, '.6') : '');
  const on = $derived(policy.enabled);
  // client-to-client: OpenVPN passes traffic between clients itself, past the rules
  const bypass = $derived(c2c && !wg);
  const routed = $derived([...new Set([...(net?.routes ?? []), ...Object.values(net?.client_lans ?? {}).flat()])]);
  const ctx = $derived({ serverIp: net?.server_ip, subnet, routed, bypass });
  const info = {
    server: { name: 'This server', sub: () => net?.server_ip || 'address not known' },
    clients: { name: 'Other VPN clients', sub: () => subnet || 'the VPN network' },
    routed: { name: 'Networks it routes', sub: () => (routed.length ? [...new Set(routed.map((c) => names[c] ?? c))].join(', ') : 'none set up') },
    internet: { name: 'Internet', sub: () => 'and everything else' },
  };
  const dests = $derived(
    destinations(policy, ctx).map((d) => ({ ...d, name: info[d.key].name, sub: d.bypass ? 'client-to-client is on' : info[d.key].sub() })),
  );

  function toggle(d) {
    if (!on || !d.usable || readOnly()) return;
    const next = flip(policy, ctx, d.key);
    policy.default = next.default;
    policy.rules = next.rules;
  }
  const key = (e, d) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), toggle(d));

  const verdict = (d) => (!on ? 'reachable' : d.bypass ? 'not filtered' : !d.usable ? '–' : d.ok ? 'allowed' : 'blocked');
  const tone = (d) => (!on || d.ok ? 'yes' : !d.usable ? 'na' : 'no');
  const label = (d) => `${d.name}: ${verdict(d)}${on && d.usable ? `. Click to ${d.ok ? 'block' : 'allow'} for every client` : ''}`;
  const extra = (d) => (on && d.more ? ` · ${d.more} more rule${d.more === 1 ? '' : 's'}` : '');
  const gate = $derived(
    !on
      ? ['No filtering', 'trapdoor adds no rules']
      : [policy.rules.length ? `${policy.rules.length} rule${policy.rules.length === 1 ? '' : 's'}, first match decides` : 'No rules yet', `no match → ${policy.default === 'allow' ? 'allow' : 'block'}`],
  );
  const rowY = (i) => 14 + i * 50;
</script>

<figure class="diagram" aria-label="What the access settings do">
  <svg viewBox="0 0 800 214">
    <title>{kind} clients pass {on ? `through chain ${chain}` : 'without filtering'} to the server, other clients, routed networks and the internet</title>
    <defs>
      <marker id="ad-yes" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 10 5 0 10z" class="f-yes" /></marker>
      <marker id="ad-na" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 10 5 0 10z" class="f-na" /></marker>
      <marker id="ad-no" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 10 5 0 10z" class="f-no" /></marker>
    </defs>

    <!-- client -->
    <rect x="1" y="72" width="150" height="70" rx="10" class="box" />
    <text x="76" y="98" class="t b" text-anchor="middle">{kind} client</text>
    <text x="76" y="118" class="t s m mono" text-anchor="middle">{clientIp || 'VPN address'}</text>
    <path d="M152 107 H206" class="wire yes" marker-end="url(#ad-yes)" />
    <text x="179" y="99" class="t xs m" text-anchor="middle">{device}</text>

    <!-- the filter -->
    <rect x="210" y="62" width="200" height="90" rx="10" class="box gate" class:on />
    <text x="310" y="86" class="t b" text-anchor="middle">{on ? `Chain ${chain}` : 'Access rules off'}</text>
    <text x="310" y="108" class="t s" text-anchor="middle">{gate[0]}</text>
    <text x="310" y="128" class="t s m" text-anchor="middle">{gate[1]}</text>

    <!-- destinations: click to switch -->
    {#each dests as d, i (d.key)}
      {@const y = rowY(i)}
      <path d="M411 107 C 450 107, 445 {y + 20}, 494 {y + 20}" class="wire {tone(d)}" marker-end="url(#ad-{tone(d)})" />
      <g
        class="dest {tone(d)}"
        class:click={on && d.usable && !readOnly()}
        role="switch"
        aria-checked={d.ok}
        aria-disabled={!on || !d.usable || readOnly()}
        aria-label={readOnly() ? `${d.name}: ${verdict(d)}` : label(d)}
        tabindex={on && d.usable && !readOnly() ? 0 : undefined}
        onclick={() => toggle(d)}
        onkeydown={(e) => key(e, d)}
      >
        <title>{label(d)}</title>
        <rect x="498" y={y} width="301" height="40" rx="8" class="box" />
        <text x="512" y={y + 17} class="t s b">{d.name}</text>
        <text x="512" y={y + 32} class="t xs m">{d.sub.length > 34 ? d.sub.slice(0, 33) + '…' : d.sub}{extra(d)}</text>
        {#if on && d.usable}
          <rect x="712" y={y + 10} width="78" height="20" rx="10" class="pill {tone(d)}" />
          <text x="751" y={y + 24} class="t xs v {tone(d)}" text-anchor="middle">{verdict(d)}</text>
        {:else}
          <text x="788" y={y + 25} class="t xs v {tone(d)}" text-anchor="end">{verdict(d)}</text>
        {/if}
      </g>
    {/each}
  </svg>

  <!-- phones: the same, stacked -->
  <div class="stack">
    <div class="sbox"><strong>{kind} client</strong> <span class="mono faint">{clientIp}</span></div>
    <div class="arrow">↓ <span class="mono faint">{device}</span></div>
    <div class="sbox" class:on><strong>{on ? `Chain ${chain}` : 'Access rules off'}</strong><span class="faint">{gate[0]}; {gate[1]}</span></div>
    <div class="arrow">↓</div>
    {#each dests as d (d.key)}
      <button type="button" class="sbox row {tone(d)}" disabled={!on || !d.usable || readOnly()} role="switch" aria-checked={d.ok} aria-label={readOnly() ? `${d.name}: ${verdict(d)}` : label(d)} onclick={() => toggle(d)}>
        <span><strong>{d.name}</strong> <span class="faint">{d.sub}{extra(d)}</span></span>
        <span class="v {tone(d)}">{verdict(d)}</span>
      </button>
    {/each}
  </div>

  <figcaption class="hint">
    {#if !on}
      Clients reach whatever the server routes for them. Turn the rules on to limit it.
    {:else}
      Click a destination to allow or block it for every client; it becomes a rule below. Rules for single clients come first and win.
      {#if wg}Rules apply to traffic coming in on <span class="mono">{device}</span>.{:else}Rules apply to traffic coming in on every OpenVPN tun device.{/if}
    {/if}
  </figcaption>
</figure>

<aside class="needs" aria-label="What the rules need">
  <strong>Good to know</strong>
  <ul>
    {#if fwMode === 'off'}
      <li>The agent does not manage the firewall (<span class="mono">TD_FIREWALL=off</span>). Set it to <span class="mono">direct</span> or <span class="mono">file</span> to use these rules.</li>
    {/if}
    <li>Rules only limit. A client reaches a network only if the server routes it there too: the <a href="/network?tab=routing">Network page</a> ({wg ? 'allowed IPs' : 'routes'}).</li>
    <li>A rule for one client needs a fixed VPN address{wg ? ' (every WireGuard client has one)' : ': Network → Edit, or the button that shows up here'}.</li>
    {#if !wg}
      <li class:warn={c2c}>
        {c2c ? 'Clients can reach each other inside OpenVPN' : 'Clients do not reach each other inside OpenVPN'} (<span class="mono">client-to-client</span>, Network → Routing).
        {c2c ? 'OpenVPN then passes traffic between clients itself, so these rules cannot block it. Turn it off under Network → Routing to control Other VPN clients here.' : 'Good: traffic between clients goes through these rules.'}
      </li>
    {/if}
  </ul>
</aside>

<style>
  .diagram { margin: 0; display: grid; gap: var(--space-2); }
  svg { width: 100%; height: auto; max-height: 230px; }
  .box { fill: var(--sunken); stroke: var(--line); stroke-width: 1.2; }
  .gate.on { stroke: var(--accent); stroke-width: 1.6; }
  .dest.no .box { stroke-dasharray: 4 4; }
  .dest.click { cursor: pointer; }
  .dest.click:hover .box, .dest.click:focus-visible .box { stroke: var(--accent); stroke-width: 1.8; stroke-dasharray: none; }
  .dest:focus { outline: none; }
  .t { fill: var(--ink); font-family: inherit; font-size: 14px; }
  .b { font-weight: 600; }
  .s { font-size: 13px; }
  .xs { font-size: 11.5px; }
  .m { fill: var(--ink-3); }
  .mono { font-family: var(--font-mono, ui-monospace, monospace); }
  .v { font-weight: 600; }
  .v.yes { fill: var(--ok); }
  .v.no { fill: var(--bad); }
  .pill.yes { fill: var(--ok-soft); }
  .pill.no { fill: var(--bad-soft); }
  .wire { fill: none; stroke-width: 2; }
  .wire.yes { stroke: var(--ok); }
  .wire.no { stroke: var(--bad); stroke-dasharray: 6 5; }
  .f-yes { fill: var(--ok); }
  .f-no { fill: var(--bad); }
  .f-na { fill: var(--ink-3); }
  .wire.na { stroke: var(--ink-3); stroke-dasharray: 3 5; }
  .v.na { fill: var(--ink-3); }
  .dest.na .box { stroke-dasharray: 4 4; }
  figcaption { margin: 0; }
  .needs { margin-top: var(--space-2); padding: var(--space-3) var(--space-4); border: 1px solid var(--line); border-radius: var(--radius-s); font-size: var(--step--1); color: var(--ink-2); }
  .needs ul { margin: var(--space-1) 0 0; padding-left: 1.2em; display: grid; gap: 2px; }
  .needs li.warn { color: var(--warn); }

  .stack { display: none; gap: 6px; font-size: var(--step--1); }
  .sbox { display: grid; gap: 2px; padding: 8px 10px; border: 1px solid var(--line); border-radius: var(--radius-s); background: var(--sunken); color: var(--ink); font: inherit; text-align: left; }
  .sbox.on { border-color: var(--accent); }
  .sbox.row { display: flex; justify-content: space-between; align-items: center; gap: var(--space-2); }
  button.sbox:not(:disabled) { cursor: pointer; }
  .sbox.row.no { border-style: dashed; }
  .stack .v { font-weight: 600; white-space: nowrap; }
  .stack .v.yes { color: var(--ok); }
  .stack .v.no { color: var(--bad); }
  .arrow { padding-left: 12px; color: var(--ink-3); }
  @media (max-width: 640px) {
    svg { display: none; }
    .stack { display: grid; }
  }
</style>
