// Package persistence guarda en NATS JetStream KV el personal asignado a cada
// sub-máquina. El emitter es el ÚNICO que escribe aquí; los dashboards y el
// monitor solo leen.
//
//	Bucket: "papid_personal_asignado"
//	Clave:  "<sub-máquina>"   (ej. "a2i-1-r", "a2i-2-r", "envasadora-1")
//	Valor:  JSON con el personal de la máquina física + metadatos de la orden
//
// El valor es el MISMO para todas las sub-máquinas de una máquina física (es la
// unión del personal, igual que lo que se publica en papid.emitter.dashboard).
// El campo "maquinas" de cada persona indica a qué sub-máquinas está asignada.
//
// Consultar desde la terminal:
//
//	nats kv get papid_personal_asignado a2i-1-r
//	nats kv ls  papid_personal_asignado
//	nats kv watch papid_personal_asignado
package persistence

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/nats-io/nats.go"

	"papid-envasadoras-emitter/internal/model"
)

const bucketName = "papid_personal_asignado"

// EstadoAsignado es lo que se guarda en el KV por cada sub-máquina.
type EstadoAsignado struct {
	// Sub-máquina a la que corresponde esta clave (ej. "a2i-1-r").
	SubMaquina string `json:"sub_maquina"`
	// Máquina física que la agrupa (el MACHINE_CODE del emitter, ej. "a2i").
	MachineCode string `json:"machine_code"`
	// MachineName es el nombre legible que trae papid.admin.asignacion
	// (ej. "A2i - 3 | Revoltura").
	MachineName string `json:"machine_name,omitempty"`
	MachineID   string `json:"machine_id,omitempty"`
	// IsActive es true cuando todos los asignados ya pasaron por verde.
	IsActive     bool               `json:"is_active"`
	IsFrozen     bool               `json:"is_frozen"`
	OrderDetails model.OrderDetails `json:"order_details"`
	// Personal de la máquina física. Cada persona trae el campo "maquinas"
	// con las sub-máquinas donde está asignada.
	Personal []model.PersonaSync `json:"personal"`
	// Actualizado es la marca de tiempo de la última escritura.
	Actualizado string `json:"actualizado"`
}

// esperaKV acota cuánto se espera la confirmación de JetStream en cada
// operación. El default de la librería son 5s, que con 3 intentos bloqueaba el
// worker del KV 16 segundos y retrasaba las escrituras siguientes.
const esperaKV = 3 * time.Second

// KV envuelve el acceso al bucket.
type KV struct {
	kv nats.KeyValue
	nc *nats.Conn // solo para reportar el estado de la conexión en los errores
}

// New abre el bucket y lo crea si no existe (TTL de 24h, alineado con el
// reseteo diario de turno).
//
// Si la conexión NATS se estableció con RetryOnFailedConnect, puede que
// todavía no esté lista. Se espera hasta 10s a que IsConnected() sea true
// antes de tocar JetStream.
func New(nc *nats.Conn) (*KV, error) {
	// Esperar a la conexión real (RetryOnFailedConnect puede retornar antes
	// de que haya enlace con el servidor).
	if !nc.IsConnected() {
		for i := 0; i < 50; i++ { // hasta 10s en intervalos de 200ms
			time.Sleep(200 * time.Millisecond)
			if nc.IsConnected() {
				break
			}
		}
		if !nc.IsConnected() {
			return nil, fmt.Errorf("timeout esperando conexión NATS real")
		}
	}

	js, err := nc.JetStream(nats.MaxWait(esperaKV))
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	kv, err := js.KeyValue(bucketName)
	if err != nil {
		kv, err = js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket:      bucketName,
			Description: "Personal asignado por sub-máquina (lo escribe el emitter)",
			History:     10,
			TTL:         24 * time.Hour,
			Storage:     nats.FileStorage,
		})
		if err != nil {
			return nil, fmt.Errorf("crear bucket KV %q: %w", bucketName, err)
		}
		log.Printf("[emitter] Bucket KV %q creado", bucketName)
	}
	return &KV{kv: kv, nc: nc}, nil
}

// estadoConexion describe la conexión NATS, para distinguir un KV enfermo de
// una red caída: con el buffer de reconexión los Publish no fallan, así que el
// timeout del KV suele ser el primer síntoma de que no hay conexión real.
func (k *KV) estadoConexion() string {
	if k.nc == nil {
		return "sin conexión"
	}
	switch {
	case k.nc.IsConnected():
		return "conectado a " + k.nc.ConnectedUrl()
	case k.nc.IsReconnecting():
		return "RECONECTANDO (no hay servidor)"
	case k.nc.IsClosed():
		return "CERRADA"
	default:
		return "estado desconocido"
	}
}

// Guardar escribe el estado bajo la clave de una sub-máquina.
// Si el primer intento da timeout (disco lento, red congestionada), reintenta
// hasta 2 veces más con un respiro entre intentos. Si aun así falla, el worker
// lo logueará y la siguiente tanda lo intentará de nuevo.
func (k *KV) Guardar(estado EstadoAsignado) error {
	if estado.SubMaquina == "" {
		return nil // sin sub-máquina no hay clave donde guardar
	}
	estado.Actualizado = time.Now().Format(time.RFC3339)
	data, err := json.Marshal(estado)
	if err != nil {
		return err
	}

	const intentos = 2
	for i := 0; i < intentos; i++ {
		_, err = k.kv.Put(estado.SubMaquina, data)
		if err == nil {
			return nil
		}
		if i < intentos-1 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	return fmt.Errorf("%w (NATS: %s)", err, k.estadoConexion())
}

// Cargar lee el estado de una sub-máquina. Devuelve nil si no hay dato
// (es decir, si esa máquina no tiene personal asignado).
func (k *KV) Cargar(subMaquina string) (*EstadoAsignado, error) {
	if subMaquina == "" {
		return nil, nil
	}
	entry, err := k.kv.Get(subMaquina)
	if err != nil {
		if err == nats.ErrKeyNotFound {
			return nil, nil
		}
		return nil, err
	}
	var estado EstadoAsignado
	if err := json.Unmarshal(entry.Value(), &estado); err != nil {
		return nil, fmt.Errorf("valor inválido en la clave %q: %w", subMaquina, err)
	}
	return &estado, nil
}

// CargarTodas lee el estado de TODAS las sub-máquinas registradas en el KV,
// incluidas las de otros emitters. La usa el panel para la vista global.
func (k *KV) CargarTodas() ([]EstadoAsignado, error) {
	claves, err := k.kv.Keys()
	if err != nil {
		if err == nats.ErrNoKeysFound {
			return nil, nil // bucket vacío
		}
		return nil, err
	}
	todos := make([]EstadoAsignado, 0, len(claves))
	for _, clave := range claves {
		entry, err := k.kv.Get(clave)
		if err != nil {
			continue
		}
		var estado EstadoAsignado
		if json.Unmarshal(entry.Value(), &estado) == nil {
			todos = append(todos, estado)
		}
	}
	sort.Slice(todos, func(i, j int) bool { return todos[i].SubMaquina < todos[j].SubMaquina })
	return todos, nil
}

// Borrar elimina la clave de una sub-máquina (al desasignarla).
// Así `nats kv ls` muestra solo las máquinas que tienen personal.
func (k *KV) Borrar(subMaquina string) error {
	if subMaquina == "" {
		return nil
	}
	err := k.kv.Delete(subMaquina)
	if err == nats.ErrKeyNotFound {
		return nil // ya no estaba, no es error
	}
	return err
}
