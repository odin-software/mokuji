.PHONY: dev test build run migrate compose-up compose-down clean

# Levanta servicios locales (mailpit) y corre el binario con live reload (air).
dev: compose-up
	@command -v air >/dev/null 2>&1 || { echo "air no instalado: go install github.com/air-verse/air@latest"; exit 1; }
	air

# Corre el binario una sola vez (sin watch).
run:
	go run .

# Solo corre migraciones contra la DB local y sale.
migrate:
	go run . -migrate-only

# Corre todos los tests.
test:
	go test ./...

# Compila el binario en ./bin/mokuji.
build:
	go build -o ./bin/mokuji .

# Levanta servicios locales con docker compose.
compose-up:
	docker compose up -d

# Detiene servicios locales.
compose-down:
	docker compose down

# Limpia binarios y data local.
clean:
	rm -rf ./bin ./data
