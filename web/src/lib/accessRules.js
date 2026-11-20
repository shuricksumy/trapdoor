// The Access diagram's switches as plain functions, so they can be tested.
//
// A destination's switch is a plain rule for all clients, any protocol, to
// that network ("quick rule"), kept after the rules for single clients so
// those still win and before any rule for "anywhere". The internet is
// everything no rule matches, so its switch is the default; the other
// destinations keep their state when it flips. The server's address lies
// inside the VPN network, so its rule comes first and is written whenever the
// VPN network has one.
//
// policy: { enabled, default: 'allow'|'deny', rules: [{ client, to, proto, ports, action, note }] }
// ctx: { serverIp, subnet, routed: [cidr], bypass } — bypass: OpenVPN's
// client-to-client is on, so traffic between clients never meets the rules.

const allowed = (action) => action === 'allow';
const bare = (t) => t.replace(/\/32$/, '');
// the agent saves one address as a /32
const same = (to, t) => !!t && to.some((x) => bare(x) === bare(t));
const isQuick = (r) => !r.client && (!r.proto || r.proto === 'any') && !r.ports;

/** The destinations with a network: server, other clients, routed networks. */
export function targets(ctx) {
  return [
    { key: 'server', to: ctx.serverIp ? [ctx.serverIp] : [] },
    { key: 'clients', to: ctx.subnet ? [ctx.subnet] : [] },
    { key: 'routed', to: [...new Set(ctx.routed ?? [])] },
  ];
}

/** Whether all traffic to these networks is allowed now. */
export function stateOf(policy, ctx, to) {
  const q = policy.rules.find((r) => isQuick(r) && same(to, r.to));
  if (q) return allowed(q.action);
  // a rule for the VPN network covers the server's address too
  if (ctx.subnet && ctx.serverIp && !ctx.bypass && to.length === 1 && bare(to[0]) === bare(ctx.serverIp)) return stateOf(policy, ctx, [ctx.subnet]);
  return allowed(policy.default);
}

/** Rules besides the quick rule that also decide about these networks (single clients, ports, "anywhere"). */
export function othersFor(policy, to) {
  return policy.rules.filter((r) => !(isQuick(r) && same(to, r.to)) && (!r.to || same(to, r.to))).length;
}

/** Every destination for the diagram: key, to, ok (allowed), more (other rules), usable, bypass. */
export function destinations(policy, ctx) {
  return [
    ...targets(ctx).map((t) => {
      const bypass = t.key === 'clients' && !!ctx.bypass;
      return { ...t, ok: bypass || stateOf(policy, ctx, t.to), more: othersFor(policy, t.to), usable: t.to.length > 0 && !bypass, bypass };
    }),
    { key: 'internet', to: [], ok: allowed(policy.default), more: policy.rules.filter((r) => !r.to).length, usable: true, bypass: false },
  ];
}

/** The rules for the wanted state of each destination ({ server, clients, routed }: allowed?). */
export function applyWanted(policy, ctx, want) {
  const ts = targets(ctx);
  const def = allowed(policy.default);
  const all = ts.flatMap((t) => t.to);
  // keep the notes written on these rules in the editor
  const notes = Object.fromEntries(policy.rules.filter((r) => isQuick(r) && r.note && same(all, r.to)).map((r) => [bare(r.to), r.note]));
  const quick = [];
  for (const t of ts) {
    if (t.key === 'clients' && ctx.bypass) continue; // OpenVPN passes it itself
    const need = want[t.key] !== def || (t.key === 'server' && !ctx.bypass && want.clients !== def);
    if (need) for (const to of t.to) quick.push({ action: want[t.key] ? 'allow' : 'deny', to, ...(notes[bare(to)] ? { note: notes[bare(to)] } : {}) });
  }
  const keep = policy.rules.filter((r) => !(isQuick(r) && same(all, r.to)));
  const at = keep.findIndex((r) => !r.to);
  return at < 0 ? [...keep, ...quick] : [...keep.slice(0, at), ...quick, ...keep.slice(at)];
}

/** Flip one destination; returns the new { default, rules }. The policy is not changed. */
export function toggle(policy, ctx, key) {
  const d = destinations(policy, ctx).find((x) => x.key === key);
  if (!policy.enabled || !d?.usable) return { default: policy.default, rules: policy.rules };
  const want = Object.fromEntries(targets(ctx).map((t) => [t.key, stateOf(policy, ctx, t.to)]));
  let def = policy.default;
  // The internet is the default; the other destinations keep what they do now.
  if (key === 'internet') def = d.ok ? 'deny' : 'allow';
  else want[key] = !d.ok;
  return { default: def, rules: applyWanted({ ...policy, default: def }, ctx, want) };
}
