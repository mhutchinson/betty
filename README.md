# Trillian Lite Tile Log (MySQL Flavour)

This is a prototype of a tile-based log designed to be as light as possible, while being backed by an RDBMS.

## Installation

This uses a tool to look after the database schema. This is totally overkill for a prototype, but it's a convenient and safe opportunity to experiment with this. Plus it's not hard, so enjoy.

Installation:

```bash
go install -tags mysql github.com/golang-migrate/migrate/v4/cmd/migrate@v4.17.1
```

## Deploying

```bash
docker compose up -d
migrate -database 'mysql://user:password@tcp(localhost:42006)/litelog' -source file:///`pwd`/storage/tsql/migrations up
```
