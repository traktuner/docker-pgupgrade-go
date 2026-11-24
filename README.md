# docker-pgupgrade-go

Migriere PostgreSQL-Datenbanken zwischen Docker-Containern (z. B. auf eine neue Major-Version) oder zwischen beliebigen host:port-Endpunkten (z. B. in Kubernetes). Fokus: Einfache, sichere Bedienung ohne manuelle Dumps/Kopieren.

## Features
- Interaktiver Docker-Modus (Standard, keine Argumente): Nur laufende PostgreSQL-Container werden zur Auswahl angeboten
- Nicht-interaktiver Job-Modus (für CI/Kubernetes): vollständige Konfiguration per Flags oder Env-Variablen, Verbindungen per TCP (`host:port`), Exit-Code ≠ 0 bei Fehlern
- Passwort-Eingabe ohne Echo (maskiert); Prefill aus Container-Env (`POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`)
- Optional: Automatisches Starten des Ziel-Containers (Image, Name, Port, Volume) inkl. Health-Check-Wait
- Standardmäßig Streaming-Migration ohne temporäre Datei (Pipe `pg_dump` → `pg_restore`)
- Optional: Migration globaler Objekte (Rollen) via `pg_dumpall --globals-only`
- Fallback: Dateibasierte Migration (plain SQL) wenn gewünscht
 - Optional: Post-Migration Verifikation (Schema-Vergleich, Zeilenanzahl-Vergleich pro Tabelle)

## Voraussetzungen
- Docker CLI verfügbar und Zugriff auf die Ziel-Engine
- Quell- und Ziel-Container müssen laufen (oder der Ziel-Container wird durch das Tool erstellt)
- Offizielle Postgres-Images enthalten `pg_dump`, `pg_restore`, `psql`, `pg_isready`

## Nutzung
1. Binary ausführen (oder mit `go run` starten)
2. Quell-Container auswählen
3. Zugangsdaten (teils vorbefüllt) bestätigen; Passwort wird versteckt eingegeben
4. Ziel-Container entweder auswählen oder automatisch erstellen lassen (Image, Volume, Port vorschlagen)
5. Streaming-Migration wählen (empfohlen), optional mit globalen Objekten
6. Tool wartet auf "ready" und führt Migration durch
7. Optional Verifikation wählen: `none` (Standard), `quick` (Schema), `full` (Schema + Row Counts)

Beispiel-Flow (vereinfacht):

```
Please choose the original PostgreSQL container:
[0] pg-old
Enter the number of the original container: 0
Enter the username for the original DB [postgres]:
Enter the password for the original DB [hidden, press Enter to keep existing]:
Enter the database name for the dump [postgres]: mydb
...
Do you want to automatically create the destination container? (yes/no): yes
Enter the image for the new container [postgres:latest]: postgres:16
Enter a name for the new container [pg-new]: pg-16
Enter a host port to expose [5433]: 5433
Enter a volume name for data [pgdata_new]: pgdata_16
Enter the username for the new DB [postgres]:
Enter the password for the new DB [hidden, press Enter to keep existing]:
Waiting for the new PostgreSQL to be ready...
Use streaming migration (no temporary file)? (yes/no): yes
Also migrate global objects (roles)? (yes/no): no
Streaming dump from container 'pg-old' to container 'pg-16' for database 'mydb'...
Database migration completed successfully.
```

## Hinweise & Grenzen
- Streaming über stdin erlaubt kein paralleles `pg_restore -j`. Für sehr große DBs evtl. besser:
  - Archivdatei (`pg_dump -Fc`) lokal erzeugen, danach `pg_restore -j N` ins Ziel
- `pg_upgrade` ist eine Alternative, benötigt aber Datenverzeichnisse beider Versionen und andere Rahmenbedingungen
- Sicherheit: Passwörter werden nicht geloggt; im Streaming-Pfad wird keine Shell mehr verwendet
 - Verifikation: `quick` vergleicht Schema (ohne Owner/ACLs), `full` ergänzt Row Counts für alle Nutzertabellen

## Nicht-interaktiver Modus (Kubernetes / CI)

