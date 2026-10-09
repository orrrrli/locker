// Building blocks: helpers over the OpenPencil plugin API + Locker components.
// Every color is bound to a variable; every text uses a type token.
import { createSVGNodes } from '@open-pencil/core';
import { color, font, type, radius, size, device as dev } from './tokens.mjs';

// WCAG contrast of a token's Claro value against white and black: crest fills are the same in both modes.
const lum = ({ r, g, b }) => [r, g, b].map((x) => (x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4)).reduce((s, x, i) => s + x * [0.2126, 0.7152, 0.0722][i], 0);
export const crestInk = (fillTok) => { const L = lum(color[fillTok].claro); return 1.05 / (L + 0.05) >= (L + 0.05) / 0.05 ? 'onCrestLight' : 'onCrestDark'; };
// iOS medium activity indicator: 8 round-capped spokes in a 20pt box, clockwise from 12 o'clock.
// One SVG per spoke (filled capsule): SVG stroke caps are not imported, and multi-path fills merge into one vector.
const spokeSVG = (i) => {
  const a = (i * Math.PI) / 4, s = Math.sin(a), c = Math.cos(a), w = 1.1;
  const p = (r, side) => `${(10 + r * s + side * w * c).toFixed(2)} ${(10 - r * c + side * w * s).toFixed(2)}`;
  return `<svg viewBox="0 0 20 20" xmlns="http://www.w3.org/2000/svg"><path d="M${p(4.6, 1)}L${p(8.6, 1)}A${w} ${w} 0 0 0 ${p(8.6, -1)}L${p(4.6, -1)}A${w} ${w} 0 0 0 ${p(4.6, 1)}Z"/></svg>`;
};
const MONTHS = ['ene', 'feb', 'mar', 'abr', 'may', 'jun', 'jul', 'ago', 'sep', 'oct', 'nov', 'dic'];
export { pitch } from './tokens.mjs';

export function createKit(figma) {
  const g = figma.graph;

  // --- color variables (Claro / Oscuro) ---
  const col = g.createCollection('Color');
  const CLARO = col.modes[0].modeId;
  g.renameMode(col.id, CLARO, 'Claro');
  const OSCURO = 'oscuro';
  g.addMode(col.id, OSCURO, 'Oscuro', CLARO);
  const v = {};
  for (const [name, modes] of Object.entries(color)) {
    v[name] = g.createVariable(name, 'COLOR', col.id, modes.claro);
    v[name].valuesByMode[OSCURO] = modes.oscuro;
  }
  const modeOf = { claro: CLARO, oscuro: OSCURO };

  const solid = (c) => ({ type: 'SOLID', color: { r: c.r, g: c.g, b: c.b, a: 1 }, opacity: c.a ?? 1, visible: true });
  const paint = (node, token, field = 'fills') => {
    const c = color[token].claro;
    node[field] = [solid(c)];
    g.bindVariable(node.id, `${field}/0/color`, v[token].id);
    return node;
  };

  // --- layout ---
  // stack('V'|'H', { gap, pad:[t,r,b,l], align, justify, w, h, fill, r, name, clip })
  const stack = (dir, o = {}) => {
    const f = figma.createFrame();
    f.name = o.name ?? (dir === 'V' ? 'VStack' : 'HStack');
    f.layoutMode = dir === 'V' ? 'VERTICAL' : 'HORIZONTAL';
    f.itemSpacing = o.gap ?? 0;
    const [t, r, b, l] = o.pad ?? [0, 0, 0, 0];
    Object.assign(f, { paddingTop: t, paddingRight: r, paddingBottom: b, paddingLeft: l });
    f.primaryAxisAlignItems = o.justify ?? 'MIN';
    f.counterAxisAlignItems = o.align ?? 'MIN';
    f.primaryAxisSizingMode = (dir === 'V' ? o.h : o.w) === undefined ? 'AUTO' : 'FIXED';
    f.counterAxisSizingMode = (dir === 'V' ? o.w : o.h) === undefined ? 'AUTO' : 'FIXED';
    if (o.w !== undefined || o.h !== undefined) f.resize(o.w ?? f.width, o.h ?? f.height);
    f.fills = [];
    if (o.fill) paint(f, o.fill);
    if (o.r !== undefined) f.cornerRadius = o.r;
    f.clipsContent = !!o.clip;
    for (const k of o.children ?? []) f.appendChild(k);
    return f;
  };
  const grow = (n) => { n.layoutGrow = 1; return n; };
  const stretch = (n) => { n.layoutAlign = 'STRETCH'; return n; };

  // --- text ---
  const text = (str, tok, colorToken = 'label', name) => {
    const s = type[tok];
    const t = figma.createText();
    t.name = name ?? tok;
    t.fontName = { family: font[s.family], style: s.weight };
    t.characters = s.upper ? str.toUpperCase() : str;
    t.fontSize = s.size;
    t.lineHeight = s.lh; // OpenPencil stores px numbers, not Figma's { unit, value }
    t.letterSpacing = s.ls;
    t.textAutoResize = 'WIDTH_AND_HEIGHT';
    paint(t, colorToken);
    if (s.tnum) g.getNode(t.id).fontFeatures = [{ tag: 'tnum', enabled: true }];
    return t;
  };

  // --- icons (SVG, recolored through a token) ---
  const icon = (svg, token, name, fit) => {
    const box = figma.createFrame();
    box.name = name; box.fills = [];
    createSVGNodes(g, box.id, svg);
    for (const n of box.findAll(() => true)) {
      if (n.fills?.length) { n.fills = [solid(color[token].claro)]; g.bindVariable(n.id, 'fills/0/color', v[token].id); }
      if (n.strokes?.length) { n.strokes = n.strokes.map((s) => ({ ...s, color: { ...color[token].claro, a: 1 }, opacity: color[token].claro.a ?? 1 })); g.bindVariable(n.id, 'strokes/0/color', v[token].id); } // stroke alpha is not read from the variable
    }
    const vb = /viewBox="0 0 ([\d.]+) ([\d.]+)"/.exec(svg);
    box.resize(+vb[1], +vb[2]);
    if (fit) { const glyph = box.children[0]; glyph.resize(fit[2], fit[3]); glyph.x = fit[0]; glyph.y = fit[1]; }
    return box;
  };

  return { figma, g, v, modeOf, paint, stack, grow, stretch, text, icon, solid };
}

