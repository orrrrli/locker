// R4–R5 — Ajustes: account links, per-team mute, sign out / delete; delete blocked while sole admin.
import { size } from '../tokens.mjs';
import { SVG, crestInk } from '../kit.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };
const chevron = (k) => k.stack('V', { name: 'chevronSlot', pad: [4, 0, 0, 0], children: [k.icon(SVG.chevronRight, 'label3', 'chevron')] }); // the export chevron 2pt below center
const doneNav = (k, c) => k.stack('H', { name: 'nav', w: 361, justify: 'MAX', children: [c.navPill('Listo', false, true)] });
const accountCard = (c) => c.card([c.optionRow({ label: 'Cerrar sesión' }), c.optionRow({ label: 'Eliminar cuenta', labelColor: 'red', last: true })], 'account');

export function ajustes(k, c, mode) {
  const { stack, text } = k;
  const section = (label, card, extra = []) => stack('V', { name: label, w: size.screenW, gap: 10, children: [c.sectionLabel(label),
    stack('V', { children: [stack('V', { name: 'cardPad', pad: [0, 0, 0, 16], children: [card] }), ...extra] })] });
  const access = c.card([
    c.optionRow({ label: 'Vincular Sign in with Apple', subtitle: 'Entra sin contraseña', trailing: chevron(k) }),
    c.optionRow({ label: 'Agregar contraseña', subtitle: 'Por si cambias de teléfono', trailing: chevron(k), last: true }),
  ], 'access');
  const mute = c.card([
    c.optionRow({ label: 'Atlético Narvarte', trailing: c.toggle(false), h: 51.5 }),
    c.optionRow({ label: 'Deportivo Roma', trailing: c.toggle(true), h: 51.5, last: true }),
  ], 'mute');
  const version = text('Locker 1.0 (12) · Términos · Privacidad', 'caption', 'label2', 'version');
  const content = stack('V', { name: 'content', w: size.screenW, children: [
    c.profile('DR', 'Diego Ramírez', 'diego.ramirez@gmail.com'),
    stack('V', { name: 'body', pad: [28, 0, 0, 0], gap: 24, align: 'CENTER', children: [
      section('Acceso', access),
      section('Silenciar equipos', mute, [c.footnote('Apaga las notificaciones push de ese equipo. Los avisos siguen llegando a tu bandeja.')]),
      accountCard(c), version,
    ] }),
  ] });
  return c.screen('Ajustes', mode, [at(content, 0, 110), at(doneNav(k, c), 16, 61)]);
}

export function eliminarCuentaBloqueado(k, c, mode) {
  const { figma, stack, text, paint } = k;
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.profile('AS', 'Andrés Soto', 'andres.soto@icloud.com'),
    stack('V', { name: 'body', pad: [28, 0, 0, 0], children: [accountCard(c)] }),
  ] });

  // Alert: title, centered message, the teams that need another admin, two actions.
  const team = (ini, tok, name) => stack('H', { name: 'team', h: 24, gap: 8, align: 'CENTER', children: [
    stack('H', { name: 'badge', w: 24, h: 24, justify: 'CENTER', align: 'CENTER', fill: tok, r: 12, children: [text(ini, 'miniInitials', tok.startsWith('crest') ? crestInk(tok) : 'onAccent', 'initials')] }),
    text(name, 'miniLabel', 'label', 'name')] });
  const message = stack('V', { name: 'message', align: 'CENTER', children: ['Eres el único admin de estos equipos. ', 'Nombra a alguien desde Plantilla y vuelve a ', 'intentarlo.'].map((l) => text(l, 'legal', 'label', 'line')) });
  const action = (label, strong) => stack('H', { name: 'action', w: 135, h: 44, justify: 'CENTER', align: 'CENTER', children: [text(label, strong ? 'navStrong' : 'navLabel', 'accent', 'label')] });
  const actions = stack('H', { name: 'actions', w: 270, h: 44.5, children: [action('Ir a Plantilla'), action('Entendido', true)] });
  const top = figma.createLine(); actions.appendChild(top); top.layoutPositioning = 'ABSOLUTE'; top.resize(270, 0); top.x = 0; top.y = 0.25; top.strokeWeight = 0.5; paint(top, 'sep', 'strokes');
  const mid = figma.createLine(); actions.appendChild(mid); mid.layoutPositioning = 'ABSOLUTE'; mid.resize(44.5, 0); mid.rotation = -90; mid.x = 135.25; mid.y = 0; mid.strokeWeight = 0.5; paint(mid, 'sep', 'strokes');
  const alert = stack('V', { name: 'Alert', w: 270, fill: 'glass', r: 28, clip: true, children: [
    stack('V', { name: 'body', w: 270, h: 188, pad: [21, 16, 0, 16], align: 'CENTER', children: [
      text('Antes nombra otro admin', 'noticeTitle', 'label', 'title'),
      stack('V', { name: 'messageSlot', pad: [6, 0, 0, 0], children: [message] }),
      stack('V', { name: 'teams', w: 200, gap: 8, pad: [13, 0, 0, 0], children: [team('AN', 'accent', 'Atlético Narvarte'), team('LV', 'crest4', 'Leones del Valle')] }),
    ] }),
    actions,
  ] });
  alert.effects = [{ type: 'DROP_SHADOW', color: { r: 0, g: 0, b: 0, a: 0.18 }, offset: { x: 0, y: 12 }, radius: 40, spread: 0, visible: true, blendMode: 'NORMAL' }];
  const scrim = figma.createFrame(); scrim.name = 'scrim'; scrim.resize(size.screenW, size.screenH); paint(scrim, 'scrim');
  scrim.appendChild(at(alert, 61.5, 309.75));
  return c.screen('Eliminar cuenta — bloqueado: único admin', mode, [at(content, 0, 110), at(doneNav(k, c), 16, 61), scrim]);
}
