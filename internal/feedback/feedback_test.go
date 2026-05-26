package feedback

import (
	"testing"
)

func TestIsEnabledDefaultsFalse(t *testing.T) {
	// El package-level var arranca en false. Cualquier test que setee
	// debe restaurar (TestMain o t.Cleanup) — los tests futuros lo harán
	// vía t.Cleanup.
	if IsEnabled() {
		t.Errorf("IsEnabled() = true, want false al inicio del proceso")
	}
}

func TestSetEnabledTogglesFlag(t *testing.T) {
	t.Cleanup(func() { SetEnabled(false) })
	SetEnabled(true)
	if !IsEnabled() {
		t.Errorf("después de SetEnabled(true), IsEnabled() debe ser true")
	}
	SetEnabled(false)
	if IsEnabled() {
		t.Errorf("después de SetEnabled(false), IsEnabled() debe ser false")
	}
}
