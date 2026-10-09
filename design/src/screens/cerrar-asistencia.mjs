// R10 — Cerrar asistencia: admin confirms who actually played, prefilled from the «Voy» answers.
import { size } from '../tokens.mjs';

const at = (n, x, y) => { n.x = x; n.y = y; return n; };

const ROSTER = [
  ['LH', 'Luis Hernández', 'Portero', 1, true], ['SC', 'Santiago Cruz', 'Defensa', 2, true], ['EV', 'Emilio Vargas', 'Defensa', 3, false],
  ['AS', 'Andrés Soto', 'Defensa', 4, true], ['MT', 'Miguel Torres', 'Defensa', 5, true], ['FR', 'Fernando Ruiz', 'Medio', 6, false],
  ['JC', 'Jorge Castillo', 'Medio', 7, true], ['CM', 'Carlos Mendoza', 'Medio', 8, true], ['PD', 'Pablo Díaz', 'Delantero', 9, false],
  ['DR', 'Diego Ramírez', 'Delantero', 10, true], ['RF', 'Ricardo Flores', 'Delantero', 11, true], ['ÓP', 'Óscar Peña', 'Defensa', 14, true],
];

export function cerrarAsistencia(k, c, mode) {
  const { stack } = k;
  const played = ROSTER.filter((r) => r[4]).length;
  const list = c.card(ROSTER.map(([initials, name, role, number, present]) => c.attendeeRow({ initials, name, role, number, present })), 'roster');
  const content = stack('V', { name: 'content', w: size.screenW, align: 'CENTER', children: [
    c.header('vs. Club Portales · dom 28 sep', 'Jugaron', { count: played }),
    stack('V', { name: 'body', pad: [24, 0, 0, 0], align: 'CENTER', children: [list,
      c.footnote('Prellenado con quienes dijeron «Voy». Al guardar queda como asistencia oficial.')] }),
  ] });
  const nav = stack('H', { name: 'nav', w: 361, justify: 'SPACE_BETWEEN', children: [c.navPill('Cancelar'), c.navPill('Guardar', true)] });
  return c.screen('Cerrar asistencia — admin', mode, [at(content, 0, 110), at(nav, 16, 61)]);
}
