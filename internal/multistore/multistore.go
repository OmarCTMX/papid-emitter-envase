// Package multistore mantiene el estado ÚNICO del personal de una máquina
// física (la del emitter). Una persona aparece UNA sola vez, con un solo
// estado (verde/naranja), pero puede pertenecer a varias sub-máquinas
// (ej. a2i-1-r, a2i-2-r). El campo Maquinas guarda ese conjunto.
//
// Los mensajes de asignación (signed/unsigned) llegan por sub-máquina y se
// FUSIONAN aquí: no se sobreescriben. Así se puede saber en qué sub-máquinas
// está asignada cada persona.
package multistore

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"papid-envasadoras-emitter/internal/model"
)

// Trabajador es el estado de una persona en la máquina.
type Trabajador struct {
	ID                  string
	Nombre              string
	Rol                 string
	Estado              string // empty | pending | active
	EmployeeID          string
	TipoAsignacionID    int
	YaRegistrado        bool
	FechaHoraInicioReal string
	UsbPort             string
	Maquinas            map[string]bool // sub-máquinas donde está asignado
}

// maxOrdenes acota el mapa de órdenes: sin límite, un machine_code mal formado
// lo haría crecer para siempre.
const maxOrdenes = 64

// Store guarda el estado único del personal de la máquina del emitter.
type Store struct {
	mu          sync.RWMutex
	machineCode string // máquina física (ej. a2i)
	machineID   string
	// machineName es el nombre legible que manda papid.admin.asignacion
	// (ej. "A2i - 3 | Revoltura").
	machineName string
	isFrozen    bool
	// orderDetails es la orden del ÚLTIMO signed recibido. Solo se usa para
	// papid.personalui.full, que no distingue sub-máquinas.
	orderDetails model.OrderDetails
	// ordenes guarda la orden de CADA sub-máquina (a2i-1-r, a2i-2-r, ...).
	// El personal se comparte entre las sub-máquinas de una máquina física,
	// pero la orden es distinta en cada una, así que no puede ser un solo
	// campo: se sobreescribiría con cada signed.
	ordenes  map[string]model.OrderDetails
	personal [model.TotalSlots]Trabajador
	onChange func()
}

// New crea un store con los espacios vacíos.
func New(machineCode string) *Store {
	s := &Store{
		machineCode: machineCode,
		ordenes:     make(map[string]model.OrderDetails),
	}
	for i := range s.personal {
		s.personal[i] = vacante()
	}
	return s
}

// Restaurar reconstruye el estado desde el KV al arrancar (personal + orden de
// ESTA máquina), para no esperar un nuevo mensaje tras un reinicio. No dispara
// onChange: solo deja el estado listo para la primera publicación.
func (s *Store) Restaurar(machineID, machineName string, isFrozen bool,
	orden model.OrderDetails, personal []model.PersonaSync) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.machineID = machineID
	s.machineName = machineName
	s.isFrozen = isFrozen
	s.orderDetails = orden
	s.ordenes[s.machineCode] = orden

	// Vacía los slots y recoloca el personal guardado.
	for i := range s.personal {
		s.personal[i] = vacante()
	}
	for i, p := range personal {
		if i >= model.TotalSlots {
			break
		}
		estado := model.EstadoPending
		yaReg := false
		switch p.Status {
		case "Activo":
			estado, yaReg = model.EstadoActive, true
		case "Inactivo":
			estado, yaReg = model.EstadoPending, true
		}
		maqs := map[string]bool{s.machineCode: true}
		for _, m := range p.Maquinas {
			maqs[m] = true
		}
		s.personal[i] = Trabajador{
			ID:                  p.NfcTag,
			Nombre:              p.Nombre,
			Rol:                 valorODefault(p.Rol, "—"),
			Estado:              estado,
			EmployeeID:          p.EmployeeID,
			TipoAsignacionID:    p.TipoAsignacionID,
			YaRegistrado:        yaReg,
			FechaHoraInicioReal: p.FechaHoraInicioReal,
			UsbPort:             p.UsbPort,
			Maquinas:            maqs,
		}
	}
}

