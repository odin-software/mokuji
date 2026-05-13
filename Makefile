.PHONY: dev test build run migrate compose-up compose-down clean

# Levanta servicios locales (mailpit) y corre el binario en foreground.
dev: compose-up run

# Corre el binario en foreground.
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
