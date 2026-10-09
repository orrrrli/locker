// R1–R3 — Bienvenida: welcome, email sign-in, sign-up with the under-15 age gate error.
import { size, space } from '../tokens.mjs';
import { pitch, SVG } from '../kit.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };

// Welcome: always the dark green pitch, whatever the mode (only the OS chrome follows the mode... and it is white on green too).
// Top to bottom: title and lead (they name the four parts of the app), a real card from the app whose rows say which
// part they come from, the social proof (sample number), then two doors: Apple, create an account with email, or sign in.
export function bienvenida(k, c, mode) {
  const { figma, stack, text, paint } = k;
  const ACTIONS_Y = 584;

  // Pitch: vertical gradient and a big rounded "center circle" that ends at DECO_END, above the actions.
  const DECO_END = 571; // halfway between the social row (ends 558) and the actions (584)
  const bg = figma.createFrame(); bg.name = 'pitch'; bg.resize(size.screenW, size.screenH);
  bg.fills = [{ type: 'GRADIENT_LINEAR', visible: true, opacity: 1,
    gradientStops: [{ position: 0, color: { ...pitch.top, a: 1 } }, { position: 1, color: { ...pitch.bottom, a: 1 } }],
    gradientTransform: { m00: 0, m01: 1, m02: 0, m10: -1, m11: 0, m12: 1 } }];
  const ring = figma.createFrame(); ring.name = 'center-circle'; ring.resize(643, DECO_END - 120); ring.cornerRadius = 50; ring.fills = [];
  paint(ring, 'heroLine', 'strokes'); ring.strokes[0].opacity = 0.07; ring.strokeWeight = 1.5; ring.strokeAlign = 'INSIDE';
  bg.appendChild(at(ring, -120, 120));

  const crests = [['AN', 'crest1'], ['DR', 'crest2'], ['LV', 'crest3'], ['HS', 'crest4'], ['RM', 'crest5']];
  const CREST_PITCH = 34; // 4pt overlap + 2pt ring: clears the initials of the crest underneath
  const pile = figma.createFrame(); pile.name = 'crests'; pile.fills = []; pile.resize((crests.length - 1) * CREST_PITCH + size.crest, size.crest);
  [...crests].reverse().forEach(([ini, tok], i) => pile.appendChild(at(c.crest(ini, tok), (crests.length - 1 - i) * CREST_PITCH, 0))); // left crest on top
  const social = stack('H', { name: 'social', gap: space.sm, align: 'CENTER', children: [
    pile, stack('V', { name: 'count', children: [text('1,240 equipos', 'subhead', 'hero2', 'line1'), text('ya juegan con Locker', 'subhead', 'hero2', 'line2')] }),
  ] });

  const lead = text('Partidos, plantilla, avisos y cuotas. Lo que un equipo amateur necesita, nada más.', 'lead', 'hero2', 'lead');
  lead.textAutoResize = 'HEIGHT'; lead.resize(300, lead.height);
  const intro = stack('V', { name: 'intro', w: 353, children: [
    text('Locker', 'heroEyebrow', 'heroEyebrow', 'eyebrow'),
    stack('V', { name: 'title', pad: [6, 0, 0, 0], children: [text('Tu equipo,', 'display', 'hero', 'line1'), text('en orden.', 'display', 'hero', 'line2')] }),
    stack('V', { name: 'lead', pad: [16, 0, 0, 0], children: [lead] }),
  ] });

  // A sample of the app, marked as such (audit #2): flat rows with no shadow and no separator, so it doesn't read as
  // the user's own data or as something to tap. The rows still say which part of the app they come from.
  const preview = stack('V', { name: 'preview', gap: 8, children: [
    stack('V', { name: 'labelSlot', pad: [0, 0, 0, 4], children: [text('Ejemplo', 'heroEyebrow', 'heroEyebrow', 'label')] }), // 4pt: lines up with the text column at x=20
    c.card([
      c.infoRow({ title: 'vs. Halcones Sur', subtitle: 'Partidos · 12 van', right: ['dom 12 oct', '9:00'], last: true }),
      c.infoRow({ title: 'Arbitraje · Jornada 5', subtitle: 'Cuotas · vence dom 12', right: ['$120'], rightTok: 'jersey', rightTop: 8.5, last: true }),
    ], 'Sample'),
  ] });

  // One sentence in runs; "Términos" and "Aviso de privacidad" are on-hero links (underlined, 44pt hit frame).
  // Spaces next to a link are NBSP: OpenPencil drops a plain trailing space when it measures some runs.
  const run = (str) => text(str, 'legal', 'heroLegal', 'legal');
  const link = (str) => c.link(str, 'legal', { onHero: true });
  const legal = stack('H', { name: 'legal', align: 'CENTER', children: [
    run('Al continuar aceptas los '), link('Términos'), run(' y el '), link('Aviso de privacidad'), run('.'),
  ] });

  // Two doors: new users create an account (Apple or email), returning users sign in.
  const signIn = stack('H', { name: 'signIn', h: 44, align: 'CENTER', children: [text('¿Ya tienes cuenta? ', 'link', 'hero2', 'prompt'), c.link('Iniciar sesión', 'linkStrong', { onHero: true })] });
  const actions = stack('V', { name: 'actions', w: 353, gap: space.sm, align: 'CENTER', children: [
    c.buttonLarge('Continuar con Apple', 'apple', 353), // registers new users too (R2.4), so not "Iniciar sesión" (audit #3)
    c.buttonLarge('Crear cuenta con correo', 'onHero', 353),
    signIn,
    legal,
  ] });

  // intro 120 → 312 (eyebrow 15.5, title 114, lead 62); sample 328 → 496 (label 15.5 + 8 + card 144); social 520 → 558; actions from 584.
  return c.screen('Bienvenida', mode, [bg, at(intro, 20, 120), at(preview, 16, 328), at(social, 20, 520), at(actions, 20, ACTIONS_Y)], { bg: 'island', chromeMode: 'oscuro' });
}