// SetOnChange registra el callback de cambios (protegido con lock: antes se
// escribía y leía sin sincronizar, lo que era un race de datos).
func (s *Store) SetOnChange(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// notificar dispara el callback. Se llama SIEMPRE con el lock liberado.
func (s *Store) notificar() {
	s.mu.RLock()
	fn := s.onChange
	s.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// maquinas son las sub-máquinas a las que se asigna este personal:
//   - Modo prefijo: solo la que vino en el signed (ej. ["a2i-1-r"]).
//   - Con DEF_MAQUINAS: TODAS las declaradas, porque comparten personal
//     (ej. ["a2i-1-r","a2i-2-r","a2i-3-r"]).
//
// Asignar viene de papid.admin.signed: trae personal Y orden.
func (s *Store) Asignar(maquinas []string, msg model.MensajeAsignacion) {
	s.aplicarAsignacion(maquinas, msg, false, true)
}

// AsignarForzado hace lo mismo pero pone en verde a los entrantes.
func (s *Store) AsignarForzado(maquinas []string, msg model.MensajeAsignacion) {
	s.aplicarAsignacion(maquinas, msg, true, true)
}

// AsignarPersonal viene de papid.admin.asignacion: define SOLO el roster de
// personal y deja intacta la orden de la máquina.
//
// Con personnel vacío se queda la máquina sin gente pero CON su orden, que es
// justo lo que no permite hacer un unsigned (ese borra las dos cosas).
func (s *Store) AsignarPersonal(maquinas []string, msg model.MensajeAsignacion) {
	s.aplicarAsignacion(maquinas, msg, false, false)
}

// conOrden decide si el mensaje manda sobre la orden de la máquina. Los signed
// sí; papid.admin.asignacion no, porque no la trae y aplicarla la borraría.
func (s *Store) aplicarAsignacion(maquinas []string, msg model.MensajeAsignacion, forzar, conOrden bool) {
	if len(maquinas) == 0 {
		return
	}
	s.mu.Lock()
	// Solo se sobreescriben si vienen con dato: papid.admin.asignacion no
	// manda machine_id, y aplicar el vacío perdería el que dejó el signed.
	if msg.MachineID != "" {
		s.machineID = msg.MachineID
	}
	if msg.MachineName != "" {
		s.machineName = msg.MachineName
	}
	s.isFrozen = msg.IsFrozen

	if conOrden {
		s.orderDetails = msg.OrderDetails

		// La orden se guarda bajo la sub-máquina que vino EN EL MENSAJE, no
		// bajo todas las de destino: el personal se comparte, la orden no.
		if msg.MachineCode != "" {
			if _, ya := s.ordenes[msg.MachineCode]; ya || len(s.ordenes) < maxOrdenes {
				s.ordenes[msg.MachineCode] = msg.OrderDetails
			} else {
				log.Printf("[emitter] Límite de %d órdenes alcanzado, se ignora la de %q",
					maxOrdenes, msg.MachineCode)
			}
		}
	}

	// El encargado viaja en el personnel pero no ocupa espacio: los espacios
	// son para los trabajadores. Sin este filtro, un roster de 4 con encargado
	// dejaba fuera a un trabajador real por falta de slot.
	personal := sinEncargados(msg.Personnel)

	// employee_ids que vienen en este signed de subCode.
	entrantes := make(map[string]bool)
	for _, p := range personal {
		entrantes[p.EmployeeID] = true
	}

	// 1. Quitar estas máquinas a quienes ya NO vienen en este signed.
	//    Si a alguien no le queda ninguna máquina, se vacía su espacio.
	for i := range s.personal {
		t := &s.personal[i]
		if t.Estado == model.EstadoEmpty || entrantes[t.EmployeeID] {
			continue
		}
		quitoAlguna := false
		for _, mc := range maquinas {
			if t.Maquinas[mc] {
				delete(t.Maquinas, mc)
				quitoAlguna = true
			}
		}
		if quitoAlguna && len(t.Maquinas) == 0 {
			s.personal[i] = vacante()
		}
	}

	// 2. Agregar / actualizar los entrantes.
	for _, p := range personal {
		idx := s.buscarPorEmployee(p.EmployeeID)
		if idx == -1 {
			idx = s.slotLibre()
			if idx == -1 {
				log.Printf("[emitter] Sin slot libre para %s (máquina llena)", p.Name)
				continue
			}
			s.personal[idx] = Trabajador{
				Estado:   model.EstadoPending,
				Maquinas: map[string]bool{},
			}
		}
		t := &s.personal[idx]
		if t.Maquinas == nil {
			t.Maquinas = map[string]bool{}
		}
		// Se asigna a TODAS las máquinas destino.
		for _, mc := range maquinas {
			t.Maquinas[mc] = true
		}
		// Datos base (se refrescan siempre).
		t.ID = p.NfcTag
		t.Nombre = p.Name
		t.Rol = valorODefault(p.TipoAsignacion, "—")
		t.EmployeeID = p.EmployeeID
		t.TipoAsignacionID = p.TipoAsignacionID
		if forzar {
			t.Estado = model.EstadoActive
			t.YaRegistrado = true
		}
	}
	s.mu.Unlock()
	if conOrden {
		log.Printf("[emitter] Asignación fusionada con orden (sub-máquinas %v, %d trabajadores)",
			maquinas, len(personal))
	} else {
		log.Printf("[emitter] Personal actualizado sin tocar la orden (sub-máquinas %v, %d trabajadores)",
			maquinas, len(personal))
	}
	s.notificar()
}

// Desasignar quita las sub-máquinas indicadas a todos. Quien quede sin
// máquinas se vacía.
func (s *Store) Desasignar(maquinas []string) {
	if len(maquinas) == 0 {
		return
	}
	s.mu.Lock()
	for i := range s.personal {
		t := &s.personal[i]
		if t.Estado == model.EstadoEmpty {
			continue
		}
		quitoAlguna := false
		for _, mc := range maquinas {
			if t.Maquinas[mc] {
				delete(t.Maquinas, mc)
				quitoAlguna = true
			}
		}
		if quitoAlguna && len(t.Maquinas) == 0 {
			s.personal[i] = vacante()
		}
	}
	// Sin personal ya no hay orden que mostrar en esas sub-máquinas.
	for _, mc := range maquinas {
		delete(s.ordenes, mc)
	}
	s.mu.Unlock()
	log.Printf("[emitter] Desasignadas sub-máquinas %v", maquinas)
	s.notificar()
}

// QuitarOrden borra la orden de una sub-máquina sin tocar el personal.
// Lo dispara papid.admin.quitarorden: el admin retira la orden de la máquina
// pero la gente sigue ahí (al contrario de Desasignar, que borra las dos).
func (s *Store) QuitarOrden(subMaquina string) {
	if subMaquina == "" {
		return
	}
	s.mu.Lock()
	if _, tiene := s.ordenes[subMaquina]; tiene {
		delete(s.ordenes, subMaquina)
		s.orderDetails = model.OrderDetails{} // el "último" también se limpia
		s.mu.Unlock()
		log.Printf("[emitter] Orden quitada de %s (personal intacto)", subMaquina)
		s.notificar()
		return
	}
	s.mu.Unlock()
}

// RegistrarPorID pone en verde (active) al trabajador con ese tag.
// Devuelve true si es válido (se registró o ya estaba activo).
func (s *Store) RegistrarPorID(tag, usbPort string) bool {
	s.mu.Lock()
	cambiado := false
	yaActivo := false
	for i := range s.personal {
		t := &s.personal[i]
		if t.ID != tag {
			continue
		}
		if t.Estado == model.EstadoPending {
			t.Estado = model.EstadoActive
			t.YaRegistrado = true
			t.UsbPort = usbPort
			if t.FechaHoraInicioReal == "" {
				t.FechaHoraInicioReal = time.Now().Format(time.RFC3339)
			}
			cambiado = true
			log.Printf("[emitter] Registrado: %s", t.Nombre)
			break
		}
		if t.Estado == model.EstadoActive {
			yaActivo = true
		}
	}
	s.mu.Unlock()

	if cambiado {
		s.notificar()
		return true
	}
	return yaActivo
}

// RetirarPorID regresa a pending (naranja) al trabajador activo con ese tag.
func (s *Store) RetirarPorID(tag string) bool {
	s.mu.Lock()
	cambiado := false
	for i := range s.personal {
		t := &s.personal[i]
		if t.ID == tag && t.Estado == model.EstadoActive {
			t.Estado = model.EstadoPending
			cambiado = true
			log.Printf("[emitter] Tarjeta retirada: %s", t.Nombre)
			break
		}
	}
	s.mu.Unlock()

	if cambiado {
		s.notificar()
	}
	return cambiado
}

// Trabajadores devuelve una copia del personal, INCLUYENDO el nfc_tag.
// La usa el panel de pruebas para poder simular tarjetas por persona (el tag
// no viaja en el KV ni en los mensajes publicados).
func (s *Store) Trabajadores() []Trabajador {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Trabajador, 0, model.TotalSlots)
	for _, t := range s.personal {
		if t.Estado == model.EstadoEmpty {
			continue
		}
		copia := t
		copia.Maquinas = make(map[string]bool, len(t.Maquinas))
		for k, v := range t.Maquinas {
			copia.Maquinas[k] = v
		}
		out = append(out, copia)
	}
	return out
}

// StatusDe expone el status legible de un trabajador (Activo/Inactivo/Asignado).
func StatusDe(t Trabajador) string { return statusDe(t) }

// MachineID devuelve el machine_id de la última asignación recibida.
// Lo usa el panel para reconstruir un signed sin perder ese dato.
func (s *Store) MachineID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.machineID
}

