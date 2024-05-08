# Trillian Lite Tile Log (MySQL Flavour)

This is a prototype of a tile-based log designed to be as light as possible, while being backed by an RDBMS.

## Installation

This uses a tool to look after the database schema. This is totally overkill for a prototype, but it's a convenient and safe opportunity to experiment with this. Plus it's not hard, so enjoy.

Installation:

```bash
go install -tags mysql github.com/golang-migrate/migrate/v4/cmd/migrate@v4.17.1
```

## Deploying Locally

```bash
docker compose up -d
migrate -database 'mysql://user:password@tcp(localhost:42006)/litelog' -source file:///`pwd`/storage/tsql/migrations up
```

## Deploying on GCP

First the `cloudbuild` modules should be deployed, or else ensure that an image is available in a Docker registry you have access to.
Next, ensure that the project/regions etc are set correctly in the `terragrunt.hcl` files.
Install [Cloud SQL Proxy](https://github.com/GoogleCloudPlatform/cloud-sql-proxy).
Also install the `migrate` tool:

```bash
go install -tags mysql github.com/golang-migrate/migrate/v4/cmd/migrate@v4.17.1
```

Now you're ready to deploy.
This is done in 2 stages: the first stage creates the infrastructure, and the second stage deploys the Cloud Run functions.
In between these stages you need to set up a proxy connection to Cloud SQL and use the `migrate` tool to create the tables.

```bash
cd ./deployment/live/sqlog/dev/
terragrunt apply -var='skip_fe=true'
cloud-sql-proxy ${DB_CONNECTION_STRING} --port 6000 &
migrate -database 'mysql://sqlog-app:${DB_PASS}@tcp(localhost:6000)/sqlog' -source file:///`pwd`/storage/tsql/migrations up
terragrunt apply
```

## Performance

Running using the `docker compose` script provided on a modest desktop, the [hammer](https://github.com/transparency-dev/serverless-log/tree/main/hammer) was able to sustain the following read/write traffic without hitting bottlenecks:

```
Read: Current max: 120000/s. Oversupply in last second: 0
Write: Current max: 21212/s. Oversupply in last second: 0
```
