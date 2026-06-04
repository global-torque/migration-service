# `go.sh` — make.sh for Go API services

Canonical [make.sh](go.sh) for Go services that build one or more binaries
under `cmd/*` and ship as containers on the Hetzner alpha host (and
optionally a paired worker on Cloud Run).

Repos using this template: i-investment-api, i-escrow-api, i-kyc-api,
i-payment-api, i-wallet-api, i-distribution-api, i-esign-api, evm-api,
i-filer-api, i-notification-worker. (The last two override `run-dev`
locally — see [Per-repo overrides](#per-repo-overrides).)

## Commands

```
./make.sh build [NAME]              # compile every cmd/* (or just cmd/NAME)
./make.sh build-NAME                # alias form, used in some Dockerfiles
./make.sh run-dev [http|cloudrun]   # `go run ./cmd/<arg>/`, defaults to http
./make.sh test                      # TZ=UTC go test -count=1 ./...
./make.sh lint                      # golangci-lint -c .golangci.yml run --fix
./make.sh deploy dev|prod           # build + push image + restart Hetzner unit
./make.sh worker-deploy dev|prod    # update Cloud Run worker (paired-worker repos only)
./make.sh help                      # list the above
```

Go has **no `run` arm** — Go containers' `CMD` is the compiled binary
path (`/app/http`, `/app/worker`, etc.), not a shell. `run-dev` is for
local development only.

## Build artifacts

```
build                  → compiles every directory under cmd/
build NAME             → compiles only cmd/NAME
build-NAME             → same as `build NAME`; used by Dockerfiles that
                         build a single binary per stage
                         (e.g. i-filer-api/build/<name>/Dockerfile runs
                         `./make.sh build-<name>` to skip unrelated binaries)
```

Each `cmd/<bin>` directory becomes a root-level executable named `<bin>`,
compiled with the standard ldflags:

```
-s -w
-X main.repository=$COMPANY_NAME/$SERVICE_NAME
-X main.revisionID=$GIT_COMMIT
-X main.version=$BUILD_DATE:$GIT_COMMIT
-X main.service=$SERVICE_NAME
```

The Dockerfile then `COPY`s these binaries into the runtime image.

## Identity / env vars

| Var | Default | Notes |
|-----|---------|-------|
| `SERVICE_NAME` | `basename($PWD)` minus a leading `i-` | Set in CI when the repo dir name differs from the service unit name. |
| `COMPANY_NAME` | `torque-investments` | Image namespace under the registry. |
| `REGISTRY` | `cr.webdevelop.pro` | Private container registry. |
| `REPO_IMAGE` | `$REGISTRY/$COMPANY_NAME/$SERVICE_NAME` | Final image path. |
| `GIT_COMMIT` | `git rev-parse --short HEAD` | Image SHA tag + Go ldflags. |
| `BUILD_DATE` | `date +%Y%m%d` | ldflags `version`. |
| `DEPLOY_TAG_DEV` | `latest-dev` | Dev image tag pushed/pulled by `deploy dev`. |
| `DEPLOY_TAG_PROD` | `latest-prod` | Prod image tag pushed/pulled by `deploy prod`. |
| `SYSTEMD_UNIT_DEV` | `wd-$SERVICE_NAME.service` | Override when a repo's dev unit does not match the default. |
| `SYSTEMD_UNIT_PROD` | `prod-wd-$SERVICE_NAME.service` | Override when a repo's prod unit does not match the default. |
| `SYSTEMD_SERVICE_MODE` | `service` | Use `oneshot` for jobs that must finish before deploy is considered successful. |
| `SYSTEMD_ONESHOT_TIMEOUT` | `900` | Seconds to wait for a one-shot unit to finish successfully. |
| `SYSTEMD_VERIFY_DELAY` | `3` | Seconds to wait before checking a long-running service is still active. |

Cloud Run worker vars (only relevant for repos that ship a paired worker
— escrow, kyc, payment, wallet, filer-resize):

| Var | Default |
|-----|---------|
| `WORKER_NAME` | `${SERVICE_NAME%-api}-worker` (e.g. `kyc-api` → `kyc-worker`) |
| `GCP_PROJECT` | `webdevelop-live` |
| `GCP_REPO` | `torque-investments` |
| `GCP_REGION_PROD` | `europe-west6` |
| `GCP_REGION_DEV` | `europe-central2` |
| `WORKER_IMAGE_PROD` | `europe-west6-docker.pkg.dev/$GCP_PROJECT/$GCP_REPO/$WORKER_NAME` |
| `WORKER_IMAGE_DEV` | `cr.webdevelop.pro/$GCP_REPO/$WORKER_NAME` |

Prod workers keep the existing Artifact Registry path. Dev workers deploy
from `cr.webdevelop.pro/torque-investments/<worker>:latest-dev` so Cloud Run
uses the same private registry namespace as alpha.

## `run-dev`

Local-only foreground runner. Pick which binary under `cmd/` to run:

```
./make.sh run-dev            # → go run ./cmd/http/    (default)
./make.sh run-dev http       # same
./make.sh run-dev cloudrun   # → go run ./cmd/cloudrun/  (the Cloud Run worker)
```

The argument is validated against `http|cloudrun`, then `cmd/<arg>/`
must exist on disk. Repos with non-canonical binary names override
`run_dev()` locally (see [Per-repo overrides](#per-repo-overrides)).

## `deploy dev|prod`

```
./make.sh deploy dev   # pushes :latest-dev,  restarts wd-<svc>.service
./make.sh deploy prod  # pushes :latest-prod, restarts prod-wd-<svc>.service
```

1. `docker build --platform=linux/amd64` with `GIT_COMMIT`, `BUILD_DATE`,
   `SERVICE_NAME`, `REPOSITORY` build-args.
2. Tag twice: `:latest-<env>` + `:<short-sha>`.
3. `docker push` both tags.
4. Pull + restart the systemd unit locally when running on alpha, otherwise
   over SSH. The script resets failed state, restarts the unit, then verifies
   the result:
   - `SYSTEMD_SERVICE_MODE=service`: after `SYSTEMD_VERIFY_DELAY`, the unit
     must still be active.
   - `SYSTEMD_SERVICE_MODE=oneshot`: the unit must finish with
     `ActiveState=inactive` and `Result=success` before
     `SYSTEMD_ONESHOT_TIMEOUT`.

`DEPLOY_SSH` defaults to the team-standard `alpha` alias
(`root@78.46.85.62:822`); override via env or `.makerc`. If a repo cannot use
an SSH config alias, set `DEPLOY_SSH` to the host and `DEPLOY_SSH_OPTS` to
flags such as `-p 822`.

The systemd unit's quadlet has `Image=` pinned to `:latest-<env>` and is
rendered by ansible (`roles/<svc>/`), so the pull+restart picks up the
freshly pushed image (`Pull=IfNotPresent` → uses the just-pulled tag).

Systemd unit naming convention:

- dev → `wd-<svc>.service`
- prod → `prod-wd-<svc>.service`

`<svc>` here is `SERVICE_NAME`, the repo basename with the leading `i-`
stripped. When it diverges from the unit name, set it in `./.makerc`
(see [Per-repo overrides](#per-repo-overrides)) — not in CI.

## `worker-deploy dev|prod`

For the four repos that pair an API container with a Cloud Run worker:
escrow, kyc, payment, wallet (and i-filer-api's resize-worker, which
deviates from the canonical name pattern).

```
./make.sh worker-deploy dev   # → kyc-worker-dev    in europe-central2
./make.sh worker-deploy prod  # → kyc-worker        in europe-west6
```

1. `docker build` (same args as `deploy`).
2. Dev: tag + push to `$WORKER_IMAGE_DEV:$SHA` and `$WORKER_IMAGE_DEV:latest-dev`.
3. Prod: tag + push to `$WORKER_IMAGE_PROD:$SHA` and `$WORKER_IMAGE_PROD:latest-prod`.
4. Dev deploys `--image=$WORKER_IMAGE_DEV:latest-dev`; prod deploys `--image=$WORKER_IMAGE_PROD:$SHA`.

The `PING` env-var bump forces a new revision when nothing else
changed (e.g. CI re-run on the same SHA). Dev Cloud Run service names
get a `-dev` suffix to match `tf/gcp/locals.tf:env_default_services`.

To wire `worker-deploy` into a repo's CI, set `HAS_WORKER: "true"` and
`WIF_SERVICE_ACCOUNT` in `.github/workflows/deploy.yml` env block —
the canonical workflow chains `worker-deploy $ENV` after `deploy $ENV`
when the flag is set.

## Per-repo overrides

`make.sh` is **byte-identical across every repo** — re-syncing is a plain
`cp`. Repo-specifics live in two optional sibling files:

- **`./.makerc`** — sourced at the very top, *before* defaults are
  computed. Set vars only (`SERVICE_NAME`, `WORKER_NAME`, `DEPLOY_SSH`,
  `COMPANY_NAME`, …). Example — `i-email-worker`'s repo dir doesn't match
  its alpha unit (`wd-email-api`), so its `.makerc` is just:

  ```sh
  SERVICE_NAME=email-api
  ```

  Migration-style repos can also override deploy behavior:

  ```sh
  COMPANY_NAME=webdevelop-pro
  DEPLOY_TAG_PROD=latest-master
  SYSTEMD_UNIT_PROD=prod-wd-migration-job.service
  SYSTEMD_SERVICE_MODE_PROD=oneshot
  ```

- **`./.make.override.sh`** — sourced *after* the canonical functions are
  defined, *before* command dispatch. Use it to replace a whole function.
  Example — **i-filer-api** ships three binaries (`cmd/server`,
  `cmd/scanner`, `cmd/resize-worker`), so its `.make.override.sh`
  redefines `run_dev`:

  ```sh
  http     → cmd/server          (the API; default)
  cloudrun → cmd/resize-worker   (the Cloud Run worker)
  server | scanner | resize-worker  # also accepted as native names
  ```

Keep the arm names canonical — only override vars/bodies. That way
`./make.sh deploy prod` works the same everywhere even when the
implementation differs.

- **i-notification-worker** — single binary at `cmd/server`. Doesn't use
  the canonical `deploy dev|prod` arm: a hand-rolled workflow builds
  three tags from one image (`:latest-<env>`, the git-sha, and on master
  also `:latest-internal` for the auth-bypass container), and restarts
  both `wd-notification-api` *and* `wd-notification-internal` units.
  See [the repo's deploy.yml](https://github.com/webdevelop-pro/i-notification-worker/blob/master/.github/workflows/deploy.yml).

## Syncing a new repo

```sh
cp ansible-devops/templates/make.sh/go.sh /path/to/repo/make.sh
chmod +x /path/to/repo/make.sh
```

Then in the repo's `.github/workflows/deploy.yml` env block:

- Set `SERVICE_NAME` if the repo basename doesn't match its service
  unit name.
- Set `HAS_WORKER: "true"` and `WIF_SERVICE_ACCOUNT` if the repo has a
  paired Cloud Run worker.
- Set `WRITE_GCP_MEDIA_ADMIN: "true"` if the Dockerfile bakes
  `etc/gcp_sa_key.json` (currently only i-filer-api).

Push to `dev` first to verify CI; the systemd unit's quadlet must
already exist on alpha (render it via `ansible-playbook -i
inventories/<env>/host.ini playbooks/10-instances/<svc>.yml`).

