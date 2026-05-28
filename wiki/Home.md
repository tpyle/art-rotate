# art-rotate

Tools for rotating JFrog Artifactory tokens. Three components share the same rotation core:

* **[CLI](CLI)** — `art-rotate`, a one-shot tool that rotates a token and writes the new one to stdout or a file.
* **[Kubernetes](Kubernetes)** — `art-k8s-rotate`, a controller that rotates the Artifactory tokens stored inside `kubernetes.io/dockerconfigjson` secrets.
* **[GitHub Action](GitHub-Action)** — `actions/rotate`, a composite action that fits into a workflow and emits the new token as a step output.

## Token types

Artifactory has two practical token shapes:

* **Access tokens** — JWTs scoped to one or more permission targets (groups, scoped users, etc.). Created programmatically via the Access API.
* **Identity tokens** — user-bound tokens generated from the JFrog profile UI (or the same Access API with `scope=applied-permissions/user`). The response includes both a JWT and an opaque `reference_token`; the opaque form is what Docker logins and CLI tools accept.

All three components detect identity tokens automatically (via the `applied-permissions/user` scope marker) and request a fresh reference token from the server on rotation.

## Two rotation methods

* **Refresh** — `POST /access/api/v1/tokens` with `grant_type=refresh_token`. Requires the original refresh token (issued at token creation time alongside the access token). Doesn't require admin privilege.
* **Create** — Introspect the existing token via `/access/api/v1/tokens/me`, then `POST /access/api/v1/tokens` with the mirrored scope/audience/refreshability. Requires the bearer to have create-token permission for the subject.

`--method auto` (the default) picks refresh when a refresh token is supplied **and** the source token is refreshable; otherwise it falls back to create.
