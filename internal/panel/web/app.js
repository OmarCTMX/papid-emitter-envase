// ============================================================
//  Monitor del Emitter
//  Pide el estado a /api/estado cada 2s y manda las simulaciones
//  a /api/simular.
// ============================================================

const PUERTOS = ["3-1", "3-3", "3-4"];
let cfgCargada = false;

// Personal real del KV PAPID_PERSONAL, para el dropdown de nombres del
// formulario de asignación. Se pide una vez al cargar la página.
// Cada elemento: { id, nombre, nombreCompleto, nfc_tag, machines }
let directorio = [];

// ---------- Utilidades ----------

// Escapa el texto antes de meterlo al HTML.
function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// Muestra un aviso flotante abajo a la derecha.
function aviso(msg, esError) {
  const a = document.getElementById("aviso");
  a.textContent = msg;
  a.className = esError ? "err" : "";
  a.style.display = "block";
  clearTimeout(a._t);
  a._t = setTimeout(() => { a.style.display = "none"; }, 3500);
}

// ---------- Simulación de eventos ----------

// Manda la petición al servidor, que publica el evento en NATS.
async function simular(cuerpo) {
  try {
    const r = await fetch("/api/simular", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cuerpo),
    });
    const d = await r.json();
    aviso(d.mensaje, !d.ok);
    setTimeout(cargar, 500);
  } catch (e) {
    aviso("Error: " + e.message, true);
  }
}

// Asignación (signed o forced) con el personal del formulario.
function simularSigned(accion) {
  const filas = [...document.querySelectorAll("#sg-personal tr")];
  const personal = filas.map(f => {
    const i = f.querySelectorAll("input");
    return { employee_id: i[0].value, nombre: i[1].value, rol: i[2].value, tag: i[3].value };
  }).filter(p => p.nombre && p.tag);

  simular({
    accion: accion,
    machine_code: document.getElementById("sg-maquina").value.trim(),
    machine_id: document.getElementById("sg-id").value.trim(),
    // Sin order_details: el servidor hereda la orden que ya tenía la máquina.
    personal: personal,
  });
}

// Desasigna la máquina completa.
function simularUnsigned() {
  const m = document.getElementById("sg-maquina").value.trim();
  if (!m) {
    aviso("Escribe la máquina a desasignar", true);
    return;
  }
  simular({ accion: "unsigned", machine_code: m });
}

// Deja la máquina con su personal pero SIN orden. El servidor arma el signed
// con el personal que ya tiene el store, así que no depende del formulario.
function simularQuitarOrden() {
  const m = document.getElementById("sg-maquina").value.trim();
  if (!m) {
    aviso("Escribe la máquina a la que se le quita la orden", true);
    return;
  }
  simular({ accion: "quitar_orden", machine_code: m });
}

// Tarjeta suelta (para probar el intruso).
function simularSuelto(accion) {
  simular({
    accion: accion,
    tag: document.getElementById("lb-tag").value.trim(),
    usb_port: document.getElementById("lb-puerto").value,
  });
}

// Poner o quitar la tarjeta de una persona concreta.
function tarjeta(accion, tag, puerto) {
  simular({ accion: accion, tag: tag, usb_port: puerto });
}

// Desasigna a UNA persona: el servidor manda un signed con el resto.
function quitarUno(empleado) {
  simular({ accion: "quitar_uno", employee_id: empleado });
}

// ---------- Pintado ----------

// Dropdown con los nombres reales del KV PAPID_PERSONAL. Va al lado del input
// de nombre: elegir a alguien llena la fila, pero el input sigue siendo libre.
// Si el directorio viene vacío (bucket ausente) no se pinta nada.
function dropdownDirectorio() {
  if (!directorio.length) return '';
  const opts = directorio.map((o, i) =>
    '<option value="' + i + '">' + esc(o.nombre || o.nombreCompleto) + '</option>'
  ).join("");
  return '<select class="dd-nombre" onchange="elegirDelDirectorio(this)" ' +
    'title="Personal real del KV PAPID_PERSONAL">' +
    '<option value="">— del KV —</option>' + opts + '</select>';
}

