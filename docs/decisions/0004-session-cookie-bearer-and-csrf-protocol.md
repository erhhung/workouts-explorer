# ADR 0004: Session, Cookie, Bearer, And CSRF Protocol

## Status

Accepted

## Context

Browser users and REST API clients authenticate against the same local account
system. Browser clients require an HTTP-only cookie and CSRF protection; API
clients require an opaque bearer token without CSRF. Sessions are revocable and
expire after an absolute lifetime of two hours by default.

Returning a reusable bearer credential in every browser signin response would
weaken the protection gained from an HTTP-only cookie. Relying only on cookies
would make Swagger and non-browser clients unnecessarily difficult to use.

The accepted same-origin topology in ADR 0003 avoids CORS in ordinary browser
operation but does not by itself define credential issuance, rotation, or CSRF
token delivery.

## Recommended Decision

### One session model, separate credential issuance

Use one durable `sessions` model with shared lifecycle, authorization, expiry,
and revocation rules. Issue credentials through explicit browser-session and
API-token operations in the OpenAPI contract:

| Operation | Purpose | Credential result |
|---|---|---|
| `POST /api/session` | Sign in a browser | Sets the session cookie and returns session metadata plus the CSRF token |
| `GET /api/session` | Read the current session | Returns session metadata and, for cookie authentication, the CSRF token |
| `DELETE /api/session` | Sign out the current session | Revokes the current session and clears its cookie when cookie-authenticated |
| `POST /api/session-tokens` | Sign in an API or Swagger client | Returns one bearer token plus session metadata and sets no cookie |

- Browser signin sets a Secure, HTTP-only session cookie and returns session
  metadata plus a session-bound CSRF token. It does not return a bearer token.
- API-token signin returns an opaque bearer token and does not depend on cookie
  authentication or CSRF.
- Both credentials resolve to ordinary revocable session records and authorize
  the same resources for the same identity.

The two signin operations accept the same `username` and `password` request
schema and use identical enumeration-resistant credential validation and
rate-limit policy. The `username` field accepts a username or email as required
by the functional specification. Choosing the API-token operation is not itself
a privilege boundary; it only selects the credential transport.

The browser operation returns a session representation containing the compact
uppercase session ID, identity summary, absolute `expiresAt`, and `csrfToken`.
The API-token operation returns the same non-CSRF session metadata plus
`accessToken` and the fixed `tokenType` value `Bearer`. The bearer token appears
only in that creation response.

### Credential storage

Session-cookie and bearer credentials are independent 32-byte random values,
encoded as unpadded base64url. PostgreSQL stores SHA-256 verifiers, not raw
credentials. This fast verifier is appropriate because the input is a uniformly
random 256-bit secret rather than a human password.

CSRF tokens are independent 32-byte random values. The session stores the CSRF
value so `GET /api/session` can return it after cookie authentication; possession
of that value without the HTTP-only session credential does not authenticate a
request. CSRF values are classified as sensitive and excluded from logs,
telemetry, and diagnostics.

The SPA never stores bearer tokens in local storage, session storage, IndexedDB,
or persistent application state.

### Cookie policy

The browser session cookie is named `workouts_session` and is:

- `Secure` outside explicitly configured local development;
- `HttpOnly`;
- `SameSite=Lax`;
- scoped to the application origin and `/` path;
- host-only, with no `Domain` attribute; and
- expired when the absolute server-side session expires.

The cookie name and attributes are fixed contract constants rather than operator
configuration. Local development may disable only `Secure`; it retains
`HttpOnly`, `SameSite=Lax`, host-only scope, and the `/` path. Production cookie
behavior derives from validated public URL configuration rather than untrusted
forwarded headers.

### CSRF policy

Every state-changing request authenticated by a cookie requires the session's
CSRF value in the fixed `X-CSRF-Token` request header. Safe reads do not require
it. A missing or invalid CSRF value returns a forbidden Problem Details response.
Bearer-authenticated requests ignore browser cookies and do not require CSRF.

If a request presents both mechanisms, explicit bearer authentication takes
precedence. An invalid bearer credential is not allowed to fall back silently to
a valid cookie. In that case the API returns an unauthorized Problem Details
response and does not evaluate CSRF.

