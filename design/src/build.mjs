// Builds locker-ios.fig from tokens + components + screens.
//   node --import ./src/fonts.mjs src/run.mjs src/build.mjs locker-ios.fig
// Claude Design export baseline for tools/compare.mjs: git -C design show 2f1f1ab:locker-ios.fig > /tmp/claude-design-export.fig
import { createKit, createParts } from './kit.mjs';
import { cuotasAdmin } from './screens/cuotas-admin.mjs';
import { partidosAdmin, partidosJugadorVacio } from './screens/partidos.mjs';
import { detalleJugador, detalleAdminCerradas } from './screens/detalle.mjs';
import { cerrarAsistencia } from './screens/cerrar-asistencia.mjs';
import { plantillaAdmin, plantillaJugador } from './screens/plantilla.mjs';
import { avisosJugador, avisosAdminVacio, nuevoAviso, avisoEnviado } from './screens/avisos.mjs';
import { cuotasJugador, nuevoCobro } from './screens/cuotas.mjs';
import { ajustes, eliminarCuentaBloqueado } from './screens/ajustes.mjs';
import { misEquipos } from './screens/mis-equipos.mjs';
import { bienvenida, correoLogin, crearCuentaMenor, crearCuentaApple } from './screens/bienvenida.mjs';
import { logoParche, logoMacIcon } from './screens/logo.mjs';
import { logoWordmark, wordmarkKBota, appIcon, contenidoBalon } from './screens/wordmark.mjs';

export default async (figma) => {
  const k = createKit(figma);
  const c = createParts(k);

  // Page 1: components, laid out in a column.
  figma.currentPage.name = 'Componentes';
  const comps = c.library();
  let y = 0;
  for (const comp of comps) { comp.x = 0; comp.y = y; y += comp.height + 40; }

  // One page per export section; each screen in Claro and Oscuro side by side.
  const section = (name, screens) => {
    const page = figma.createPage(); page.name = name;
    screens.forEach((build, row) => ['claro', 'oscuro'].forEach((mode, i) => {
      const d = c.device(build(k, c, mode)); page.appendChild(d);
      d.x = (row * 2 + i) * 497; d.y = 0;
    }));
    return name;
  };
  const pages = [
    section('Bienvenida', [bienvenida, correoLogin, (k, c, m) => correoLogin(k, c, m, { loading: true }), (k, c, m) => crearCuentaMenor(k, c, m, { valid: true }), crearCuentaMenor, crearCuentaApple,
      (k, c, m) => crearCuentaApple(k, c, m, { picking: true }), (k, c, m) => crearCuentaApple(k, c, m, { creating: true })]),
    section('Mis equipos', [misEquipos]),
    section('Partidos', [partidosAdmin, partidosJugadorVacio]),
    section('Detalle', [detalleJugador, detalleAdminCerradas]),
    section('Cerrar asistencia', [cerrarAsistencia]),
    section('Plantilla', [plantillaAdmin, plantillaJugador]),
    section('Avisos', [avisosJugador, avisosAdminVacio, nuevoAviso, avisoEnviado]),
    section('Cuotas', [cuotasJugador, cuotasAdmin, nuevoCobro]),
    section('Ajustes', [ajustes, eliminarCuentaBloqueado]),
  ];
  const logo = figma.createPage(); logo.name = 'Logo'; logo.appendChild(logoParche(figma));
  const mac = logoMacIcon(figma); logo.appendChild(mac); mac.x = 1624 + 200;
  pages.push('Logo');
  const wm = figma.createPage(); wm.name = 'Wordmark'; wm.appendChild(logoWordmark(figma));
  const kb = wordmarkKBota(figma); wm.appendChild(kb); kb.y = -(800 + 200);
  [['gajos', 'Gajos'], ['escudo', 'Escudo']].forEach(([kind, title], i) => {
    const f = appIcon(figma, kind, title); wm.appendChild(f); f.x = 1600 + 200 + i * (2 * 1104 + 200); f.y = -(800 + 200);
  });
  pages.push('Wordmark');
  const contenido = figma.createPage(); contenido.name = 'Contenido'; contenido.appendChild(contenidoBalon(figma));
  pages.push('Contenido');
  return { components: comps.length, pages: ['Componentes', ...pages] };
};
