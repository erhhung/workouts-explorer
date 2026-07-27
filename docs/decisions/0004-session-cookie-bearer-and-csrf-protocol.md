# ADR 0004: Session, Cookie, Bearer, And CSRF Protocol

## Status

Proposed

This ADR must be accepted before the Milestone 1 OpenAPI authentication contract
is frozen and before Milestone 2 authentication is implemented.

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

## Proposed Decision

### One session model, separate credential issuance

Use one durable `sessions` model with shared lifecycle, authorization, expiry,
and revocation rules. Issue credentials through explicit browser-session and
API-token operations in the OpenAPI contract.

- Browser signin sets a Secure, HTTP-only session cookie and returns session
  metadata plus a session-bound CSRF token. It does not return a bearer token.
- API-token signin returns an opaque bearer token and does not depend on cookie
  authentication or CSRF.
- Both credentials resolve to ordinary revocable session records and authorize
  the same resources for the same identity.

The two operations use identical enumeration-resistant credential validation and
rate-limit policy. Choosing an API-token operation is not itself a privilege
boundary; it only selects the credential transport.

### Credential storage

Session and bearer credentials contain at least 128 bits of cryptographic
entropy. PostgreSQL stores one-way verifiers, not raw credentials. CSRF secrets
are independently generated and stored in a form that permits validation
without exposing bearer material.

The SPA never stores bearer tokens in local storage, session storage, IndexedDB,
or persistent application state.

### Cookie policy

The browser session cookie is:

- `Secure` outside explicitly configured local development;
- `HttpOnly`;
- `SameSite=Lax`;
- scoped to the application origin and `/` path;
- host-only unless deployment requirements prove otherwise; and
- expired when the absolute server-side session expires.

Cookie names and the CSRF header name are fixed contract constants rather than
operator configuration. TLS termination and trusted forwarded-header behavior
are deployment security settings.

### CSRF policy

Every state-changing request authenticated by a cookie requires the session's
CSRF value in the documented request header. Safe reads do not require it.
Bearer-authenticated requests ignore browser cookies and do not require CSRF.

If a request presents both mechanisms, explicit bearer authentication takes
precedence. An invalid bearer credential is not allowed to fall back silently to
a valid cookie.

### Lifecycle

- Session lifetime is absolute and globally configured, default two hours.
- Signout revokes the current session and expires its browser cookie when used.
- Password reset revokes all sessions for the identity.
- Disabled or deleting accounts invalidate authentication immediately.
- Expired and revoked sessions are denied even if a client retains a credential.
- Session rotation and concurrent-session limits are not required for the MVP
  unless the implementation security review identifies a concrete need.

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

## Consequences

- OpenAPI exposes two explicit signin transports with one authorization model.
- Browser JavaScript receives a CSRF token but never needs bearer material.
- Session lookup remains a database operation and requires cleanup of expired
  records.
- Contract and integration tests must cover cookie-only, bearer-only, both,
  missing CSRF, invalid bearer precedence, revocation, and expiry.

## Acceptance Evidence

Before accepting this ADR, finalize operation paths and schemas in OpenAPI,
verify Swagger can obtain and use the bearer credential, and threat-model token
exposure, login CSRF, cookie scope, trusted proxy headers, and mixed credentials.
