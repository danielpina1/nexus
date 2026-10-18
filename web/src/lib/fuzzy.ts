/** Cheap fuzzy score: word-prefix and substring hits beat scattered subsequences; 0 = no match. */
export function fuzzyScore(hay: string, needle: string): number {
  const h = hay.toLowerCase();
  const n = needle.toLowerCase().trim();
  if (!n) return 1;
  let total = 0;
  for (const term of n.split(/\s+/)) {
    const i = h.indexOf(term);
    if (i === 0) total += 10;
    else if (i > 0) total += /[\s/·:-]/.test(h[i - 1]) ? 8 : 5;
    else {
      // subsequence
      let j = 0;
      for (const c of h) if (c === term[j]) j++;
      if (j < term.length) return 0;
      total += 1;
    }
  }
  return total;
}
