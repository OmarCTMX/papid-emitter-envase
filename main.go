// Emitter de Personal (envasadoras) — un solo servicio que maneja TODAS las
// envasadoras, cada una INDEPENDIENTE, y publica el estado de cada una en:
//
//	papid.emitter.<machine_code>
//
// A diferencia del emitter de las a2i (donde las sub-máquinas COMPARTEN
// personal y se fusionan en un solo estado), aquí cada envasadora es una
// máquina real distinta: tiene su propio personal, su propia orden y sus
// propias tarjetas. Por eso el emitter mantiene UN STORE POR ENVASADORA y
// enruta cada mensaje al store que corresponde según su machine_code.
//
// Las envasadoras se declaran en el .env (MACHINE_CODES). Para agregar o
// renombrar una máquina basta editar esa lista: no se toca el código.
//
// Escucha:  papid.admin.signed / unsigned / forced / asignacion / quitarorden,
//           papid.personal.login / logout
// Publica:  papid.emitter.<machine_code>, papid.emitter.sync, .dashboard,
//           papid.personalui.sync/full, papid.personal.denied/cleared
//
// Uso:  go run .
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/nats-io/nats.go"

	"papid-envasadoras-emitter/internal/model"
	"papid-envasadoras-emitter/internal/multistore"
	"papid-envasadoras-emitter/internal/panel"
	"papid-envasadoras-emitter/internal/persistence"
)

// Subjects de NATS.
const (
	// Entrada
	subjAsignar     = "papid.admin.signed"
	subjDesaignar   = "papid.admin.unsigned"
	subjLogin       = "papid.personal.login"
	subjLogout      = "papid.personal.logout"
	subjForced      = "papid.admin.signed.forced"
	subjPersonal    = "papid.admin.asignacion"
	subjQuitarOrden = "papid.admin.quitarorden"

	// Salida
	topicEmitterPrefix = "papid.emitter."          // + machine_code
	topicEmitterSync   = "papid.emitter.sync"      // compañero (con maquinas)
	topicDashboard     = "papid.emitter.dashboard" // dashboards
	topicFull          = "papid.personalui.full"
	topicSync          = "papid.personalui.sync"
	topicDenied        = "papid.personal.denied"
	topicCleared       = "papid.personal.cleared"
)

// debounceDelay agrupa ráfagas de eventos de UNA máquina en una sola publicación.
const debounceDelay = 200 * time.Millisecond