### Signin request protections

Both signin operations require `application/json`; form-encoded requests are
rejected. The browser-session operation additionally rejects cross-site Fetch
Metadata and an `Origin` header that does not match the configured public origin.
The API does not enable credentialed CORS.

These checks prevent login CSRF without requiring a pre-authentication CSRF
cookie. Requests without browser Fetch Metadata, such as command-line clients,
remain valid when their `Origin` is absent, although command-line clients should
normally use `/api/session-tokens`.

### Lifecycle

- Session lifetime is absolute and globally configured, default two hours.
- Signout revokes the current session and expires its browser cookie when used.
- Password reset revokes all sessions for the identity.
- Disabled or deleting accounts invalidate authentication immediately.
- Expired and revoked sessions are denied even if a client retains a credential.
- Credentials are never refreshed; signing in creates a new session with a new
  absolute lifetime.
- Session rotation and concurrent-session limits are not required for the MVP.

Cookie-authenticated signout requires CSRF. Bearer-authenticated signout revokes
the bearer session without CSRF. When both credentials are present, bearer
precedence means only the bearer session is revoked and the browser cookie is not
cleared.

### Error behavior

- Invalid signin credentials return the same unauthorized Problem Details
  response regardless of whether the username or email exists.
- Expired, revoked, malformed, or unknown credentials return unauthorized.
- Missing or invalid CSRF on otherwise valid cookie authentication returns
  forbidden.
- Authentication errors contain a safe request ID and never return credential,
  verifier, cookie, CSRF, or account-existence details.

## Alternatives Considered

### Return one token in both JSON and a cookie

This is simple for clients, but browser JavaScript can read the response token
and preserve it outside the HTTP-only boundary. It is rejected by the proposal.

### Cookie authentication for every client

This would complicate Swagger and command-line clients and impose CSRF behavior
where browser ambient authority does not exist.

### Stateless signed tokens

Stateless tokens make immediate revocation, password-reset invalidation, and
account-deletion denial harder. Opaque database-backed sessions fit the expected
scale and stated lifecycle.

### One signin operation with a transport field

A request field such as `credentialTransport` would reduce the route count but
produce conditional response and cookie behavior in one operation. Separate
resource-creation operations are clearer in OpenAPI and reduce accidental bearer
exposure in browser code.

### `__Host-` cookie prefix

The prefix strengthens browser enforcement but requires `Secure` even in local
development. The fixed host-only cookie policy retains the important production
properties without requiring local TLS. Reconsider the prefix if local
development adopts HTTPS by default.

## Consequences

- OpenAPI exposes two explicit signin transports with one authorization model.
- Browser JavaScript receives a CSRF token but never needs bearer material.
- Session lookup remains a database operation and requires cleanup of expired
  records.
- Storing the CSRF value permits retrieval after page reload, but it makes the
  database value sensitive even though it is not an authentication credential.
- Contract and integration tests must cover cookie-only, bearer-only, both,
  missing CSRF, invalid bearer precedence, signin origin checks, revocation, and
  expiry.

## Security Review

- **Bearer exposure:** The browser signin response never contains a bearer token,
  and Swagger obtains one only from the explicit API-token operation.
- **Login CSRF:** JSON-only input, same-origin deployment, no credentialed CORS,
  Fetch Metadata, and configured-origin checks protect cookie issuance.
- **Cookie scope:** Secure production transport, HTTP-only access, host-only
  scope, `SameSite=Lax`, and `/` path prevent script access and cross-host reuse.
- **Trusted proxies:** Authentication behavior uses configured public origin and
  does not infer trust from arbitrary forwarded headers. Deployment middleware
  separately allowlists trusted proxies for request metadata.
- **Mixed credentials:** Bearer precedence is deterministic, and an invalid
  bearer cannot downgrade to cookie authentication.
- **Database disclosure:** Session verifiers do not reveal credentials. A leaked
  CSRF value cannot authenticate without the corresponding cookie credential.

Milestone 1 records these operations and schemas in OpenAPI and verifies Swagger
bearer entry as part of contract testing.
