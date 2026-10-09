// Fidelity check: compares a screen of the design against the same screen in the Claude Design export.
//   node tools/compare.mjs <export.fig> <exportScreenId> <design.fig> "<design screen name>" [tolerancePt=0.75]
// The export is the baseline in git: git -C design show 2f1f1ab:locker-ios.fig > /tmp/claude-design-export.fig
// Texts and icons are compared by INK: both screens are exported at 3x and the glyph bounding box
// is measured inside each element's region (node boxes don't compare: export text boxes come from
// Chrome, design boxes from line height). Painted boxes (cards, buttons) are compared by node geometry.
// Prints every element whose ink or box moved or resized more than the tolerance.
import { execFileSync } from 'node:child_process';
import { readFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { decode } from 'fast-png';

const [exportFig, exportId, designFig, designName, tolArg] = process.argv.slice(2);
if (!designName) { console.error('usage: compare.mjs <export.fig> <screenId> <design.fig> "<design screen name>" [tol]'); process.exit(2); }
const tol = Number(tolArg ?? 0.75);
const SCALE = 3;
const here = (p) => fileURLToPath(new URL(p, import.meta.url));
const cli = here('../node_modules/@open-pencil/cli/bin/openpencil.js');
const fonts = here('../src/fonts.mjs');

// Runs inside OpenPencil's eval: screen id + flat list of texts, icons and painted boxes, relative to the screen.
const collect = (pick) => `
const scr = ${pick};
const o = scr.absoluteBoundingBox, out = [];
const solid = (n) => (n.fills || []).some((f) => f.type === 'SOLID' && f.visible !== false);
for (const n of scr.findAll(() => true)) {
  const b = n.absoluteBoundingBox; if (!b || !n.visible || n.type === 'VECTOR') continue;
  const box = { x: b.x - o.x, y: b.y - o.y, w: b.width, h: b.height };
  if (box.y >= o.height || box.y + box.h <= 0) continue; // scrolled out of the screen
  if (n.type === 'TEXT') out.push({ kind: 'text', key: n.characters, ...box });
  else if (solid(n)) { if (box.w < o.width || box.h < o.height) out.push({ kind: 'box', key: n.name, ...box }); }
  else if ((n.fills || []).some((f) => f.type === 'IMAGE') && b.width < 60) out.push({ kind: 'icon', key: n.name, ...box });
  else if (n.type === 'FRAME' && n.name !== 'SVG' && n.findOne?.((c) => c.type === 'VECTOR') && !n.findOne((c) => c.type === 'TEXT') && b.width < 60) out.push({ kind: 'icon', key: n.name, ...box });
}
return { id: scr.id, nodes: out };`;

const run = (file, code) => JSON.parse(execFileSync('node', [cli, 'eval', file, '--json', '-c', code], { encoding: 'utf8', maxBuffer: 1 << 26 }));
const A = run(exportFig, collect(`figma.getNodeById(${JSON.stringify(exportId)})`));
const B = run(designFig, collect(`figma.root.children.flatMap((p) => p.findAll((n) => n.name === ${JSON.stringify(designName)}))[0]`));

const dir = mkdtempSync(join(tmpdir(), 'compare-'));
const shot = (file, id, name) => {
  const out = join(dir, `${name}.png`);
  execFileSync('node', ['--import', fonts, cli, 'export', file, '--node', id, '-s', String(SCALE), '-o', out], { stdio: 'ignore' });
  const png = decode(readFileSync(out));
  const ch = png.channels;
  return { w: png.width, h: png.height, px: (x, y) => { const i = (y * png.width + x) * ch; return [png.data[i], png.data[i + 1], png.data[i + 2]]; } };
};
const imgA = shot(exportFig, A.id, 'export'), imgB = shot(designFig, B.id, 'design');

// Ink bounding box (in pt) inside a region: pixels that differ from the region's background,
// taken as the most common color on the region's border.
const ink = (img, reg, own) => {
  const x0 = Math.max(0, Math.floor(reg.x * SCALE)), y0 = Math.max(0, Math.floor(reg.y * SCALE));
  const x1 = Math.min(img.w - 1, Math.ceil((reg.x + reg.w) * SCALE)), y1 = Math.min(img.h - 1, Math.ceil((reg.y + reg.h) * SCALE));
  const count = new Map();
  for (let x = x0; x <= x1; x++) for (const y of [y0, y1]) { const k = img.px(x, y).join(); count.set(k, (count.get(k) ?? 0) + 1); }
  for (let y = y0; y <= y1; y++) for (const x of [x0, x1]) { const k = img.px(x, y).join(); count.set(k, (count.get(k) ?? 0) + 1); }
  const bg = [...count.entries()].sort((a, b) => b[1] - a[1])[0][0].split(',').map(Number);
  const isInk = (x, y) => { if (x === x0 || x === x1 || y === y0 || y === y1) return false; const p = img.px(x, y); return Math.abs(p[0] - bg[0]) + Math.abs(p[1] - bg[1]) + Math.abs(p[2] - bg[2]) >= 90; };
  // Split ink into horizontal runs (text lines) and keep the runs centered inside the element's own
  // box, so a neighbor line whose box overlaps the region (export multi-line titles) isn't counted.
  const runs = [];
  for (let y = y0; y <= y1; y++) {
    let hit = false; for (let x = x0; x <= x1 && !hit; x++) hit = isInk(x, y);
    if (hit) { if (runs.length && runs.at(-1)[1] === y - 1) runs.at(-1)[1] = y; else runs.push([y, y]); }
  }
  const mid = ([a, b]) => (a + b + 1) / 2 / SCALE;
  let keep = runs.filter((r) => mid(r) >= own.y && mid(r) <= own.y + own.h);
  if (!keep.length && runs.length) { // box doesn't contain its own ink (export shifted text): nearest line
    const c = own.y + own.h / 2;
    keep = [runs.reduce((best, r) => (Math.abs(mid(r) - c) < Math.abs(mid(best) - c) ? r : best))];
  }
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const [ra, rb] of keep) for (let y = ra; y <= rb; y++) for (let x = x0; x <= x1; x++) {
    if (!isInk(x, y)) continue;
    minX = Math.min(minX, x); maxX = Math.max(maxX, x); minY = Math.min(minY, y); maxY = Math.max(maxY, y);
  }
  if (minX === Infinity) return null;
  return { x: minX / SCALE, y: minY / SCALE, w: (maxX - minX + 1) / SCALE, h: (maxY - minY + 1) / SCALE };
};