Ohne Argumente startet der klassische interaktive Docker-Wizard. Mit Argumenten läuft das Tool im nicht-interaktiven Modus: Es verbindet sich per TCP zu Quelle und Ziel und führt `pg_dump`, `pg_restore`, `psql`, `pg_isready` (und optional `pg_dumpall`) lokal aus. Bei jedem Fehler beendet es sich mit Exit-Code ≠ 0 (2: Konfigurationsfehler, 1: Laufzeit-/Verifikationsfehler).

Beispiel:

```
docker-pgupgrade-go \
  -src-host 10.0.0.5 -src-db mydb \
  -dst-host 10.0.0.6 -dst-db mydb \
  -dst-wait 120 -verify full
```

Flags und Env-Variablen (Flag gewinnt, Env liefert den Default):

| Flag | Env | Bedeutung |
| --- | --- | --- |
| `-src-host` | `PGSRC_HOST` | Quell-Host (Pflicht) |
| `-src-port` | `PGSRC_PORT` | Quell-Port (Default 5432) |
| `-src-user` | `PGSRC_USER` | Quell-User (Default `postgres`) |
| `-src-password` | `PGSRC_PASSWORD` | Quell-Passwort (Env bevorzugen, Flag erscheint in Prozesslisten) |
| `-src-db` | `PGSRC_DB` | Quelldatenbank (Pflicht) |
| `-dst-host` | `PGDST_HOST` | Ziel-Host (Pflicht) |
| `-dst-port` | `PGDST_PORT` | Ziel-Port (Default 5432) |
| `-dst-user` | `PGDST_USER` | Ziel-User (Default `postgres`) |
| `-dst-password` | `PGDST_PASSWORD` | Ziel-Passwort |
| `-dst-db` | `PGDST_DB` | Zieldatenbank (Default: `PGSRC_DB`) |
| `-globals` | `PGUPGRADE_GLOBALS` | Globale Objekte (Rollen) migrieren; wahr: `1`, `true`, `yes`, `y` (case-insensitiv) |
| `-verify` | `PGUPGRADE_VERIFY` | `none` (Default), `quick`, `full`; unbekannte Werte brechen mit Konfigurationsfehler (Exit 2) ab |
| `-dst-wait` | `PGUPGRADE_DST_WAIT` | Sekunden bis das Ziel Verbindungen akzeptieren muss |

Voraussetzung: PostgreSQL-Client-Tools (`postgresql-client`) müssen im gleichen Container/Image installiert sein. Die Client-Tools müssen mindestens so neu sein wie der neueste beteiligte PostgreSQL-Server (`pg_dump`/`pg_restore` verweigern sonst den Lauf mit „server version mismatch").

Hinweis zu `-globals`: Der Import globaler Objekte überschreibt Rollen am Ziel — inklusive deren Passwort-Hashes. Wenn Quelle und Ziel dasselbe Setup nutzen, gilt danach am Ziel das Passwort der Quelle (im Beispiel oben würde `PGDST_PASSWORD` danach `srcpass` entsprechen).

## Kubernetes

Das Repository enthält eine `Dockerfile`: Mehrstufiger Build (Go → statisches Binary) auf Basis des offiziellen `postgres:18`-Images, das alle benötigten Client-Tools mitbringt. CI baut und pusht das Image nach `ghcr.io/traktuner/docker-pgupgrade-go`.

Ein Beispiel-Job für eine Migration im Cluster liegt unter [`k8s/migration-job.example.yaml`](k8s/migration-job.example.yaml): Verbindungswerte stehen inline im Manifest (für Produktivumgebungen gerne via ConfigMap ausgelagert), Passwörter kommen aus einem Secret. `backoffLimit: 0` verhindert blinde Wiederholungen; `restartPolicy: Never` hält den Pod mit Logs für Nachuntersuchung.

Wichtig: Die Zieldatenbank muss existieren und leer sein (frische Instanz); `pg_restore --clean --if-exists` entfernt beim Restore vorhandene Objekte. Ein Ausschalten der Quellinstanz während der Migration gehört zur üblichen Vorsicht bei Online-Dumps.

## Build
- Voraussetzungen: Go, Docker
- Abhängigkeiten holen und bauen:

```
go get golang.org/x/term@latest
go mod tidy
go build ./...
```
