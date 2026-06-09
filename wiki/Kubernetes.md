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

Each tag is a multi-arch manifest covering `linux/amd64` and `linux/arm64`; the container runtime picks the right one automatically.

Single binary (~26 MB), static (`netgo,osusergo`, `CGO_ENABLED=0`), runs as UID 65532. No shell — for debugging, temporarily swap to `gcr.io/distroless/base-debian12:debug`.

## RBAC

The ServiceAccount needs at minimum:

```yaml
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "update"]
```

Full deployment manifest (ServiceAccount + ClusterRole + ClusterRoleBinding + CronJob): [examples/cronjob.yaml](https://github.com/tpyle/art-rotate/blob/trunk/examples/cronjob.yaml).

## Flags

| Flag | Purpose |
|---|---|
| `--namespaces` | Comma-separated list of namespaces. Mutually exclusive with `--all-namespaces`. |
| `--all-namespaces` | Scan everything the SA can see. |
| `--label-selector` | k8s label selector applied to Secrets, e.g. `art-rotate=true`. |
| `--names` | Comma-separated allow-list of Secret names. |
| `--admin-token` | Optional fallback bearer for create/revoke. |
| `--max-domain-strips` | How many subdomain labels to strip while probing (default `3`). |
| `--min-age` | Rotate only if the token was issued at least this long ago (e.g. `168h`). OR-combined with `--expires-within`. Zero disables. |
| `--expires-within` | Rotate only if the token expires within this duration (e.g. `24h`). Non-expiring tokens never satisfy this gate. OR-combined with `--min-age`. Zero disables. |
| `--revoke-old` | Delete the old token after a successful rotation. |
| `--dry-run` | Log what would happen without mutating anything. |
| `--kubeconfig` | Path to a kubeconfig (out-of-cluster). Empty = in-cluster. |
| `--log-level` | `debug`, `info`, `warn`, or `error`. |
| `--probe-timeout` | HTTP timeout per Artifactory ping (default `10s`). |

## Example CronJob

Minimal weekly rotation across two namespaces, only touching tokens older than seven days. Pair this with the ServiceAccount + ClusterRole + ClusterRoleBinding in [examples/cronjob.yaml](https://github.com/tpyle/art-rotate/blob/trunk/examples/cronjob.yaml) for a complete deployment.

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: art-rotate
  namespace: art-rotate
spec:
  schedule: "15 2 * * *"      # daily at 02:15 UTC
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      backoffLimit: 0
      template:
        spec:
          serviceAccountName: art-rotate
          restartPolicy: Never
          securityContext:
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
          containers:
            - name: art-k8s-rotate
              image: ghcr.io/tpyle/art-k8s-rotate:v0.3.1
              args:
                - --namespaces=team-a,team-b
                - --label-selector=art-rotate=true
                - --min-age=168h          # rotate if older than a week ...
                - --expires-within=24h    # ... OR within a day of expiry
              resources:
                requests: { cpu: 50m,  memory: 64Mi }
                limits:   { cpu: 500m, memory: 256Mi }
              securityContext:
                allowPrivilegeEscalation: false
                readOnlyRootFilesystem: true
                capabilities:
                  drop: ["ALL"]
              env:
                # Fallback bearer when individual secret tokens cannot
                # create new tokens for themselves. Optional.
                - name: ARTIFACTORY_ADMIN_TOKEN
                  valueFrom:
                    secretKeyRef:
                      name: art-rotate-admin
                      key: token
                      optional: true
```

Apply with:

```bash
kubectl create namespace art-rotate
kubectl apply -f examples/cronjob.yaml
```

Mark which Secrets are eligible by labelling them (the example uses `art-rotate=true`):

```bash
kubectl -n team-a label secret jfrog-pull art-rotate=true
```

Trigger a one-shot run for testing without waiting for the schedule:

```bash
kubectl create job --from=cronjob/art-rotate art-rotate-now -n art-rotate
kubectl logs -f -n art-rotate job/art-rotate-now
```

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
