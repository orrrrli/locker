// Logo exploration: approach 1, "Parche". Frame = native rounded rects; the script lettering is
// traced from the reference image with potrace (design/src/logo/locker-script.svg), not pasted as raster.
import { readFileSync } from 'node:fs';
import { createSVGNodes } from '@open-pencil/core';

const svg = (f) => readFileSync(new URL(`../logo/${f}`, import.meta.url), 'utf8');
const SCRIPT = svg('locker-script.svg');
const GREEN = { r: 14 / 255, g: 70 / 255, b: 40 / 255 }; // #0E4628, the pitch green of the Bienvenida hero
const CREAM = { r: 242 / 255, g: 242 / 255, b: 233 / 255 };
const solid = (c) => [{ type: 'SOLID', color: { ...c, a: 1 }, opacity: 1, visible: true }];
const shadow = (a, y, blur) => [{ type: 'DROP_SHADOW', color: { r: 0, g: 0, b: 0, a }, offset: { x: 0, y }, radius: blur, spread: 0, visible: true, blendMode: 'NORMAL' }];

const rrect = (figma, parent, name, x, y, w, h, r, c) => {
  const f = figma.createFrame(); f.name = name; f.resize(w, h); f.cornerRadius = r; f.fills = c ? solid(c) : [];
  parent.appendChild(f); f.x = x; f.y = y; return f; // after appendChild: reparenting keeps the absolute position
};

// Script at scale s with its 1624×968 canvas origin at (x, y) inside parent.
// The fill lives in the SVG itself: the renderer ignores fills set on the imported vector afterwards.
// bold > 0 thickens the letters by a same-color stroke (the importer applies it in canvas px, not path units).
const script = (figma, parent, s, x, y, bold = 0, ink = '#0E4628') => {
  const paint = `fill="${ink}"` + (bold ? ` stroke="${ink}" stroke-width="${bold}" stroke-linejoin="round"` : '');
  const svg = SCRIPT.replace('fill="#0E4628"', paint);
  const n = figma.getNodeById(createSVGNodes(figma.graph, parent.id, svg, { name: 'Locker script' }).id);
  if (s !== 1) n.rescale(s);
  n.x = x; n.y = y; return n;
};

// Outer cream band, green frame line, cream panel (measured from the reference at 1624×968), plus the script.
// Colors come from a palette so every icon can render inverted: paper = band and panel, ink = line and script.
const NORMAL = { paper: CREAM, ink: GREEN, inkHex: '#0E4628' };
const INVERSE = { paper: GREEN, ink: CREAM, inkHex: '#F2F2E9' };
const PATCH = [
  ['Banda', 15, 113, 1594, 746, 146, 'paper'],
  ['Línea', 50, 146, 1525, 683, 110, 'ink'],
  ['Panel', 80, 176, 1464, 626, 80, 'paper'],
];
const patch = (figma, parent, s, x, y, bold = 0, pal = NORMAL) => {
  for (const [name, px, py, w, h, r, c] of PATCH) rrect(figma, parent, name, x + px * s, y + py * s, w * s, h * s, r * s, pal[c]);
  script(figma, parent, s, x, y, bold, pal.inkHex);
};

export function logoParche(figma) {
  const root = figma.createFrame(); root.name = 'Logo · Parche'; root.resize(1624, 968); root.fills = solid(GREEN);
  patch(figma, root, 1, 0, 0);
  return root;
}

// macOS app icon exploration: 1024 canvas, 824 body at 100 with r185 (Apple's macOS icon grid).
const BODY = 824, PAD = 100, R = 185;
const body = (figma, canvas, fill) => {
  const b = rrect(figma, canvas, 'Body', PAD, PAD, BODY, BODY, R, fill);
  b.cornerSmoothing = 0.6; b.clipsContent = true; b.effects = shadow(0.3, 10, 28);
  return b;
};

