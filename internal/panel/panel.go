// Package panel sirve el monitor web del emitter.
//
// Muestra las máquinas registradas (leídas del KV, así se ven también las de
// otros emitters) y permite SIMULAR los eventos de entrada — asignar, poner y
// quitar tarjeta, desasignar — publicándolos a NATS igual que se harían desde
// la terminal con `nats pub`.
//
// OJO: es una herramienta de PRUEBAS. Publica eventos de entrada
// (papid.admin.signed, papid.personal.login, ...), o sea suplanta al sistema
// de asignación y a los lectores. No expone el panel a una red no confiable:
// no tiene autenticación (ver PANEL_ADDR en el README).
package panel

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"

	"papid-envasadoras-emitter/internal/model"
	"papid-envasadoras-emitter/internal/multistore"
	"papid-envasadoras-emitter/internal/persistence"
)

// archivosWeb trae el HTML, el CSS y el JS del monitor. Van embebidos en el
// binario (go:embed), así que el emitter sigue siendo un solo ejecutable sin
// archivos externos, pero se editan como archivos normales.
//
//go:embed web
var archivosWeb embed.FS

// Subjects de entrada que el panel puede simular.
const (
	subjSigned      = "papid.admin.signed"
	subjForced      = "papid.admin.signed.forced"
	subjUnsigned    = "papid.admin.unsigned"
	subjLogin       = "papid.personal.login"
	subjLogout      = "papid.personal.logout"
	subjQuitarOrden = "papid.admin.quitarorden"
)

// Config son los datos del emitter que el panel necesita mostrar.
type Config struct {
	MachineCodes []string // envasadoras que atiende este emitter (independientes)
	Addr         string   // dirección donde escuchar (ej. ":3200")
}

// TrabajadorMaquina es un trabajador junto con la envasadora a la que pertenece.
// El gestor lo usa para que el panel liste el personal de TODAS las máquinas.
type TrabajadorMaquina struct {
	Code string
	T    multistore.Trabajador
}

// Gestor es lo que el panel necesita del emitter: resolver el store de una
// máquina (para heredar su orden al simular) y listar todo el personal.
// main.go implementa esta interfaz.
type Gestor interface {
	Store(code string) *multistore.Store
	Codes() []string
	Trabajadores() []TrabajadorMaquina
}

// Panel agrupa las dependencias del servidor web.
type Panel struct {
	cfg Config
	nc  *nats.Conn
	g   Gestor
	kv  *persistence.KV
	// dir es el catálogo de personal (KV PAPID_PERSONAL). Puede ser nil si el
	// bucket no existe; en ese caso el monitor funciona sin los dropdowns.
	dir *persistence.Directorio
}

func New(cfg Config, nc *nats.Conn, g Gestor, kv *persistence.KV,
	dir *persistence.Directorio) *Panel {
	return &Panel{cfg: cfg, nc: nc, g: g, kv: kv, dir: dir}
}

// Iniciar arranca el servidor en segundo plano.
func (p *Panel) Iniciar() {
	mux := http.NewServeMux()
	// Monitor web (uso humano).
	mux.HandleFunc("/", p.pagina)
	mux.Handle("GET /static/", estaticos())
	mux.HandleFunc("/api/estado", p.estado)
	mux.HandleFunc("/api/simular", p.simular)
	mux.HandleFunc("GET /api/directorio", p.directorio)
	// API para que otros sistemas consuman (ver api.go).
	p.registrarAPI(mux)

	srv := &http.Server{
		Addr:              p.cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("[emitter] Monitor web en http://localhost%s", p.cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[emitter] El monitor web no pudo arrancar: %v", err)
		}
	}()
}

// pagina sirve el index.html del monitor.
func (p *Panel) pagina(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	html, err := archivosWeb.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "no se encontró la página del monitor", http.StatusInternalServerError)
		log.Printf("[emitter] Panel: %v", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(html)
}

// estaticos sirve el CSS y el JS desde /static/.
// Con no-cache para que al reconstruir el binario el navegador tome la versión
// nueva sin tener que forzar la recarga.
func estaticos() http.Handler {
	sub, err := fs.Sub(archivosWeb, "web")
	if err != nil {
		log.Printf("[emitter] Panel: no se pudieron montar los estáticos: %v", err)
		return http.NotFoundHandler()
	}
	archivos := http.FileServer(http.FS(sub))
	return http.StripPrefix("/static/", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			archivos.ServeHTTP(w, r)
		}))
}