// Llena la fila con los datos del operador elegido. El rol no está en el
// catálogo, así que ese input no se toca.
function elegirDelDirectorio(sel) {
  if (sel.value === "") return;
  const op = directorio[Number(sel.value)];
  if (!op) return;

  const inputs = sel.closest("tr").querySelectorAll("input");
  inputs[0].value = op.id || "";                        // employee_id
  inputs[1].value = op.nombre || op.nombreCompleto || ""; // Nombre
  inputs[3].value = op.nfc_tag || "";                   // nfc_tag
}

function filaPersonalEditable(emp, nom, rol, tag) {
  return '<tr>' +
    '<td><input placeholder="' + esc(emp) + '"></td>' +
    '<td><div class="celda-nombre">' +
      '<input placeholder="' + esc(nom) + '">' + dropdownDirectorio() +
    '</div></td>' +
    '<td><input placeholder="' + esc(rol) + '"></td>' +
    '<td><input placeholder="' + esc(tag) + '"></td>' +
    '</tr>';
}

// Bloque de configuración del emitter.
function pintarConfig(d) {
  const codes = (d.machine_codes && d.machine_codes.length)
    ? d.machine_codes.join(", ")
    : "(ninguna)";

  document.getElementById("cfg").innerHTML =
    '<div>Envasadoras: <b>' + esc(codes) + '</b></div>' +
    '<div>Cada una es independiente (su propio personal y orden).</div>';
}

// Tarjetas de las máquinas registradas en el KV.
function pintarMaquinas(d) {
  const cont = document.getElementById("maquinas");
  if (!d.maquinas || !d.maquinas.length) {
    cont.innerHTML = '<p class="vacio">Todavía no hay máquinas en el KV. Manda una asignación abajo.</p>';
    return;
  }

  cont.innerHTML = d.maquinas.map(m => {
    const personas = (m.personal || []).map(p =>
      '<div class="persona">' +
        '<span class="nom">' + esc(p.nombre) +
          '<br><span class="puerto">' + esc(p.rol) + '</span></span>' +
        (p.usb_port && p.status === "Activo"
          ? '<span class="puerto">' + esc(p.usb_port) + '</span>' : '') +
        '<span class="badge ' + esc(p.status) + '">' + esc(p.status) + '</span>' +
      '</div>').join("") || '<p class="vacio">Sin personal</p>';

    return '<div class="maq">' +
      '<h3>' + esc(m.sub_maquina) +
        '<span class="badge ' + (m.is_active ? "Activo" : "Asignado") + '">' +
          (m.is_active ? "completa" : "pendiente") + '</span>' +
      '</h3>' +
      (m.machine_name ? '<div class="maq-nombre">' + esc(m.machine_name) + '</div>' : '') +
      '<div class="meta">máquina: ' + esc(m.machine_code) +
        ' · id: ' + esc(m.machine_id || "—") +
        ' · ' + esc(m.actualizado || "") + '</div>' +
      personas +
      bloqueOrden(m.order_details) + '</div>';
  }).join("");
}

