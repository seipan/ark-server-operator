# ark-server-operator Design Notes

Decisions made while designing the operator that replaces the
[KubicArk](https://github.com/Cervator/KubicArk) repository. Captures the *why*
behind the current `api/v1/arkcluster_types.go` and the planned
`ArkServer` / `ArkBackup` CRDs.

## 1. Why an Operator (not just Helm)

KubicArk's current pain points fall into two categories:

| Category | Examples |
|---|---|
| **Static duplication** | 12 maps × 5 YAMLs ≈ 60 near-identical files; `storageClassName: "nfs"` hardcoded; `sed` substitution in `start.Jenkinsfile`; image tag unspecified |
| **Runtime intelligence** (the README's "An eventual goal") | Hibernation when no players online; ChatOps from Discord; graceful restart with in-game warning; smart backup with `SaveWorld` first; spot-node draining |

**Helm solves only the first category.** The second category requires reading
runtime state (player count via RCON, save flush completion, in-game events)
and reacting to it — which is exactly what a Reconciler does and a templating
engine cannot.

The deciding rule:

> If the operator's work ends when the Pod becomes `Running`, use Helm.
> If meaningful work remains (RCON probe, hibernation, graceful save), use an Operator.

ARK is firmly in the Operator camp because of the 20–30 minute startup cost
(idle maps must be hibernated to be affordable) and the SaveWorld dependency
of every restart/backup operation.

### Phased rollout

```
Phase 1 — Helm-equivalent capability        ← v0.1: ArkCluster + ArkServer + Reconciler
                                              produce the same K8s manifests
                                              that the existing repo applies.

Phase 2 — Observability via RCON            ← v0.2: status.playersOnline,
                                              status.rconReachable, metrics.

Phase 3 — Autonomous behavior               ← v0.3: hibernation, graceful restart,
                                              backup orchestration, ChatOps surface.
```

Phase 1 is the floor: even if Phases 2–3 never ship, the operator must at
least *match* the current KubicArk capability.

## 2. CRD layout

Three CRDs, all in API group `view.yadon3141.com/v1` (Kubebuilder default;
renaming to `ark.…` is out-of-scope for the initial design).

| CRD | Cardinality | Owns | Has state machine? | Talks RCON? |
|---|---|---|---|---|
| `ArkCluster` | 1 per game cluster | Global CMs, shared PVC, password resolution, backup CronJob | No (config holder) | No |
| `ArkServer` | 1 per map | StatefulSet, Service, map PVC, Override CMs | **Yes** (`desiredState`) | **Yes** |
| `ArkBackup` | 1 per backup run | Job | Yes (`phase`) | Only via the Job (SaveWorld) |

**`ArkCluster` = config aggregator.** Holds anything that two or more maps
would otherwise duplicate. Does not own a Pod.

**`ArkServer` = a running map.** Owns the StatefulSet, runs the state
machine, and is the only CRD that opens an RCON connection.

**`ArkBackup` = one backup execution.** A `CronJob` (owned by `ArkCluster`)
creates an `ArkBackup` per server per tick.

Boundary rule: anything you would set the same on every map → `ArkCluster`;
anything you would tune per-map → `ArkServer`.

## 3. `ArkCluster` field-by-field decisions

Implemented in `api/v1/arkcluster_types.go`.

### `clusterName` (not `clusterId`)

K8s-CRD-layer naming: `clusterName` reads naturally. The Reconciler maps it
to arkmanager's `arkopt_clusterid` when rendering `arkmanager.cfg`. The
internal arkmanager term is intentionally not surfaced to the spec.

### `image` — both `tag` and `digest` required

```yaml
image:
  repository: nightdragon1/ark-docker
  tag: v1.2.3                  # human / Renovate
  digest: sha256:abc...         # authoritative pull
```

- **Digest is what gets pulled** (immutable; supply-chain safe; satisfies the
  organization-wide CLAUDE.md policy that "依存パッケージのバージョンまたは
  ハッシュ値は必ず固定すること").
- **Tag is metadata** so Renovate can bump both together and humans can read
  the version from `kubectl describe`. Mirrors the GitHub Actions
  `actions/checkout@SHA # vX.Y.Z` pattern.

### No `initImage` field (busybox is internal)

Rejected. Reasons:
1. End-users have no reason to swap busybox.
2. The init script's lifetime is coupled to the operator's, so it should be
   pinned by the operator's release, not the CR.
3. **The initContainer itself may be removed** — the Reconciler can pre-merge
   the global+override ini contents and ship one ConfigMap, eliminating the
   need for an in-Pod merge step. Decision: leave room for this in v0.2.

### `sharedStorage` — `existingClaimName` OR (`storageClassName`, `size`, `accessModes`)

Two modes:
- **Bring-your-own-PVC**: operators that already run an NFS provisioner or
  CSI (Filestore, EFS, Longhorn, Rook-CephFS) reference the existing PVC.
- **Operator-managed**: the Reconciler creates a PVC from the supplied class.

This breaks KubicArk's hard dependency on `KubicGameHosting`. Any RWM-capable
StorageClass works. Future webhook: enforce mutual exclusion between the two
modes.

### `game` / `gameUserSettings` (not `gameSettings`)

Field names mirror ARK's actual file names (`Game.ini`,
`GameUserSettings.ini`). Considered and rejected:
- `gameSettings` — collides with `gameUserSettings` (differ by only "User")
  and is not an ARK term.
- Grouping under `iniFiles.*` — defers a useful symmetry but adds nesting
  for no current benefit.

Each field accepts either `inline` (small files) or `configMapRef` (large
files such as the 248-line `GlobalGameUserSettings.ini`).

### `arkManager` — structured `arkmanager.cfg`

Renders into the plain-text `arkmanager.cfg` the existing ConfigMap holds
verbatim. Three subgroups reflect arkmanager's three key-naming conventions:

| Subgroup | arkmanager.cfg key prefix | Meaning |
|---|---|---|
| `flags` | `arkflag_*` | Bare `-flag` (no value) |
| `options` | `arkopt_*` | `-flag=value` |
| (top-level) | `ark_*`, plain keys | URL `?Key=Value` and arkmanager-own settings |

`messages` carries the six in-game broadcast templates with `%d` placeholders.

`options.activeEvent` is an enum of ARK's seasonal events. Invalid values are
blocked at admission, eliminating a class of typo-induced surprise.

### `playerLists`

Three flat `[]string` lists with semantic names (`allowedCheaters`,
`joinNoCheck`, `exclusiveJoinList`) rather than the verbose ARK file names.
The Reconciler writes them into `AllowedCheaterSteamIDs.txt`,
`PlayersJoinNoCheck.txt`, `PlayersExclusiveJoinList.txt`.

### `passwords` — single Secret reference

The Jenkins-era two-tier mechanism (`ark-server-secrets.yaml` placeholder +
`unattended-password.yaml` overwrite via `sed`) is removed. With Jenkins out
of scope, operators apply a Secret directly with `kubectl apply` and never
commit passwords to Git. One field, one Secret.

### `backup` — S3 only

Considered destinations: `gcs`, `s3`, `azureBlob`, `pvc`. Kept only `s3`.

Reasoning:
- `pvc` violates backup semantics (same-cluster destination dies with the
  source); rejected.
- `s3` with `endpointURL` reaches everything else (MinIO, Cloudflare R2,
  Wasabi, Backblaze B2, GCS interoperability mode, AWS S3).
- Removing the dispatch struct collapses `BackupDestination` to a single
  required field; no `type` discriminator needed.

`method: arkmanager` is the default (it invokes `arkmanager backup` which
runs SaveWorld and produces a well-formed `.tar.gz`). `method: tar` is
preserved for compatibility with the legacy `backup-server.sh` flow.

`preBackup.saveWorld: true` ensures the Reconciler issues RCON `SaveWorld`
and waits for completion before the backup Job starts — closing a hole in
the current `backup-server.sh` which captures an in-flight save.

### No `defaults` block

Earlier draft had `ArkCluster.spec.defaults.{resources,pvc,replicas,…}`.
Removed. Reasons:
1. Implementing defaults-merge logic in the Reconciler adds complexity for
   little gain (11 of 12 maps already share values).
2. **Defaulting webhook** on `ArkServer` is the K8s-native equivalent and
   lives closer to where the value is consumed.
3. Cross-CR defaulting is harder to reason about than per-resource
   defaulting and produces less predictable `kubectl explain` output.

The operator's release will pin the reasonable defaults (350m/2000m,
7.25Gi/10Gi, 30Gi PVC) in Go constants and apply them via the webhook.

### `status` shape

Standard K8s conditions (`metav1.Condition`) with three known types:
`Ready`, `ConfigsApplied`, `SharedStorageBound`. Supplemented by scalar
fields: `managedServers`, `sharedStorageReady`, `globalConfigMapsReady`,
`imageDigest`, `lastReconcileTime`.

`imageDigest` in status (versus only in spec) lets `kubectl get arkcluster`
show the digest actually in use during a rollout, which can lag `spec`.

## 4. `ArkServer` design intent (not yet scaffolded)

To be created with:

```bash
kubebuilder create api --group view --version v1 --kind ArkServer
```

Anticipated fields:

```yaml
spec:
  clusterRef: { name: kubicarkcluster }
  map: Genesis                            # SERVERMAP env
  sessionName: "KubicArk - Genesis"
  desiredState: Running                   # Running | Stopped | Hibernated | Wiped
  ports:
    client: 31011
    raw:    31012
    query:  31013
    rcon:   31014
  resources: ...                          # overrides operator defaults
  storage:
    size: 30Gi                            # overrides operator default
  overrideGame:           |               # inline; small per-map deltas
    EngramEntryAutoUnlocks=...
  overrideGameUserSettings: |
    [MessageOfTheDay]
    Message=...
status:
  phase: Running
  podName: arkgenesis-0
  rconReachable: true
  playersOnline: 3
  idleSince: null
  lastBackupTime: 2026-05-31T04:00:00Z
  conditions: [Ready, GameReady, BackupHealthy]
```

Design points already decided:

- **`desiredState` is the state machine input.** Reconciler creates/deletes
  the StatefulSet/Service/PVC/CMs accordingly.
- **`Wiped` deletes the PVC** → must be guarded by a finalizer that takes a
  final backup before allowing removal.
- **`Hibernated` deletes the StatefulSet but keeps the PVC** (= the current
  `stop-server.sh` behavior) and is auto-set by the Reconciler when
  `playersOnline == 0` for `idleThreshold` minutes (v0.3).
- **Port auto-allocation** is a defaulting-webhook job: if `ports` is empty,
  assign `base + index*10` based on the order ArkServers are created.
- **Cross-validation** at admission: reject duplicate `ports`, reject
  `clusterRef` to a nonexistent ArkCluster, enforce the `map` enum
  (`TheIsland`, `Genesis`, `Genesis2`, etc.).

## 5. `ArkBackup` design intent

A short-lived CR representing one backup execution:

```yaml
spec:
  serverRef: { name: gen1 }
  destination: { ... }   # usually inherited from ArkCluster.spec.backup
status:
  phase: Succeeded       # Pending | Running | Succeeded | Failed
  startedAt: ...
  completedAt: ...
  objectURL: s3://my-backups/ark/gen1/2026-05-31T040000Z/gen1.tar.gz
  bytes: 12345678
```

Created by:
1. The cluster-wide CronJob generated from `ArkCluster.spec.backup.schedule`
   (one CR per server per tick).
2. The `Wiped` finalizer on `ArkServer` (one-shot "last backup before wipe").
3. Future ChatOps trigger.

The Reconciler runs `arkmanager backup` (or `tar`, per the cluster's
`backup.method`) inside the target Pod, ships the artifact to S3, and stamps
`status.objectURL`.

## 6. RCON

The single most important runtime channel between the operator and ARK.

- **Protocol**: Source RCON over TCP. Simple framed binary; client lib
  candidates: `github.com/gorcon/rcon` (preferred; pure Go; no postinstall).
- **Authentication**: admin password from the `passwords` Secret.
- **Address**: ArkServer Service's RCON port, **ClusterIP** only — never
  expose RCON via NodePort. ChatOps surface goes through the operator, not
  RCON itself.

Operator usage:

| Use | RCON command |
|---|---|
| Player count probe → `status.playersOnline` | `ListPlayers` |
| Graceful restart warning | `Broadcast "..."` (three times: T-15m, T-5m, T-1m) |
| Save flush before backup/wipe | `SaveWorld` (then wait for log confirmation) |
| Optional admin actions surfaced via ChatOps (Phase 3) | `Kick`, `Ban`, `DestroyWildDinos`, … |

## 7. Migration story from KubicArk

| KubicArk artifact | Operator equivalent | Notes |
|---|---|---|
| `gen1/ark-statefulset.yaml` × 12 | One `ArkServer` per map | StatefulSet generated by Reconciler |
| `gen1/ark-service.yaml` × 12 | Generated from `ArkServer.spec.ports` | |
| `gen1/ark-pvc.yaml` × 12 | Generated from `ArkServer.spec.storage` | |
| `gen1/OverrideGameIniCM.yaml` × 12 | `ArkServer.spec.overrideGame` (inline) | |
| `gen1/OverrideGameUserSettingsCM.yaml` × 12 | `ArkServer.spec.overrideGameUserSettings` (inline) | |
| `GlobalGameIniCM.yaml` | `ArkCluster.spec.game.configMapRef` | Existing CM re-used; or inline after migration |
| `GlobalGameUserSettingsCM.yaml` | `ArkCluster.spec.gameUserSettings.configMapRef` | Same |
| `ArkManagerCfgCM.yaml` | `ArkCluster.spec.arkManager` (structured) | No longer a hand-written CM |
| `ArkPlayerListsCM.yaml` | `ArkCluster.spec.playerLists` | |
| `ark-server-secrets.yaml` | `ArkCluster.spec.passwords.secretRef` | Secret applied out-of-band, never in Git |
| `unattended-password.yaml` | **Removed** | Two-tier mechanism unneeded without Jenkins |
| `ark-pvc-shared.yaml` | `ArkCluster.spec.sharedStorage` | Can keep existing PVC via `existingClaimName` |
| `start-server.sh` | `kubectl apply ArkServer` + Reconciler | |
| `stop-server.sh` | Set `desiredState: Stopped` | |
| `wipe-server.sh` | Set `desiredState: Wiped` (with finalizer) | |
| `backup-server.sh` + `backup.Jenkinsfile` | `ArkBackup` CRD + Reconciler-generated CronJob | |
| `start.Jenkinsfile` (sed substitution) | **Removed** | No two-tier Secret = no sed |
| `jobs.dsl` / Jenkinsfiles | **Removed** | No Jenkins; ChatOps + kubectl replaces |
| `validate-jenkinsfile.*` / `validate-jobdsl.*` | **Removed** | |

## 8. Out of scope (for now)

Tracked here so they aren't silently dropped:

- **API-group rename** (`view` → `ark`). Requires `kubebuilder edit
  --domain=…` and a re-scaffold. Postponed to a v0.x bump.
- **Multi-cluster mode** (one ArkCluster managing ArkServers in *another*
  K8s cluster). Not required; would complicate RBAC significantly.
- **Mod management** (`arkmanager installmod`). Worth a dedicated CRD
  (`ArkMod`) if it materializes, but no current demand.
- **Per-map `image` override.** Easy to add to `ArkServer.spec.image` later;
  not needed in v0.1 since all 12 maps share the same image.
- **Conversion webhook.** v1 is single-version. Add hub/spoke (`--conversion
  --spoke v2`) when a breaking schema change actually arrives.
- **Helm chart / OLM bundle distribution.** Generate via Kubebuilder plugins
  (`helm/v2-alpha`) at packaging time, not now.

## 9. Open questions

- **`map` enum granularity.** Should ArkServer's `map` field be a free string
  (any DLC) or an enum of currently-supported maps? Enum gives admission-time
  validation; string lets users add maps without recompiling the operator.
  Leaning enum + escape hatch (`mapOverride: <raw string>` for
  cutting-edge DLCs).
- **`Hibernated` semantics.** Is it identical to `Stopped` but auto-recovered
  on player demand (requires an external trigger), or also identical to
  `Stopped` but the operator periodically polls for "wake-up" via some
  external signal (Discord → Pod re-creation)? Decision deferred to Phase 3.
- **`initContainer` removal.** Worth doing in v0.2 once the merge logic is
  unit-tested in Go? Trades a busybox dependency for more Reconciler code.
- **Status subresource for `ArkBackup`.** Should backups be cleaned up on
  TTL like Jobs (`spec.ttlSecondsAfterFinished`)? Likely yes.

---

Last revised after the `BackupDestination` → S3-only simplification.
