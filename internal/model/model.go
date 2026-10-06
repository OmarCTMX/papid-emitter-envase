// Package model define las estructuras de datos del emitter.
package model

// Estados posibles de cada espacio.
const (
	EstadoEmpty   = "empty"
	EstadoPending = "pending"
	EstadoActive  = "active"
)

// TotalSlots es la cantidad fija de espacios por máquina.
const TotalSlots = 3

// EventoRFID es el mensaje de login/logout del lector.
type EventoRFID struct {
	Tag     string `json:"tag"`
	UsbPort string `json:"usb_port"`
}

// OrderDetails son los datos de la orden que viaja en papid.admin.signed.
// Todos los campos son texto porque así llegan en el mensaje original.
// Los opcionales llevan omitempty para no ensuciar el JSON que publicamos
// cuando el emisor no los manda.
type OrderDetails struct {
	OrderID     string `json:"order_id,omitempty"`
	DocNum      string `json:"doc_num,omitempty"`
	ProductName string `json:"product_name,omitempty"`
	Quantity    string `json:"quantity,omitempty"`
	Units       string `json:"units,omitempty"`
	// Status es el de la ORDEN (Cerrada, Abierta, ...), no el del personal.
	Status        string `json:"status,omitempty"`
	LotesActuales string `json:"lotes_actuales"`
	LotesTotales  string `json:"lotes_totales"`
}

// MensajeAsignacion es el mensaje de papid.admin.signed / unsigned y también
// de papid.admin.asignacion.
//
// La diferencia entre los dos primeros y el tercero es la ORDEN:
//   - signed / unsigned: traen order_details y lo aplican.
//   - asignacion: solo trae personal, la orden de la máquina no se toca.
//
// Por eso los campos de la orden pueden llegar vacíos y hay que respetar lo
// que ya estaba guardado (ver aplicarAsignacion en multistore).
type MensajeAsignacion struct {
	MachineCode string `json:"machine_code"`
	// MachineName es el nombre legible de la máquina (ej. "A2i - 3 | Revoltura").
	// Solo lo manda papid.admin.asignacion.
	MachineName  string            `json:"machine_name,omitempty"`
	MachineID    string            `json:"machine_id"`
	IsActive     bool              `json:"is_active"`
	IsFrozen     bool              `json:"is_frozen"`
	OrderDetails OrderDetails      `json:"order_details"`
	Personnel    []PersonaAsignada `json:"personnel"`
	// Encargado llega en papid.admin.asignacion, normalmente en null. Se deja
	// declarado para no perderlo si algún día trae datos.
	Encargado *PersonaAsignada `json:"encargado,omitempty"`
}

// PersonaAsignada es cada elemento del array "personnel" recibido.
type PersonaAsignada struct {
	Name             string `json:"name"`
	TipoAsignacion   string `json:"tipo_asignacion"`
	TipoAsignacionID int    `json:"tipo_asignacion_id"`
	NfcTag           string `json:"nfc_tag"`
	Status           string `json:"status"`
	EmployeeID       string `json:"employee_id"`
}

// MensajeSync es lo que publicamos en papid.personalui.sync.
type MensajeSync struct {
	MachineCode string        `json:"machine_code"`
	Personnel   []PersonaSync `json:"personnel"`
	// DefMaquinas: si está presente (modo DEF_MAQUINAS), los dashboards deben
	// filtrar por code EXACTO contra esta lista en vez de por prefijo.
	DefMaquinas []string `json:"def_maquinas,omitempty"`
	// OrderDetails es la orden de ESTA sub-máquina, tal como llegó en su
	// papid.admin.signed. Solo viaja en papid.emitter.<sub-máquina>, donde
	// machine_code identifica a una sola máquina y la orden no es ambigua.
	//
	// En los mensajes de unión (papid.emitter.sync, papid.emitter.dashboard,
	// papid.personalui.sync) va en nil y el campo desaparece del JSON: ahí el
	// personal es la unión de las sub-máquinas y cada una tiene su propia
	// orden, así que un solo campo no significaría nada.
	//
	// Es puntero para distinguir "este subject no manda la orden" (campo
	// ausente) de "no hay orden asignada" (campo presente y sin identidad).
	// Al desasignar se manda la orden vacía, no se omite el campo.
	OrderDetails *OrderDetails `json:"order_details,omitempty"`
}

// MensajeFull es lo que publicamos en papid.personalui.full.
type MensajeFull struct {
	IsActive     bool          `json:"is_active"`
	IsFrozen     bool          `json:"is_frozen"`
	MachineCode  string        `json:"machine_code"`
	MachineID    string        `json:"machine_id"`
	OrderDetails OrderDetails  `json:"order_details"`
	Personnel    []PersonaSync `json:"personnel"`
}

// PersonaSync es cada elemento del "personnel" que publicamos.
type PersonaSync struct {
	EmployeeID          string `json:"employee_id"`
	Nombre              string `json:"nombre,omitempty"`
	Rol                 string `json:"rol,omitempty"`
	FechaHoraInicioReal string `json:"fecha_hora_inicio_real,omitempty"`
	Status              string `json:"status"`
	TipoAsignacionID    int    `json:"tipo_asignacion_id"`
	UsbPort             string `json:"usb_port,omitempty"`
	// NfcTag es la tarjeta de la persona, tal como llegó en el signed. Se
	// publica para que el historial (papid-datosPersonal) registre con qué
	// tarjeta trabajó cada quien. Va con omitempty: los consumidores que no lo
	// usan no ven diferencia.
	NfcTag   string   `json:"nfc_tag,omitempty"`
	Maquinas []string `json:"maquinas,omitempty"` // sub-máquinas donde está asignado
}
