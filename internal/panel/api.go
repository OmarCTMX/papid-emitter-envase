// API HTTP del emitter, pensada para que otros sistemas la consuman.
//
//	GET    /api/personal                      → todas las máquinas
//	GET    /api/personal/{maquina}            → una máquina
//	POST   /api/personal/{maquina}            → asignar personal
//	DELETE /api/personal/{maquina}            → desasignar la máquina completa
//	DELETE /api/personal/{maquina}/{empleado} → desasignar a una persona
//
// Las lecturas salen del KV (papid_personal_asignado) y las escrituras se
// hacen publicando los eventos NATS correspondientes (papid.admin.signed /
// unsigned), o sea el mismo camino que usa el sistema de asignación. La API no
// modifica el estado directamente: lo hace el emitter al recibir el evento.
package panel

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"papid-envasadoras-emitter/internal/model"
)

// registrarAPI engancha las rutas de la API en el mux.
func (p *Panel) registrarAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/personal", p.apiListar)
	mux.HandleFunc("GET /api/personal/{maquina}", p.apiObtener)
	mux.HandleFunc("POST /api/personal/{maquina}", p.apiAsignar)
	mux.HandleFunc("DELETE /api/personal/{maquina}", p.apiDesasignar)
	mux.HandleFunc("DELETE /api/personal/{maquina}/{empleado}", p.apiDesasignarUno)
}

// --- Cuerpos de petición y respuesta ---

// peticionAsignar es el cuerpo de POST /api/personal/{maquina}.
type peticionAsignar struct {
	MachineID string `json:"machine_id,omitempty"`
	Forzado   bool   `json:"forzado,omitempty"` // true = todos entran en verde
	// OrderDetails es la orden de ESTA máquina (opcional; si falta va 0/0).
	OrderDetails model.OrderDetails `json:"order_details,omitempty"`
	Personal     []personaAsignar   `json:"personal"`
}

type personaAsignar struct {
	EmployeeID string `json:"employee_id"`
	Nombre     string `json:"nombre"`
	Rol        string `json:"rol"`
	NfcTag     string `json:"nfc_tag"`
}

// resultado es la respuesta de las operaciones de escritura.
type resultado struct {
	OK      bool   `json:"ok"`
	Mensaje string `json:"mensaje"`
	Maquina string `json:"maquina,omitempty"`
	Subject string `json:"subject,omitempty"` // subject NATS donde se publicó
}

// --- Lecturas ---

func (p *Panel) apiListar(w http.ResponseWriter, r *http.Request) {
	if p.kv == nil {
		apiError(w, http.StatusServiceUnavailable, "el KV no está disponible")
		return
	}
	estados, err := p.kv.CargarTodas()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "no se pudo leer el KV: "+err.Error())
		return
	}
	if estados == nil {
		estados = nil // que salga [] y no null
	}
	escribirJSON(w, http.StatusOK, map[string]any{
		"maquinas": noNil(estados),
	})
}

func (p *Panel) apiObtener(w http.ResponseWriter, r *http.Request) {
	maquina := r.PathValue("maquina")
	if p.kv == nil {
		apiError(w, http.StatusServiceUnavailable, "el KV no está disponible")
		return
	}
	estado, err := p.kv.Cargar(maquina)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "no se pudo leer el KV: "+err.Error())
		return
	}
	if estado == nil {
		apiError(w, http.StatusNotFound,
			fmt.Sprintf("la máquina %q no tiene personal asignado", maquina))
		return
	}
	escribirJSON(w, http.StatusOK, estado)
}

// --- Escrituras ---

func (p *Panel) apiAsignar(w http.ResponseWriter, r *http.Request) {
	maquina := r.PathValue("maquina")

	var req peticionAsignar
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "JSON inválido: "+err.Error())
		return
	}
	if len(req.Personal) == 0 {
		apiError(w, http.StatusBadRequest,
			"el campo 'personal' viene vacío; para desasignar usa DELETE")
		return
	}

	// Se reutiliza el mismo armado de evento que el panel.
	sim := peticionSimular{
		Accion:       "signed",
		MachineCode:  maquina,
		MachineID:    req.MachineID,
		OrderDetails: req.OrderDetails,
	}
	if req.Forzado {
		sim.Accion = "forced"
	}
	for _, per := range req.Personal {
		sim.Personal = append(sim.Personal, personaSimula{
			EmployeeID: per.EmployeeID,
			Nombre:     per.Nombre,
			Rol:        per.Rol,
			Tag:        per.NfcTag,
		})
	}
	p.publicarYResponder(w, sim, fmt.Sprintf("Personal asignado a %s", maquina), maquina)
}

func (p *Panel) apiDesasignar(w http.ResponseWriter, r *http.Request) {
	maquina := r.PathValue("maquina")
	p.publicarYResponder(w,
		peticionSimular{Accion: "unsigned", MachineCode: maquina},
		fmt.Sprintf("Máquina %s desasignada", maquina), maquina)
}

func (p *Panel) apiDesasignarUno(w http.ResponseWriter, r *http.Request) {
	maquina := r.PathValue("maquina")
	empleado := r.PathValue("empleado")

	sim, err := p.quitarUno(peticionSimular{
		Accion:      "quitar_uno",
		MachineCode: maquina,
		EmployeeID:  empleado,
	})
	if err != nil {
		apiError(w, http.StatusNotFound, err.Error())
		return
	}
	p.publicarYResponder(w, sim,
		fmt.Sprintf("Empleado %s desasignado de %s", empleado, maquina), maquina)
}

// publicarYResponder arma el evento, lo publica y contesta.
func (p *Panel) publicarYResponder(w http.ResponseWriter, sim peticionSimular, mensajeOK, maquina string) {
	subject, payload, err := construirEvento(p.completarOrden(sim))
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := p.nc.Publish(subject, payload); err != nil {
		apiError(w, http.StatusBadGateway, "no se pudo publicar en NATS: "+err.Error())
		return
	}
	if err := p.nc.FlushTimeout(3 * time.Second); err != nil {
		log.Printf("[emitter] API: flush incompleto: %v", err)
	}
	log.Printf("[emitter] API: %s (%s)", mensajeOK, subject)
	escribirJSON(w, http.StatusOK, resultado{
		OK: true, Mensaje: mensajeOK, Maquina: maquina, Subject: subject,
	})
}

// --- Helpers ---

func escribirJSON(w http.ResponseWriter, codigo int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(codigo)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[emitter] API: error escribiendo la respuesta: %v", err)
	}
}

func apiError(w http.ResponseWriter, codigo int, mensaje string) {
	escribirJSON(w, codigo, resultado{OK: false, Mensaje: mensaje})
}

// noNil evita que un slice vacío se serialice como null.
func noNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