// A · Insignia: the squircle is the patch. Band = the body, line and panel follow its edge, script fills the panel.
function iconA(figma, canvas, pal) {
  const b = body(figma, canvas, pal.paper);
  const line = rrect(figma, b, 'Línea', 26, 26, BODY - 52, BODY - 52, R - 26, pal.ink); line.cornerSmoothing = 0.6;
  const panel = rrect(figma, b, 'Panel', 46, 46, BODY - 92, BODY - 92, R - 46, pal.paper); panel.cornerSmoothing = 0.6;
  // Panel spans source x 80..1544; map it onto the icon panel width, center vertically on the source panel.
  const s = (BODY - 92) / 1464;
  script(figma, b, s, 46 - 80 * s, BODY / 2 - 489 * s, 0, pal.inkHex);
}

// B · Cosido: the patch as an object. Stitched patch on a squircle in the ink color, tilted, with real shadow.
function iconB(figma, canvas, pal) {
  const b = body(figma, canvas, pal.ink);
  const s = 700 / 1594, w = 1594 * s, h = 746 * s;
  const holder = rrect(figma, b, 'Patch', (BODY - w) / 2 - 15 * s, (BODY - h) / 2 - 113 * s, 1624 * s, 968 * s, 0, null);
  holder.rotation = -6;
  patch(figma, holder, s, 0, 0, 0, pal);
  holder.children[0].effects = shadow(0.35, 14, 30); // Banda carries the shadow
  const st = rrect(figma, holder, 'Costura', 25 * s, 123 * s, 1574 * s, 726 * s, 136 * s, null);
  st.strokes = solid(pal.ink); st.strokeWeight = 3; st.strokeAlign = 'CENTER'; st.dashPattern = [14, 9];
  holder.insertChild(1, st);
}

// C · Rótulo: the word alone across the whole icon, no frame, tilted up on the diagonal so it has room to breathe.
const TILT = -12; // degrees; negative rises to the right in this renderer (same as B)
// D · Relieve: same word, soft relief instead of the hard shadow: a light rim on the top-left edges and a blurred
// shadow below, so the letters read as raised from the surface.
function iconC(figma, canvas, pal, relief = false) {
  const b = body(figma, canvas, pal.paper);
  // C: panel (source x 80..1544) slightly wider than the body, so only the weld stubs get clipped.
  // D: smaller so the whole word sits inside the icon; the soft shadow needs margin to read.
  const s = relief ? 0.5 : 0.59;
  const word = rrect(figma, b, 'Word', 0, 0, 1624 * s, 968 * s, 0, null);
  if (relief) {
    script(figma, word, s, -3, -3, 14, '#FFFFFF');
    const top = script(figma, word, s, 0, 0, 14, pal.inkHex);
    for (const v of top.children) v.effects = shadow(0.3, 8, 12); // tight, so letter edges stay crisp
  } else {
    // Hard drop shadow under the letters for depth, no outline.
    script(figma, word, s, 12, 12, 14, '#082D1A');
    script(figma, word, s, 0, 0, 14, pal.inkHex);
  }
  // OpenPencil rotates about the box center and x/y stay the unrotated top-left, so centering the box centers
  // the word (the source ink center 812,487 is ~the box center 812,484).
  word.rotation = TILT;
  word.x = (BODY - word.width) / 2; word.y = (BODY - word.height) / 2;
}

// Row 1: normal colors. Row 2: the same icons inverted.
export function logoMacIcon(figma) {
  const icons = [['A · Insignia', iconA], ['B · Cosido', iconB], ['C · Rótulo', iconC],
    ['D · Relieve', (f, c, p) => iconC(f, c, p, true)]];
  const root = figma.createFrame(); root.name = 'Logo · macOS'; root.resize(icons.length * 1104 - 80, 2 * 1024 + 80);
  root.fills = solid({ r: 0.91, g: 0.91, b: 0.91 });
  [[NORMAL, ''], [INVERSE, ' · Invertido']].forEach(([pal, suffix], row) => icons.forEach(([name, build], i) => {
    const canvas = rrect(figma, root, name + suffix, i * 1104, row * 1104, 1024, 1024, 0, null);
    build(figma, canvas, pal);
  }));
  return root;
}
