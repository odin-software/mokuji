// Package ratelimit implementa rate limiting in-memory por clave usando
// token buckets de golang.org/x/time/rate.
//
// Diseño: una instancia de Limiter mantiene un bucket por cada clave que ve
// (típicamente IP o email). El mismo rate y burst aplican a todas las claves.
// En crash o restart los contadores se pierden — aceptable para nuestro
// modelo single-process self-hostable; el costo de persistencia sería
// desproporcionado al threat real.
//
// Una goroutine de Cleanup remueve buckets que no se usaron en TTL para
// evitar crecimiento ilimitado de memoria con muchas claves únicas.
package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter aplica el mismo rate y burst a cada clave.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*entry
	limit   rate.Limit
	burst   int
}

type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// New construye un Limiter. r es el rate sostenido (eventos por segundo);
// burst es la cantidad máxima de eventos en ráfaga.
//
// Helpers útiles para r: rate.Every(time.Duration) para 1 evento cada X.
func New(r rate.Limit, burst int) *Limiter {
	return &Limiter{
		buckets: map[string]*entry{},
		limit:   r,
		burst:   burst,
	}
}

// Allow consulta si la clave puede emitir un evento ahora. Retorna true si
// hay token disponible (y lo consume); false si está rate-limited.
//
// Si l es nil, retorna true (sin rate limiting). Útil para tests y para
// configuraciones donde una capa específica no necesita límites.
func (l *Limiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.buckets[key]
	if !ok {
		e = &entry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.buckets[key] = e
	}
	e.lastSeen = time.Now()
	return e.limiter.Allow()
}

// Cleanup corre hasta que ctx se cancele, removiendo buckets cuya lastSeen
// sea más vieja que ttl. Llamar desde una goroutine al startup.
func (l *Limiter) Cleanup(ctx context.Context, ttl time.Duration) {
	interval := ttl / 4
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.removeStale(ttl)
		}
	}
}

func (l *Limiter) removeStale(ttl time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-ttl)
	for k, e := range l.buckets {
		if e.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

// Size retorna la cantidad de buckets activos. Útil para tests y
// observabilidad eventual.
func (l *Limiter) Size() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
