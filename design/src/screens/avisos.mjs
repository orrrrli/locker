// R11 — Avisos: player inbox, admin empty state, compose sheet, and read receipts for a sent notice.
import { size } from '../tokens.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };

const section = (k, c, label, rows) => k.stack('V', { name: label, w: size.screenW, gap: 10, children: [c.sectionLabel(label),
  k.stack('V', { name: 'cardSlot', pad: [0, 0, 0, 16], children: [c.card(rows)] })] });

export function avisosJugador(k, c, mode) {
  const { stack } = k;
  const content = stack('V', { name: 'content', w: size.screenW, children: [
    c.header('Atlético Narvarte', 'Avisos', { count: 2, countColor: 'accent' }),
    stack('V', { name: 'body', pad: [24, 0, 0, 0], gap: 24, children: [
      section(k, c, 'Sin abrir', [
        c.noticeRow({ initials: 'AS', title: 'Cambio de cancha el domingo', time: '8:14', body: 'Jugamos en la cancha 2, no en la 1. Lleguen 20 minutos antes para calentar.' }),
        c.noticeRow({ initials: 'AS', title: 'Cuota de arbitraje', time: 'Ayer', body: 'Ya está cargada la cuota de la jornada 5. Son $120 por cabeza.', last: true }),
      ]),
      section(k, c, 'Anteriores', [
        c.noticeRow({ initials: 'CM', title: 'Uniforme para la liga', time: 'mar', body: 'Esta temporada jugamos de local con la playera verde y short negro.', unread: false }),
        c.noticeRow({ initials: 'AS', title: 'Bienvenido, Óscar', time: '28 sep', body: 'Se une Óscar Peña como defensa. Denle la bienvenida el domingo.', unread: false, last: true }),
      ]),
    ] }),
  ] });
  return c.screen('Avisos — jugador', mode, [at(content, 0, 110), at(c.tabBar('Avisos', { Avisos: 2 }), 14, 756)]);
}

export function avisosAdminVacio(k, c, mode) {
  const { stack, text } = k;
  const body = text('Cambios de cancha, horarios, cuotas. Llega como notificación a los 18.', 'lead', 'label2', 'body');
  body.textAutoResize = 'HEIGHT'; body.resize(300, body.height);
  const empty = stack('V', { name: 'empty', w: 353, children: [
    text('Todavía nada', 'eyebrow', 'label2', 'eyebrow'),
    stack('V', { name: 'title', pad: [5, 0, 0, 0], children: [text('Manda el primer aviso', 'title2', 'label', 'title')] }),
    stack('V', { name: 'body', pad: [8.33, 0, 0, 0], children: [body] }),
    stack('V', { name: 'action', pad: [18.67, 0, 0, 0], children: [c.pillButton('Nuevo aviso')] }),
  ] });
  return c.screen('Avisos — admin, sin avisos', mode, [at(c.header('Atlético Narvarte', 'Avisos'), 0, 110), at(empty, 20, 355),
    at(c.iconButton('compose'), 333, 61), at(c.tabBar('Avisos'), 14, 756)]);
}

export function nuevoAviso(k, c, mode) {
  const { figma, stack } = k;
  const message = c.field({ label: 'Mensaje', value: 'El partido se recorre a las 10:00 por un torneo infantil en la cancha 1. Mismo lugar.', multiline: true });
  const caret = figma.createFrame(); caret.name = 'caret'; caret.resize(2, 20); k.paint(caret, 'accent');
  message.appendChild(caret); caret.layoutPositioning = 'ABSOLUTE'; caret.x = 247.03; caret.y = 56; // right after "lugar."
  const card = c.card([c.field({ label: 'Título', value: 'Horario del domingo', valueTok: 'fieldTitle' }), message], 'compose');
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('Para los 18 · te quedan 3 de 10 hoy', 'Nuevo aviso'),
    stack('V', { name: 'body', pad: [24, 0, 0, 0], align: 'CENTER', children: [card,
      c.footnote('Llega como notificación push a quienes no tienen el equipo silenciado; los demás lo ven en su bandeja.', { top: 25, h: 66 })] }),
  ] });
  const nav = stack('H', { name: 'nav', w: 361, justify: 'SPACE_BETWEEN', children: [c.navPill('Cancelar'), c.navPill('Enviar', true)] });
  return c.screen('Nuevo aviso — admin (sheet)', mode, [at(content, 0, 135), at(c.grabber(), 178.5, 60), at(nav, 16, 74)]);
}

export function avisoEnviado(k, c, mode) {
  const { stack, text } = k;
  const title = text('Cambio de cancha el domingo', 'title1', 'label', 'title'); title.textAutoResize = 'HEIGHT'; title.resize(353, title.height);
  const body = text('Jugamos en la cancha 2, no en la 1. Lleguen 20 minutos antes para calentar. El estacionamiento de atrás está cerrado, usen el de Eje 5.', 'lead', 'label', 'body');
  body.textAutoResize = 'HEIGHT'; body.resize(353, body.height);
  const head = stack('V', { name: 'header', w: size.screenW, pad: [0, 20, 0, 20], children: [
    text('Andrés Soto · hoy, 8:14', 'eyebrow', 'label2', 'eyebrow'),
    stack('V', { name: 'titleSlot', pad: [0, 0, 0, 0], children: [title] }),
    stack('V', { name: 'bodySlot', pad: [15, 0, 0, 0], children: [body] }),
  ] });
  const seen = stack('V', { name: 'seen', w: size.screenW, gap: 10, children: [c.sectionLabel('Visto por 12 de 18'),
    stack('H', { name: 'pileRow', gap: 12, pad: [0, 0, 0, 20], align: 'CENTER', children: [c.avatarPile(['LH', 'SC', 'AS', 'MT', 'JC'], '+7'), text('abrieron el aviso', 'link', 'label2', 'status')] })] });
  const unseen = [['PD', 'Pablo Díaz'], ['RF', 'Ricardo Flores'], ['EV', 'Emilio Vargas'], ['FR', 'Fernando Ruiz'], ['ÓP', 'Óscar Peña'], ['RJ', 'Raúl Jiménez']]
    .map(([ini, name]) => c.personRow({ avatar: c.avatar(ini, false), name, detail: null, h: 54.5 }));
  const content = stack('V', { name: 'content', w: size.screenW, children: [head,
    stack('V', { name: 'body', pad: [26.33, 0, 0, 0], gap: 24, children: [seen, // the export's 3-line body box is 1.67 shorter
      stack('V', { children: [section(k, c, 'No han abierto el aviso · 6', unseen), c.footnote('Solo los admins ven esta lista.')] })] }),
  ] });
  return c.screen('Aviso enviado — admin: quién lo abrió', mode, [at(content, 0, 110), at(c.backButton('Avisos'), 16, 61), at(c.iconButton(), 333, 61)]);
}