const r = (v) => Math.round(v * 100) / 100;
const rows = [];
const report = (kind, key, a, b) => {
  let pa = a, pb = b;
  if (kind !== 'box') {
    // Region: union of both boxes, tight (export text boxes are already padded by line height).
    // No side padding: it can reach a container's edge (avatar circle) and count its background as ink.
    const reg = { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), w: 0, h: 0 };
    reg.w = Math.max(a.x + a.w, b.x + b.w) - reg.x; reg.h = Math.max(a.y + a.h, b.y + b.h) - reg.y;
    // Same line filter on both sides: the vertical overlap of both boxes (a tall design box mustn't pull in the next line).
    const top = Math.max(a.y, b.y), bot = Math.min(a.y + a.h, b.y + b.h);
    const own = bot > top ? { y: top, h: bot - top } : { y: reg.y, h: reg.h };
    pa = ink(imgA, reg, own); pb = ink(imgB, reg, own);
    if (!pa || !pb) { rows.push({ kind, key, worst: Infinity, noInk: true, at: `${r(a.x)},${r(a.y)}` }); return; }
  }
  const d = { dx: pb.x - pa.x, dy: pb.y - pa.y, dw: pb.w - pa.w, dh: pb.h - pa.h };
  rows.push({ kind, box: b, key: String(key).slice(0, 32), worst: r(Math.max(...Object.values(d).map(Math.abs))), at: `${r(a.x)},${r(a.y)}`, ...Object.fromEntries(Object.entries(d).map(([k, v]) => [k, r(v)])) });
};

