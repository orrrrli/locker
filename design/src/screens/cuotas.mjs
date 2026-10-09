// R12 — Cuotas (player view) and Nuevo cobro (admin sheet).
import { size } from '../tokens.mjs';
import { SVG } from '../kit.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };

const section = (k, c, label, rows, extra = []) => k.stack('V', { name: label, w: size.screenW, gap: 10, children: [c.sectionLabel(label),
  k.stack('V', { name: 'cardSlot', children: [k.stack('V', { name: 'cardPad', pad: [0, 0, 0, 16], children: [c.card(rows)] }), ...extra] })] });

export function cuotasJugador(k, c, mode) {
  const { stack, text, icon } = k;
  const paid = (amount) => stack('H', { name: 'paid', gap: 6, children: [stack('V', { name: 'checkSlot', pad: [6, 0, 0, 0], children: [icon(SVG.check16, 'accent', 'check')] }), text(amount, 'jersey', 'label2', 'amount')] });
  const head = stack('V', { name: 'header', w: size.screenW, pad: [0, 20, 0, 20], children: [
    text('Atlético Narvarte · debes', 'eyebrow', 'label2', 'eyebrow'),
    stack('V', { name: 'amountSlot', pad: [2, 0, 0, 0], children: [c.clockRow('$200', 'MXN', { captionTok: 'title3', captionPad: 3, captionX: 142.92 })] }),
  ] });
  const content = stack('V', { name: 'content', w: size.screenW, children: [head,
    stack('V', { name: 'body', pad: [28, 0, 0, 0], gap: 24, children: [
      section(k, c, 'Pendientes', [
        c.infoRow({ title: 'Arbitraje · Jornada 5', subtitle: 'Asistentes vs. Pumitas · vence dom 12', right: ['$120'], rightTok: 'jersey', rightTop: 8.5 }),
        c.infoRow({ title: 'Balones nuevos', subtitle: 'Todo el equipo · vence 31 oct', right: ['$80'], rightTok: 'jersey', rightTop: 8.5, last: true }),
      ], [c.footnote('Pagas directo al admin; él marca el cargo como pagado.')]),
      section(k, c, 'Pagadas', [
        c.infoRow({ title: 'Inscripción liga', titleTok: 'body', subtitle: '2 sep', trailing: paid('$450'), trailingTop: 8.33 }),
        c.infoRow({ title: 'Arbitraje · Jornada 4', titleTok: 'body', subtitle: '21 sep', trailing: paid('$120'), trailingTop: 8.33, last: true }),
      ]),
    ] }),
  ] });
  return c.screen('Cuotas — jugador', mode, [at(content, 0, 110), at(c.tabBar('Cuotas', { Avisos: 2 }), 14, 756)]);
}

export function nuevoCobro(k, c, mode) {
  const { stack, text, icon, grow } = k;
  const amount = stack('H', { name: 'Field/amount', w: 341, h: 79.5, pad: [12, 20, 0, 0], children: [
    grow(stack('V', { name: 'text', children: [text('Monto', 'eyebrow', 'label2', 'label'), text('$120', 'title1', 'label', 'value')] })),
    stack('V', { name: 'unitSlot', pad: [29.5, 0, 0, 0], children: [text('MXN por jugador', 'rowLabel', 'label2', 'unit')] }),
  ] });
  const details = c.card([c.field({ label: 'Concepto', value: 'Arbitraje · Jornada 6', valueTok: 'fieldTitle' }), amount], 'details');
  const who = c.card([
    c.optionRow({ label: 'Todo el equipo', trailing: text('18', 'link', 'label2', 'count') }),
    c.optionRow({ label: 'Asistentes de un partido', trailing: icon(SVG.check20, 'accent', 'check') }),
    c.optionRow({ label: 'Partido', labelColor: 'label2', value: 'vs. Club Portales · 28 sep', trailing: stack('V', { name: 'chevronSlot', pad: [4, 0, 0, 0], children: [icon(SVG.chevronRight, 'label3', 'chevron')] }), last: true }),
  ], 'who');
  const summary = stack('H', { name: 'summary', w: size.screenW, gap: 12, pad: [5, 20, 0, 20], children: [
    stack('V', { name: 'pileSlot', pad: [4, 0, 0, 0], children: [c.avatarPile(['LH', 'SC', 'AS', 'MT', 'JC'], '+9', { fill: 'fill', ink: 'label', d: 30, step: 21 })] }),
    stack('V', { name: 'total', children: [
      stack('H', { name: 'line1', gap: 2.33, children: [text('14 cargos de $120 · ', 'footnote', 'label2', 'count'), text('$1,680 ', 'calloutStrongFlat', 'label', 'total')] }),
      text('MXN', 'calloutStrongFlat', 'label', 'unit'),
    ] }),
  ] });
  const content = stack('V', { name: 'content', w: size.screenW, children: [
    c.header('Atlético Narvarte', 'Nuevo cobro'),
    stack('V', { name: 'body', pad: [24, 0, 0, 0], gap: 24, children: [
      stack('V', { name: 'detailsSlot', pad: [0, 0, 0, 16], children: [details] }),
      stack('V', { name: 'who', w: size.screenW, gap: 10, children: [c.sectionLabel('Cobrar a'), stack('V', { name: 'cardPad', pad: [0, 0, 0, 16], children: [who] }), summary] }),
    ] }),
  ] });
  const nav = stack('H', { name: 'nav', w: 361, justify: 'SPACE_BETWEEN', children: [c.navPill('Cancelar'), c.navPill('Crear', true)] });
  return c.screen('Nuevo cobro — admin (sheet)', mode, [at(content, 0, 135), at(c.grabber(), 178.5, 60), at(nav, 16, 74)]);
}
