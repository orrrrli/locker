# Locker

[English](README.md) | **Español**

Una app de iOS para equipos de fútbol amateur. Los admins manejan el roster, programan partidos, llevan
la asistencia y las cuotas, y mandan push notifications a su equipo.

> **Estado:** Fase 1 en diseño.

## ¿Qué es Locker?

Los equipos de fútbol amateur viven en un grupo de WhatsApp. La hora del partido se pierde entre memes, nadie
sabe quién va a llegar el sábado, el admin lleva de memoria quién debe lo del árbitro y los goles dependen de
quién cuente mejor la historia.

Locker le da a cada equipo un solo lugar para todo eso:

- **Un espacio para el equipo.** Creas un equipo, invitas jugadores con un link y apruebas quién entra. Un
  jugador puede estar en varios equipos, con un rol, número y posición distintos en cada uno.
- **Avisos que sí llegan.** Los admins mandan push notifications a todo el equipo. Cada aviso también se guarda
  en un buzón dentro de la app, y los admins ven quién lo abrió ("Visto por 12 de 15").
- **Partidos y asistencia.** Programas partidos, juntas las respuestas de "voy / no voy", mandas recordatorios
  automáticos y registras quién llegó de verdad.
- **Cuotas sin el chat incómodo.** Registras quién pagó y quién debe, por mes o por partido. Cada jugador solo ve
  lo suyo.
- **Estadísticas confiables (Fase 2).** Goles por jugador, validados por los compañeros que estuvieron ahí, con
  un nivel de confianza visible en cada número.
- **Presume a tu equipo (Fases 2-3).** Cada equipo decide qué se hace público (resultados, estadísticas, roster,
  posts) y qué se queda en el vestidor.

**Para quién es:** equipos amateur y recreativos, jugadores de 15 años en adelante. Una versión para niños está
planeada para más adelante.

## Stack

| Capa | Tecnología |
|---|---|
| Cliente | Swift, SwiftUI (sin dependencias de terceros) |
| API | Go (`net/http`, `pgx`, `sqlc`, `goose`) |
| Base de datos | PostgreSQL |
| Push | APNs (token auth) |
| Email | Resend |
| Infra | Docker Compose en un VPS detrás de nginx, GitHub Actions → GHCR |

## Arquitectura

```
iOS app ──HTTPS──> nginx ──> Go API ──> Postgres
                               ├──> APNs
                               └──> Resend
```

## Estructura del repo

```
api/         API en Go
interface/   App de iOS (proyecto de Xcode)
```

## Roadmap

| Fase | Nombre | Alcance |
|---|---|---|
| 1 | El vestidor | Auth, equipos, invitaciones, roster, notificaciones, partidos + RSVP, asistencia, cuotas |
| 2 | La cancha | Goles, validación de partidos, estadísticas, perfiles públicos |
| 3 | La tribuna | Feed de fotos y video, moderación |

## Decisiones y trade-offs

- **Validar goles sin que el rival esté registrado.** Al inicio casi ningún rival va a estar en la app. Los goles
  de los jugadores tienen que sumar el marcador del equipo, los que asistieron confirman el resultado, los rivales
  empiezan como "equipos fantasma" (solo un nombre) que pueden reclamar sus partidos después, y cada estadística
  muestra su nivel de confianza en lugar de fingir que está verificada.
- **El buzón es la fuente de verdad; la push solo entrega.** Una push puede fallar sin avisar; un aviso guardado
  en el buzón no se pierde. "Visto" significa "abrió el aviso", porque iOS no reporta lo que se lee en la pantalla
  bloqueada.
- **Asistencia real sobre RSVP.** El RSVP es una intención. Un admin cierra la lista real de asistentes después de
  cada partido, y todo lo que depende de la asistencia usa esa lista.
- **La historia cuelga de la membresía, no del usuario.** Un usuario puede estar en varios equipos con roles y
  números distintos. Borrar una cuenta elimina los datos personales y anonimiza las membresías, así la historia
  del equipo y los marcadores siguen cuadrando.
- **Un usuario, varias identidades.** Sign in with Apple y correo/contraseña apuntan al mismo usuario. Las cuentas
  se unen solas solo cuando los dos correos están verificados, para evitar que alguien se robe una cuenta.
- **Permisos contra etiquetas.** Los roles (`admin`, `player`) dan permisos; "capitán" es una etiqueta deportiva
  sin permisos. Un equipo nunca se puede quedar sin admin.
- **Primero la librería estándar.** Routing con `net/http`, HTTP/2 de la librería estándar para APNs y una
  goroutine con ticker para las tareas programadas. Dependencias solo donde ahorran trabajo real.
- **Solo iOS nativo, a propósito.** La API es independiente, así que se puede agregar un cliente de Android después.
