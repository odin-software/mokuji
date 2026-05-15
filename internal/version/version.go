// Package version expone la versión publicada del binario.
//
// Value se inyecta en build vía ldflags leyendo el archivo VERSION del root
// del repo. En desarrollo (go run, air, tests) queda como "dev".
package version

var Value = "dev"