// OrdenDe devuelve la orden de una sub-máquina. Si esa sub-máquina no ha
// recibido ningún signed, devuelve la orden vacía.
func (s *Store) OrdenDe(subMaquina string) model.OrderDetails {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ordenes[subMaquina]
}

// MachineName devuelve el nombre legible de la máquina, si algún
// papid.admin.asignacion lo trajo.
func (s *Store) MachineName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.machineName
}

// BuildSync arma el sync clásico (sin el campo maquinas).
func (s *Store) BuildSync() model.MensajeSync {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return model.MensajeSync{MachineCode: s.machineCode, Personnel: s.personnel(false)}
}

// BuildEmitterSync arma el sync para papid.emitter.sync (con maquinas).
func (s *Store) BuildEmitterSync() model.MensajeSync {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return model.MensajeSync{MachineCode: s.machineCode, Personnel: s.personnel(true)}
}

// BuildFull arma el mensaje completo (papid.personalui.full).
func (s *Store) BuildFull() model.MensajeFull {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hayAsignados := false
	todosYaReg := true
	for _, t := range s.personal {
		if t.Estado == model.EstadoEmpty {
			continue
		}
		hayAsignados = true
		if !t.YaRegistrado {
			todosYaReg = false
		}
	}
	return model.MensajeFull{
		IsActive:     hayAsignados && todosYaReg,
		IsFrozen:     s.isFrozen,
		MachineCode:  s.machineCode,
		MachineID:    s.machineID,
		OrderDetails: s.orderDetails,
		Personnel:    s.personnel(false),
	}
}

