// R6–R7 — Mis equipos: the user's teams plus a join request waiting for admin approval.
import { size } from '../tokens.mjs';
import { SVG } from '../kit.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };

export function misEquipos(k, c, mode) {
  const { stack, text, icon } = k;

  // Muted team: detail line ends with a bell-slash.
  const muted = stack('H', { name: 'detail', gap: 6.33, align: 'CENTER', children: [text('Jugador · 22 jugadores ', 'link', 'label2', 'detail'), icon(SVG.bellSlash, 'label2', 'muted')] });
  const teams = c.card([
    c.teamRow({ badge: c.badge('AN', 'accent'), title: 'Atlético Narvarte', detail: 'Admin · 18 jugadores · dom 12, 9:00' }),
    c.teamRow({ badge: c.badge('DR', 'crest3'), title: 'Deportivo Roma', detailNode: muted, last: true }),
  ], 'teams');

  const pending = stack('V', { name: 'pending', w: size.screenW, children: [
    c.sectionLabel('Pendiente de aprobación'),
    stack('V', { name: 'cardSlot', pad: [10, 0, 0, 16], children: [c.card([
      c.teamRow({ badge: c.badge('LV', 'fill', 'label2'), title: 'Leones del Valle', titleColor: 'label2', detail: 'Te uniste por enlace · hace 2 h', chevron: false, last: true }),
    ], 'pendingCard')] }),
    c.footnote('Te avisaremos cuando un admin apruebe tu solicitud.'),
  ] });

  const actions = c.card([
    c.actionRow({ glyph: 'plus', label: 'Crear equipo' }),
    c.actionRow({ glyph: 'link', label: 'Unirme con un enlace', last: true }),
  ], 'actions');

  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('Diego Ramírez', 'Mis equipos'),
    stack('V', { name: 'body', pad: [28, 0, 0, 0], gap: 28, align: 'CENTER', children: [teams, pending, actions] }),
  ] });

  return c.screen('Mis equipos — con solicitud pendiente', mode, [at(content, 0, 110), at(c.iconButton('sun'), 16, 61), at(c.iconButton('plus'), 333, 61)]);
}
