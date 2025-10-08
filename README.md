# docker-pgupgrade-go

Migriere PostgreSQL-Datenbanken zwischen Docker-Containern (z. B. auf eine neue Major-Version). Fokus: Einfache, sichere Bedienung ohne manuelle Dumps/Kopieren.

## Features
- Nur laufende PostgreSQL-Container werden zur Auswahl angeboten
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
Streaming dump from 'pg-old' to 'pg-16' for database 'mydb'...
Database migration completed successfully.
```

## Hinweise & Grenzen
- Streaming über stdin erlaubt kein paralleles `pg_restore -j`. Für sehr große DBs evtl. besser:
  - Archivdatei (`pg_dump -Fc`) lokal erzeugen, danach `pg_restore -j N` ins Ziel
- `pg_upgrade` ist eine Alternative, benötigt aber Datenverzeichnisse beider Versionen und andere Rahmenbedingungen
- Sicherheit: Passwörter werden nicht geloggt; Quoting in Pipes ist gehärtet
 - Verifikation: `quick` vergleicht Schema (ohne Owner/ACLs), `full` ergänzt Row Counts für alle Nutzertabellen

## Build
- Voraussetzungen: Go, Docker
- Abhängigkeiten holen und bauen:

```
go get golang.org/x/term@latest
go mod tidy
go build ./...
```
