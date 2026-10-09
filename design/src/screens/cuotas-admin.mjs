// R12 — Cuotas · admin: detail of one charge, who paid and who owes.
import { size, space } from '../tokens.mjs';

const MEMBERS = [
  ['LH', 'Luis Hernández', true], ['SC', 'Santiago Cruz', true], ['EV', 'Emilio Vargas', false],
  ['AS', 'Andrés Soto', true], ['MT', 'Miguel Torres', true], ['FR', 'Fernando Ruiz', false],
  ['JC', 'Jorge Castillo', true], ['CM', 'Carlos Mendoza', true], ['PD', 'Pablo Díaz', false],
  ['DR', 'Diego Ramírez', true], ['RF', 'Ricardo Flores', false], ['ÓP', 'Óscar Peña', false],
];

export function cuotasAdmin(k, c, mode) {
  const { stack, text, stretch } = k;

  // Content column (scrolls on device): header, action, list, hint.
  const paid = MEMBERS.filter((m) => m[2]).length;
  const header = stack('V', { name: 'header', gap: 0, pad: [0, space.xl, 0, space.xl],
    children: [
      text('Arbitraje · Jornada 5 · $120 c/u', 'eyebrow', 'label2', 'eyebrow'),
      stack('H', { name: 'stat', gap: 11, align: 'MAX', children: [
        text(String(paid), 'hero', 'label', 'value'),
        stack('V', { name: 'caption', pad: [0, 0, 1.33, 0], children: [text(`de ${MEMBERS.length} pagaron · faltan $${(MEMBERS.length - paid) * 120}`, 'title3', 'label2', 'caption')] }),
      ] }),
    ] });

  const remind = c.buttonSecondary(`Recordar a ${MEMBERS.length - paid} pendientes`);
  const card = stack('V', { name: 'members', w: 361, pad: [0, 0, 0, 20], fill: 'bg2', r: 22, clip: true,
    children: MEMBERS.map(([initials, name, paid]) => c.memberRow({ initials, name, paid })) });
  const hint = stack('V', { name: 'hint', pad: [0, 36, 0, 36], w: size.screenW, children: [
    text('Desliza una fila para marcar pagado o quitar el cargo. «Recordar» manda un aviso solo a quienes deben y cuenta para tu cupo diario.', 'footnote', 'label2', 'hint'),
  ] });
  const hintText = hint.children[0]; hintText.textAutoResize = 'HEIGHT'; hintText.layoutAlign = 'STRETCH';

  // Vertical rhythm measured from the export: header at 110, action at 191.5, list at 259.5.
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    stretch(header),
    stack('V', { name: 'action', pad: [21.5, 0, 20.5, 0], children: [remind] }),
    stack('V', { name: 'list', gap: 10.5, align: 'CENTER', children: [card, hint] }),
  ] });
  content.x = 0; content.y = 110;

  // Nav buttons on top of content; status bar, island and home come from c.screen.
  const back = c.backButton('Cuotas'); back.x = 16; back.y = 61;
  const more = c.iconButton(); more.x = 333; more.y = 61;
  return c.screen('Cuotas — admin', mode, [content, back, more]);
}