// ---------------------------------------------------------------- icons
export const SVG = {
  chevronLeft: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M13.5 4.5 L7 11 L13.5 17.5" fill="none" stroke="#000" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  ellipsis: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M4.5 9a2 2 0 1 0 0 4a2 2 0 1 0 0-4zM11 9a2 2 0 1 0 0 4a2 2 0 1 0 0-4zM17.5 9a2 2 0 1 0 0 4a2 2 0 1 0 0-4z"/></svg>',
  signal: '<svg viewBox="0 0 19 12" xmlns="http://www.w3.org/2000/svg"><path d="M1 8h1a1 1 0 0 1 1 1v2a1 1 0 0 1-1 1H1a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1zM6 5.5h1a1 1 0 0 1 1 1V11a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V6.5a1 1 0 0 1 1-1zM11 3h1a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-1a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1zM16 0h1a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1h-1a1 1 0 0 1-1-1V1a1 1 0 0 1 1-1z"/></svg>',
  // Fitted to the export's 17×12 wifi PNG: two round-capped arcs (outer ±49°, inner ±44.5°) and a dot, concentric at (8.5, 10.5).
  wifi: '<svg viewBox="0 0 17 12" xmlns="http://www.w3.org/2000/svg"><path d="M1.708 4.595A9 9 0 0 1 15.292 4.595A0.833 0.833 0 0 1 14.034 5.689A7.333 7.333 0 0 0 2.966 5.689A0.833 0.833 0 0 1 1.708 4.595ZM4.715 6.648A5.4 5.4 0 0 1 12.285 6.648A0.867 0.867 0 0 1 11.07 7.885A3.667 3.667 0 0 0 5.93 7.885A0.867 0.867 0 0 1 4.715 6.648ZM7 10.5a1.5 1.5 0 1 0 3 0a1.5 1.5 0 1 0 -3 0Z"/></svg>',
  batteryEdge: '<svg viewBox="0 0 27 13" xmlns="http://www.w3.org/2000/svg"><path d="M4.3 0.5h15.4a3.8 3.8 0 0 1 3.8 3.8v4.4a3.8 3.8 0 0 1-3.8 3.8H4.3A3.8 3.8 0 0 1 0.5 8.7V4.3A3.8 3.8 0 0 1 4.3 0.5z" fill="none" stroke="#000"/><path d="M24.5 4.5c1 0.2 2.2 0.9 2.2 2s-1.2 1.8-2.2 2z"/></svg>',
  sun: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M7.4 11a3.6 3.6 0 1 0 7.2 0a3.6 3.6 0 1 0 -7.2 0Z M11 1.3V4.8 M11 17.2V20.7 M1.3 11H4.8 M17.2 11H20.7 M4.15 4.15L6.6 6.6 M15.4 15.4L17.85 17.85 M4.15 17.85L6.6 15.4 M15.4 6.6L17.85 4.15" fill="none" stroke="#000" stroke-width="1.8" stroke-linecap="round"/></svg>',
  plus: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M11 3.6V18.4M3.6 11H18.4" fill="none" stroke="#000" stroke-width="2" stroke-linecap="round"/></svg>',
  link: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><g transform="translate(0.15 -0.5) translate(11 11) scale(1.09) rotate(-45) translate(-11 -11)"><path d="M10.2 8H6.1a3 3 0 0 0 0 6h4.1a3 3 0 0 0 2.6-1.5 M11.8 14h4.1a3 3 0 0 0 0-6h-4.1a3 3 0 0 0-2.6 1.5" fill="none" stroke="#000" stroke-width="1.9" stroke-linecap="round"/></g></svg>',
  chevronRight: '<svg viewBox="0 0 14 14" xmlns="http://www.w3.org/2000/svg"><path d="M5 2.8L9.3 7L5 11.2" fill="none" stroke="#000" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  bellSlash: '<svg viewBox="0 0 14 14" xmlns="http://www.w3.org/2000/svg"><path d="M3.8 10V6.4a3.2 3.2 0 0 1 6.4 0V10 M2.6 10.3H11.4 M5.9 12.1H8.1 M1.8 1.8L12.2 12.4" fill="none" stroke="#000" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  chevronDown: '<svg viewBox="0 0 12 12" xmlns="http://www.w3.org/2000/svg"><path d="M3 4.6L6 7.4L9 4.6" fill="none" stroke="#000" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  // Tab bar glyphs, 26pt.
  calendar: '<svg viewBox="0 0 26 26" xmlns="http://www.w3.org/2000/svg"><path d="M6.5 6.2H20.2a3 3 0 0 1 3 3V19.8a3 3 0 0 1-3 3H6.5a3 3 0 0 1-3-3V9.2a3 3 0 0 1 3-3Z M3.5 10.2H23.2 M9 1.9V6.8 M17.6 1.9V6.8" fill="none" stroke="#000" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  people: '<svg viewBox="0 0 26 26" xmlns="http://www.w3.org/2000/svg"><path d="M5.9 8a3.3 3.3 0 1 0 6.6 0a3.3 3.3 0 1 0 -6.6 0Z M16.1 9.3a2.9 2.9 0 1 0 5.8 0a2.9 2.9 0 1 0 -5.8 0Z M2.5 22.7a6.85 6.85 0 0 1 13.7 0 M19 16.6a4.8 5.9 0 0 1 4.8 6.1" fill="none" stroke="#000" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  bell: '<svg viewBox="0 0 26 26" xmlns="http://www.w3.org/2000/svg"><path d="M4 19.8H22 M6.8 19.8V11.5a6.2 6.2 0 0 1 12.4 0V19.8 M10.8 22.8H15.2" fill="none" stroke="#000" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  dollar: '<svg viewBox="0 0 26 26" xmlns="http://www.w3.org/2000/svg"><path d="M2.8 13a10.2 10.2 0 1 0 20.4 0a10.2 10.2 0 1 0 -20.4 0Z M13 6.5V19.5 M16.2 9.6c-0.4-1.2-1.6-2-3.2-2c-1.9 0-3.3 1-3.3 2.6c0 3.6 6.8 1.9 6.8 5.6c0 1.6-1.5 2.7-3.5 2.7c-1.7 0-3-0.9-3.4-2.2" fill="none" stroke="#000" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  share: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M11 2.6V13.6 M7.2 6.4L11 2.6L14.8 6.4 M4.6 10V18H17.4V10" fill="none" stroke="#000" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  lock: '<svg viewBox="0 0 13 13" xmlns="http://www.w3.org/2000/svg"><path d="M4.3 5.6V4a2.2 2.2 0 0 1 4.4 0V5.6 M3.9 5.6H9.1a1.3 1.3 0 0 1 1.3 1.3V10.2a1.3 1.3 0 0 1-1.3 1.3H3.9a1.3 1.3 0 0 1-1.3-1.3V6.9a1.3 1.3 0 0 1 1.3-1.3Z" fill="none" stroke="#000" stroke-width="1.1" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  search: '<svg viewBox="0 0 18 18" xmlns="http://www.w3.org/2000/svg"><path d="M2.9 8.2a5.3 5.3 0 1 0 10.6 0a5.3 5.3 0 1 0 -10.6 0Z M12.1 12.1L15.4 15.4" fill="none" stroke="#000" stroke-width="1.7" stroke-linecap="round"/></svg>',
  xmark: '<svg viewBox="0 0 16 16" xmlns="http://www.w3.org/2000/svg"><path d="M3.6 4.6L12.4 13.4M12.4 4.6L3.6 13.4" fill="none" stroke="#000" stroke-width="1.8" stroke-linecap="round"/></svg>',
  checkmark: '<svg viewBox="0 0 18 18" xmlns="http://www.w3.org/2000/svg"><path d="M3.4 9.6L7.1 13.1L14.8 4.9" fill="none" stroke="#000" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  star: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M11 2.6L13.5 7.9L19.2 8.6L15 12.5L16.1 18.2L11 15.4L5.9 18.2L7 12.5L2.8 8.6L8.5 7.9Z"/></svg>',
  key: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M12.4 6.6a3 3 0 1 0 6 0a3 3 0 1 0 -6 0Z M13.3 8.7L4 18 M6.2 15.8L8.2 17.8 M8.4 13.6L10.2 15.4" fill="none" stroke="#000" stroke-width="1.8" stroke-linecap="round"/></svg>',
  trash: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M2.6 4.6H19.4 M8.6 4.6V3.2H13.4V4.6 M5.5 4.6L6.4 18.8H15.6L16.5 4.6" fill="none" stroke="#000" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  compose: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M11.3 4.8H3.6V18H11.4 M8.2 13.4L8.9 10.5L16.1 3.3a1.5 1.5 0 0 1 2.2 2.2L11.1 12.7Z" fill="none" stroke="#000" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  check20: '<svg viewBox="0 0 20 20" xmlns="http://www.w3.org/2000/svg"><path d="M4 10.6L8 14.4L16.4 5.4" fill="none" stroke="#000" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  check16: '<svg viewBox="0 0 16 16" xmlns="http://www.w3.org/2000/svg"><path d="M3.2 8.5L6.4 11.5L12.9 4.5" fill="none" stroke="#000" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  // exclamationmark.triangle, outline.
  warning: '<svg viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M11 3 L19.7 18.9 H2.3 Z M11 9 V13.8 M11 17 V17.01" fill="none" stroke="#000" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  batteryLevel: '<svg viewBox="0 0 27 13" xmlns="http://www.w3.org/2000/svg"><path d="M4.2 2h15.6a2.2 2.2 0 0 1 2.2 2.2v4.6a2.2 2.2 0 0 1-2.2 2.2H4.2A2.2 2.2 0 0 1 2 8.8V4.2A2.2 2.2 0 0 1 4.2 2z"/></svg>',
};