// --- Respuesta de /api/estado ---

type respuesta struct {
	// MachineCodes son las envasadoras que atiende este emitter (independientes).
	MachineCodes []string         `json:"machine_codes"`
	Maquinas     []maquinaJSON    `json:"maquinas"`
	MiPersonal   []trabajadorJSON `json:"mi_personal"`
}

type maquinaJSON struct {
	SubMaquina  string `json:"sub_maquina"`
	MachineCode string `json:"machine_code"`
	MachineName string `json:"machine_name"`
	MachineID   string `json:"machine_id"`
	IsActive    bool   `json:"is_active"`
	Actualizado string `json:"actualizado"`
	// OrderDetails es la orden de ESTA sub-máquina. Cada una tiene la suya
	// aunque compartan el personal.
	OrderDetails model.OrderDetails `json:"order_details"`
	Personal     []trabajadorJSON   `json:"personal"`
}

type trabajadorJSON struct {
	EmployeeID string   `json:"employee_id"`
	Nombre     string   `json:"nombre"`
	Rol        string   `json:"rol"`
	Status     string   `json:"status"`
	UsbPort    string   `json:"usb_port"`
	Tag        string   `json:"tag"`
	Maquinas   []string `json:"maquinas"`
}

func (p *Panel) estado(w http.ResponseWriter, r *http.Request) {
	res := respuesta{MachineCodes: p.cfg.MachineCodes}

	// Vista global: lo que hay en el KV (incluye otras máquinas/emitters).
	if p.kv != nil {
		if estados, err := p.kv.CargarTodas(); err == nil {
			for _, e := range estados {
				mq := maquinaJSON{
					SubMaquina: e.SubMaquina, MachineCode: e.MachineCode,
					MachineName: e.MachineName,
					MachineID:   e.MachineID, IsActive: e.IsActive,
					Actualizado: e.Actualizado, OrderDetails: e.OrderDetails,
				}
				for _, t := range e.Personal {
					mq.Personal = append(mq.Personal, trabajadorJSON{
						EmployeeID: t.EmployeeID, Nombre: t.Nombre, Rol: t.Rol,
						Status: t.Status, UsbPort: t.UsbPort, Maquinas: t.Maquinas,
					})
				}
				res.Maquinas = append(res.Maquinas, mq)
			}
		}
	}

	// Personal de TODAS las envasadoras de este emitter, con el tag, para
	// simular tarjetas por persona. Cada trabajador trae su propia máquina.
	for _, tm := range p.g.Trabajadores() {
		t := tm.T
		maqs := make([]string, 0, len(t.Maquinas))
		for m := range t.Maquinas {
			maqs = append(maqs, m)
		}
		if len(maqs) == 0 {
			maqs = append(maqs, tm.Code)
		}
		res.MiPersonal = append(res.MiPersonal, trabajadorJSON{
			EmployeeID: t.EmployeeID, Nombre: t.Nombre, Rol: t.Rol,
			Status: multistore.StatusDe(t), UsbPort: t.UsbPort,
			Tag: t.ID, Maquinas: maqs,
		})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		log.Printf("[emitter] Error escribiendo /api/estado: %v", err)
	}
}

// --- /api/directorio ---

// directorio devuelve el catálogo de personal del KV PAPID_PERSONAL, que el
// monitor usa para el dropdown de nombres reales en "Simular asignación".
//
// Si el bucket no está disponible se responde una lista vacía (200) en vez de
// un error: el monitor debe seguir usable escribiendo los datos a mano.
func (p *Panel) directorio(w http.ResponseWriter, r *http.Request) {
	operadores := []persistence.Operador{}
	if p.dir != nil {
		if ops, err := p.dir.Operadores(); err != nil {
			log.Printf("[emitter] Panel: no se pudo leer el directorio: %v", err)
		} else {
			operadores = ops
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"operadores": operadores,
	}); err != nil {
		log.Printf("[emitter] Error escribiendo /api/directorio: %v", err)
	}
}

