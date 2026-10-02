# SQL Style

This repository formats PostgreSQL, PostGIS, PL/pgSQL, Goose migrations, and
psql pipeline scripts manually. Apply these rules to SQL under
`api/migrations/`, `internal/osm/migrations/`, and `osm/`.

## Formatter Policy

Do not run `pg_format --inplace` over repository SQL. The formatter is useful
for inspecting ordinary standalone statements, but it does not safely preserve
all constructs used here. In particular, tested versions have rewritten psql
variables, moved Goose directives, changed aliases that also parse as keywords,
and produced malformed PL/pgSQL declaration or indentation structure.

If `pg_format` is used as an aid, write its output outside the repository and
review a diff. Never accept formatter output without checking Goose boundaries,
psql directives and variables, dollar-quoted bodies, aliases, and function
signatures.

## Layout

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

## Functions And Blocks

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

## Goose Migrations

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

## psql Pipeline Scripts

- Preserve psql directives such as `\if`, `\endif`, `\echo`, and `\set` at the
  left margin.
- Preserve variable syntax exactly, including `:'OSM_REGION_ID'` and
  `:"OSM_BUILD_SCHEMA"`. Do not let a formatter quote, capitalize, or add spaces
  inside these expressions.
- Keep transaction and checkpoint boundaries visible. Formatting must never
  move a checkpoint call out of the transaction that writes its batch output.
- Keep one empty line between setup statements, procedural definitions, `CALL`,
  cleanup, and validation statements.

## Verification

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