// ---------------------------------------------------------------- components
// Each part is a function: screens call it with their content, the library page
// calls it once with sample content and turns the result into a component.
// (OpenPencil does not re-measure text overridden inside instances, so screens use fresh frames.)
export function createParts(k) {
  const { figma, stack, text, icon, paint, grow } = k;
  const shadow = () => [{ type: 'DROP_SHADOW', color: { r: 0, g: 0, b: 0, a: 0.08 }, offset: { x: 0, y: 1 }, radius: 4, spread: 0, visible: true, blendMode: 'NORMAL' }];
  // iOS inset separator: 0.5pt hairline on a row's bottom edge, outside the layout flow.
  // y = h - 0.75 renders centered where the export drew it.
  const hairline = (row, w, h) => {
    const sep = figma.createLine(); sep.name = 'separator';
    row.appendChild(sep); sep.layoutPositioning = 'ABSOLUTE';
    sep.resize(w, 0); sep.x = 0; sep.y = h - 0.75; sep.strokeWeight = 0.5; paint(sep, 'sep', 'strokes');
    return row;
  };
  // Rows inside a grouped card: the last one has no separator and is 0.5pt shorter.
  const rowH = (h, last) => (last ? h - 0.5 : h);
  // 44pt-tall touch target around a text node, text vertically centered (HIG minimum).
  const hit = (node, name = 'hitArea') => stack('H', { name, h: size.touch, align: 'CENTER', children: [node] });
  const box = (name, w, h, r, token) => { const f = figma.createFrame(); f.name = name; f.resize(w, h); f.cornerRadius = r; paint(f, token); return f; };

  const parts = {
    // iPhone body around a screen, for presentation only.
    device: (screen) => {
      const d = figma.createFrame();
      d.name = `iPhone · ${screen.name}`;
      d.resize(screen.width + dev.bezel * 2, screen.height + dev.bezel * 2);
      d.cornerRadius = dev.radius;
      d.fills = [k.solid(dev.body)];
      d.strokes = [{ type: 'SOLID', color: { ...dev.edge }, opacity: 1, visible: true }];
      d.strokeWeight = dev.edgeWidth; d.strokeAlign = 'INSIDE';
      d.effects = [{ type: 'DROP_SHADOW', color: dev.shadow.color, offset: { x: 0, y: dev.shadow.y }, radius: dev.shadow.blur, spread: 0, visible: true, blendMode: 'NORMAL' }];
      d.appendChild(screen);
      screen.x = dev.bezel; screen.y = dev.bezel; screen.cornerRadius = dev.screenRadius;
      return d;
    },
    // Status bar: 9:41 left, signal / wifi / battery right.
    // Status bar: fixed iOS geometry (positions measured from the export), not auto layout.
    statusBar: () => {
      const bar = figma.createFrame(); bar.name = 'StatusBar'; bar.resize(size.screenW, 54); bar.fills = [];
      const place = (n, x, y) => { bar.appendChild(n); n.x = x; n.y = y; return n; };
      place(stack('H', { name: 'time', children: [text('9:41', 'statusTime', 'label', 'time')] }), 54.8, 23.5); // hug frame so the text gets measured
      place(icon(SVG.signal, 'label', 'signal'), 282, 27.3);
      place(icon(SVG.wifi, 'label', 'wifi'), 308, 27.3); // same 17×12 box as the export's wifi image
      place(icon(SVG.batteryEdge, 'labelDim', 'battery-edge'), 332, 26.8);
      place(icon(SVG.batteryLevel, 'label', 'battery-level'), 332, 26.8);
      return bar;
    },
    dynamicIsland: () => box('DynamicIsland', 126, 37, 24, 'island'),
    homeIndicator: () => box('HomeIndicator', 139, 5, 100, 'homeBar'),

    // Screen: background, content nodes (absolute, in order), then OS chrome on top.
    screen: (name, mode, nodes, { bg = 'bg', chromeMode = mode } = {}) => {
      const s = figma.createFrame();
      s.name = `${name} · ${mode === 'claro' ? 'Claro' : 'Oscuro'}`;
      s.resize(size.screenW, size.screenH);
      paint(s, bg); s.clipsContent = true; s.cornerRadius = dev.screenRadius;
      for (const n of nodes) s.appendChild(n);
      const island = parts.dynamicIsland(); island.x = 133.5; island.y = 11;
      const home = parts.homeIndicator(); home.x = 127; home.y = 839;
      const chrome = [parts.statusBar(), island, home];
      for (const n of chrome) s.appendChild(n);
      const coll = [...k.g.variableCollections.keys()][0];
      k.g.getNode(s.id).variableModes = { [coll]: k.modeOf[mode] };
      if (chromeMode !== mode) for (const n of chrome) k.g.getNode(n.id).variableModes = { [coll]: k.modeOf[chromeMode] };
      return s;
    },
    // Large-title header: eyebrow over a 40pt title, 20pt side margins.
    // switcher: adds a chevron-down after the eyebrow (team switcher).
    // count: dimmed number after the title ("Jugaron 9").
    header: (eyebrow, title, { titleColor = 'label', switcher = false, count, countColor = 'label3' } = {}) => {
      const eb = text(switcher ? `${eyebrow} ` : eyebrow, 'eyebrow', 'label2', 'eyebrow'); // the export keeps a trailing space before the chevron
      const top = switcher ? stack('H', { name: 'switcher', gap: 7, align: 'CENTER', children: [eb, icon(SVG.chevronDown, 'label2', 'chevron')] }) : eb;
      const t = text(title, 'hero', titleColor, 'title');
      const titleRow = count === undefined ? t : stack('H', { name: 'titleRow', gap: 10, children: [t, text(String(count), 'hero', countColor, 'count')] });
      return stack('V', { name: 'header', w: size.screenW, pad: [0, 20, 1.5, 20], children: [top, titleRow] });
    }, // the export's header box is 61.5 tall

    // Back button: glass pill, chevron + previous screen title.
    backButton: (label = 'Atrás') => {
      const b = stack('H', { name: 'BackButton', h: size.touch, gap: 2, pad: [0, 13.2, 0, 8], align: 'CENTER', fill: 'glass', r: 22,
        children: [icon(SVG.chevronLeft, 'accent', 'chevron'), text(label, 'navLabel', 'accent', 'label')] });
      b.effects = shadow(); return b;
    },
    // Round glass icon button (••• menu, icon-only back).
    iconButton: (glyph = 'ellipsis') => {
      const b = stack('H', { name: 'IconButton', w: size.touch, h: size.touch, justify: 'CENTER', align: 'CENTER', fill: 'glass', r: 22,
        children: [icon(SVG[glyph], 'accent', glyph)] });
      b.effects = shadow(); return b;
    },
    // Full-width secondary button.
    buttonSecondary: (label = 'Acción') => stack('H', { name: 'ButtonSecondary', w: 361, h: 48, justify: 'CENTER', align: 'CENTER', fill: 'fill', r: radius.button,
      children: [text(label, 'bodyStrong', 'label', 'label')] }),
    // Large 52pt pill button. variant: primary | disabled | apple | onHero
    // variant: primary | disabled | secondary | selected (inverted) | apple | onHero
    // loading: same fill and size, a spinner in the variant's ink replaces the label.
    buttonLarge: (label = 'Acción', variant = 'primary', w = 361, h = size.buttonLarge, { loading = false } = {}) => {
      const [fill, ink, tok] = { primary: ['accent', 'onAccent', 'bodyStrong'], disabled: ['fill', 'label3', 'bodyStrong'],
        secondary: ['fill', 'label', 'bodyStrong'], selected: ['label', 'bg', 'bodyStrong'],
        apple: ['appleBg', 'appleLabel', 'appleLabel'], onHero: ['heroFill', 'hero', 'bodyStrong'] }[variant];
      if (loading) return stack('H', { name: `Button/${variant}/loading`, w, h, justify: 'CENTER', align: 'CENTER', fill, r: h / 2, children: [parts.spinner(ink)] });
      const kids = [text(label, tok, ink, 'label')];
      if (variant === 'apple') kids.unshift(stack('H', { name: 'logo', w: 22, h: 22, justify: 'CENTER', align: 'CENTER', children: [text('\uF8FF', tok, ink, 'glyph')] })); // Apple logo glyph in SF Pro, in the export's 22pt slot
      return stack('H', { name: `Button/${variant}`, w, h, gap: 6, justify: 'CENTER', align: 'CENTER', fill, r: h / 2, children: kids });
    },
    // iOS activity indicator (medium, 20pt): the top spoke leads, the ones behind it (counterclockwise) fade.
    spinner: (ink = 'label2') => {
      const f = figma.createFrame(); f.name = 'Spinner'; f.fills = []; f.resize(20, 20);
      for (let i = 0; i < 8; i++) { const sp = icon(spokeSVG(i), ink, `spoke${i}`); f.appendChild(sp); sp.x = 0; sp.y = 0; sp.opacity = 1 - ((8 - i) % 8) * 0.1; }
      return f;
    },
    // Text link in a 44pt hit frame, in the caller's type style. Accent on bg/bg2;
    // onHero: underlined, in the hero text color (green pitch background).
    link: (label, tok = 'link', { onHero = false } = {}) => {
      const t = text(label, tok, onHero ? 'hero' : 'accent', 'label');
      if (onHero) t.textDecoration = 'UNDERLINE';
      return hit(t, onHero ? 'Link/onHero' : 'Link');
    },
    // Form field row inside a grouped card: label over value, optional trailing node.
    // last: no separator and 67.5 tall (export card rhythm). error: label turns red.
    // valueTok: type of the value; multiline: wrapping 17/23 body in a tall row (h) with the caret after the text.
    // type 'date': compact iOS date control, label leading and the value in a trailing gray pill (accent; label2 when placeholder, label when error).
    // dim: the value fades to 0.4 (form locked while a request is in flight).
    field: ({ label = 'Campo', value = 'Valor', valueTok, secure = false, caret = false, trailing, last = false, error = false, placeholder = false, multiline = false, h: fixedH, type: kind, dim = false } = {}) => {
      if (kind === 'date') {
        const h = rowH(size.field, last);
        const pill = stack('H', { name: 'datePill', h: 34, pad: [0, 11, 0, 11], align: 'CENTER', fill: 'fill', r: 8, children: [text(value, 'body', placeholder ? 'label2' : error ? 'label' : 'accent', 'value')] });
        const row = stack('H', { name: 'Field/date', w: 341, h, gap: 8, pad: [0, 20, 0, 0], align: 'CENTER',
          children: [grow(text(label, 'body', error ? 'red' : 'label', 'label')), pill, ...(trailing ? [trailing] : [])] });
        return last ? row : hairline(row, 341, h);
      }
      if (multiline) {
        const body = text(value, 'lead', 'label', 'value'); body.textAutoResize = 'HEIGHT'; body.resize(321, body.height);
        return stack('V', { name: 'Field/multiline', w: 341, h: fixedH ?? 174, gap: 5, pad: [12, 20, 0, 0], children: [text(label, 'eyebrow', 'label2', 'label'), body] });
      }
      const h = rowH(size.field, last);
      const valueRow = stack('H', { name: 'value', h: 26, gap: 0, align: 'CENTER', pad: [secure ? 2 : 0, 0, 0, 0], /* dots sit 1pt lower */ children: [text(value, valueTok ?? (secure ? 'secure' : 'fieldValue'), placeholder ? 'label3' : 'label', 'value')] });
      if (dim) valueRow.opacity = 0.4;
      if (caret) { const cr = figma.createFrame(); cr.name = 'caret'; cr.resize(2, 22); paint(cr, 'accent'); valueRow.appendChild(cr); valueRow.itemSpacing = -14.5; } // ls 2 leaves trailing space after the dots
      const col = grow(stack('V', { name: 'text', gap: 2, children: [text(label, 'eyebrow', error ? 'red' : 'label2', 'label'), valueRow] }));
      const row = stack('H', { name: 'Field', w: 341, h, pad: [0, 20, 0, 0], align: 'CENTER', children: [col, ...(trailing ? [trailing] : [])] });
      return last ? row : hairline(row, 341, h);
    },
    // Grouped card (inset list): rows get last = true on the final one.
    card: (rows, name = 'Card') => stack('V', { name, w: 361, pad: [0, 0, 0, 20], fill: 'bg2', r: radius.card, clip: true, children: rows }),
    // picker: an open wheel date picker ({ day, month, year }) as the card's last row, under a separator (native inline DatePicker).
    formCard: (fields, { picker } = {}) => parts.card([...fields.map((f, i) => parts.field({ ...f, last: !picker && i === fields.length - 1 })),
      ...(picker ? [parts.datePicker({ ...picker, inCard: true })] : [])], 'FormCard'),
    // 48pt team badge: initials on a colored disc.
    // Crest fills get the per-fill crest ink unless the caller passes one.
    badge: (initials = 'AA', fillTok = 'accent', ink = fillTok.startsWith('crest') ? crestInk(fillTok) : 'onAccent') => stack('H', { name: 'Badge', w: size.badge, h: size.badge, justify: 'CENTER', align: 'CENTER', fill: fillTok, r: size.badge / 2,
      children: [text(initials, 'badge', ink, 'initials')] }),
    // Team row: badge, name over a detail line, optional trailing (chevron).
    teamRow: ({ badge, title = 'Equipo', detail = 'Detalle', titleColor = 'label', detailNode, chevron = true, last = false } = {}) => {
      const h = rowH(size.teamRow, last);
      const col = grow(stack('V', { name: 'text', gap: 2, children: [text(title, 'rowTitle', titleColor, 'title'), detailNode ?? text(detail, 'link', 'label2', 'detail')] }));
      const row = stack('H', { name: 'TeamRow', w: 341, h, gap: 14, pad: [0, 20, 0, 0], align: 'CENTER',
        children: [badge ?? parts.badge(), col, ...(chevron ? [stack('V', { name: 'chevronSlot', pad: [1.5, 0, 0, 0], children: [icon(SVG.chevronRight, 'label3', 'chevron')] })] : [])] }); // the export chevron sits 0.75 below center
      return last ? row : hairline(row, 341, h);
    },
    // Action row: 22pt glyph + label.
    actionRow: ({ glyph = 'plus', label = 'Acción', last = false } = {}) => {
      const h = rowH(size.actionRow, last);
      const row = stack('H', { name: 'ActionRow', w: 341, h, gap: 12, pad: [0, 20, 0, 0], align: 'CENTER',
        children: [icon(SVG[glyph], 'label2', glyph), text(label, 'rowLabel', 'label', 'label')] });
      return last ? row : hairline(row, 341, h);
    },
    // Two-column row: title over subtitle on the left, trailing column (right-aligned) or single value.
    infoRow: ({ title = 'Título', titleTok = 'rowStrong', subtitle = 'Detalle', subtitleColor = 'label2', right = [], rightColor = 'label', rightTok = 'body', rightTop = 11.25, trailing, trailingTop = 10, last = false } = {}) => {
      const h = rowH(72.5, last);
      // Second line sits 1pt lower than a plain 22pt stack (export rhythm).
      const line2 = (n) => stack('V', { name: 'line2', pad: [1, 0, 0, 0], children: [n] });
      const left = stack('V', { name: 'left', children: [text(title, titleTok, 'label', 'title'), line2(text(subtitle, 'callout', subtitleColor, 'subtitle'))] });
      // Two-line trailing column starts at a fixed x (export grid) and right-aligns its lines to the date; a single value right-aligns to the edge.
      if (right.length > 1) { left.counterAxisSizingMode = 'FIXED'; left.resize(231.02, left.height); } else grow(left);
      const trail = stack('V', { name: 'right', align: 'MAX', children: right.map((s, i) => (i ? line2(text(s, 'callout', 'label2', 'sub')) : text(s, rightTok, rightColor, 'value'))) });
      if (right.length === 1) trail.paddingTop = rightTop; // single value centers on the 72.5 row
      if (trailing) { trail.appendChild(trailing); trail.paddingTop = trailingTop; } // trailing line box starts 10pt below the row's top padding
      const row = stack('H', { name: 'InfoRow', w: 341, h, gap: 12, pad: [15, 20, 0, 0], align: 'MIN', children: [left, trail] });
      return last ? row : hairline(row, 341, h);
    },
    // Floating glass tab bar. badges: { Avisos: 2 }
    tabBar: (selected = 'Partidos', badges = {}) => {
      const TABS = [['Partidos', 'calendar'], ['Plantilla', 'people'], ['Avisos', 'bell'], ['Cuotas', 'dollar']];
      const bar = stack('H', { name: 'TabBar', w: 365, h: 66, gap: 2, pad: [5, 5, 5, 5], fill: 'glass', r: 33,
        children: TABS.map(([label, glyph]) => {
          const on = label === selected;
          const ic = icon(SVG[glyph], on ? 'accent' : 'label2', glyph);
          let glyphNode = ic;
          if (badges[label]) {
            glyphNode = figma.createFrame(); glyphNode.name = 'glyph'; glyphNode.fills = []; glyphNode.resize(26, 26); glyphNode.clipsContent = false;
            const b = stack('H', { name: 'badge', w: 18, h: 18, justify: 'CENTER', align: 'CENTER', fill: 'red', r: 9, children: [text(String(badges[label]), 'tabBadge', 'onRed', 'count')] });
            glyphNode.appendChild(ic); glyphNode.appendChild(b); b.x = 17; b.y = -4;
          }
          return stack('V', { name: `Tab/${label}`, w: 87.25, h: 56, gap: 3, pad: [7.5, 0, 0, 0], align: 'CENTER', r: 28, fill: on ? 'tabSelected' : undefined,
            children: [glyphNode, text(label, 'tabLabel', on ? 'accent' : 'label2', 'label')] });
        }) });
      bar.effects = [{ type: 'DROP_SHADOW', color: { r: 0, g: 0, b: 0, a: 0.1 }, offset: { x: 0, y: 2 }, radius: 12, spread: 0, visible: true, blendMode: 'NORMAL' }];
      return bar;
    },
    // Big time + caption row. OpenPencil draws a 56/56 line 5.33pt lower than the browser
    // (no negative half-leading), so the clock floats in a fixed 56pt row; the caption keeps the export's x.
    clockRow: (time, caption, { timeColor = 'label', captionTok = 'clockCaption', captionPad = 3, captionX = 124.17 } = {}) => {
      const row = stack('H', { name: 'when', w: 353, h: 56, align: 'MAX', pad: [0, 0, captionPad, captionX], children: [text(caption, captionTok, 'label2', 'caption')] });
      const clock = text(time, 'clock', timeColor, 'time'); row.appendChild(clock);
      clock.layoutPositioning = 'ABSOLUTE'; clock.x = 0; clock.y = -5.33;
      return row;
    },
    // Overlapping 34pt avatars (left one on top), with a trailing "+N" disc.
    avatarPile: (initials, more, { fill = 'accent', ink = 'onAccent', d = 34, step = 25 } = {}) => {
      if (fill === 'fill') fill = 'fillOpaque'; // overlapping discs must be opaque, or each overlap darkens
      const pile = figma.createFrame(); pile.name = 'AvatarPile'; pile.fills = []; pile.resize(initials.length * step + d, d);
      const at = (n, x) => { pile.appendChild(n); n.x = x; n.y = 0; };
      at(parts.avatar(more, false, { fill: 'bg2', ink: 'label', ring: 'bg', d }), initials.length * step);
      [...initials].reverse().forEach((ini, i) => at(parts.avatar(ini, true, { fill, ink, ring: 'bg', d }), (initials.length - 1 - i) * step));
      return pile;
    },
    // Footnote under a card or control: 36pt margins, starts 11pt below, fixed slot height.
    footnote: (str, { h = 50, tok = 'footnote', center = false, top = 11 } = {}) => {
      const t = text(str, tok, 'label2', 'note'); t.textAutoResize = 'HEIGHT'; t.resize(321, t.height);
      if (center) t.textAlignHorizontal = 'CENTER';
      return stack('V', { name: 'footnote', w: size.screenW, h, pad: [top, 36, 0, 36], children: [t] });
    },
    // Person chip: small avatar + first name. Without initials it is a plain "y N más" pill.
    chip: (name, initials) => stack('H', { name: 'Chip', h: 36, gap: 8, pad: initials ? [0, 11.67, 0, 3] : [0, 14, 0, 14], align: 'CENTER', fill: 'bg2', r: 18,
      children: [...(initials ? [stack('H', { name: 'avatar', w: 30, h: 30, justify: 'CENTER', align: 'CENTER', fill: 'accent', r: 15, children: [text(initials, 'chipInitials', 'onAccent', 'initials')] })] : []),
        text(name, 'chipLabel', initials ? 'label' : 'label2', 'name')] }),
    // Text pill in the nav bar: glass (Cancelar) or accent-filled (Guardar).
    // strong: glass pill with a semibold label ("Listo").
    navPill: (label, primary = false, strong = false) => {
      const b = stack('H', { name: primary ? 'NavPill/primary' : 'NavPill', h: size.touch, pad: primary ? [0, 18, 0, 18] : [0, 16, 0, 16], align: 'CENTER', r: 22, fill: primary ? 'accent' : 'glass',
        children: [text(label, primary ? 'bodyStrong' : strong ? 'navStrong' : 'navLabel', primary ? 'onAccent' : 'accent', 'label')] });
      if (!primary) b.effects = shadow();
      return b;
    },
    // Person row (64.5): avatar, name over detail, trailing node. captain adds the bold "C" after the name.
    // detailSuffix: second run on the detail line (" · Admin"). Rows without separator are 64 (last in card).
    personRow: ({ avatar, name = 'Nombre', nameColor = 'label', detail = 'Detalle', detailSuffix, captain = false, trailing, separator = true, kind = 'PersonRow', h: rowHeight = 64.5, nudge = 2 } = {}) => {
      const h = separator ? rowHeight : rowHeight - 0.5;
      const nameNode = text(name, 'body', nameColor, 'name');
      const nameLine = captain ? stack('H', { name: 'nameLine', h: 22, gap: 13.67, children: [nameNode, stack('V', { name: 'captainSlot', pad: [3.67, 0, 0, 0], children: [text('C', 'captain', 'label', 'captain')] })] }) : nameNode;
      const detailLine = detailSuffix ? stack('H', { name: 'detail', children: [text(detail, 'callout', 'label2', 'detail'), text(detailSuffix, 'callout', 'label2', 'suffix')] }) : detail === null ? null : text(detail, 'callout', 'label2', 'detail');
      const col = grow(stack('V', { name: 'text', pad: [nudge, 0, 0, 0], /* nudge 2: text sits 1pt below center; avatar stays centered */ children: [nameLine, ...(detail === null ? [] : [stack('V', { name: 'line2', pad: [1, 0, 0, 0], children: [detailLine] })])] }));
      const row = stack('H', { name: kind, w: 341, h, gap: 14, pad: [0, 20, 0, 0], align: 'CENTER',
        children: [avatar ?? parts.avatar(), col, ...(trailing ? [trailing] : [])] });
      return separator ? hairline(row, 341, h) : row;
    },
    // Dimmed jersey number on the right; tabular digits like the export.
    // lift: points to raise the number (big jersey digits sit higher than the row's text).
    number: (n, tok = 'body', lift = 0) => {
      const t = text(String(n), tok, 'label3', 'number'); k.g.getNode(t.id).fontFeatures = [{ tag: 'tnum', enabled: true }];
      return lift ? stack('V', { name: 'numberSlot', pad: [0, 0, lift * 2, 0], children: [t] }) : t;
    },
    // Attendance row: absent players are dimmed and lose the avatar fill.
    attendeeRow: ({ initials = 'AA', name = 'Nombre', role = 'Posición', number = 1, present = true } = {}) => parts.personRow({
      avatar: parts.avatar(initials, present, { ink: present ? 'onAccent' : 'label3' }), name, nameColor: present ? 'label' : 'label3', detail: role,
      trailing: parts.number(number), kind: present ? 'Attendee' : 'Attendee/absent' }),
    // Round 36pt action button inside a row (reject / accept).
    roundAction: (glyph, primary = false) => stack('H', { name: `RoundAction/${glyph}`, w: 36, h: 36, justify: 'CENTER', align: 'CENTER', r: 18, fill: primary ? 'accent' : 'fill',
      children: [icon(SVG[glyph], primary ? 'onAccent' : 'label2', glyph)] }),
    // Search field (38pt pill).
    searchField: (placeholder = 'Buscar') => stack('H', { name: 'Search', w: 361, h: 38, gap: 6, pad: [0, 12, 0, 12], align: 'CENTER', fill: 'fill', r: 19,
      children: [icon(SVG.search, 'label2', 'search'), text(placeholder, 'rowLabel', 'label2', 'placeholder')] }),
    // Swipe action tile (84x64): glyph over a 12pt label, white on a colored fill.
    swipeAction: (label, glyph, fillTok) => stack('V', { name: `Swipe/${label}`, w: 84, h: 64, gap: 3, pad: [12.25, 0, 0, 0], align: 'CENTER', fill: fillTok,
      children: [icon(SVG[glyph], 'onRed', glyph), text(label, 'swipeLabel', 'onRed', 'label')] }),
    // Notice row (inbox): avatar, title + time, two-line body. Unread: bold title, accent avatar, dark body.
    noticeRow: ({ initials = 'AA', title = 'Aviso', time = 'hoy', body = 'Mensaje', unread = true, last = false } = {}) => {
      const h = rowH(92.5, last);
      const b = text(body, 'footnote', unread ? 'label' : 'label2', 'body'); b.textAutoResize = 'HEIGHT'; b.resize(273, b.height);
      const titleRow = stack('H', { name: 'titleRow', w: 273, gap: 8, children: [grow(text(title, unread ? 'noticeTitle' : 'noticeTitleRead', 'label', 'title')), stack('V', { name: 'timeSlot', pad: [1, 0, 0, 0], children: [text(time, 'noticeTime', 'label2', 'time')] })] });
      const row = stack('H', { name: unread ? 'Notice/unread' : 'Notice', w: 341, h, gap: 14, pad: [14, 20, 0, 0],
        children: [parts.avatar(initials, unread, unread ? {} : { fill: 'fill', ink: 'label2' }), stack('V', { name: 'text', gap: 1, pad: [1, 0, 0, 0], /* text 1pt below the avatar top */ children: [titleRow, b] })] });
      return last ? row : hairline(row, 341, h);
    },
    // Sheet grabber (36x5).
    grabber: () => box('Grabber', 36, 5, 3, 'label3'),
    // Compact accent pill (48pt), e.g. an empty-state call to action.
    pillButton: (label) => stack('H', { name: 'PillButton', h: 48, pad: [0, 21.6, 0, 21.6], align: 'CENTER', fill: 'accent', r: 24, children: [text(label, 'bodyStrong', 'onAccent', 'label')] }),
    // Option row (48.5): label, optional value text, trailing node (count, check, chevron).
    // subtitle: second line under the label (row grows to 66.5). h overrides the base height.
    optionRow: ({ label = 'Opción', labelColor = 'label', subtitle, value, trailing, last = false, h: baseH } = {}) => {
      const h = rowH(baseH ?? (subtitle ? 66.5 : 48.5), last);
      const lead = subtitle ? stack('V', { name: 'text', children: [text(label, 'rowLabel', labelColor, 'label'), text(subtitle, 'link', 'label2', 'subtitle')] }) : text(label, 'rowLabel', labelColor, 'label');
      const row = stack('H', { name: 'OptionRow', w: 341, h, gap: 8, pad: [0, 20, 0, 0], align: 'CENTER',
        children: [grow(lead), ...(value ? [text(value, 'rowLabel', 'label', 'value')] : []), ...(trailing ? [trailing] : [])] });
      if (value && trailing) row.itemSpacing = 12;
      return last ? row : hairline(row, 341, h);
    },
    // iOS switch (51x31).
    toggle: (on = false) => {
      const t = figma.createFrame(); t.name = on ? 'Switch/on' : 'Switch/off'; t.resize(51, 31); t.cornerRadius = 16; paint(t, on ? 'accent' : 'fill');
      const knob = box('knob', 27, 27, 14, 'knob'); knob.effects = [{ type: 'DROP_SHADOW', color: { r: 0, g: 0, b: 0, a: 0.15 }, offset: { x: 0, y: 3 }, radius: 8, spread: 0, visible: true, blendMode: 'NORMAL' }];
      t.appendChild(knob); knob.x = on ? 22 : 2; knob.y = 2;
      return t;
    },
    // Profile header: 56pt avatar, name, email.
    profile: (initials, name, email) => stack('H', { name: 'Profile', w: size.screenW, gap: 14, pad: [0, 20, 0, 20], children: [
      stack('H', { name: 'avatar', w: 56, h: 56, justify: 'CENTER', align: 'CENTER', fill: 'accent', r: 28, children: [text(initials, 'profileInitials', 'onAccent', 'initials')] }),
      stack('V', { name: 'text', pad: [2, 0, 0, 0], children: [text(name, 'profileName', 'label', 'name'), stack('V', { name: 'emailSlot', pad: [1, 0, 0, 0], children: [text(email, 'link', 'label2', 'email')] })] }),
    ] }),
    // Section label above a grouped card (eyebrow style, 20pt from the screen edge).
    sectionLabel: (str) => stack('V', { name: 'sectionLabel', h: 15.5, pad: [0, 0, 0, 20], children: /* OpenPencil measures the 15.5 line as 16 */ [text(str, 'eyebrow', 'label2', 'label')] }),
    // Team crest: colored initials disc with a pitch-colored ring (overlaps its neighbour).
    crest: (initials = 'AA', token = 'crest1') => {
      const b = stack('H', { name: 'Crest', w: size.crest, h: size.crest, justify: 'CENTER', align: 'CENTER', fill: token, r: size.crest / 2,
        children: [text(initials, 'crest', crestInk(token), 'initials')] });
      paint(b, 'heroRing', 'strokes'); b.strokeWeight = 2; b.strokeAlign = 'OUTSIDE';
      return b;
    },
    // Inline iOS wheel date picker (open state): day / month / year wheels, selection band behind the middle row.
    // month is 1-12. Values around the selection are layout data, not product copy.
    // inCard: a bare row for formCard (341 wide after the card's 20pt inset; right pad 20 keeps the wheels centered on the card).
    datePicker: ({ day = 4, month = 10, year = 2013, inCard = false } = {}) => {
      const ROW = 34, H = 216, ink = ['label3', 'label2', 'label', 'label2', 'label3'];
      const wheel = (name, w, at) => stack('V', { name, w, h: H, justify: 'CENTER', align: 'CENTER', children: [-2, -1, 0, 1, 2].map((d, i) =>
        stack('H', { name: 'row', w, h: ROW, justify: 'CENTER', align: 'CENTER', children: [text(at(d), 'title3', ink[i], 'value')] })) });
      const wrap = (n, m) => ((n - 1 + m) % m) + 1;
      const p = stack('H', { name: 'DatePicker', w: inCard ? 341 : 361, h: H, justify: 'CENTER', ...(inCard ? { pad: [0, 20, 0, 0] } : { fill: 'bg2', r: radius.card, clip: true }), children: [
        wheel('day', 70, (d) => String(wrap(day + d, 31))), wheel('month', 110, (d) => MONTHS[wrap(month + d, 12) - 1]), wheel('year', 90, (d) => String(year + d))] });
      const band = box('selection', 345, ROW, 8, 'fill');
      p.insertChild(0, band); band.layoutPositioning = 'ABSOLUTE'; band.x = inCard ? -12 : 8; band.y = (H - ROW) / 2;
      return p;
    },
    // Small pill button used inside rows.
    buttonChip: (label = 'Acción') => stack('H', { name: 'ButtonChip', h: 32, pad: [1.5, 13.58, 0, 13.58], justify: 'CENTER', align: 'CENTER', fill: 'fill', r: radius.chip,
      children: [text(label, 'calloutStrong', 'label', 'label')] }),
    // Avatar: accent-filled when paid, bare initials when pending.
    // ring: background-colored outline so overlapping avatars stay separated.
    avatar: (initials = 'AA', filled = true, { fill, ink, ring, d = size.avatar } = {}) => {
      const a = stack('H', { name: 'Avatar', w: d, h: d, justify: 'CENTER', align: 'CENTER', r: d / 2, fill: fill ?? (filled ? 'accent' : undefined),
        children: [text(initials, d < 34 ? 'chipInitials' : 'avatar', ink ?? (filled ? 'onAccent' : 'label2'), 'initials')] });
      if (ring) { paint(a, ring, 'strokes'); a.strokeWeight = 2.5; a.strokeAlign = 'OUTSIDE'; }
      return a;
    },
    // Member row inside a grouped card: avatar, name, trailing; 0.5 separator under it.
    memberRow: ({ initials = 'AA', name = 'Nombre Apellido', paid = true } = {}) => {
      const trailing = paid ? stack('V', { name: 'statusSlot', pad: [2, 0, 0, 0], children: [text('Pagado', 'callout', 'label2', 'status')] }) : parts.buttonChip('Marcar pagado'); // the export status sits 1pt low
      const row = stack('H', { name: paid ? 'MemberRow/Pagado' : 'MemberRow/Pendiente', w: 341, h: size.row, gap: 14, pad: [0, 20, 0, 0], align: 'CENTER',
        children: [stack('V', { name: 'avatarSlot', h: size.row, pad: [8.83, 0, 0, 0], children: [parts.avatar(initials, paid)] }), grow(text(name, 'body', 'label', 'name')), trailing] });
      return hairline(row, 341, size.row);
    },
  };

  // Library page: one sample of each part, as a real component.
  const library = () => {
    const samples = {
      'System/StatusBar': parts.statusBar(), 'System/DynamicIsland': parts.dynamicIsland(), 'System/HomeIndicator': parts.homeIndicator(),
      'Nav/BackButton': parts.backButton(), 'Nav/IconButton': parts.iconButton(),
      'Button/Secondary': parts.buttonSecondary(), 'Button/Chip': parts.buttonChip(),
      'Button/Primary': parts.buttonLarge('Iniciar sesión'), 'Button/Disabled': parts.buttonLarge('Continuar', 'disabled'),
      'Nav/TabBar': parts.tabBar('Partidos', { Avisos: 2 }), 'List/InfoRow': parts.infoRow({ right: ['dom 19 oct', '10:30'] }),
      'List/Chip': parts.chip('Luis', 'LH'), 'List/AvatarPile': parts.avatarPile(['LH', 'SC', 'AS'], '+7'),
      'Nav/Pill': parts.navPill('Cancelar'), 'Nav/PillPrimary': parts.navPill('Guardar', true), 'List/Attendee': parts.attendeeRow(), 'Form/Search': parts.searchField(), 'List/Notice': parts.noticeRow(), 'Button/Pill': parts.pillButton('Nuevo aviso'), 'Sheet/Grabber': parts.grabber(), 'Control/SwitchOn': parts.toggle(true), 'Control/SwitchOff': parts.toggle(false), 'List/OptionRow': parts.optionRow({ trailing: icon(SVG.check20, 'accent', 'check') }), 'List/RoundAction': parts.roundAction('checkmark', true),
      'List/TeamRow': parts.teamRow(), 'List/ActionRow': parts.actionRow(), 'List/Badge': parts.badge(),
      'Form/Field': parts.field(), 'Form/Card': parts.formCard([{ label: 'Correo', value: 'nombre@correo.com' }, { label: 'Contraseña', value: '••••••••••', secure: true }]),
      'List/Avatar': parts.avatar(), 'List/MemberRow/Pagado': parts.memberRow(), 'List/MemberRow/Pendiente': parts.memberRow({ paid: false }),
      'Button/Link': parts.link('¿La olvidaste?'), 'Button/Loading': parts.buttonLarge('Iniciar sesión', 'primary', 361, size.buttonLarge, { loading: true }),
      'Form/DateCard': parts.formCard([{ label: 'Fecha de nacimiento', value: '4 oct 2013', type: 'date' }]), 'Form/DatePicker': parts.datePicker(),
    };
    return Object.entries(samples).map(([name, frame]) => { const c = figma.createComponentFromNode(frame); c.name = name; return c; });
  };

  return { ...parts, hit, library };
}
