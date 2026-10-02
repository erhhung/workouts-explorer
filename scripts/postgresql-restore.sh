#!/usr/bin/env bash

set -euo pipefail

usage() {
    cat <<EOF
Usage:
  $(basename "$0") <dump.sql>
  $(basename "$0") -

Restore a PostgreSQL cluster from a plain-text SQL dump.

Arguments:
  <dump.sql>    Path to PostgreSQL dump file.
  -             Read the SQL dump from stdin.

Environment:
  PGPASSWORD    PostgreSQL password. Required.

Optional environment:
  PGHOST        PostgreSQL host.
                Default:
                postgresql-postgresql-0.postgresql-postgresql-headless.postgresql.svc.cluster.local

  PGPORT        PostgreSQL port. Default: 5432
  PGUSER        PostgreSQL user. Default: postgres
  PGDATABASE    Initial PostgreSQL database. Default: postgres
  LOG           Restore log. Default: /tmp/postgresql-restore.log
  ERRLOG        Error excerpt log. Default: /tmp/postgresql-restore-error.log

Examples:
  $(basename "$0") postgresql-homelab.sql
  gzip -dc postgresql-homelab.sql.gz | $(basename "$0") -
EOF
}

#
# Arguments
#
if (( $# == 0 )); then
    usage
    exit 0
fi

if (( $# != 1 )); then
    echo "ERROR: expected exactly one argument." >&2
    echo >&2
    usage >&2
    exit 2
fi

DUMP="$1"

if [[ "$DUMP" != "-" ]]; then
    if [[ "$DUMP" != *.sql ]]; then
        echo "ERROR: dump file must have a .sql extension: $DUMP" >&2
        exit 2
    fi

    if [[ ! -f "$DUMP" ]]; then
        echo "ERROR: dump file does not exist: $DUMP" >&2
        exit 1
    fi

    if [[ ! -r "$DUMP" ]]; then
        echo "ERROR: cannot read dump file: $DUMP" >&2
        exit 1
    fi
fi

if [[ -z "${PGPASSWORD:-}" ]]; then
    echo "ERROR: PGPASSWORD environment variable is not set." >&2
    exit 1
fi

#
# PostgreSQL connection
#
PGHOST="${PGHOST:-postgresql-postgresql-0.postgresql-postgresql-headless.postgresql.svc.cluster.local}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-postgres}"
PGDATABASE="${PGDATABASE:-postgres}"

LOG="${LOG:-/tmp/postgresql-restore.log}"
ERRLOG="${ERRLOG:-/tmp/postgresql-restore-error.log}"

TMPDIR_RESTORE=""
PV_FIFO=""
DIALOG_FIFO=""
DIALOG_PID=""
PROGRESS_PID=""

# shellcheck disable=SC2329
cleanup() {
    if [[ -n "${PROGRESS_PID:-}" ]]; then
        kill "$PROGRESS_PID" >/dev/null 2>&1 || true
        wait "$PROGRESS_PID" >/dev/null 2>&1 || true
    fi

    if [[ -n "${DIALOG_PID:-}" ]]; then
        kill "$DIALOG_PID" >/dev/null 2>&1 || true
        wait "$DIALOG_PID" >/dev/null 2>&1 || true
    fi

    if [[ -n "${TMPDIR_RESTORE:-}" && -d "$TMPDIR_RESTORE" ]]; then
        rm -rf "$TMPDIR_RESTORE"
    fi

    # Restore terminal state even if dialog/pv/psql was interrupted.
    stty sane </dev/tty >/dev/null 2>&1 || true
    reset >/dev/tty 2>&1 || true
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

for cmd in \
    awk basename dialog mkfifo mktemp psql pv reset stat stdbuf stty tail
do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "ERROR: required command not found: $cmd" >&2
        exit 1
    fi
done

#
# Determine whether progress can be calculated as a percentage.
#
if [[ "$DUMP" == "-" ]]; then
    DUMP_NAME="<stdin>"
    DUMP_SIZE=""
    DUMP_SIZE_DISPLAY="unknown (streaming input)"
else
    DUMP_NAME="$(basename "$DUMP")"
    DUMP_SIZE="$(stat -c '%s' "$DUMP")"

    DUMP_GIB="$(
        awk -v n="$DUMP_SIZE" \
            'BEGIN { printf "%.2f", n / 1073741824 }'
    )"

    DUMP_SIZE_DISPLAY="${DUMP_GIB} GiB"
fi

: >"$LOG"

if ! dialog \
    --title "PostgreSQL Restore" \
    --yesno \
"Restore:

$DUMP_NAME

Size: $DUMP_SIZE_DISPLAY

To:
$PGHOST:$PGPORT/$PGDATABASE

WARNING:
All existing PostgreSQL client sessions will be terminated.

Existing CREATE ROLE statements will be skipped.
The existing repmgr database will be preserved.

Continue?" \
    21 78 \
    </dev/tty \
    2>/dev/tty
then
    exit 0
fi

#
# Kill existing client sessions.
#
# The restore itself runs with synchronous_commit=local
# because the synchronous replicas may not be running yet.
#
dialog \
    --title "PostgreSQL Restore" \
    --infobox \
    "Terminating existing PostgreSQL client sessions..." \
    6 64 \
    </dev/tty \
    2>/dev/tty

set +e

PGOPTIONS='-c synchronous_commit=local' \
PGAPPNAME='postgresql-restore-cleanup' \
psql \
    -v ON_ERROR_STOP=1 \
    -h "$PGHOST" \
    -p "$PGPORT" \
    -U "$PGUSER" \
    -d "$PGDATABASE" \
    -c "
SELECT
    pid,
    usename,
    application_name,
    pg_terminate_backend(pid, 5000) AS terminated
FROM pg_stat_activity
WHERE pid <> pg_backend_pid()
  AND backend_type = 'client backend'
ORDER BY pid;
" \
    >>"$LOG" 2>&1

rc=$?

set -e

if (( rc != 0 )); then
    tail -200 "$LOG" >"$ERRLOG"

    dialog \
        --title "PostgreSQL Restore FAILED" \
        --textbox "$ERRLOG" \
        30 110 \
        </dev/tty \
        2>/dev/tty

    exit "$rc"
fi

#
# Set up progress plumbing:
#
#   pv stderr --> PV_FIFO --> formatter --> DIALOG_FIFO --> dialog
#
# SQL:
#
#   dump/stdin --> pv --> awk --> psql
#
TMPDIR_RESTORE="$(mktemp -d /tmp/postgresql-restore.XXXXXX)"
PV_FIFO="$TMPDIR_RESTORE/pv"
DIALOG_FIFO="$TMPDIR_RESTORE/dialog"

mkfifo "$PV_FIFO" "$DIALOG_FIFO"

if [[ "$DUMP" == "-" ]]; then
    INITIAL_STATUS="Streaming restore from stdin..."
else
    INITIAL_STATUS="Starting restore..."
fi

dialog \
    --title "PostgreSQL Restore" \
    --backtitle "Restoring $DUMP_NAME" \
    --gauge "$INITIAL_STATUS" \
    14 78 0 \
    <"$DIALOG_FIFO" \
    2>/dev/tty &

DIALOG_PID=$!

#
# Convert pv's machine-readable progress into dialog's XXX protocol.
#
if [[ -n "$DUMP_SIZE" ]]; then
    #
    # Regular file:
    #
    #   elapsed bytes rate percentage
    #
    (
        while read -r elapsed bytes rate percent; do
            [[ -n "${percent:-}" ]] || continue

            percent="${percent%%.*}"

            (( percent < 0 )) && percent=0
            (( percent > 100 )) && percent=100

            status="$(
                awk \
                    -v bytes="$bytes" \
                    -v total="$DUMP_SIZE" \
                    -v rate="$rate" \
                    -v elapsed="$elapsed" \
                    '
                    function human(n, i, unit) {
                        split("B KiB MiB GiB TiB", unit, " ")
                        i = 1

                        while (n >= 1024 && i < 5) {
                            n /= 1024
                            i++
                        }

                        if (i == 1)
                            return sprintf("%.0f %s", n, unit[i])

                        return sprintf("%.2f %s", n, unit[i])
                    }

                    function duration(sec, d, h, m, s, out) {
                        sec = int(sec)

                        d = int(sec / 86400)
                        sec %= 86400

                        h = int(sec / 3600)
                        sec %= 3600

                        m = int(sec / 60)
                        s = sec % 60

                        out = ""

                        if (d > 0)
                            out = out sprintf("%dd ", d)

                        if (h > 0 || d > 0)
                            out = out sprintf("%dh ", h)

                        if (m > 0 || h > 0 || d > 0)
                            out = out sprintf("%dm ", m)

                        out = out sprintf("%ds", s)

                        return out
                    }

                    BEGIN {
                        if (rate > 0 && bytes < total)
                            eta = (total - bytes) / rate
                        else
                            eta = 0

                        printf \
                            "Transferred: %s / %s\nRate: %s/s\nElapsed: %s    ETA: %s", \
                            human(bytes), \
                            human(total), \
                            human(rate), \
                            duration(elapsed), \
                            rate > 0 ? duration(eta) : "calculating..."
                    }
                    '
            )"

            printf 'XXX\n'
            printf '%s\n' "$percent"
            printf '%s\n' "$status"
            printf 'XXX\n'
        done
    ) <"$PV_FIFO" >"$DIALOG_FIFO" &
else
    #
    # stdin:
    #
    # Total size is unknown, so display bytes/rate/elapsed while leaving
    # the percentage gauge at zero.
    #
    (
        while read -r elapsed bytes rate; do
            [[ -n "${rate:-}" ]] || continue

            status="$(
                awk \
                    -v bytes="$bytes" \
                    -v rate="$rate" \
                    -v elapsed="$elapsed" \
                    '
                    function human(n, i, unit) {
                        split("B KiB MiB GiB TiB", unit, " ")
                        i = 1

                        while (n >= 1024 && i < 5) {
                            n /= 1024
                            i++
                        }

                        if (i == 1)
                            return sprintf("%.0f %s", n, unit[i])

                        return sprintf("%.2f %s", n, unit[i])
                    }

                    function duration(sec, d, h, m, s, out) {
                        sec = int(sec)

                        d = int(sec / 86400)
                        sec %= 86400

                        h = int(sec / 3600)
                        sec %= 3600

                        m = int(sec / 60)
                        s = sec % 60

                        out = ""

                        if (d > 0)
                            out = out sprintf("%dd ", d)

                        if (h > 0 || d > 0)
                            out = out sprintf("%dh ", h)

                        if (m > 0 || h > 0 || d > 0)
                            out = out sprintf("%dm ", m)

                        out = out sprintf("%ds", s)

                        return out
                    }

                    BEGIN {
                        printf \
                            "Transferred: %s\nRate: %s/s\nElapsed: %s\nTotal size: unknown (stdin)", \
                            human(bytes), \
                            human(rate), \
                            duration(elapsed)
                    }
                    '
            )"

            printf 'XXX\n'
            printf '0\n'
            printf '%s\n' "$status"
            printf 'XXX\n'
        done
    ) <"$PV_FIFO" >"$DIALOG_FIFO" &
fi

PROGRESS_PID=$!

#
# pv source function.
#
# Numeric output goes to PV_FIFO while SQL goes to stdout.
#
read_dump() {
    if [[ -n "$DUMP_SIZE" ]]; then
        pv \
            --numeric \
            --interval 1 \
            --size "$DUMP_SIZE" \
            --format '%t %b %r %{progress-amount-only}' \
            "$DUMP" \
            2>"$PV_FIFO"
    else
        pv \
            --numeric \
            --interval 1 \
            --format '%t %b %r' \
            2>"$PV_FIFO"
    fi
}

set +e

read_dump |
awk '
    # Roles are already created by the fresh cluster bootstrap.
    /^CREATE ROLE / {
        next
    }

    # repmgr is infrastructure state belonging to the newly initialized
    # HA cluster. Consume the remainder of the dump so pv reaches EOF,
    # but do not pass the old repmgr database dump to psql.
    /^-- Database "repmgr" dump$/ {
        skip = 1
        next
    }

    !skip {
        print
    }
' |
PGOPTIONS='-c synchronous_commit=local' \
PGAPPNAME='postgresql-full-restore' \
stdbuf -oL -eL psql \
    -v ON_ERROR_STOP=1 \
    -h "$PGHOST" \
    -p "$PGPORT" \
    -U "$PGUSER" \
    -d "$PGDATABASE" \
    >"$LOG" 2>&1

pipeline_status=("${PIPESTATUS[@]}")

set -e

#
# Close progress UI.
#
wait "$PROGRESS_PID" 2>/dev/null || true
PROGRESS_PID=""

wait "$DIALOG_PID" 2>/dev/null || true
DIALOG_PID=""

#
# Preserve failure from any pipeline stage:
#
#   read_dump | awk | psql
#
rc=0

for status in "${pipeline_status[@]}"; do
    if (( status != 0 )); then
        rc="$status"
    fi
done

if (( rc == 0 )); then
    dialog \
        --title "PostgreSQL Restore" \
        --msgbox \
"Restore completed successfully.

Full log:
$LOG" \
        10 60 \
        </dev/tty \
        2>/dev/tty

    exit 0
fi

tail -200 "$LOG" >"$ERRLOG"

dialog \
    --title "PostgreSQL Restore FAILED" \
    --msgbox \
"Restore failed with exit code $rc.

The last 200 log lines will be shown next.

Full log:
$LOG" \
    12 68 \
    </dev/tty \
    2>/dev/tty

dialog \
    --title "PostgreSQL Restore FAILED" \
    --textbox "$ERRLOG" \
    30 110 \
    </dev/tty \
    2>/dev/tty

echo "Restore failed with exit code $rc" >&2
echo "Full log: $LOG" >&2

exit "$rc"
