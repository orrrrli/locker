// LOCKER wordmark as clean geometry, redrawn from the traced reference (locker-wordmark.svg) to fix its
// hand-drawn drift. Glyphs are drawn upright on shared metrics, then sheared once to a single italic angle.
// Units are canvas px of the 1600×800 artboard. Upright space: u = x, y down; shear pivots on the baseline.

const SLANT = Math.tan((17.5 * Math.PI) / 180);
const T = 255, B = 505;          // cap line, baseline (shared by every letter)
const SW = 86;                   // vertical stem width
const TOP = T + 65, FOOT = B - 74; // top-bar bottom, bottom-bar top (bottoms stay heavier on purpose)
const DIAG = 1.13;               // du/dy of the K leg, R leg, L kick and E chamfer (all parallel)
const LEG = 124, ARM = 108;      // horizontal widths of the lower diagonals and of the K upper arm
const R_BIG = 58, R_SM = 4;      // O/C outer corners, generic corner rounding

// Polygon with per-vertex fillet radius -> SVG subpath. pts: [u, y, r]; map transforms every point.
const poly = (pts, map) => {
  const n = pts.length, f = (p) => map(p).map((v) => +v.toFixed(2)).join(' ');
  let d = '';
  pts.forEach(([u, y, r = R_SM], i) => {
    const [pu, py] = pts[(i + n - 1) % n], [nu, ny] = pts[(i + 1) % n];
    const a = [pu - u, py - y], c = [nu - u, ny - y];
    const la = Math.hypot(...a), lc = Math.hypot(...c);
    const da = [a[0] / la, a[1] / la], dc = [c[0] / lc, c[1] / lc];
    const th = Math.acos(Math.max(-1, Math.min(1, da[0] * dc[0] + da[1] * dc[1]))); // interior angle
    const t = r ? Math.min(r / Math.tan(th / 2), la / 2, lc / 2) : 0;
    const rr = t * Math.tan(th / 2), h = (4 / 3) * Math.tan((Math.PI - th) / 4) * rr;
    const p1 = [u + da[0] * t, y + da[1] * t], p2 = [u + dc[0] * t, y + dc[1] * t];
    d += (i ? ' L ' : 'M ') + f(p1);
    if (t) d += ` C ${f([p1[0] - da[0] * h, p1[1] - da[1] * h])} ${f([p2[0] - dc[0] * h, p2[1] - dc[1] * h])} ${f(p2)}`;
  });
  return d + ' Z';
};
const skew = ([u, y]) => [u + (B - y) * SLANT, y];
const flat = (p) => p;
const rev = (pts) => [...pts].reverse(); // holes wind the other way (nonzero fill)

// Diagonal edge helper: u on a DIAG-sloped line that passes through (u0, y0).
const diag = (u0, y0) => (y) => u0 + DIAG * (y - y0);

// Interlocks: a bottom bar ends in a DIAG kick, and the next letter's bottom-left is knocked out parallel to it,
// one GAP away (perpendicular), so the left letter reads as sitting on top. Same rule as the K leg over the E.
// Horizontal offset between two parallel edges of upright slope du/dy = s that leaves GAP between them once sheared
// (the shear turns s into s - SLANT, so the offset is measured on the final italic, not on the upright drawing).
const GAP = 13, hgap = (s) => GAP * Math.hypot(1, s - SLANT), GAP_H = hgap(DIAG);
const kick = (end, len) => [[end, B - len / DIAG, 3], [end + len, B, 2]]; // bar end -> tip on the baseline
const cut = (x, c) => [[c, B, 3], [x, B - (c - x) / DIAG, 3]];           // baseline at c -> left edge

