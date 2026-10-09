// R7–R8 — Plantilla: roster with join requests and a swiped row (admin); read-only roster (player).
import { size } from '../tokens.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };

// [initials, name, position, number, extra]; extra: 'admin' | 'captain' | 'me'
const ROSTER = [
  ['LH', 'Luis Hernández', 'Portero', 1], ['SC', 'Santiago Cruz', 'Defensa', 2], ['EV', 'Emilio Vargas', 'Defensa', 3],
  ['AS', 'Andrés Soto', 'Defensa', 4, 'admin'], ['MT', 'Miguel Torres', 'Defensa', 5], ['FR', 'Fernando Ruiz', 'Medio', 6],
  ['JC', 'Jorge Castillo', 'Medio', 7], ['CM', 'Carlos Mendoza', 'Medio', 8, 'captain'], ['PD', 'Pablo Díaz', 'Delantero', 9],
  ['DR', 'Diego Ramírez', 'Delantero', 10, 'me'], ['RF', 'Ricardo Flores', 'Delantero', 11], ['ÓP', 'Óscar Peña', 'Defensa', 14],
];

const playerRow = (c, [initials, name, position, number, extra], { meHighlight = false, separator = true } = {}) => {
  const me = extra === 'me' && meHighlight;
  return c.personRow({
    avatar: c.avatar(initials, me, me ? {} : { fill: 'fill', ink: 'label' }),
    name, captain: extra === 'captain',
    detail: position, detailSuffix: extra === 'admin' ? ' · Admin' : me ? ' · Tú' : undefined,
    trailing: c.number(number, 'jersey', 1.33), separator,
  });
};

// Row swiped left 252pt: Capitán / Admin / Quitar revealed, full card width (no inset).
const swipedRow = (k, c, player) => {
  const slot = k.figma.createFrame(); slot.name = 'swiped'; slot.fills = []; slot.resize(341, 64.5); slot.clipsContent = false;
  const wide = k.figma.createFrame(); wide.name = 'track'; wide.fills = []; wide.resize(361, 64); wide.clipsContent = true;
  const actions = k.stack('H', { name: 'actions', children: [c.swipeAction('Capitán', 'star', 'gray'), c.swipeAction('Admin', 'key', 'accent'), c.swipeAction('Quitar', 'trash', 'red')] });
  const content = k.stack('H', { name: 'content', w: 361, h: 64, pad: [0, 0, 0, 20], fill: 'bg2', children: [playerRow(c, player, { separator: false })] });
  wide.appendChild(at(actions, 109, 0)); wide.appendChild(at(content, -252, 0));
  slot.appendChild(at(wide, -20, 0));
  // Full-width hairline under the swiped row (the export draws it edge to edge).
  const sep = k.figma.createLine(); sep.name = 'separator'; slot.appendChild(sep);
  sep.resize(361, 0); sep.x = -20; sep.y = 64.5 - 0.75; sep.strokeWeight = 0.5; k.paint(sep, 'sep', 'strokes');
  return slot;
};

const section = (k, c, label, rows) => k.stack('V', { name: label, w: size.screenW, gap: 10, children: [c.sectionLabel(label),
  k.stack('V', { name: 'cardSlot', pad: [0, 0, 0, 16], children: [c.card(rows)] })] });

export function plantillaAdmin(k, c, mode) {
  const { stack } = k;
  const requests = [['RJ', 'Raúl Jiménez', 'Por enlace · hace 2 h'], ['MA', 'Mateo Aguilar', 'Por enlace · ayer']].map(([ini, name, detail], i, all) =>
    c.personRow({ avatar: c.avatar(ini, false, { fill: 'fill', ink: 'label2' }), name, detail, separator: i < all.length - 1,
      trailing: stack('H', { name: 'actions', gap: 14, children: [c.roundAction('xmark'), c.roundAction('checkmark', true)] }) }));
  const players = ROSTER.map((p) => (p[0] === 'JC' ? swipedRow(k, c, p) : playerRow(c, p)));
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('Atlético Narvarte · 18', 'Plantilla'),
    stack('V', { name: 'body', pad: [16, 0, 0, 0], align: 'CENTER', children: [c.searchField(),
      stack('V', { name: 'lists', pad: [20, 0, 0, 0], gap: 24, children: [section(k, c, 'Quieren unirse · 2', requests), section(k, c, 'Jugadores', players)] })] }),
  ] });
  return c.screen('Plantilla — admin: solicitudes y swipe', mode, [at(content, 0, 110), at(c.iconButton('link'), 333, 61), at(c.tabBar('Plantilla', { Avisos: 2 }), 14, 756)]);
}

export function plantillaJugador(k, c, mode) {
  const { stack } = k;
  // The list scrolls inside a card that stops above the tab bar (export: 496.5 tall, clipped).
  const roster = c.card(ROSTER.map((p) => playerRow(c, p, { meHighlight: true })), 'roster');
  roster.primaryAxisSizingMode = 'FIXED'; roster.resize(361, 496.5);
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('Atlético Narvarte · 18', 'Plantilla'),
    stack('V', { name: 'body', pad: [16, 0, 0, 0], gap: 20, align: 'CENTER', children: [c.searchField(), roster] }),
  ] });
  return c.screen('Plantilla — jugador', mode, [at(content, 0, 110), at(c.tabBar('Plantilla', { Avisos: 2 }), 14, 756)]);
}