// Orden de la máquina, debajo del personal. El personal es el mismo en las
// sub-máquinas de un grupo, pero la orden es distinta en cada una: es el dato
// que las diferencia.
//
// order_details llega completo desde papid.admin.signed:
//   order_id, doc_num, product_name, quantity, units, status,
//   lotes_actuales, lotes_totales
function bloqueOrden(orden) {
  const o = orden || {};

  // Se considera "sin orden" cuando no llegó ningún campo con contenido.
  const hayAlgo = o.doc_num || o.product_name || o.quantity ||
    o.status || o.lotes_totales || o.lotes_actuales || o.order_id;
  if (!hayAlgo) {
    return '<div class="orden vacia">Orden: sin datos</div>';
  }

  const hechos = Number(o.lotes_actuales);
  const meta = Number(o.lotes_totales);
  const pct = (meta > 0 && !isNaN(hechos)) ? Math.min(100, (hechos / meta) * 100) : 0;

  // Encabezado: doc_num identifica la orden ante el operador; el status va
  // como badge aparte porque es el de la ORDEN, no el del personal.
  let html = '<div class="orden">' +
    '<div class="orden-cab">' +
      '<span class="orden-tit">Orden ' + esc(o.doc_num || "—") + '</span>' +
      (o.status ? '<span class="badge orden-estado">' + esc(o.status) + '</span>' : '') +
    '</div>';

  if (o.product_name) {
    html += '<div class="orden-prod">' + esc(o.product_name) + '</div>';
  }

  // Cantidad y clave interna, en una tira de datos secundarios.
  const datos = [];
  if (o.quantity) {
    datos.push(esc(o.quantity) + (o.units ? ' ' + esc(o.units) : ''));
  }
  if (o.order_id) {
    datos.push('id: ' + esc(o.order_id));
  }
  if (datos.length) {
    html += '<div class="orden-datos">' + datos.join(' · ') + '</div>';
  }

  html += '<div class="orden-cifras">' +
      '<span class="orden-tit">Lotes</span>' +
      '<span class="orden-lotes"><b>' + esc(o.lotes_actuales || "—") + '</b> / ' +
        esc(o.lotes_totales || "—") + '</span>' +
    '</div>' +
    '<div class="orden-barra"><span style="width:' + pct.toFixed(1) + '%"></span></div>' +
  '</div>';

  return html;
}

// ---------- Órdenes por máquina ----------

// Una tira a todo el ancho por sub-máquina, con el order_details completo.
// A diferencia del bloque compacto de la tarjeta, aquí no se oculta ningún
// campo: es la vista para consultar la orden a detalle.
function pintarOrdenes(d) {
  const cont = document.getElementById("ordenes");
  if (!d.maquinas || !d.maquinas.length) {
    cont.innerHTML = '<p class="vacio">Sin máquinas en el KV, así que no hay órdenes.</p>';
    return;
  }
  cont.innerHTML = d.maquinas.map(m => tiraOrden(m)).join("");
}

function tiraOrden(m) {
  const o = m.order_details || {};
  const hayOrden = o.doc_num || o.product_name || o.quantity ||
    o.status || o.lotes_totales || o.lotes_actuales || o.order_id;

  const cabecera = '<div class="ord-cab">' +
    '<span class="ord-maq">' + esc(m.sub_maquina) + '</span>' +
    (hayOrden
      ? '<span class="ord-folio">Orden ' + esc(o.doc_num || "—") + '</span>' +
        (o.status ? '<span class="badge orden-estado">' + esc(o.status) + '</span>' : '')
      : '<span class="ord-folio">sin orden asignada</span>') +
    '</div>';

  if (!hayOrden) {
    return '<div class="ord vacia">' + cabecera +
      '<p class="vacio">Esta máquina no ha recibido un signed con orden.</p></div>';
  }

  const hechos = Number(o.lotes_actuales);
  const meta = Number(o.lotes_totales);
  const pct = (meta > 0 && !isNaN(hechos)) ? Math.min(100, (hechos / meta) * 100) : 0;

  return '<div class="ord">' + cabecera +
    '<div class="ord-prod">' + esc(o.product_name || "— sin producto —") + '</div>' +
    '<div class="ord-datos">' +
      dato("Cantidad", o.quantity) +
      dato("Unidades", o.units) +
      dato("Lotes", (o.lotes_actuales || "—") + " / " + (o.lotes_totales || "—")) +
      dato("Avance", meta > 0 ? pct.toFixed(0) + "%" : "—") +
      dato("order_id", o.order_id) +
      dato("machine_id", m.machine_id) +
      dato("Actualizado", m.actualizado) +
    '</div>' +
    '<div class="orden-barra"><span style="width:' + pct.toFixed(1) + '%"></span></div>' +
  '</div>';
}

// Celda de dato: etiqueta arriba, valor abajo. Un guion cuando viene vacío,
// para que la rejilla no se descuadre entre máquinas.
function dato(etiqueta, valor) {
  return '<div class="ord-dato">' +
    '<span class="ord-et">' + esc(etiqueta) + '</span>' +
    '<span class="ord-val">' + esc(valor || "—") + '</span>' +
  '</div>';
}