// ---- Glyphs (upright) -------------------------------------------------------------------------------
const L = (x, kickLen = 31) => [poly([[x, T], [x + SW, T], [x + SW, FOOT, 3], [x + 171, FOOT], ...kick(x + 171, kickLen), [x, B, 50]], skew)];
const O = (x, knock = 0, kickLen = 0, w = 254) => {
  const outer = poly([[x, T, R_BIG], [x + w, T, R_BIG], ...(kickLen ? kick(x + w, kickLen) : [[x + w, B, R_BIG]]),
    ...(knock ? cut(x, knock) : [[x, B, R_BIG]])], skew);
  return [outer, poly(rev([[x + SW, TOP, 3], [x + w - SW, TOP, 3], [x + w - SW, FOOT, 3], [x + SW, FOOT, 3]]), skew)];
};
const C = (x, kickLen = 0, knock = 0, w = 210) => [poly([[x, T, R_BIG], [x + w, T], [x + w, TOP, 3], [x + SW, TOP, 3],
  [x + SW, FOOT, 3], [x + w, FOOT, 3], ...(kickLen ? kick(x + w, kickLen) : [[x + w, B]]),
  ...(knock ? cut(x, knock) : [[x, B, R_BIG]])], skew)];
// bota: the kick boot in negative space, between the stem and the arms, toe down on the baseline. The top (stem, arms,
// sock opening) is ours; the boot below is traced from the reference K (Image #12), in upright units relative to the
// stem's left edge. Stem side: pull tab, round heel, then stepped studs down to the toe. Arms side: the arm tip and
// two more speed bars with round ends, the instep between them, then a round toe. The arms and leg move KW right.
export const KW = 48;
// Sole: a straight diagonal from the heel to the toe, with studs cut into the stem, two at the heel and four under the
// forefoot, the arch between them plain (as in the reference K). Studs stand square to the sole as seen after the shear.
const sole = (y) => 60 + (142 * (y - 392)) / 113;          // r of the sole at y, heel (60, 392) to toe (202, B)
// Studs: [y on the sole, length]. Each tapers from BASE to TIP wide; TILT leans the tip toward the heel (-) or toe (+).
const STUDS = [[393, 13], [406, 12], [450, 12], [463, 11], [476, 9], [489, 7]], BASE = 10, TIP = 5, TILT = 0;
const SOLE = (() => {
  const up = ([vx, vy]) => [vx + vy * SLANT, vy];           // a sheared-space vector back to upright
  const d = [142 - 113 * SLANT, 113], k = Math.hypot(...d), t = up([d[0] / k, d[1] / k]), n = up([-d[1] / k, d[0] / k]);
  const at = (c, along, out, r) => [c[0] + t[0] * along + n[0] * out, c[1] + t[1] * along + n[1] * out, r];
  const studs = STUDS.flatMap(([y, l]) => {
    const c = at([sole(y), y], BASE / 2, 0), tip = TILT * l;
    return [at(c, -BASE / 2, 0, 1.5), at(c, tip - TIP / 2, l, TIP / 2.5), at(c, tip + TIP / 2, l, TIP / 2.5), at(c, BASE / 2, 0, 1.5)];
  });
  return [[52, 384, 8], [60, 392, 3], ...studs, [202, B, 2]];
})();
// Arms' left edge, toe to arm tip: round toe, bar 3, instep, bar 2, instep, bar 1 (the arm tip).
const INSTEP = [[216, 486, 18], [183, 444.5, 3], [158.6, 444.5, 6.5], [158.6, 431.4, 6.5], [173.3, 431.4, 2], [164, 416.6, 2],
  [133, 416.6, 6], [133, 403.4, 6], [160.4, 403.4, 2], [156, 380, 3], [138, 378, 4], [148, 330, 80]];