// Texts: same content (case-insensitive), in reading order. The export splits rich text into
// consecutive nodes ("Recordar a " + "5" + " pendientes"); those are joined before matching.
const norm = (s) => s.replace(/\u00a0/g, ' ').toLocaleLowerCase('es'); // the export uses NBSP around interpolations
const textsB = B.nodes.filter((n) => n.kind === 'text');
const textsA = A.nodes.filter((n) => n.kind === 'text');
for (let k = 0; k < textsA.length; k++) {
  const a = textsA[k];
  // Repeated texts ("9:00", "Pagado"): take the nearest copy, not the first one.
  const nearest = (key, at) => textsB.reduce((best, b, idx) => norm(b.key) !== norm(key) ? best
    : best < 0 || Math.hypot(b.x - at.x, b.y - at.y) < Math.hypot(textsB[best].x - at.x, textsB[best].y - at.y) ? idx : best, -1);
  let i = nearest(a.key, a);
  let box = a, j = k;
  for (let joined = a.key; i < 0 && j + 1 < textsA.length && textsB.some((b) => norm(b.key).startsWith(norm(joined))); ) {
    const next = textsA[++j]; joined += next.key;
    const x = Math.min(box.x, next.x), y = Math.min(box.y, next.y);
    box = { x, y, w: Math.max(box.x + box.w, next.x + next.w) - x, h: Math.max(box.y + box.h, next.y + next.h) - y };
    i = nearest(joined, box);
  }
  if (i < 0) { rows.push({ kind: 'text', key: a.key.slice(0, 32), worst: Infinity, missing: true, at: `${r(a.x)},${r(a.y)}` }); continue; }
  report('text', i >= 0 && j > k ? textsB[i].key : a.key, box, textsB.splice(i, 1)[0]); k = j;
}
for (const b of textsB) rows.push({ kind: 'text', key: b.key.slice(0, 32), worst: Infinity, extra: true });

// Icons and boxes: nearest of the same kind with a similar size.
for (const kind of ['icon', 'box']) {
  const pool = B.nodes.filter((n) => n.kind === kind);
  for (const a of A.nodes.filter((n) => n.kind === kind)) {
    let best = -1, bestD = Infinity;
    pool.forEach((b, i) => {
      if (Math.abs(b.w - a.w) > 6 || Math.abs(b.h - a.h) > 6) return;
      const dist = Math.hypot(b.x - a.x, b.y - a.y);
      if (dist < bestD) { bestD = dist; best = i; }
    });
    if (best < 0 || bestD > 12) { rows.push({ kind, key: `${a.key} ${r(a.w)}x${r(a.h)}`, worst: Infinity, missing: true, at: `${r(a.x)},${r(a.y)}` }); continue; }
    const b = pool.splice(best, 1)[0];
    report(kind, kind === 'icon' ? b.key : a.key, a, b);
  }
  // Leftover design icons drawn as layers of a matched one (battery edge + level) are not extra.
  const matched = rows.filter((x) => x.kind === kind && x.box).map((x) => x.box);
  const overlaps = (b) => matched.some((m) => b.x < m.x + m.w && m.x < b.x + b.w && b.y < m.y + m.h && m.y < b.y + b.h);
  for (const b of pool) if (kind === 'box' || !overlaps(b)) rows.push({ kind, key: `${b.key} ${r(b.w)}x${r(b.h)}`, worst: Infinity, extra: true, at: `${r(b.x)},${r(b.y)}` });
}

const off = rows.filter((x) => x.worst > tol).sort((a, b) => b.worst - a.worst);
console.log(`${rows.length - off.length}/${rows.length} within ${tol}pt  (exports in ${dir})`);
for (const x of off) {
  if (x.missing) console.log(`MISSING ${x.kind} "${x.key}" @${x.at}`);
  else if (x.extra) console.log(`EXTRA   ${x.kind} "${x.key}" ${x.at ? '@' + x.at : ''}`);
  else if (x.noInk) console.log(`NO INK  ${x.kind} "${x.key}" @${x.at}`);
  else console.log(`${x.kind.padEnd(4)} "${x.key}" @${x.at}  dx=${x.dx} dy=${x.dy} dw=${x.dw} dh=${x.dh}`);
}
process.exit(off.length ? 1 : 0);