func main() {
	if err := godotenv.Overload(); err != nil {
		log.Println("[emitter] No se encontró .env, se usan variables del entorno")
	}

	// MACHINE_CODES: lista de envasadoras que atiende este emitter. Cada una es
	// INDEPENDIENTE. Ej: MACHINE_CODES=envasadora-1,envasadora-2,envasadora-3
	// Se acepta MACHINE_CODE (singular) como alias para una sola máquina.
	codes := leerMachineCodes()
	if len(codes) == 0 {
		log.Fatal("[emitter] Define MACHINE_CODES en el .env (ej: envasadora-1,envasadora-2)")
	}
	log.Printf("[emitter] Envasadoras (independientes): %v", codes)

	// Conexión a NATS.
	nc := conectar()
	defer nc.Drain()

	// KV (una clave por máquina). El emitter es el único escritor.
	var kv *persistence.KV
	if k, err := persistence.New(nc); err != nil {
		log.Printf("[emitter] No se pudo inicializar el KV: %v (se sigue sin persistencia)", err)
	} else {
		kv = k
		log.Printf("[emitter] KV listo: personal asignado por máquina")
	}

	// gestor crea y guarda un store por envasadora, y las publica.
	g := nuevoGestor(codes, nc, kv)
	g.arrancarWorkers()

	// --- Monitor web (ver todas las envasadoras) ---
	if addr := getenv("PANEL_ADDR", ":3200"); addr != "off" && addr != "0" {
		var dir *persistence.Directorio
		if d, err := persistence.AbrirDirectorio(nc); err != nil {
			log.Printf("[emitter] Directorio PAPID_PERSONAL no disponible: %v "+
				"(el monitor pedirá los datos a mano)", err)
		} else {
			dir = d
			log.Printf("[emitter] Directorio PAPID_PERSONAL listo")
		}
		panel.New(panel.Config{
			MachineCodes: codes,
			Addr:         addr,
		}, nc, g, kv, dir).Iniciar()
	} else {
		log.Println("[emitter] Monitor web desactivado (PANEL_ADDR=off)")
	}

	// --- Suscripciones ---
	// Todas comparten la misma idea: decodificar, ver a qué envasadora va
	// (por machine_code) y aplicar al store de esa máquina. Lo que no sea de
	// ninguna envasadora declarada se ignora.
	suscribir := func(subject string, h nats.MsgHandler) {
		if _, err := nc.Subscribe(subject, h); err != nil {
			log.Fatalf("[emitter] No se pudo suscribir a %s: %v", subject, err)
		}
		log.Printf("[emitter] Suscrito a %s", subject)
	}

	parse := func(data []byte) (model.MensajeAsignacion, *multistore.Store, bool) {
		var msg model.MensajeAsignacion
		if json.Unmarshal(data, &msg) != nil || msg.MachineCode == "" {
			return msg, nil, false
		}
		st := g.Store(msg.MachineCode) // nil si el code no está declarado
		return msg, st, st != nil
	}

	leerEvento := func(data []byte) (model.EventoRFID, bool) {
		var ev model.EventoRFID
		if json.Unmarshal(data, &ev) != nil {
			return ev, false
		}
		return ev, true
	}
	publicarEvento := func(subject string, ev model.EventoRFID) {
		data := fmt.Sprintf(`{"usb_port":"%s","tag":"%s"}`, ev.UsbPort, ev.Tag)
		nc.Publish(subject, []byte(data))
		log.Printf("[emitter] Publicado en %s: %s", subject, data)
	}

	// Asignación: cada envasadora recibe SU personal, en su propio store. El
	// destino es siempre [code], nunca una lista: aquí no hay sub-máquinas.
	suscribir(subjAsignar, func(m *nats.Msg) {
		if msg, st, ok := parse(m.Data); ok {
			st.Asignar([]string{msg.MachineCode}, msg)
		}
	})
	suscribir(subjForced, func(m *nats.Msg) {
		if msg, st, ok := parse(m.Data); ok {
			st.AsignarForzado([]string{msg.MachineCode}, msg)
		}
	})
	suscribir(subjPersonal, func(m *nats.Msg) {
		if msg, st, ok := parse(m.Data); ok {
			st.AsignarPersonal([]string{msg.MachineCode}, msg)
		}
	})
	suscribir(subjDesaignar, func(m *nats.Msg) {
		if msg, st, ok := parse(m.Data); ok {
			st.Desasignar([]string{msg.MachineCode})
			g.marcarRetirada(msg.MachineCode) // borra su clave del KV tras publicar
		}
	})
	suscribir(subjQuitarOrden, func(m *nats.Msg) {
		var req struct {
			MachineCode string `json:"machine_code"`
		}
		if json.Unmarshal(m.Data, &req) != nil || req.MachineCode == "" {
			return
		}
		if st := g.Store(req.MachineCode); st != nil {
			st.QuitarOrden(req.MachineCode)
		}
	})

	// Login/logout: el tag no dice de qué máquina es, así que se intenta en
	// TODAS las envasadoras. La que tenga a esa persona la registra; si ninguna
	// la tiene, es intruso (denied) / tarjeta no activa (cleared).
	suscribir(subjLogin, func(m *nats.Msg) {
		if ev, ok := leerEvento(m.Data); ok && !g.registrarEnAlguna(ev.Tag, ev.UsbPort) {
			publicarEvento(topicDenied, ev)
		}
	})
	suscribir(subjLogout, func(m *nats.Msg) {
		if ev, ok := leerEvento(m.Data); ok && !g.retirarEnAlguna(ev.Tag) {
			publicarEvento(topicCleared, ev)
		}
	})

	log.Printf("[emitter] Emitter de envasadoras corriendo (%d máquinas)", len(codes))
	log.Println("[emitter] Publica en: papid.emitter.<machine>, papid.personalui.sync/full, papid.personal.denied/cleared")
	log.Println("[emitter] Presiona Ctrl+C para detener")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("[emitter] Apagando...")

	g.publicarPendientes()
	if err := nc.FlushTimeout(5 * time.Second); err != nil {
		log.Printf("[emitter] Flush incompleto al apagar: %v", err)
	}
}

// ============================================================
//  Gestor de envasadoras: un store por máquina + publicación.
// ============================================================

// maquina agrupa el store de UNA envasadora y su canal de "hay cambios".
type maquina struct {
	code    string
	store   *multistore.Store
	cambios chan struct{} // capacidad 1: colapsa ráfagas en una señal
	retirar bool          // true tras un unsigned: se borra del KV tras publicar
}

// gestor mantiene todas las envasadoras y centraliza la publicación/KV.
type gestor struct {
	nc  *nats.Conn
	kv  *persistence.KV
	mu  sync.RWMutex
	m   map[string]*maquina
	ord []string // orden de declaración (para logs/panel estables)
}

