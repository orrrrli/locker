// R9–R10 — Detalle de partido: player RSVP, and admin view once answers are closed.
import { size } from '../tokens.mjs';
import { SVG } from '../kit.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };

// Header block: eyebrow, big time + city, rival as a 34pt title.
const matchHeader = (k, c, { eyebrow, rival, timeColor = 'label' }) => k.stack('V', { name: 'header', w: size.screenW, pad: [0, 20, 0, 20], children: [
  k.text(eyebrow, 'eyebrow', 'label2', 'eyebrow'),
  k.stack('V', { name: 'whenSlot', pad: [4, 0, 0, 0], children: [c.clockRow('9:00', 'Ciudad de México', { timeColor, captionTok: 'rowLabel', captionPad: 4 })] }),
  k.stack('V', { name: 'rival', pad: [2.5, 0, 0, 0], children: [k.text(rival, 'title1', 'label', 'rival')] }),
] });

// Trailing "Mapa ›": the chevron sits 7pt below the label's line box top (export).
const mapChevron = (k, withLabel) => k.stack('H', { name: 'map', gap: 11.67, children: [
  ...(withLabel ? [k.stack('V', { name: 'mapSlot', pad: [2, 0, 0, 0], children: [k.text('Mapa', 'callout', 'label2', 'map')] })] : []),
  k.stack('V', { name: 'chevronSlot', pad: [7, 0, 0, 0], children: [k.icon(SVG.chevronRight, 'label3', 'chevron')] })] });

// Voy / No voy segmented pair. closed: the whole pair dims to 45%.
const rsvpPair = (k, c, { selected, closed = false }) => {
  const pair = k.stack('H', { name: 'rsvp', w: size.screenW, gap: 10, pad: [0, 16, 0, 16], children: [
    c.buttonLarge('Voy', selected === 'Voy' ? 'selected' : 'secondary', 175.5, 56),
    c.buttonLarge('No voy', selected === 'No voy' ? 'selected' : 'secondary', 175.5, 56),
  ] });
  if (closed) pair.opacity = 0.45;
  return pair;
};

export function detalleJugador(k, c, mode) {
  const { stack, text } = k;
  const place = c.card([c.infoRow({ title: 'Deportivo Vicente Suárez', titleTok: 'body', subtitle: 'Cancha 2 · Col. Narvarte', trailing: mapChevron(k, true), last: true })], 'place');

  // export rhythm: the RSVP note slot is 49.33 and chips start 9.67 under their label.
  const going = [[['LH', 'Luis'], ['SC', 'Santiago'], ['EV', 'Emilio']], [['AS', 'Andrés'], ['MT', 'Miguel'], [null, 'y 7 más']]];
  const chips = stack('V', { name: 'chips', gap: 8, children: going.map((row) => stack('H', { name: 'row', gap: 8, children: row.map(([ini, name]) => c.chip(name, ini ?? undefined)) })) });
  const counts = stack('H', { name: 'counts', gap: 15.33, children: [ // trailing spaces are trimmed, gaps stand in for them
    stack('H', { name: 'no', gap: 2.33, children: [text('No van · ', 'link', 'label2', 'label'), text('3', 'linkStrong', 'label', 'count')] }),
    stack('H', { name: 'pending', gap: 2.33, children: [text('Sin responder · ', 'link', 'label2', 'label'), text('4', 'linkStrong', 'label', 'count')] }),
  ] });

  const body = stack('V', { name: 'body', w: size.screenW, pad: [25, 0, 0, 0], gap: 28, children: [
    stack('V', { name: 'placeSlot', pad: [0, 0, 0, 16], children: [place] }),
    stack('V', { name: 'rsvpGroup', gap: 10, children: [c.sectionLabel('¿Vas?'), stack('V', { children: [rsvpPair(k, c, {}), c.footnote('Puedes cambiar tu respuesta hasta el inicio del partido.')] })] }),
    stack('V', { name: 'going', w: size.screenW, gap: 9.67, pad: [0, 20, 0, 20], children: [text('Van · 11', 'eyebrow', 'label2', 'label'), chips,
      stack('V', { name: 'countsSlot', pad: [4, 0, 0, 0], children: [counts] })] }),
  ] });

  const content = stack('V', { name: 'content', w: size.screenW, children: [matchHeader(k, c, { eyebrow: 'Domingo 12 de octubre · en 8 días', rival: 'vs. Halcones Sur' }), body] });
  return c.screen('Detalle — jugador', mode, [at(content, 0, 110), at(c.backButton('Partidos'), 16, 61), at(c.iconButton('share'), 333, 61)]);
}

export function detalleAdminCerradas(k, c, mode) {
  const { stack, text, icon } = k;
  const place = c.card([c.infoRow({ title: 'Cancha La Pradera', titleTok: 'body', subtitle: 'Col. Portales', trailing: mapChevron(k, false), last: true })], 'place');

  const closedLabel = stack('H', { name: 'sectionLabel', gap: 5, pad: [0, 0, 0, 20], align: 'CENTER', children: [icon(SVG.lock, 'label2', 'lock'), text('Respuestas cerradas', 'eyebrow', 'label2', 'label')] });
  const said = stack('V', { name: 'said', w: size.screenW, gap: 10, pad: [0, 20, 0, 20], children: [
    stack('V', { name: 'labelSlot', h: 15.5, children: [text('Dijeron que iban · 13', 'eyebrow', 'label2', 'label')] }),
    stack('H', { name: 'pileRow', gap: 12, align: 'CENTER', children: [c.avatarPile(['LH', 'SC', 'AS', 'MT', 'JC'], '+8', { fill: 'fill', ink: 'label' }), text('Aún sin asistencia oficial', 'link', 'label2', 'status')] }),
  ] });
  const close = stack('V', { name: 'close', w: size.screenW, align: 'CENTER', children: [c.buttonLarge('Cerrar asistencia', 'primary', 361, 56),
    c.footnote('Solo admins. Registra quién jugó para las cuotas por partido.', { tok: 'legal', center: true, h: 46 })] });

  const body = stack('V', { name: 'body', w: size.screenW, pad: [25, 0, 0, 0], gap: 28, children: [
    stack('V', { name: 'placeSlot', pad: [0, 0, 0, 16], children: [place] }),
    stack('V', { name: 'rsvpGroup', gap: 10, children: [closedLabel, stack('V', { children: [rsvpPair(k, c, { selected: 'Voy', closed: true }),
      c.footnote('Se cerraron al inicio del partido. Quedaron 13 «Voy», 2 «No voy» y 3 sin responder.', { h: 49.5 })] })] }),
    said, close,
  ] });

  const content = stack('V', { name: 'content', w: size.screenW, children: [matchHeader(k, c, { eyebrow: 'Domingo 28 de septiembre · jugado', rival: 'vs. Club Portales', timeColor: 'label2' }), body] });
  return c.screen('Detalle — admin, respuestas cerradas', mode, [at(content, 0, 110), at(c.backButton('Partidos'), 16, 61), at(c.iconButton(), 333, 61)]);
}
