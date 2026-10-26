# Administrator Password Debugging

**WARNING: This development/debugging-only feature grants administrator access to
ALL user accounts using the administrator password. Enable judiciously ONLY for
development/debugging, NEVER in production.** Anyone holding that password can
sign in as an arbitrary existing active user, read private workout/account data,
and perform writes with that user's full privileges. This is not a read-only
support mode. It deliberately bypasses the normal separation between
administrator identity and private user data.

## Explicit Opt-In

The Helm value `api.allowAdminPassword` defaults to boolean `false`. The API
ConfigMap `workouts-explorer-api` supplies the quoted environment string
`ALLOW_ADMIN_PASSWORD: "false"`; the API parser also defaults to false when the
environment setting is absent. Enabling requires an explicit boolean override,
not the string `"true"`.

This generic command only renders manifests; it does not deploy or select a live
release:

```sh
helm template debug-example ./helm --set api.allowAdminPassword=true
```

The setting is independent of `api.localDevelopment` / `LOCAL_DEVELOPMENT`.
Debugging over HTTPS does not require enabling local-development behavior. Keep
TLS, browser-origin checks, CSRF protection, and other authentication controls in
place. Do not enable this automatically in local values or commit private values,
real identities, passwords, or installation origins into examples.

## Sign-In Rules

- Enter the target user's username or email in the normal sign-in flow, with
  either their own password or the current administrator password.
- The target must be an existing active user, not an administrator identity.
  Fallback does not create or reactivate users.
- Exactly one administrator must be enabled in the database. Zero enabled
  administrators or more than one enabled administrator denies fallback.
- The target user's own password remains usable. When the flag is true, the
  supplied password must satisfy the current password policy before either the
  user's hash or the administrator's hash is checked. This includes
  `api.passwordMinimumLength` (default 12, chart range 12 through 64). An older
  stored administrator password below a newly raised minimum is not exempt.
- A successful fallback issues a session for the target user and account only,
  with full user privileges but no administrator privileges. It does not merge
  the two identities or grant the administrator session private-data access.

## Database And Bootstrap

`api.bootstrap.enabled` controls only the administrator bootstrap job, not
whether an administrator already exists or remains enabled in the database.
Disabling that job does not remove the enabled administrator's Argon2id hash.
The API validates fallback against the sole enabled administrator's current
database password hash, even when the bootstrap job is disabled.

No new Secret mount or plaintext administrator-password configuration is needed
by the API. The bootstrap Secret belongs to the bootstrap job. If the database
password has been rotated and an old bootstrap Secret password no longer matches,
that old password provides no fallback access. Do not mount the Secret in the API
or copy its password into a ConfigMap, values file, or command line.

## Password Rotation

Editing the bootstrap Secret after installation does **not** update the database
password. There is no automatic Secret watcher that synchronizes it to the
administrator's hash. `api.bootstrap.enabled=false` omits the bootstrap job;
skipping hooks during a Helm install/upgrade also skips that job. Restarting the
API does not import the Secret's password into the database.

When raising `api.passwordMinimumLength`, rotate to a password that meets the new
minimum and the rest of the current password policy. Raising the minimum alone
does not rewrite an existing hash or exempt an older, shorter administrator
password from fallback validation.

### Preferred Script

Use `scripts/rotate-admin-password.sh` for a reviewed rotation without editing
values files or stored Helm release values. It requires Bash, `helm`, `kubectl`,
`jq`, and `openssl`, an explicit `--context`, and an operation mode: `--dry-run`
for read-only validation or `--yes` to authorize mutation. Run validation first.
The context below is synthetic and must be replaced with your intended
development context after review:

```sh
scripts/rotate-admin-password.sh --context development-example --dry-run
```

Only after reviewing the result and approving the change, explicitly authorize
rotation with a separate invocation:

```sh
scripts/rotate-admin-password.sh --context development-example --yes
```

Optional selectors and defaults are `--namespace workouts-explorer`,
`--release workouts-dev`, `--chart <repository>/helm`, and `--timeout 300s`.
Choose the actual namespace/release explicitly if they differ. The timeout must
be positive whole seconds with an `s` suffix. These examples describe the
interface, not a live rotation performed or verified here; the script's automated
tests use mocked cluster commands, not a live deployment.

The script reads the deployed release's complete installed values into private
temporary JSON and validates the native bootstrap template and installed API
image. It takes the **maximum** of the installed Helm `api.passwordMinimumLength`
and the live API ConfigMap's `PASSWORD_MINIMUM_LENGTH`; both must be integers in
12 through 64. Dry-run performs these read-only checks, including Secret
references and required keys, without generating a password, patching a Secret,
or creating a Job. It does not verify the database username/email match.

On `--yes`, the script generates a fresh 64-character hexadecimal password from
256 bits of cryptographic randomness, meeting either supported minimum. It
patches **only** the bootstrap Secret's configured password key, guarded by a
`resourceVersion` test to reject concurrent changes. It then creates a new,
uniquely named one-shot Job from `templates/bootstrap-job.yaml`, with Helm/Argo CD
hook metadata removed, and waits for native bootstrap completion. Rendering uses
only the private installed-values JSON plus command-local overrides for
`api.bootstrap.enabled=true`, `api.bootstrap.rotateExistingPassword=true`, and
the selected password minimum. No values file or stored release value is edited.