// personnel construye la lista de personal (omitiendo los espacios vacíos).
// Si conMaquinas es true se agrega el campo "maquinas", que solo viaja en
// papid.emitter.sync y papid.emitter.dashboard.
// Debe llamarse con el lock tomado.
func (s *Store) personnel(conMaquinas bool) []model.PersonaSync {
	out := make([]model.PersonaSync, 0, model.TotalSlots)
	for _, t := range s.personal {
		if t.Estado == model.EstadoEmpty {
			continue
		}
		p := model.PersonaSync{
			EmployeeID:          t.EmployeeID,
			Nombre:              t.Nombre,
			Rol:                 t.Rol,
			FechaHoraInicioReal: t.FechaHoraInicioReal,
			Status:              statusDe(t),
			TipoAsignacionID:    t.TipoAsignacionID,
			UsbPort:             t.UsbPort,
			NfcTag:              t.ID, // t.ID es el nfc_tag del trabajador
		}
		if conMaquinas {
			p.Maquinas = maquinasOrdenadas(t.Maquinas)
		}
		out = append(out, p)
	}
	return out
}

func (s *Store) buscarPorEmployee(empID string) int {
	for i := range s.personal {
		if s.personal[i].Estado != model.EstadoEmpty && s.personal[i].EmployeeID == empID {
			return i
		}
	}
	return -1
}

func (s *Store) slotLibre() int {
	for i := range s.personal {
		if s.personal[i].Estado == model.EstadoEmpty {
			return i
		}
	}
	return -1
}

// rolEncargado es el tipo_asignacion que se excluye del roster. Al encargado
// no se le lleva control de tarjeta, solo a los trabajadores de la máquina.
const rolEncargado = "encargado"

// sinEncargados quita del roster a quien venga con tipo_asignacion "Encargado".
// Llega dentro del personnel de papid.admin.asignacion, pero no debe ocupar
// uno de los TotalSlots espacios ni aparecer en el dashboard.
func sinEncargados(personal []model.PersonaAsignada) []model.PersonaAsignada {
	out := make([]model.PersonaAsignada, 0, len(personal))
	for _, p := range personal {
		if strings.EqualFold(strings.TrimSpace(p.TipoAsignacion), rolEncargado) {
			log.Printf("[emitter] Ignorado (es encargado): %s", p.Name)
			continue
		}
		out = append(out, p)
	}
	return out
}

func statusDe(t Trabajador) string {
	switch {
	case t.Estado == model.EstadoActive:
		return "Activo"
	case t.YaRegistrado:
		return "Inactivo"
	default:
		return "Asignado"
	}
}

func maquinasOrdenadas(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func vacante() Trabajador {
	return Trabajador{Estado: model.EstadoEmpty}
}

func valorODefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
