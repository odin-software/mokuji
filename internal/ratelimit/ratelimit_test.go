package ratelimit

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestAllowFreshKey(t *testing.T) {
	l := New(rate.Every(time.Hour), 3)
	for i := range 3 {
		if !l.Allow("ip-1") {
			t.Errorf("intento %d con burst=3 debería permitirse", i+1)
		}
	}
}

func TestExhaustsBurst(t *testing.T) {
	l := New(rate.Every(time.Hour), 2)
	if !l.Allow("ip-1") {
		t.Fatal("primer intento debería pasar")
	}
	if !l.Allow("ip-1") {
		t.Fatal("segundo intento debería pasar")
	}
	if l.Allow("ip-1") {
		t.Error("tercer intento debería ser rate-limited")
	}
}

func TestDifferentKeysIsolated(t *testing.T) {
	l := New(rate.Every(time.Hour), 1)
	if !l.Allow("ip-1") {
		t.Fatal("primer Allow para ip-1 debería pasar")
	}
	if !l.Allow("ip-2") {
		t.Error("ip-2 no debería estar afectada por ip-1")
	}
}

func TestNilLimiterAllowsAll(t *testing.T) {
	var l *Limiter
	for range 100 {
		if !l.Allow("ip-1") {
			t.Fatal("nil Limiter debería permitir todo")
		}
	}
	if l.Size() != 0 {
		t.Errorf("nil Limiter Size: esperado 0, obtenido %d", l.Size())
	}
}

func TestRemoveStaleRemovesOld(t *testing.T) {
	l := New(rate.Every(time.Hour), 1)
	l.Allow("old-key")
	l.Allow("recent-key")

	// Forzar a old-key a aparecer viejo manipulando lastSeen.
	l.mu.Lock()
	l.buckets["old-key"].lastSeen = time.Now().Add(-1 * time.Hour)
	l.mu.Unlock()

	l.removeStale(30 * time.Minute)

	if l.Size() != 1 {
		t.Errorf("Size después de cleanup: esperado 1, obtenido %d", l.Size())
	}
	if _, ok := l.buckets["recent-key"]; !ok {
		t.Error("recent-key no debería haberse removido")
	}
}

func TestRecoverAfterTime(t *testing.T) {
	// 2 eventos por segundo con burst 2: después de agotar, esperando ~600ms
	// recargamos al menos 1 token.
	l := New(2.0, 2)
	if !l.Allow("k") {
		t.Fatal("primero debería pasar")
	}
	if !l.Allow("k") {
		t.Fatal("segundo debería pasar")
	}
	if l.Allow("k") {
		t.Fatal("tercero debería bloquear")
	}
	time.Sleep(600 * time.Millisecond)
	if !l.Allow("k") {
		t.Error("después de recargar token, debería permitir")
	}
}