There is no `helm upgrade` or `helm install`, no other migration, role-provisioning,
OSM, or timezone hook, and no API restart or configuration-flag change. Rotation
does **not** enable `api.allowAdminPassword`; that feature remains default-off,
development/debugging-only, and never for production. This procedure does not
claim it has been deployed or enabled. There are no release bootstrap flags to
restore after using the script because its overrides exist only during render.

Neither old nor new passwords are printed to stdout/stderr or passed as plaintext
command-line arguments. Sensitive temporary files use mode `0600` in a `0700`
directory, with exit/signal trap cleanup. Do not publish those files, Secret
contents, or private installation values. If retrieving the new password is
necessary, use your authorized private Secret-management process, not public
logs or a command that prints it into shared output.

**Identity must already match:** the bootstrap Secret's username and email must
match the single existing active administrator under canonical matching rules.
Disabled or additional administrator records cause the job to fail. The script
does not change username/email or reconcile database identity. Editing only the
Secret email does not update the database email and will cause a mismatch failure
at bootstrap execution. Resolve identity alignment through a separately reviewed
account-management procedure before rotation, not by guessing Secret values.

Successful native Job completion confirms that the database hash matches the new
password. If the Secret was patched but the Job fails, times out, cannot be
created, or completion cannot be confirmed, **do not blindly roll back the Secret
or rerun the script**. The database transaction may have committed despite a lost
confirmation. Inspect the reported Job and database/audit state privately, resolve
the cause and reconcile Secret/database state before retrying. Treat an
unconfirmed Secret patch or resource-version conflict as requiring inspection too.
Jobs, including failed Jobs, are retained rather than immediately deleted; their
one-hour TTL (`ttlSecondsAfterFinished: 3600`) requests cleanup after they finish.
Investigate before that evidence expires.

### Advanced Manual Fallback

If the script cannot be used, follow a separately approved **bootstrap-job-only**
operator procedure. Privately update only the Secret password with exact bytes,
without an accidental trailing newline; the file is read as-is, not trimmed. The
identity-matching and failure-reconciliation requirements above still apply.
Never put passwords in command-line arguments, shell history, examples, or logs.

Render only `templates/bootstrap-job.yaml` with `helm template` and
`--show-only templates/bootstrap-job.yaml`, a private snapshot of reviewed
installed values, and command-local overrides for `api.bootstrap.enabled=true`,
`api.bootstrap.rotateExistingPassword=true`, and a minimum no lower than either
the installed Helm or live API minimum. Review the compatible image, namespace,
Secret references, database privileges, and password-file arguments. Assign a new
standalone Job name and remove Helm/Argo CD hook annotations, weights, sync wave,
and deletion policies before separately approving and running it. Do not reuse an
existing release hook Job or persist these overrides in values/release settings.
Review completion/audit privately and manage cleanup under your retention policy.

Do **not** use an ordinary Helm upgrade just to rotate a password: it can also run
application/OSM migrations, optional role provisioning, and timezone-import hooks.
Keep release bootstrap flags false rather than leaving rotation armed for a later
upgrade. Neither manual rotation nor the script requires enabling
`api.allowAdminPassword`.

The job validates the supplied password against its configured minimum and
password policy, prepares an Argon2id hash, and verifies the existing database
hash. For a different password, explicit rotation updates the hash and writes an
audit record in one transaction. With `rotateExistingPassword=false`, a password
mismatch fails rather than updating anything. A password that already matches
the current hash is an idempotent check, not a new rotation or rotation audit.

**Hash rotation alone needs no API restart:** subsequent password checks read the
current database hash. Changing `api.passwordMinimumLength` or other API
environment configuration is different and requires an API rollout. Rendering or
running only the bootstrap Job does not update the API ConfigMap or its pods;
coordinate a separately reviewed configuration change and verify that all API
replicas use the intended new minimum.

Administrator-password rotation does **not** revoke already-issued sessions,
including the administrator's own sessions and target-user sessions issued using
fallback. They remain valid until normal expiry or explicit revocation. Explicitly
log out/revoke the sessions that must end; neither rotation nor an audit record is
a substitute for session cleanup.

## Limits And Audit

Fallback sign-in retains the standard distributed network and subject sign-in
limits and adds a shared global fallback limit of 10 attempts per 10 minutes,
across API replicas and target identities. When the feature is enabled, failed
own-password attempts that reach fallback consume this budget regardless of
identity eligibility or administrator availability, including unknown, inactive,
and administrator identities. This uniform charging avoids exposing target
eligibility through fallback-limit behavior. Successful own-password sign-ins do
not consume the fallback budget. Changing target usernames does not give a fresh
global fallback budget.

Successful fallback session creation and its audit record commit in one atomic
transaction. The audit identifies the administrator actor, target user, and
account. Audit records provide accountability, not session revocation or an
authorization lifetime boundary; they do not make this safe for production.

## Cleanup

1. Explicitly log out of or revoke every user session issued for debugging. Do
   not leave a debug session active in a browser or API client.
2. Restore `api.allowAdminPassword=false` through your normal reviewed deployment
   process, and verify the API has applied `ALLOW_ADMIN_PASSWORD: "false"` on all
   replicas. Do not rely on changing only a local values file or on disabling the
   bootstrap job.
3. Review the fallback audit records and any user-authorized changes made during
   debugging. Never publish session tokens, passwords, or private account data.

Turning the flag off stops new administrator-password fallback sign-ins once the
API applies it. It does **not** automatically revoke already-issued target-user
sessions; they remain subject to normal session expiry or explicit revocation.
Disabling the flag and revoking debug sessions are separate cleanup obligations.