const K = (x, legR, knock = 0, bota = false) => {
  const w = bota ? KW : 0; legR += w;
  const up = (y) => x + 251 + w - 0.86 * (y - T);       // upper arm right edge, through (x+251, T)
  const lr = diag(legR, B), ll = diag(legR - LEG, B);
  const crotchY = (x + 251 + w + 0.86 * T - legR + DIAG * B) / (DIAG + 0.86);
  const armL = up(T) - ARM, armJoin = T + (armL - (x + SW)) / 0.86;
  const legJoin = B + (x + SW - ll(B)) / DIAG;
  const foot = knock ? cut(x, knock) : [[x, B]];
  if (!bota) return [poly([[x, T], [x + SW, T, 3], [x + SW, armJoin, 2], [armL, T, 3], [up(T), T, 3], [up(crotchY), crotchY, 2],
    [legR, B, 3], [ll(B), B, 3], [x + SW, legJoin, 2], [x + SW, B, 3], ...foot], skew)];
  const s = x + SW, at = ([r, y, f]) => [x + r, y, f];   // s: the stem's right edge
  const stem = [[x, T], [s, T, 3], [s, 331, 3], [x + 99, 358, 5], [x + 90, 362.6, 5], [x + 81, 346, 3], [x + 56, 351, 14], [x + 44, 366, 12],
    ...SOLE.map(at), ...foot];
  const arms = [[armL, T, 3], [up(T), T, 3], [up(crotchY), crotchY, 2], [legR, B, 3], [ll(B), B, 3], ...INSTEP.map(at)];
  return [poly(stem, skew), poly(arms, skew)];
};
// topCut: u where the top-left knockout (parallel to the K upper arm) meets the cap line; 0 keeps the round corner.
const E = (x, chamfer, kickLen = 0, topCut = 0) => {
  const top = topCut ? [[x, T + (topCut - x) / 0.86, 3], [topCut, T, 3]] : [[x, T, 55]];
  return [poly([...top, [x + 220, T], [x + 220, TOP, 3], [x + SW, TOP, 3], [x + SW, TOP + 23, 3],
    [x + 216, TOP + 23, 3], [x + 170, FOOT - 23, 3], [x + SW, FOOT - 23, 3], [x + SW, FOOT, 3], [x + 223, FOOT, 3],
    ...(kickLen ? kick(x + 223, kickLen) : [[x + 223, B]]), ...cut(x, chamfer)], skew)];
};
// The counter is a slot open to the left, on the same line as the E's upper slot, so E and R share one horizontal
// speed line; the stem only exists below it, and the bowl's top-right corner is round instead of cut.
const Rg = (x, legR, knock = 0) => {
  const lr = diag(legR, B), ll = diag(legR - LEG, B), bowlB = 408, bowlR = x + 240;
  const legJoin = B + (x + SW - ll(B)) / DIAG;
  const bottom = [[bowlR, bowlB, 60], [lr(bowlB), bowlB, 2], [legR, B, 3], [ll(B), B, 3], [x + SW, legJoin, 2],
    [x + SW, B, 3], ...(knock ? cut(x, knock) : [[x, B]])];
  const s0 = TOP, s1 = TOP + 23, end = x + 157; // slot rows match the E's upper slot
  return [poly([[x, T], [bowlR, T, 95], ...bottom, [x, s1, 3], [end, s1, 11.5], [end, s0, 11.5], [x, s0, 3]], skew)];
};

// ---- Frame: two concentric stadium-cornered rects, one stroke all around (not sheared) ----------------
const FRAME = { x0: 32, y0: 187, x1: 1567.5, y1: 571, r: 120, stroke: 29 };
const rect = (x0, y0, x1, y1, r) => [[x0, y0, r], [x1, y0, r], [x1, y1, r], [x0, y1, r]];
const frame = ({ x0, y0, x1, y1, r, stroke: s } = FRAME) =>
  [poly(rect(x0, y0, x1, y1, r), flat), poly(rev(rect(x0 + s, y0 + s, x1 - s, y1 - s, r - s)), flat)];

// Letter positions: upright left edges; the K leg, E chamfer and R leg are set by their baseline corners.
// il: kick length (px) per pair (0 = off; L->O off keeps the L's short 31 kick over a round O corner),
// and whether the K upper arm knocks out the E's top-left. bota: the K's boot (see K), off by default.
export const INTERLOCK = { lo: 0, oc: 0, ck: 45, er: 45, ekTop: true };
const letters = (over = {}) => {
  const il = { ...INTERLOCK, ...over };
  const kw = il.bota ? KW : 0;            // the boot K is wider: E and R move right by kw
  const kArmR = 748 + 251 + kw, kLegR = 1037 + kw; // K upper arm right edge at the cap line, K leg right corner at the baseline
  const out = [...L(70, il.lo || 31), ...O(256, il.lo && 70 + 171 + il.lo + GAP_H, il.oc),
    ...C(524, il.ck, il.oc && 256 + 254 + il.oc + GAP_H), ...K(748, 1037, il.ck && 524 + 210 + il.ck + GAP_H, il.bota),
    ...E(990 + kw, kLegR + GAP_H, il.er, il.ekTop && kArmR + hgap(-0.86)),
    ...Rg(1227.5 + kw, 1509.5 + kw, il.er && 990 + kw + 223 + il.er + GAP_H)];
  if (!kw) return out;
  // Scale uniformly about the L's baseline corner and the cap's middle so the R lands where it did (frame unchanged).
  const k = (1509.5 - 70) / (1509.5 + kw - 70), oy = (T + B) / 2;
  return out.map((d) => { let i = 0; return d.replace(/-?\d*\.?\d+/g, (v) => (i++ % 2 ? oy + (v - oy) * k : 70 + (v - 70) * k).toFixed(2)); });
};
export const wordmarkPaths = (il) => [...frame(), ...letters(il)];

