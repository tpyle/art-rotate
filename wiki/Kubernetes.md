# Kubernetes — `art-k8s-rotate`

A small controller that rotates the Artifactory tokens stored inside `kubernetes.io/dockerconfigjson` secrets. Designed to run as a CronJob.

## What it does

For each matching Secret in the namespaces you configure:

1. Parses `.dockerconfigjson` and extracts each registry hostname.
2. Probes the hostname for Artifactory by calling `/artifactory/api/system/ping`. If it doesn't respond `OK`, strips a leading subdomain label and tries again (up to `--max-domain-strips`). This lets a `docker.artifactory.example.com` credential find the Artifactory at `artifactory.example.com`.
3. Uses the secret's password as the bearer to introspect and rotate the token, sharing the same logic as the CLI.
4. Writes the new token back into the Secret. The dockerconfigjson `auth` field is regenerated from `username:password`.

When the source is an identity token, the opaque reference form of the new token is what gets written — that's what Docker logins expect.

## Image

Each release publishes a `FROM scratch` image to GHCR:

```
ghcr.io/tpyle/art-k8s-rotate:v0.3.0
ghcr.io/tpyle/art-k8s-rotate:0.3
ghcr.io/tpyle/art-k8s-rotate:latest
```

Single binary (~26 MB), static (`netgo,osusergo`, `CGO_ENABLED=0`), runs as UID 65532. No shell — for debugging, temporarily swap to `gcr.io/distroless/base-debian12:debug`.

## RBAC

The ServiceAccount needs at minimum:

```yaml
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "update"]
```

Full deployment manifest (ServiceAccount + ClusterRole + ClusterRoleBinding + CronJob): [examples/cronjob.yaml](https://github.com/tpyle/art-rotate/blob/main/examples/cronjob.yaml).

## Flags

| Flag | Purpose |
|---|---|
| `--namespaces` | Comma-separated list of namespaces. Mutually exclusive with `--all-namespaces`. |
| `--all-namespaces` | Scan everything the SA can see. |
| `--label-selector` | k8s label selector applied to Secrets, e.g. `art-rotate=true`. |
| `--names` | Comma-separated allow-list of Secret names. |
| `--admin-token` | Optional fallback bearer for create/revoke. |
| `--max-domain-strips` | How many subdomain labels to strip while probing (default `3`). |
| `--min-age` | Skip Secrets whose token was issued less than this ago (e.g. `168h`). |
| `--revoke-old` | Delete the old token after a successful rotation. |
| `--dry-run` | Log what would happen without mutating anything. |
| `--kubeconfig` | Path to a kubeconfig (out-of-cluster). Empty = in-cluster. |
| `--log-level` | `debug`, `info`, `warn`, or `error`. |
| `--probe-timeout` | HTTP timeout per Artifactory ping (default `10s`). |

## Discovery example

Given a Secret like:

```json
{
  "auths": {
    "docker.artifactory.example.com": {
      "username": "alice",
      "password": "cmVmdGtu..."
    }
  }
}
```

The controller probes:

1. `https://docker.artifactory.example.com/artifactory/api/system/ping` — fails or returns non-`OK` (the docker subdomain often only serves the Docker registry path).
2. `https://artifactory.example.com/artifactory/api/system/ping` — returns `OK`. The credential is rotated against this base URL.

If no candidate responds, the Secret is left alone and the skip is logged at info level. Stripping refuses to go below two labels, so we won't end up probing the bare TLD.

## Operational notes

* The Update call is a normal read–modify–write with `ResourceVersion`. If something else mutates the Secret mid-run, the Update returns 409 and the rotation fails for that Secret (the next CronJob run will retry).
* Identity tokens usually need `--admin-token` (or `refreshable: true`) because they can't authorize creation of new tokens for their own subject.
* `--min-age` lets you make the CronJob idempotent across daily runs — set it to `168h` and only week-old tokens get rotated.

## Local invocation

For testing against a kubeconfig:

```bash
art-k8s-rotate \
  --kubeconfig ~/.kube/config \
  --namespaces team-a \
  --label-selector art-rotate=true \
  --dry-run \
  --log-level debug
```
