# Estilo del Monitor del Emitter

Guía visual del panel web que sirve el emitter en `http://<host>:3200`.

Archivos:

```
internal/panel/web/
├── index.html   estructura
├── styles.css   todo el estilo
└── app.js       pintado dinámico (genera el HTML de tarjetas y tablas)
```

---

## Paleta

Definida en `:root` al inicio de `styles.css`.

| Token | Valor | Uso |
|---|---|---|
| `--bg` | `#f3efe6` | Fondo de la página y de los `<th>` |
| `--card` | `#fff` | Superficie de tarjetas, tablas y cajas |
| `--ink` | `#161412` | Texto y bordes principales |
| `--muted` | `#7a736a` | Texto secundario: subtítulos, roles, metadatos |
| `--naranja` | `#ee8935` | Estado **Asignado** y valores destacados |
| `--verde` | `#4caf50` | Estado **Activo** y acciones de confirmación |
| `--rojo` | `#f44336` | Errores y acciones destructivas |
| `--borde` | `#d8d3c8` | Separadores suaves entre filas |

Comparte el naranja y el verde con el dashboard, así los estados se leen igual
en ambas pantallas.

**Tipografía:** `JetBrains Mono`, con fallback a `ui-monospace`, `Consolas`,
`monospace`. No se carga desde internet: usa la del sistema si no está.

---

## Lenguaje visual

Neobrutalista, igual que el dashboard:

- **Bordes de 2px** sólidos en contenedores y botones.
- **Esquinas rectas.** Sin `border-radius` en ningún elemento.
- **Sin degradados, sin sombras difuminadas, sin transparencias.**
- **Mayúsculas y `letter-spacing`** en títulos de sección, encabezados de tabla
  y botones.
- **Jerarquía por tamaño y color**, no por peso de sombra.

Escala de texto:

| Elemento | Tamaño |
|---|---|
| `h1` | `1.5rem` |
| `h2` (secciones) | `1rem`, mayúsculas, en `--muted` |
| Cuerpo de tablas | `.8rem` |
| Metadatos y badges | `.65rem` – `.7rem` |

---

## Componentes

### Barra de configuración (`.cfg`)

Tira horizontal con los datos del emitter. Fondo blanco, borde de 2px. Los
valores van en `<b>` con color `--naranja` para separarlos de las etiquetas.

```
┌──────────────────────────────────────────────────────────┐
│ MACHINE_CODE: a2i   Modo: DEF_MAQUINAS   Sub-máquinas: … │
└──────────────────────────────────────────────────────────┘
```

### Tarjetas de máquina (`.maq`)

Rejilla responsiva: `repeat(auto-fill, minmax(330px, 1fr))`, así se acomodan
solas según el ancho de la ventana.

```
┌─────────────────────────────────┐
│ a2i-1-r            [pendiente]  │  ← h3 + badge
│ máquina: a2i · id: 4 · 13:04    │  ← .meta en --muted
├─────────────────────────────────┤
│ Omar Ramirez            [Asignado]│  ← .persona
│ REVOLTURERO                     │
├─────────────────────────────────┤
│ Michel Davalos  3-3     [Activo] │
│ REVOLTURERO                     │
╞═════════════════════════════════╡  ← border-top de 2px
│ ORDEN 954354          [Cerrada] │  ← .orden-cab
│ COT. Pegaduro Sobre Piso 10 kg  │  ← .orden-prod
│ 113 Bts                         │  ← .orden-datos
│ LOTES                     3 / 10│  ← .orden-cifras
│ ███████░░░░░░░░░░░░░░░░░░░░░░░  │  ← .orden-barra
└─────────────────────────────────┘
```

Cada persona es una fila con `border-top` en `--borde`. El nombre usa
`text-overflow: ellipsis` para no desbordar la tarjeta.

### Orden de la máquina (`.orden`)

Va al pie de la tarjeta, separada del personal con un `border-top` de **2px**
en `--ink`, más gruesa que las líneas entre personas. La razón es semántica: en
un grupo de sub-máquinas el personal es idéntico en las tres tarjetas y la
orden es lo único que cambia, así que necesita leerse como un bloque aparte y
no como otra fila más de la lista.

Muestra los ocho campos de `order_details` tal como llegan en
`papid.admin.signed`, en cuatro renglones con jerarquía descendente:

| Renglón | Clase | Contenido | Tratamiento |
|---|---|---|---|
| Encabezado | `.orden-cab` | `Orden <doc_num>` + status | Etiqueta en `--muted`, badge a la derecha |
| Producto | `.orden-prod` | `product_name` | `.85rem` en negritas, el más notorio |
| Datos | `.orden-datos` | `quantity units` · `order_id` | `.72rem` en `--muted` |
| Lotes | `.orden-cifras` | `lotes_actuales / lotes_totales` | Los hechos a `1.05rem` en `--ink` |

El nombre del producto es lo que el operador identifica de un vistazo, así que
gana en peso visual. El `doc_num` va arriba porque es el folio con el que se
habla de la orden. El `order_id` es una clave interna y solo aparece si viene
con contenido; en los mensajes reales suele llegar vacío.

El status de la orden (`Cerrada`, `Abierta`) usa `.badge.orden-estado`, en
blanco y negro. **No** reusa el verde ni el naranja de los badges de personal:
son dos ejes distintos y compartir colores haría leer "orden cerrada" como si
fuera un estado de trabajador.

