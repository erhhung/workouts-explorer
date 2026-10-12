# Source Code Formatting

## Tools And Commands

Go uses the Go toolchain's `gofmt`; it is authoritative, including for generated
OpenAPI Go. JavaScript, TypeScript, React JSX/TSX, and CSS use Prettier 3.9.9,
exactly pinned in `ui/package.json` and `ui/package-lock.json`. Install dependencies
with Node 24 and `npm ci --prefix ui` before running the formatting targets.

From the repository root:

```sh
make format        # Write Go and JS/TS/React/CSS formatting.
make format-check  # Check both without writing files.
make fmt           # Check-only alias, also used by make verify and CI.
```

To format or check only JS/TS/React/CSS, including the acceptance Node scripts:

```sh
npm --prefix ui run format
npm --prefix ui run format:check
```

Both npm scripts run from `ui/` but explicitly select the root configuration
and ignore file. Their quoted repository-wide extension whitelist includes
`js`, `jsx`, `cjs`, `mjs`, `ts`, `tsx`, and `css`, covering `ui/src/`,
`ui/scripts/`, `ui/vite.config.ts`, and `deploy/acceptance/`. New source files
with these extensions are included automatically unless ignored.

Go targets run from the repository root and cover `api/`, `internal/`, `worker/`,
and `tests/`, including the chart tests in `tests/helm/`.
Generated Go already receives `gofmt` from its generator. Do not hand-edit
generated API code; regenerate it with `make generate` and check drift with
`make check-generated`.

