// R9 — Partidos: next match with RSVP summary, schedule and played matches; empty state for a player.
import { size } from '../tokens.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };

export function partidosAdmin(k, c, mode) {
  const { stack, text } = k;

  const pile = c.avatarPile(['LH', 'SC', 'AS', 'MT', 'JC'], '+7');
  const when = c.clockRow('9:00', 'dom 12 oct');

  const next = stack('V', { name: 'next', w: size.screenW, pad: [0, 20, 0, 20], children: [
    text('Siguiente · en 8 días', 'eyebrow', 'label2', 'eyebrow'),
    stack('V', { name: 'whenSlot', pad: [5.5, 0, 0, 0], children: [when] }),
    stack('V', { name: 'rival', pad: [5, 0, 0, 0], children: [text('vs. Halcones Sur', 'title2', 'label', 'rival')] }),
    stack('V', { name: 'venue', pad: [4.83, 0, 0, 0], children: [text('Deportivo Vicente Suárez, cancha 2', 'venue', 'label2', 'venue')] }),
    stack('H', { name: 'rsvp', pad: [18.17, 0, 0, 0], gap: 12, align: 'CENTER', children: [
      pile,
      stack('V', { name: 'counts', children: [text('12 van', 'subheadStrong', 'label', 'going'), text('3 no van · 3 sin responder', 'subhead', 'label2', 'rest')] }),
    ] }),
  ] });

  const section = (label, rows) => stack('V', { name: label, w: size.screenW, gap: 10, children: [c.sectionLabel(label),
    stack('V', { name: 'cardSlot', pad: [0, 0, 0, 16], children: [c.card(rows)] })] });

  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('Atlético Narvarte', 'Partidos', { switcher: true }),
    stack('V', { name: 'body', pad: [28, 0, 0, 0], gap: 28, children: [
      next,
      section('Calendario', [
        c.infoRow({ title: 'Real Mixcoac', subtitle: 'Cancha La Pradera', right: ['dom 19 oct', '10:30'] }),
        c.infoRow({ title: 'Club Portales', subtitle: 'Deportivo Vicente Suárez', right: ['dom 26 oct', '9:00'], last: true }),
      ]),
      section('Jugados', [
        c.infoRow({ title: 'Pumitas del Valle', subtitle: '14 jugaron', right: ['dom 5 oct'], rightColor: 'label2' }),
        c.infoRow({ title: 'Club Portales', subtitle: 'Falta cerrar asistencia', subtitleColor: 'orange', right: ['dom 28 sep'], rightColor: 'label2', last: true }),
      ]),
    ] }),
  ] });

  return c.screen('Partidos — admin', mode, [at(content, 0, 110), at(c.iconButton('plus'), 333, 61), at(c.tabBar('Partidos', { Avisos: 2 }), 14, 756)]);
}

export function partidosJugadorVacio(k, c, mode) {
  const { stack, text } = k;
  const body = text('Cuando un admin agende un partido te llegará un aviso y podrás decir si vas.', 'lead', 'label2', 'body');
  body.textAutoResize = 'HEIGHT'; body.resize(300, body.height);
  const empty = stack('V', { name: 'empty', w: 353, children: [
    text('Siguiente', 'eyebrow', 'label2', 'eyebrow'),
    stack('V', { name: 'time', pad: [0.5, 0, 5, 0], children: [text('—:—', 'clock', 'label3', 'time')] }), // the export used the 67pt natural line here
    stack('V', { name: 'title', pad: [5, 0, 0, 0], children: [text('Nada programado', 'title2', 'label', 'title')] }),
    stack('V', { name: 'body', pad: [8.33, 0, 0, 0], children: [body] }),
  ] });
  return c.screen('Partidos — jugador, sin partidos', mode, [at(c.header('Deportivo Roma', 'Partidos', { switcher: true }), 0, 110), at(empty, 20, 358), at(c.tabBar('Partidos'), 14, 756)]);
}
