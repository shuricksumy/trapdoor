// Line diff (LCS) for the "changes" preview: [{ op: ' ' | '+' | '-', text }].
// Unchanged runs longer than 2*context lines are collapsed to "…".
export function lineDiff(before, after, context = 2) {
  const a = before.replace(/\n$/, '').split('\n');
  const b = after.replace(/\n$/, '').split('\n');
  const n = a.length;
  const m = b.length;
  const dp = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const out = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ op: ' ', text: a[i] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ op: '-', text: a[i++] });
    } else {
      out.push({ op: '+', text: b[j++] });
    }
  }
  while (i < n) out.push({ op: '-', text: a[i++] });
  while (j < m) out.push({ op: '+', text: b[j++] });

  // Collapse long unchanged runs.
  const keep = out.map((l, k) => l.op !== ' ' || out.slice(Math.max(0, k - context), k + context + 1).some((x) => x.op !== ' '));
  const result = [];
  for (let k = 0; k < out.length; k++) {
    if (keep[k]) result.push(out[k]);
    else if (result.length === 0 || result[result.length - 1].op !== '…') result.push({ op: '…', text: '' });
  }
  return result.some((l) => l.op === '+' || l.op === '-') ? result : [];
}