func nuevoGestor(codes []string, nc *nats.Conn, kv *persistence.KV) *gestor {
	g := &gestor{nc: nc, kv: kv, m: make(map[string]*maquina, len(codes))}
	for _, c := range codes {
		mq := &maquina{code: c, store: multistore.New(c), cambios: make(chan struct{}, 1)}
		// Cuando el store de esta máquina cambia, se señala SU canal (no el de
		// las demás): así una envasadora no republica a las otras.
		code := c
		mq.store.SetOnChange(func() { g.señalar(code) })
		g.m[c] = mq
		g.ord = append(g.ord, c)
		// Restaura del KV lo último que se sabía de esta máquina (si hay).
		g.restaurarDesdeKV(mq)
	}
	return g
}

// Store devuelve el store de una envasadora, o nil si ese code no está declarado.
func (g *gestor) Store(code string) *multistore.Store {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if mq := g.m[code]; mq != nil {
		return mq.store
	}
	return nil
}

// Codes devuelve la lista de envasadoras (en orden de declaración).
func (g *gestor) Codes() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]string(nil), g.ord...)
}

// Trabajadores devuelve el personal de TODAS las envasadoras (con su code),
// para que el panel pueda listar y simular tarjetas por persona.
func (g *gestor) Trabajadores() []panel.TrabajadorMaquina {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := []panel.TrabajadorMaquina{}
	for _, code := range g.ord {
		for _, t := range g.m[code].store.Trabajadores() {
			out = append(out, panel.TrabajadorMaquina{Code: code, T: t})
		}
	}
	return out
}

// señalar marca que una envasadora tiene cambios por publicar.
func (g *gestor) señalar(code string) {
	g.mu.RLock()
	mq := g.m[code]
	g.mu.RUnlock()
	if mq == nil {
		return
	}
	select {
	case mq.cambios <- struct{}{}:
	default: // ya había una señal pendiente, se agrupa
	}
}

// marcarRetirada agenda el borrado del KV de una máquina desasignada (tras su
// última publicación con el estado vacío).
func (g *gestor) marcarRetirada(code string) {
	g.mu.Lock()
	if mq := g.m[code]; mq != nil {
		mq.retirar = true
	}
	g.mu.Unlock()
}

// registrarEnAlguna intenta poner la tarjeta en la envasadora que tenga a esa
// persona. Devuelve true si alguna la aceptó (o ya estaba activa).
func (g *gestor) registrarEnAlguna(tag, usbPort string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, code := range g.ord {
		if g.m[code].store.RegistrarPorID(tag, usbPort) {
			return true
		}
	}
	return false
}

// retirarEnAlguna retira la tarjeta de la envasadora donde esté activa.
func (g *gestor) retirarEnAlguna(tag string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, code := range g.ord {
		if g.m[code].store.RetirarPorID(tag) {
			return true
		}
	}
	return false
}

// arrancarWorkers lanza un worker por envasadora: espera el debounce y publica
// SOLO esa máquina. Un worker por máquina garantiza que nunca haya dos
// publicaciones de la misma envasadora en paralelo, y aísla las fallas.
func (g *gestor) arrancarWorkers() {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, code := range g.ord {
		mq := g.m[code]
		go func(mq *maquina) {
			for range mq.cambios {
				time.Sleep(debounceDelay)
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[emitter] Panic al publicar %s (recuperado): %v", mq.code, r)
						}
					}()
					g.publicar(mq)
				}()
			}
		}(mq)
	}
}

// publicarPendientes publica de inmediato lo que quede pendiente (al apagar).
func (g *gestor) publicarPendientes() {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, code := range g.ord {
		mq := g.m[code]
		select {
		case <-mq.cambios:
			g.publicar(mq)
		default:
		}
	}
}

// pub serializa y publica, registrando errores.
func (g *gestor) pub(subject string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("[emitter] Error serializando para %s: %v", subject, err)
		return
	}
	if err := g.nc.Publish(subject, data); err != nil {
		log.Printf("[emitter] Error publicando en %s: %v", subject, err)
	}
}

