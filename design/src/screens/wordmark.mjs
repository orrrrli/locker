// Wordmark approach: block "LOCKER" in a rounded frame (geometry in design/src/logo/wordmark.mjs).
// Own page so each logo approach lives on its own sheet.
import { readFileSync } from 'node:fs';
import { createSVGNodes } from '@open-pencil/core';
import { wordmarkSVG, iconSVG } from '../logo/wordmark.mjs';

const GREEN = { r: 14 / 255, g: 70 / 255, b: 40 / 255 }; // #0E4628
const CREAM = { r: 241 / 255, g: 238 / 255, b: 221 / 255 }; // #F1EEDD, the wordmark ink
const solid = (c) => [{ type: 'SOLID', color: { ...c, a: 1 }, opacity: 1, visible: true }];
const svgNode = (figma, parent, svg, name) => figma.getNodeById(createSVGNodes(figma.graph, parent.id, svg, { name }).id);
const frame = (figma, parent, name, w, h, fill) => {
  const f = figma.createFrame(); f.name = name; f.resize(w, h); f.fills = fill ? solid(fill) : [];
  if (parent) parent.appendChild(f);
  return f;
};

export function logoWordmark(figma) {
  const root = frame(figma, null, 'Wordmark', 1600, 800, GREEN);
  svgNode(figma, root, wordmarkSVG(), 'LOCKER');
  return root;
}

// Content mockup: the wordmark (no background) over a match photo, to preview social/content posts.
// Photo 2000×1333; the word sits in the blurred upper band, clear of the ball.
const PHOTO = readFileSync(new URL('../content/balon.jpg', import.meta.url));
export function contenidoBalon(figma) {
  const root = frame(figma, null, 'Contenido · Balón', 2000, 1333, null);
  root.fills = [{ type: 'IMAGE', color: { r: 1, g: 1, b: 1, a: 1 }, imageHash: figma.createImage(new Uint8Array(PHOTO)).hash, imageScaleMode: 'FILL', visible: true, opacity: 1 }];
  const s = 0.75; // ink spans ~1536×384 of the 1600×800 canvas, centered on it
  const n = svgNode(figma, root, wordmarkSVG(undefined, undefined, { bota: true }), 'LOCKER');
  n.rescale(s); n.x = 1000 - 800 * s; n.y = 330 - 400 * s;
  return root;
}

// K bota (see K in logo/wordmark.mjs): the wordmark with the boot K.
export function wordmarkKBota(figma) {
  const root = frame(figma, null, 'Wordmark · K bota', 1600, 800, GREEN);
  svgNode(figma, root, wordmarkSVG(undefined, undefined, { bota: true }), 'LOCKER');
  return root;
}
// App icon on 1024 canvases masked with the home-screen squircle (preview only; iOS applies the mask), green and
// inverted, then the home-screen sizes 60/40/29pt at @3x px on a light and a dark wallpaper.
// Squircle with the mark inside; px rescales it after the mark is in (rescaling first leaves the mark out).
const squircle = (figma, parent, name, bg, ink, px, kind) => {
  const c = frame(figma, parent, name, 1024, 1024, bg); c.cornerRadius = 225; c.cornerSmoothing = 0.6; c.clipsContent = true;
  svgNode(figma, c, iconSVG(kind, ink), 'Marca');
  if (px) c.rescale(px / 1024);
  return c;
};
export function appIcon(figma, kind, title) {
  const root = frame(figma, null, title + ' · App icon', 2 * 1104 - 80, 1024 + 80 + 300, { r: 0.91, g: 0.91, b: 0.91 });
  [[GREEN, '#F1EEDD', ''], [CREAM, '#0E4628', ' · Invertido']].forEach(([bg, ink, suffix], i) => {
    squircle(figma, root, title + suffix, bg, ink, 0, kind).x = i * 1104;
  });
  [{ r: 0.9, g: 0.9, b: 0.92 }, { r: 0.11, g: 0.11, b: 0.12 }].forEach((wall, col) => {
    const w = frame(figma, root, col ? 'Oscuro' : 'Claro', 1024, 300, wall); w.x = col * 1104; w.y = 1024 + 80;
    let x = 60;
    for (const pt of [60, 40, 29]) {
      const c = squircle(figma, w, `${pt}pt`, GREEN, '#F1EEDD', pt * 3, kind); c.x = x; c.y = (300 - pt * 3) / 2;
      x += pt * 3 + 120;
    }
  });
  return root;
}

