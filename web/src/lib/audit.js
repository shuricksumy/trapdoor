// The audit log in words: what each logged action means, and the groups
// the page filters by (an action's group is the part before the first dot).

const labels = {
  login: 'signed in',
  'login.failed': 'sign-in failed',
  logout: 'signed out',
  'cert.issue': 'added a client',
  'cert.revoke': 'revoked a client',
  'cert.unrevoke': 'restored a client',
  'cert.renew': 'renewed a certificate',
  'cert.remove': 'deleted a client',
  'cert.download': 'downloaded a profile',
  'cert.notes': 'renamed a client',
  'cert.defaults': 'changed client defaults',
  'client.network': "changed a client's network",
  'client.notify': 'changed online / offline messages',
  'client.send': 'emailed a client its profile',
  'client2fa.update': 'changed two-factor sign-in',
  'session.kill': 'disconnected a client',
  'peer.add': 'added a client',
  'peer.update': 'changed a client',
  'peer.remove': 'deleted a client',
  'peer.download': 'downloaded a config',
  'network.save': 'saved a named network',
  'network.remove': 'removed a named network',
  'firewall.save': 'changed access rules',
  'firewall.addresses': 'gave clients fixed addresses',
  'server.create': 'added a server',
  'server.remove': 'removed a server',
  'server.start': 'started a server',
  'server.stop': 'stopped a server',
  'server.restart': 'restarted a server',
  'server.settings': 'changed server settings',
  'server.config.save': 'saved server.conf',
  'server.setup': 'set up a server',
  'server.backup': 'backed up a server',
  'server.restore': 'restored a server',
  'server.autostart': 'changed autostart',
  'scripts.save': 'changed scripts',
  'host.add': 'added a node',
  'host.remove': 'removed a node',
  'node.token': 'made a join token',
  'limits.default': 'changed the default limits',
  'limits.client': "changed a client's limits",
  'limits.reset': "reset a client's usage",
  'limits.server': "changed the server's limits settings",
  'backup.download': 'downloaded a backup',
  'backup.run': 'ran a backup',
  'backup.schedule': 'changed the backup schedule',
  'backup.delete': 'deleted a backup file',
  'email.settings': 'changed email settings',
  'email.test': 'sent a test email',
  'telegram.settings': 'changed Telegram settings',
  'telegram.test': 'sent a test Telegram message',
  'presence.settings': 'changed where online / offline messages go',
  'admin.add': 'added an account',
  'admin.role': 'changed a role',
  'admin.enable': 'turned an account on',
  'admin.disable': 'turned an account off',
  'admin.password': 'set a password',
  'admin.logout': 'logged an account out',
  'admin.delete': 'deleted an account',
};

/** "cert.issue" → "added a client"; tools: "ran ping"; unknown: the action itself. */
export function actionText(action) {
  if (labels[action]) return labels[action];
  if (action?.startsWith('tool.')) return `ran ${action.slice(5)}`;
  return action ?? '';
}

/** A sign-in that failed, or a removal: shown in red. */
export function actionTone(action) {
  if (action === 'login.failed') return 'bad';
  if (/\.(remove|delete|revoke|disable)$/.test(action ?? '') || action === 'session.kill') return 'warn';
  return '';
}

/** The kinds to filter by: [value for ?action=, label]. */
export const actionGroups = [
  ['', 'Everything'],
  ['login', 'Sign-ins'],
  ['cert', 'Clients (OpenVPN)'],
  ['peer', 'Clients (WireGuard)'],
  ['client', 'Client settings'],
  ['firewall', 'Access rules'],
  ['network', 'Named networks'],
  ['limits', 'Limits'],
  ['server', 'Servers'],
  ['host', 'Nodes'],
  ['backup', 'Backups'],
  ['admin', 'Accounts'],
  ['tool', 'Tools'],
];

/** The period presets as ?from= (YYYY-MM-DD, local), '' = everything. */
export function periodFrom(preset, now = new Date()) {
  const days = { today: 0, '7d': 6, '30d': 29, '1y': 364 }[preset];
  if (days === undefined) return '';
  const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - days);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}