// --- /api/simular ---

// peticionSimular es lo que manda el navegador.
type peticionSimular struct {
	// signed | forced | unsigned | login | logout | quitar_uno | quitar_orden
	Accion      string `json:"accion"`
	MachineCode string `json:"machine_code"`
	MachineID   string `json:"machine_id"`
	Tag         string `json:"tag"`
	UsbPort     string `json:"usb_port"`
	EmployeeID  string `json:"employee_id"` // solo para quitar_uno
	// OrderDetails es la orden de la máquina. Si viene vacía se manda 0/0.
	OrderDetails model.OrderDetails `json:"order_details"`
	Personal     []personaSimula    `json:"personal"`
}

type personaSimula struct {
	EmployeeID string `json:"employee_id"`
	Nombre     string `json:"nombre"`
	Rol        string `json:"rol"`
	Tag        string `json:"tag"`
}

func (p *Panel) simular(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "usa POST", http.StatusMethodNotAllowed)
		return
	}
	var req peticionSimular
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		responder(w, false, "JSON inválido: "+err.Error())
		return
	}

	// "quitar_uno" se traduce a un signed con el personal RESTANTE (así se
	// desasigna individualmente en el sistema real). Si no queda nadie, se
	// convierte en un unsigned.
	if req.Accion == "quitar_uno" {
		var err error
		if req, err = p.quitarUno(req); err != nil {
			responder(w, false, err.Error())
			return
		}
	}

	// "quitar_orden" publica en papid.admin.quitarorden, el mismo mensaje que
	// manda el admin. El emitter lo escucha, borra la orden de esa sub-máquina
	// y republica sin ella. Es el camino limpio: no fabrica un signed falso.
	if req.Accion == "quitar_orden" {
		if req.MachineCode == "" {
			responder(w, false, "falta la máquina a la que se le quita la orden")
			return
		}
		payload, _ := json.Marshal(map[string]string{
			"machine_code": req.MachineCode,
			"timestamp":    time.Now().UTC().Format(time.RFC3339),
		})
		if err := p.nc.Publish(subjQuitarOrden, payload); err != nil {
			responder(w, false, "no se pudo publicar: "+err.Error())
			return
		}
		p.nc.FlushTimeout(3 * time.Second)
		log.Printf("[emitter] Panel simuló quitar_orden en %s (%s)", subjQuitarOrden, req.MachineCode)
		responder(w, true, fmt.Sprintf("Publicado en %s", subjQuitarOrden))
		return
	}

	subject, payload, err := construirEvento(p.completarOrden(req))
	if err != nil {
		responder(w, false, err.Error())
		return
	}

	if err := p.nc.Publish(subject, payload); err != nil {
		responder(w, false, "no se pudo publicar: "+err.Error())
		return
	}
	if err := p.nc.FlushTimeout(3 * time.Second); err != nil {
		log.Printf("[emitter] Panel: flush incompleto: %v", err)
	}
	log.Printf("[emitter] Panel simuló %s en %s", req.Accion, subject)
	responder(w, true, fmt.Sprintf("Publicado en %s", subject))
}

// completarOrden hereda la orden que ya tenía la máquina cuando la petición no
// trae una. Un signed que solo cambia el personal (el del monitor, o el que
// manda la API sin order_details) no debe borrar el producto ni los lotes: el
// signed define el roster completo, así que sin esto la orden se perdería.
func (p *Panel) completarOrden(req peticionSimular) peticionSimular {
	if req.OrderDetails == (model.OrderDetails{}) && req.MachineCode != "" {
		if st := p.g.Store(req.MachineCode); st != nil {
			req.OrderDetails = st.OrdenDe(req.MachineCode)
		}
	}
	return req
}

