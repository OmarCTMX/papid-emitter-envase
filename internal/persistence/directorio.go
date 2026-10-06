// Directorio: lectura del catálogo de personal que vive en el KV
// PAPID_PERSONAL. Este bucket NO lo escribe el emitter, lo mantiene el sistema
// de personal; aquí solo se lee para poblar los dropdowns del monitor con
// nombres reales en vez de ejemplos inventados.
//
//	Bucket: "PAPID_PERSONAL"   (en mayúsculas)
//	Clave:  "<nfc_tag>"        (ej. "0D:9B:BE:3C")
//	Valor:  JSON { "nfc_tag": "...", "operators": [ {id, nombre, ...}, ... ] }
//
// Un mismo operador puede aparecer bajo varias claves, así que al leer se
// deduplica por id.
//
// Consultar desde la terminal:
//
//	nats kv ls  PAPID_PERSONAL
//	nats kv get PAPID_PERSONAL 0D:9B:BE:3C
package persistence

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// bucketDirectorio es el catálogo de personal (solo lectura para el emitter).
const bucketDirectorio = "PAPID_PERSONAL"

// ttlDirectorio es cuánto se reutiliza la lista antes de volver a recorrer el
// bucket. El monitor pide el estado cada 2s y recorrer todas las claves en cada
// tick sería un gasto de red innecesario: el catálogo cambia muy poco.
const ttlDirectorio = 30 * time.Second

// Operador es una persona del catálogo, tal como la expone el directorio.
type Operador struct {
	ID             string   `json:"id"`
	Nombre         string   `json:"nombre"`
	NombreCompleto string   `json:"nombreCompleto"`
	NfcTag         string   `json:"nfc_tag"`
	Machines       []string `json:"machines"`
}

// entradaDirectorio es el valor crudo de cada clave del bucket.
type entradaDirectorio struct {
	NfcTag    string     `json:"nfc_tag"`
	Operators []Operador `json:"operators"`
}

// Directorio lee el catálogo de personal con una caché corta.
type Directorio struct {
	kv nats.KeyValue

	mu      sync.Mutex
	cache   []Operador
	cacheAt time.Time
}

// AbrirDirectorio se engancha al bucket PAPID_PERSONAL.
//
// A diferencia de New, aquí NO se crea el bucket si no existe: es un catálogo
// ajeno al emitter. Si falta, se devuelve error y el monitor sigue funcionando
// sin dropdowns.
func AbrirDirectorio(nc *nats.Conn) (*Directorio, error) {
	js, err := nc.JetStream(nats.MaxWait(esperaKV))
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	kv, err := js.KeyValue(bucketDirectorio)
	if err != nil {
		return nil, fmt.Errorf("bucket KV %q: %w", bucketDirectorio, err)
	}
	return &Directorio{kv: kv}, nil
}

// Operadores devuelve el personal del catálogo, deduplicado por id y ordenado
// por nombre. El resultado se cachea ttlDirectorio.
func (d *Directorio) Operadores() ([]Operador, error) {
	if d == nil {
		return nil, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cache != nil && time.Since(d.cacheAt) < ttlDirectorio {
		return d.cache, nil
	}

	claves, err := d.kv.Keys()
	if err != nil {
		if err == nats.ErrNoKeysFound {
			d.cache, d.cacheAt = []Operador{}, time.Now()
			return d.cache, nil
		}
		return nil, err
	}

	porID := make(map[string]Operador, len(claves))
	for _, clave := range claves {
		entry, err := d.kv.Get(clave)
		if err != nil {
			continue // clave borrada entre Keys() y Get(), o valor ilegible
		}
		var ent entradaDirectorio
		if json.Unmarshal(entry.Value(), &ent) != nil {
			continue
		}
		for _, op := range ent.Operators {
			if op.ID == "" {
				continue
			}
			// El nfc_tag del operador puede venir vacío; en ese caso sirve el
			// de la entrada (la clave del bucket).
			if op.NfcTag == "" {
				op.NfcTag = ent.NfcTag
			}
			fusionar(porID, op)
		}
	}

	lista := make([]Operador, 0, len(porID))
	for _, op := range porID {
		lista = append(lista, op)
	}
	sort.Slice(lista, func(i, j int) bool {
		a, b := lista[i], lista[j]
		if na, nb := strings.ToLower(a.Nombre), strings.ToLower(b.Nombre); na != nb {
			return na < nb
		}
		return a.ID < b.ID
	})

	d.cache, d.cacheAt = lista, time.Now()
	return lista, nil
}

// fusionar mete al operador en el mapa. Si ya estaba (aparece bajo varias
// claves del bucket), se completan los huecos y se unen sus máquinas en vez de
// quedarse con una sola de las versiones.
func fusionar(porID map[string]Operador, nuevo Operador) {
	previo, existe := porID[nuevo.ID]
	if !existe {
		porID[nuevo.ID] = nuevo
		return
	}
	if previo.Nombre == "" {
		previo.Nombre = nuevo.Nombre
	}
	if previo.NombreCompleto == "" {
		previo.NombreCompleto = nuevo.NombreCompleto
	}
	if previo.NfcTag == "" {
		previo.NfcTag = nuevo.NfcTag
	}
	previo.Machines = unir(previo.Machines, nuevo.Machines)
	porID[nuevo.ID] = previo
}

// unir concatena sin repetir, conservando el orden de aparición.
func unir(a, b []string) []string {
	vistos := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" || vistos[s] {
			continue
		}
		vistos[s] = true
		out = append(out, s)
	}
	return out
}