// Tabla del personal de esta máquina, con su tag, para simular tarjetas.
function pintarMiPersonal(d) {
  const tb = document.querySelector("#tabla-personal tbody");
  if (!d.mi_personal || !d.mi_personal.length) {
    tb.innerHTML = '<tr><td colspan="6" class="vacio">Esta máquina no tiene personal asignado.</td></tr>';
    return;
  }

  tb.innerHTML = d.mi_personal.map(p => {
    const opts = PUERTOS.map(x =>
      '<option' + (x === p.usb_port ? ' selected' : '') + '>' + x + '</option>').join("");
    const sel = 'sel_' + p.employee_id;
    const tag = esc(p.tag);

    return '<tr>' +
      '<td>' + esc(p.nombre) +
        '<br><span class="puerto">' + esc(p.employee_id) + '</span></td>' +
      '<td>' + esc(p.rol) + '</td>' +
      '<td><span class="badge ' + esc(p.status) + '">' + esc(p.status) + '</span></td>' +
      '<td><span class="puerto">' + tag + '</span></td>' +
      '<td><select id="' + sel + '">' + opts + '</select></td>' +
      '<td>' +
        '<button class="v" onclick="tarjeta(\'login\',\'' + tag + '\',document.getElementById(\'' + sel + '\').value)">Poner</button> ' +
        '<button class="n" onclick="tarjeta(\'logout\',\'' + tag + '\',document.getElementById(\'' + sel + '\').value)">Quitar</button> ' +
        '<button class="r" onclick="quitarUno(\'' + esc(p.employee_id) + '\')">Desasignar</button>' +
      '</td></tr>';
  }).join("");
}

// Prellena el formulario de asignación (solo la primera vez).
function prellenarFormulario(d) {
  if (cfgCargada) return;
  cfgCargada = true;

  const destino = (d.machine_codes && d.machine_codes.length)
    ? d.machine_codes[0] : "envasadora-1";
  document.getElementById("sg-maquina").value = destino;

  document.getElementById("sg-personal").innerHTML =
    filaPersonalEditable("V000", "Nombre (ej. Omar Ramirez)", "Rol (ej. Revolturero)", "Tag (ej. 94:D1:81:A9)") +
    filaPersonalEditable("V0912", "Nombre (ej. Michel Davalos)", "Rol (ej. Montacargas)", "Tag (ej. 14:6A:F3:A9)") +
    filaPersonalEditable("V1012", "Nombre (ej. Gabriel Guevara)", "Rol (ej. Revolturero)", "Tag (ej. 0D:9B:BE:3C)");
}

// ---------- Ciclo principal ----------

async function cargar() {
  let d;
  try {
    d = await (await fetch("/api/estado")).json();
  } catch (e) {
    return; // sin conexión: se reintenta en el próximo tick
  }

  pintarConfig(d);
  prellenarFormulario(d);
  pintarMaquinas(d);
  pintarOrdenes(d);
  pintarMiPersonal(d);
}

// Trae el personal real del KV PAPID_PERSONAL. Se pide una sola vez: el
// catálogo cambia poco y el formulario se arma en el primer tick.
async function cargarDirectorio() {
  try {
    const d = await (await fetch("/api/directorio")).json();
    directorio = d.operadores || [];
    // El servidor ordena por bytes, así que los acentos quedan fuera de sitio
    // ("Álvarez" después de "Estrada"). Aquí se reordena con colación española.
    directorio.sort((a, b) =>
      (a.nombre || "").localeCompare(b.nombre || "", "es"));
  } catch (e) {
    directorio = []; // sin catálogo: el formulario queda solo con inputs libres
  }
}

// El directorio se resuelve ANTES del primer cargar() porque prellenarFormulario
// arma las filas una única vez y necesita las opciones del dropdown.
(async () => {
  await cargarDirectorio();
  cargar();
  setInterval(cargar, 2000);
})();