La barra de avance (`.orden-barra`) es un `<span>` con el `width` en porcentaje
calculado en el JS, sobre un carril con el fondo de la página. Rellena en
`--naranja`. Se recorta a 100% para que una orden sobrecumplida no desborde el
carril.

Cuando la máquina no ha recibido ningún signed con orden se pinta
`.orden.vacia` con el texto "Orden: sin datos" en `--muted`, en lugar de
mostrar `0 / 0`. Un cero es un dato; la ausencia de dato no lo es.

### Órdenes por máquina (`.ord`)

Sección aparte, debajo de la rejilla de tarjetas. Una tira **a todo el ancho**
por sub-máquina, apiladas. El bloque `.orden` de la tarjeta es un resumen que
esconde campos por falta de espacio; esta vista no esconde ninguno.

```
┌──────────────────────────────────────────────────────────────────────┐
│ a2i-3-r   ORDEN 954354                                    [CERRADA]  │
│ COT. Pegaduro Sobre Piso Blanco 10 kg                                │
│ │CANTIDAD  │UNIDADES │LOTES   │AVANCE │ORDER_ID│MACHINE_ID│ACTUALIZ. │
│ │113       │Bts      │0 / 1   │0%     │—       │4         │09:16     │
│ ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  │
└──────────────────────────────────────────────────────────────────────┘
```

El código de máquina (`.ord-maq`, `1.1rem`) es lo primero, porque al recorrer
la lista se busca la máquina, no la orden. El folio queda a su lado en
`--muted` y el status como badge pegado al extremo derecho, empujado por el
`margin-right: auto` del folio.

Los datos van en `.ord-datos`, una rejilla con
`repeat(auto-fit, minmax(110px, 1fr))`: en pantalla ancha caben todos en un
renglón y en angosta se parten sin descuadrarse. Cada celda es etiqueta chica
en `--muted` arriba y valor en negritas abajo, con un `border-left` en
`--borde` que hace de separador vertical.

Los campos vacíos se pintan con un guion en vez de omitirse. Si se ocultaran,
las columnas cambiarían de posición entre una máquina y otra y ya no se podrían
comparar de un vistazo.

Una máquina sin orden usa `.ord.vacia`, que baja el borde a `--borde` para que
la tira pese menos visualmente que las que sí tienen datos.

### Badges de estado (`.badge`)

Cuadrados, borde de 1px, texto en mayúsculas. La clase coincide con el nombre
del estado, así el color sale solo:

| Clase | Fondo | Texto | Borde |
|---|---|---|---|
| `.Activo` | verde | blanco | verde |
| `.Inactivo` | transparente | `--muted` | `--muted` |
| `.Asignado` | transparente | naranja | naranja |

El verde es el único con fondo relleno: es el estado que debe saltar a la vista.

### Botones

Base: fondo blanco, borde de 2px en `--ink`, mayúsculas. Al pasar el cursor se
invierten (fondo oscuro, texto claro).

| Clase | Color | Se usa para |
|---|---|---|
| *(sin clase)* | `--ink` | Acciones neutras |
| `.v` | verde | Confirmar, poner tarjeta, asignar forzado |
| `.n` | naranja | Quitar tarjeta, asignar normal |
| `.r` | rojo | Desasignar |

Cada variante invierte sus propios colores en `:hover`.

### Tablas

Ancho completo, `border-collapse`, borde exterior de 2px. Los `<th>` van con
fondo `--bg` (más oscuro que el `--card` de las filas) para separar el
encabezado sin necesitar una línea gruesa.

### Cajas de formulario (`.caja`)

Mismo tratamiento que las tarjetas: fondo blanco y borde de 2px. Los campos se
distribuyen con `.fila`, que es un flex con `wrap`, así se reacomodan en
pantallas angostas.

### Aviso flotante (`#aviso`)

Notificación fija abajo a la derecha. Verde para éxito, rojo (`.err`) para
error. Aparece y se oculta solo a los 3.5 segundos. Oculto por default con
`display: none`.

---

## Layout

Una sola columna con `padding: 20px 24px 60px`. El padding inferior extra deja
espacio para que el aviso flotante no tape contenido.

Las secciones se separan con los `h2`, que llevan `margin: 26px 0 10px`. No hay
contenedor con ancho máximo: el panel aprovecha toda la pantalla, porque las
tarjetas ya se limitan solas con la rejilla.

---

## Cómo se genera el HTML

`index.html` solo trae los contenedores vacíos con sus ids. El contenido lo
pinta `app.js` cada 2 segundos, con funciones separadas por sección:

| Función | Qué pinta |
|---|---|
| `pintarConfig` | La barra `.cfg` |
| `pintarMaquinas` | La rejilla de tarjetas |
| `pintarMiPersonal` | La tabla de personal con tags |
| `prellenarFormulario` | Los campos de asignación (solo la primera vez) |

Todo el texto que viene del servidor pasa por `esc()` antes de insertarse, para
que un nombre con `<`, `&` o comillas no rompa el HTML.

---

## Al modificar

Los archivos de `web/` van **embebidos en el binario** con `go:embed`. Se editan
como archivos normales, pero los cambios solo entran al recompilar (con
`go run .` basta reiniciar el emitter).

Ya llevan `Cache-Control: no-cache`, así que no hace falta forzar la recarga del
navegador.