// Email sign-in. loading: the sign-in button shows its spinner (request in flight).
export function correoLogin(k, c, mode, { loading = false } = {}) {
  const { stack, text } = k;
  // 44pt hit frame; the 25.5 slot keeps the text where the export drew it (the frame overflows the row by 1pt, invisibly).
  // loading: no caret, values and links fade to 0.4 (the form is locked while the request runs).
  const dimmed = (n) => { if (loading) n.opacity = 0.4; return n; };
  const forgot = stack('V', { name: 'forgot', pad: [25.5, 0, 0, 0], children: [dimmed(c.link('¿La olvidaste?'))] });
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('Locker', 'Hola de nuevo'),
    stack('V', { name: 'body', pad: [space.xxl, 0, 0, 0], gap: space.xxl, align: 'CENTER', children: [
      c.formCard([
        { label: 'Correo', value: 'diego.ramirez@gmail.com', dim: loading },
        { label: 'Contraseña', value: '••••••••••', secure: true, caret: !loading, dim: loading, trailing: forgot },
      ]),
      // The signup line gets a 44pt hit frame; gap 15 (28 − 13) keeps its text in place.
      stack('V', { name: 'actions', gap: 15, align: 'CENTER', children: [
        c.buttonLarge('Iniciar sesión', 'primary', undefined, undefined, { loading }),
        stack('H', { name: 'signup', h: 44, align: 'CENTER', children: [text('¿No tienes cuenta? ', 'link', 'label2', 'prompt'), dimmed(c.link('Crear cuenta'))] }),
      ] }),
    ] }),
  ] });
  return c.screen(loading ? 'Correo — iniciar sesión — cargando' : 'Correo — iniciar sesión', mode, [at(content, 0, 110), at(c.iconButton('chevronLeft'), 16, 61)]);
}

// Sign-up, step 1. Default: blocked by the age gate (R1: under 15).
// valid: an adult date, so the name/email/password card shows and Continuar is enabled.
export function crearCuentaMenor(k, c, mode, { valid = false } = {}) {
  const { stack, text, icon } = k;
  const error = text('Necesitas tener al menos 15 años para usar Locker.', 'footnote', 'red', 'error');
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('Paso 1 de 2', 'Crear cuenta'),
    stack('V', { name: 'body', pad: [space.xxl, 0, 0, 0], gap: space.xxl, align: 'CENTER', children: valid ? [
      c.formCard([{ label: 'Fecha de nacimiento', value: '12 mar 1998', type: 'date' }]),
      c.formCard([
        { label: 'Nombre', value: 'Diego Ramírez' },
        { label: 'Correo', value: 'diego.ramirez@gmail.com' },
        { label: 'Contraseña', value: '••••••••••', secure: true },
      ]),
      c.buttonLarge('Continuar'),
    ] : [
      // Gating field alone (R1.2): under-15s are blocked before the name/email/password card is shown.
      stack('V', { name: 'birthdate', align: 'CENTER', children: [
        c.formCard([{ label: 'Fecha de nacimiento', value: '4 oct 2013', type: 'date', error: true, trailing: icon(SVG.warning, 'red', 'warning') }]),
        stack('V', { name: 'error', w: size.screenW, h: 30, pad: [11, 20, 0, 36], children: [error] }), // export: 30pt slot, text 11pt down
      ] }),
      c.buttonLarge('Continuar', 'disabled'),
    ] }),
  ] });
  return c.screen(valid ? 'Crear cuenta — paso 1' : 'Crear cuenta — error: menor de 15 años', mode, [at(content, 0, 110), at(c.iconButton('chevronLeft'), 16, 61)]);
}

// Sign-up after Sign in with Apple (R2.4): name and email prefilled from Apple (editable, R2.2),
// no password, date of birth first (R1). The button stays disabled until the date is set.
// picking: the wheel picker is open inside the date card with a date chosen, so the button is enabled.
// creating: date set, picker closed, the button shows its spinner (request in flight).
export function crearCuentaApple(k, c, mode, { picking = false, creating = false } = {}) {
  const { stack } = k;
  const set = picking || creating;
  const date = c.formCard([set
    ? { label: 'Fecha de nacimiento', value: '12 mar 1998', type: 'date' }
    : { label: 'Fecha de nacimiento', value: 'Selecciona tu fecha', type: 'date', placeholder: true }],
  picking ? { picker: { day: 12, month: 3, year: 1998 } } : {});
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('Con Apple', 'Crear cuenta'),
    stack('V', { name: 'body', pad: [space.xxl, 0, 0, 0], gap: space.xxl, align: 'CENTER', children: [
      date,
      c.formCard([
        { label: 'Nombre', value: 'Diego Ramírez' },
        { label: 'Correo', value: 'x7k2m@privaterelay.appleid.com' },
      ]),
      c.buttonLarge('Crear cuenta', set ? 'primary' : 'disabled', undefined, undefined, { loading: creating }),
    ] }),
  ] });
  const name = creating ? 'Crear cuenta — con Apple — creando cuenta' : picking ? 'Crear cuenta — con Apple — eligiendo fecha' : 'Crear cuenta — con Apple';
  return c.screen(name, mode, [at(content, 0, 110), at(c.iconButton('chevronLeft'), 16, 61)]);
}