// quitarUno convierte una petición "quitar_uno" en el evento equivalente.
//
// Para desasignar a UNA persona se publica un signed con el resto del personal
// (el signed define el roster completo). El servidor puede reconstruirlo porque
// su store tiene los nfc_tag, que el navegador no necesita conocer.
func (p *Panel) quitarUno(req peticionSimular) (peticionSimular, error) {
	if req.EmployeeID == "" {
		return req, fmt.Errorf("falta el employee_id de quien se va a quitar")
	}

	// Encuentra en qué envasadora está esa persona (recorre todas) para saber
	// de qué store sacar el roster restante. Cada envasadora es independiente.
	if req.MachineCode == "" {
		for _, tm := range p.g.Trabajadores() {
			if tm.T.EmployeeID == req.EmployeeID {
				req.MachineCode = tm.Code
				break
			}
		}
	}
	if req.MachineCode == "" {
		return req, fmt.Errorf("no hay nadie asignado con employee_id %q", req.EmployeeID)
	}
	st := p.g.Store(req.MachineCode)
	if st == nil {
		return req, fmt.Errorf("máquina %q no declarada en el emitter", req.MachineCode)
	}

	restantes := make([]personaSimula, 0, 3)
	encontrado := false
	for _, t := range st.Trabajadores() {
		if t.EmployeeID == req.EmployeeID {
			encontrado = true
			continue // este es el que se quita
		}
		restantes = append(restantes, personaSimula{
			EmployeeID: t.EmployeeID,
			Nombre:     t.Nombre,
			Rol:        t.Rol,
			Tag:        t.ID,
		})
	}

	if !encontrado {
		return req, fmt.Errorf("no hay nadie asignado con employee_id %q en %s", req.EmployeeID, req.MachineCode)
	}
	if req.MachineID == "" {
		req.MachineID = st.MachineID()
	}

	req.Personal = restantes
	if len(restantes) == 0 {
		// Era el último: se desasigna la máquina completa.
		req.Accion = "unsigned"
	} else {
		req.Accion = "signed"
	}
	return req, nil
}

// construirEvento arma el mismo JSON que se mandaría con `nats pub`.
func construirEvento(req peticionSimular) (subject string, payload []byte, err error) {
	switch req.Accion {
	case "login", "logout":
		if req.Tag == "" {
			return "", nil, fmt.Errorf("falta el tag")
		}
		subject = subjLogin
		if req.Accion == "logout" {
			subject = subjLogout
		}
		payload, err = json.Marshal(map[string]string{
			"tag": req.Tag, "usb_port": req.UsbPort,
		})
		return subject, payload, err

	case "unsigned":
		if req.MachineCode == "" {
			return "", nil, fmt.Errorf("falta el machine_code")
		}
		payload, err = json.Marshal(map[string]any{
			"machine_code": req.MachineCode,
			"personnel":    []any{},
		})
		return subjUnsigned, payload, err

	case "signed", "forced":
		if req.MachineCode == "" {
			return "", nil, fmt.Errorf("falta el machine_code")
		}
		if len(req.Personal) == 0 {
			return "", nil, fmt.Errorf("agrega al menos una persona")
		}
		personnel := make([]map[string]any, 0, len(req.Personal))
		for _, p := range req.Personal {
			if p.Tag == "" || p.Nombre == "" {
				return "", nil, fmt.Errorf("cada persona necesita nombre y tag")
			}
			personnel = append(personnel, map[string]any{
				"employee_id":        p.EmployeeID,
				"name":               p.Nombre,
				"tipo_asignacion":    p.Rol,
				"tipo_asignacion_id": 1,
				"nfc_tag":            p.Tag,
				"status":             "NoAsignado",
			})
		}
		maquinaID := req.MachineID
		if maquinaID == "" {
			maquinaID = "0"
		}
		// La orden se manda completa (producto, cantidad, doc_num, ...) tal
		// como la trae el signed real. Solo los lotes tienen default.
		orden := req.OrderDetails
		orden.LotesActuales = valorODefault(orden.LotesActuales, "0")
		orden.LotesTotales = valorODefault(orden.LotesTotales, "0")

		cuerpo := map[string]any{
			"machine_code":  req.MachineCode,
			"machine_id":    maquinaID,
			"is_active":     false,
			"is_frozen":     true,
			"order_details": orden,
			"personnel":     personnel,
		}
		subject = subjSigned
		if req.Accion == "forced" {
			subject = subjForced
		}
		payload, err = json.Marshal(cuerpo)
		return subject, payload, err
	}
	return "", nil, fmt.Errorf("acción desconocida: %q", req.Accion)
}

func valorODefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func responder(w http.ResponseWriter, ok bool, mensaje string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": ok, "mensaje": mensaje})
}