const svgDoc = (w, h, d, transform, fill) =>
  `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}">` +
  `<g transform="${transform}" fill="${fill}"><path d="${d.join(' ')}"/></g></svg>`;

// Whole artboard. dy recenters the frame on the 800 canvas (its box is 187..571, center 379).
export const wordmarkSVG = (fill = '#F1EEDD', dy = 21, il) =>
  svgDoc(1600, 800, wordmarkPaths(il), `translate(0,${dy})`, fill);

// ---- App icon (1024 canvas, transparent; the page paints the background) -------------------------------
// bbox of a path set from its coordinates (fillet handles sit inside their corners, so this is the ink box).
const bbox = (d) => {
  const n = d.join(' ').match(/-?\d+(\.\d+)?/g).map(Number), xs = n.filter((_, i) => !(i % 2)), ys = n.filter((_, i) => i % 2);
  return { x0: Math.min(...xs), y0: Math.min(...ys), x1: Math.max(...xs), y1: Math.max(...ys) };
};
// Fit d inside a w×h box centered on (512, 512).
const fit = (d, { w = Infinity, h = Infinity }) => {
  const b = bbox(d), k = Math.min(w / (b.x1 - b.x0), h / (b.y1 - b.y0));
  return `translate(${512 - ((b.x0 + b.x1) / 2) * k},${512 - ((b.y0 + b.y1) / 2) * k}) scale(${k})`;
};
// App icon explorations traced from two references (Image #15, #16), in the reference's own px around its ball center.
const path = (pts) => 'M ' + pts.map((p) => p.map((v) => +v.toFixed(2)).join(' ')).join(' L ') + ' Z';
const disk = (x, y, r) => poly(rect(x - r, y - r, x + r, y + r, r), flat);
// Gajos: a ball (r 46) inside four arcs. Each arc is a ring around its outer corner (radii 69..101, corner 176 out on
// the diagonal), cut by a 131 circle around the ball and by a cross-shaped gap 43.5 wide. Sampled in polar.
// o overrides: rIn (lower = thicker arcs), ball, g (half the gap), R, k (rounds the corner where the arc meets R; 0 = sharp).
const gajos = (o = {}) => {
  const { D = 176, rIn = 69, rOut = 101, R = 131, g = 21.75, ball = 46, k = 0 } = o, step = Math.PI / 1800;
  const smin = (x, y) => (k ? -k * Math.log(Math.exp(-x / k) + Math.exp(-y / k)) : Math.min(x, y));
  const quarter = (sx, sy) => {
    const out = [], inn = [];
    for (let a = step; a < Math.PI / 2; a += step) {
      const ph = a - Math.PI / 4, c = D * Math.cos(ph), s = D * Math.sin(ph);
      const near = c - Math.sqrt(rOut ** 2 - s ** 2);
      const far = smin(R, Math.abs(s) < rIn ? c - Math.sqrt(rIn ** 2 - s ** 2) : c + Math.sqrt(rOut ** 2 - s ** 2));
      const lo = Math.max(near, g / Math.cos(a), g / Math.sin(a));
      if (!(lo < far)) continue;
      const at = (r) => [sx * r * Math.cos(a), sy * r * Math.sin(a)];
      out.push(at(far)); inn.push(at(lo));
    }
    return path([...out, ...inn.reverse()]);
  };
  return [quarter(1, 1), quarter(-1, 1), quarter(-1, -1), quarter(1, -1), disk(0, 0, ball)];
};
// Escudo: seven bars (21.5 wide, top 223.5 above the center) dropping into a ball ring (140 out, 21.5 thick); the outer
// bars are the ring's tangents. Seams 22 wide: a center pentagon (52.6 to its vertices, point up), a radial from each
// vertex to 93, then two branches at ±60deg to the ring. Joints are round.
// o overrides: bars (count), w (bar and ring width), sw (seam width), a (pentagon), b (branch junction), ba (branch angle,
// deg off the radial).
const escudo = (o = {}) => {
  const { bars = 7, w = 21.5, sw = 22, a = 52.6, b = 93, ba = 60 } = o;
  const Ro = 140, Ri = Ro - w, hw = sw / 2, out = [];
  const box = (x0, y0, x1, y1) => path([[x0, y0], [x1, y0], [x1, y1], [x0, y1]]);
  out.push(box(-Ro, -223.5, -Ro + w, 0), box(Ro - w, -223.5, Ro, 0));
  for (let i = 1; i < bars - 1; i++) {
    const c = -(Ro - w / 2) + (i * 2 * (Ro - w / 2)) / (bars - 1), dx = Math.max(0, Math.abs(c) - w / 2);
    out.push(box(c - w / 2, -223.5, c + w / 2, -Math.sqrt(Ri ** 2 - dx ** 2)));
  }
  out.push(disk(0, 0, Ro), poly(rev(rect(-Ri, -Ri, Ri, Ri, Ri)), flat));
  const dir = (t) => [Math.cos(t), Math.sin(t)], deg = Math.PI / 180;
  const seam = ([ax, ay], [bx, by]) => {
    const l = Math.hypot(bx - ax, by - ay), nx = (-(by - ay) / l) * hw, ny = ((bx - ax) / l) * hw;
    const p = [[ax + nx, ay + ny], [bx + nx, by + ny], [bx - nx, by - ny], [ax - nx, ay - ny]];
    const area = p.reduce((s, [x, y], i) => s + x * p[(i + 1) % 4][1] - p[(i + 1) % 4][0] * y, 0);
    return path(area > 0 ? p : rev(p));
  };
  const toRing = ([x, y], [dx, dy], r = (Ro + Ri) / 2) => {
    const b = x * dx + y * dy, t = -b + Math.sqrt(b * b - (x * x + y * y) + r * r);
    return [x + t * dx, y + t * dy];
  };
  const th = [0, 1, 2, 3, 4].map((k) => (-90 + 72 * k) * deg);
  const V = th.map((t) => dir(t).map((v) => v * a)), W = th.map((t) => dir(t).map((v) => v * b));
  th.forEach((t, k) => {
    out.push(seam(V[k], V[(k + 1) % 5]), disk(...V[k], hw));
    out.push(seam(V[k], W[k]), disk(...W[k], hw));
    const ends = [-ba, ba].map((s) => toRing(W[k], dir(t + s * deg)));
    ends.forEach((e) => out.push(seam(W[k], e)));
    // Under 60deg the outer pentagon's tip between the two branches shrinks to a speck: fill it.
    if (ba < 60) { const p = [W[k], ...ends], ar = (p[1][0] - p[0][0]) * (p[2][1] - p[0][1]) - (p[2][0] - p[0][0]) * (p[1][1] - p[0][1]); out.push(path(ar > 0 ? p : rev(p))); }
  });
  return out;
};
// Both at ~70% of the canvas, close to the reference. Gajos: k rounds the kink where the arc's outer edge meets the R
// circle. Escudo: branches at 40deg instead of 60 meet the ring steeper, so no thin dark slivers along it.
const BIG = { w: 720, h: 720 };
const ICONS = {
  gajos: [() => gajos({ rIn: 64, ball: 48, k: 4 }), BIG],
  escudo: [() => escudo({ w: 24, sw: 24, b: 96, ba: 40 }), BIG],
};
export const iconSVG = (kind, fill = '#F1EEDD') => {
  const [make, size] = ICONS[kind], d = make();
  return svgDoc(1024, 1024, d, fit(d, size), fill);
};