SQL is formatted manually under the [SQL conventions](#sql). `pg_format` is
an optional inspection aid for ordinary standalone statements, not an automatic
repository formatter. Neither `make format` nor the npm scripts format SQL.
For an ordinary statement copied to a scratch file outside the repository:

```sh
pg_format /tmp/statement.sql > /tmp/statement.formatted.sql
diff -u /tmp/statement.sql /tmp/statement.formatted.sql
```

Review the output manually; do not apply it automatically to Goose migrations,
psql scripts, or embedded SQL strings. See [Formatter Policy](#formatter-policy)
for the constructs that must be preserved.

## Project Conventions

The root `.prettierrc.json` selects double quotes, semicolons, two-space
indentation without tabs, LF line endings, and a 100-column preferred print
width. Prettier handles wrapping, trailing commas, braces, and CSS layout;
the print width is a wrapping preference, not a hard limit for literals.

Embedded-language formatting is disabled so template-string SQL, shaders,
HTML, and other embedded content are not reformatted. Preserve literal values,
object property order, comments explaining invariants, and existing Unicode.
Formatting changes must not rename symbols, refactor control flow, or alter
CSS selectors, declaration order, values, or design tokens.

CSS checker expectations follow the existing design values, not stale source
layout assertions. The artifact-group margin remains `var(--space-8)`; its
checker was corrected from the outdated `var(--space-6)` expectation without
changing CSS or weakening the spacing assertion.

Separate top-level imports, constants, helpers, and execution phases with a
single blank line where it aids navigation. Prettier preserves these logical
separators but does not invent them. Keep related statements together.

## Exclusions

The root `.prettierignore` excludes dependency directories, generated code,
vendor code, minified assets, `dist`, coverage reports, Playwright output, and
Git internals. Public UI assets, including generated favicons and SVG sprites,
are not formatter inputs. TypeScript-emitted Vite artifacts are excluded and
remain forbidden by `make check-ui-artifacts`.

Acceptance `results/`, `fixtures/`, and exported data are not source formatting
targets. Never reformat the profile manifest, captured evidence, exported JSON,
schema files, images, or binary assets: profile bindings and evidence checks
depend on raw-byte hashes. Executable fixture helper modules outside those
data directories are source and do receive formatting.

The CLI whitelist and ignore file exclude JSON, YAML, SQL, Markdown, HTML,
images, and other data formats. The formatter's own JSON configuration and npm
metadata are maintained directly or by npm, not bulk-formatted. No additional
automatic formatter is introduced. SQL remains governed by the [SQL conventions](#sql).

## Verification

After source formatting, run:

```sh
make format-check
node --test deploy/acceptance/*.test.cjs
COVERAGE_ACCEPTANCE_PROFILE=stress node --test deploy/acceptance/*.test.cjs
npm --prefix ui test
npm --prefix ui run typecheck
npm --prefix ui run build
make check-ui-artifacts
git diff --check
```

UI tests include the theme CSP hash, generated favicon, Data Sync, dialog, and
Map CSS contracts. CSS checks normalize source whitespace so line wrapping
does not change their outcome. Full local verification remains `make verify`;
CI reaches the combined format check through its existing `fmt` dependency.

## Documentation Dates

Use the IANA time zone `America/Los_Angeles` for human-readable documentation
dates. Write date-only values as `M/D/YYYY`, without leading zeros, for example
`10/5/2026`. Do not append `PDT` or `PST` to a date-only value.

For human-readable timestamps where the UTC offset matters, use Los Angeles
local time and the correct `PDT` or `PST` abbreviation for that date, for example
`10/5/2026 9:00 AM PDT` or `1/5/2026 9:00 AM PST`. Resolve the offset using
`America/Los_Angeles`; do not hardcode daylight saving time or a fixed UTC offset.

This convention applies to documentation prose, including prose about SQL and
embedded queries, not machine data or executable examples. Preserve JSON, logs,
ISO payloads, URLs, quoted source-code values, and fixed ISO examples exactly to
avoid changing API contracts. Do not convert SQL date fields or date/time
literals in SQL or embedded query strings to the human-readable date format.

## SQL

This repository formats PostgreSQL, PostGIS, PL/pgSQL, Goose migrations, and
psql pipeline scripts manually. Apply these rules to SQL under
`api/migrations/`, `internal/osm/migrations/`, and `osm/`.

### Formatter Policy

Do not run `pg_format --inplace` over repository SQL. The formatter is useful
for inspecting ordinary standalone statements, but it does not safely preserve
all constructs used here. In particular, tested versions have rewritten psql
variables, moved Goose directives, changed aliases that also parse as keywords,
and produced malformed PL/pgSQL declaration or indentation structure.

If `pg_format` is used as an aid, write its output outside the repository and
review a diff. Never accept formatter output without checking Goose boundaries,
psql directives and variables, dollar-quoted bodies, aliases, and function
signatures.

### Layout

- Indent with four spaces. Do not use tabs.
- Write SQL keywords in uppercase. Keep PostgreSQL types, built-in functions,
  identifiers, and aliases in their established case.
- Put one space after commas and around binary operators such as `=`, `<>`,
  `+`, and `||`.
- Put a space before an opening parenthesis for SQL clauses and constraints,
  such as `CHECK (...)`, `IN (...)`, and `VALUES (...)`. Do not add a space
  between a function name and its call parenthesis.
- Separate top-level statements with one empty line.
- Keep short, closely related expressions on one line when they remain easy to
  scan. Wrap long column lists, arguments, predicates, and `SET` clauses one
  logical item per line.
- Indent continued predicates beneath `WHERE`, `ON`, or `HAVING`, with `AND`
  and `OR` aligned at the continuation level.
- Use explicit `AS` for table aliases when the surrounding file does so. Do not
  rename identifiers or aliases as part of formatting.
- Preserve comments next to the statement or invariant they explain. Formatting
  must not reflow operator instructions, expected error text, or test fixtures.
- For multiline common table expressions (CTEs), put each CTE on its own line,
  indent its query by four spaces inside `AS (...)`, and align closing parentheses
  with the CTE name. Keep the final statement at the top-level indentation.

Example:

```sql
UPDATE app.jobs AS job
SET
    state = 'running',
    started_at = transaction_timestamp()
FROM app.accounts AS account
WHERE job.account_id = account.id
    AND account.state = 'active'
    AND job.state IN ('queued', 'retrying');
```

### Functions And Blocks

Use descriptive dollar quoting instead of anonymous `$$` delimiters:

- `$function$` for `CREATE FUNCTION` bodies.
- `$procedure$` for `CREATE PROCEDURE` bodies.
- `$block$` for anonymous `DO` blocks, or a more descriptive tag such as
  `$prepare$` or `$guard_evaluations$` when it improves navigation.

Place the language and security properties on separate lines. Put declarations
one per line, indent the body by four spaces, and leave an empty line between
distinct body phases when useful.

```sql
CREATE FUNCTION app.example(target_id uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $function$
DECLARE
    current_state text;
BEGIN
    SELECT state
    INTO current_state
    FROM app.jobs
    WHERE id = target_id;

    IF current_state IS NULL THEN
        RAISE EXCEPTION 'job % does not exist', target_id;
    END IF;
END;
$function$;
```

### Goose Migrations

- Preserve `-- +goose Up` and `-- +goose Down` exactly.
- Wrap every function, procedure, or `DO` body containing internal semicolons
  with exactly one matching directive pair:

  ```sql
  -- +goose StatementBegin
  CREATE FUNCTION ...
  AS $function$
  ...
  $function$;
  -- +goose StatementEnd
  ```

- Keep the directives at the left margin and outside the dollar-quoted body.
- Leave one empty line before and after a protected procedural statement.
- Keep schema metadata updates explicit in both directions. Migration filenames,
  Goose ordinals, `schema_version`, `minimum_runtime_version`, runtime constants,
  and migration contract tests must change together.
- Formatting an existing migration must not alter behavior, privileges,
  ownership, security mode, search paths, error text, or downgrade semantics.

### psql Pipeline Scripts

- Preserve psql directives such as `\if`, `\endif`, `\echo`, and `\set` at the
  left margin.
- Preserve variable syntax exactly, including `:'OSM_REGION_ID'` and
  `:"OSM_BUILD_SCHEMA"`. Do not let a formatter quote, capitalize, or add spaces
  inside these expressions.
- Keep transaction and checkpoint boundaries visible. Formatting must never
  move a checkpoint call out of the transaction that writes its batch output.
- Keep one empty line between setup statements, procedural definitions, `CALL`,
  cleanup, and validation statements.

### Verification

After changing SQL, run:

```sh
go test ./api/migrations ./internal/osm/migrations ./internal/osm
go test ./...
git diff --check
```

Confirm anonymous procedural delimiters were not introduced:

```sh
rg 'AS \$\$|DO \$\$' api/migrations internal/osm/migrations osm
```

For migration or pipeline behavior changes, also execute the affected scripts
against disposable PostgreSQL/PostGIS before deployment. Static formatting and
contract tests do not replace a live migration test.