// publicar emite el estado de UNA envasadora en todos sus subjects y lo
// persiste (o lo borra) en el KV. Como cada máquina es independiente, el
// mensaje de "unión" (sync/dashboard) de esta máquina es simplemente su propio
// personal; el campo maquinas lista solo su code.
func (g *gestor) publicar(mq *maquina) {
	st := mq.store

	// papid.emitter.<code>: personal + orden de esta envasadora.
	porMaquina := st.BuildSync()
	porMaquina.MachineCode = mq.code
	orden := st.OrdenDe(mq.code)
	porMaquina.OrderDetails = &orden
	g.pub(topicEmitterPrefix+mq.code, porMaquina)

	// Compatibilidad con los consumidores del emitter de a2i: también se
	// publican los topics genéricos, con el machine_code de ESTA envasadora.
	base := st.BuildSync()
	base.MachineCode = mq.code
	g.pub(topicSync, base)
	g.pub(topicFull, st.BuildFull())

	esync := st.BuildEmitterSync() // trae el campo "maquinas" (= [code])
	esync.MachineCode = mq.code
	g.pub(topicEmitterSync, esync)
	g.pub(topicDashboard, esync)

	// --- KV: guardar o borrar la clave de esta envasadora ---
	g.mu.Lock()
	retirar := mq.retirar
	mq.retirar = false
	g.mu.Unlock()

	if g.kv != nil {
		if retirar {
			if err := g.kv.Borrar(mq.code); err != nil {
				log.Printf("[emitter] Error borrando %s del KV: %v", mq.code, err)
			}
		} else {
			full := st.BuildFull()
			if err := g.kv.Guardar(persistence.EstadoAsignado{
				SubMaquina:   mq.code,
				MachineCode:  mq.code,
				MachineName:  st.MachineName(),
				MachineID:    full.MachineID,
				IsActive:     full.IsActive,
				IsFrozen:     full.IsFrozen,
				OrderDetails: orden,
				Personal:     esync.Personnel,
			}); err != nil {
				log.Printf("[emitter] Error guardando %s en KV: %v", mq.code, err)
			}
		}
	}

	log.Printf("[emitter] Publicado %s (%d trabajadores)", mq.code, len(esync.Personnel))
}

// restaurarDesdeKV carga el último estado conocido de una envasadora (si el KV
// lo tiene) para que al arrancar no espere a un nuevo mensaje.
func (g *gestor) restaurarDesdeKV(mq *maquina) {
	if g.kv == nil {
		return
	}
	estado, err := g.kv.Cargar(mq.code)
	if err != nil || estado == nil {
		return
	}
	mq.store.Restaurar(estado.MachineID, estado.MachineName, estado.IsFrozen,
		estado.OrderDetails, estado.Personal)
	log.Printf("[emitter] %s restaurada del KV (%d trabajadores)", mq.code, len(estado.Personal))
}

// ============================================================
//  Conexión y configuración.
// ============================================================

func conectar() *nats.Conn {
	opts := []nats.Option{
		nats.Name("papid-envasadoras-emitter"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.ReconnectJitter(100*time.Millisecond, time.Second),
		nats.Timeout(10 * time.Second),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectBufSize(16 * 1024 * 1024),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Printf("[emitter] NATS desconectado: %v (reintentando...)", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			log.Printf("[emitter] NATS reconectado a %s", c.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			log.Println("[emitter] NATS: conexión cerrada definitivamente")
		}),
		nats.ErrorHandler(func(_ *nats.Conn, s *nats.Subscription, err error) {
			subject := ""
			if s != nil {
				subject = s.Subject
			}
			log.Printf("[emitter] NATS error (subject %q): %v", subject, err)
		}),
	}
	if u := os.Getenv("NATS_USER"); u != "" {
		opts = append(opts, nats.UserInfo(u, os.Getenv("NATS_PASS")))
	}
	nc, err := nats.Connect(getenv("NATS_URL", "nats://localhost:4222"), opts...)
	if err != nil {
		log.Fatalf("[emitter] No se pudo conectar a NATS: %v", err)
	}
	log.Printf("[emitter] Conectado a NATS en %s", nc.ConnectedUrl())
	return nc
}

func getenv(clave, def string) string {
	if v := os.Getenv(clave); v != "" {
		return v
	}
	return def
}

// leerMachineCodes lee la lista de envasadoras del .env. Acepta MACHINE_CODES
// (lista separada por comas) y, como alias para una sola, MACHINE_CODE.
// Quita duplicados y espacios. Ej: MACHINE_CODES=envasadora-1, envasadora-2
func leerMachineCodes() []string {
	raw := os.Getenv("MACHINE_CODES")
	if raw == "" {
		raw = os.Getenv("MACHINE_CODE") // alias de una sola máquina
	}
	vistos := make(map[string]bool)
	out := make([]string, 0)
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" && !vistos[p] {
			vistos[p] = true
			out = append(out, p)
		}
	}
	return out
}
