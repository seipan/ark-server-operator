/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ArkClusterSpec defines the desired state of an ARK game cluster.
//
// One ArkCluster represents a set of ArkServers (maps) that share an in-game
// cluster identity, common gameplay rules, shared cluster-travel storage,
// admin credentials, and a backup destination.
type ArkClusterSpec struct {
	// ClusterName is the in-game cluster identifier.
	// Reconciler maps this to arkmanager's arkopt_clusterid option.
	// ArkServers sharing the same ClusterName can transfer characters via cluster travel.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]*[a-z0-9]$`
	// +required
	ClusterName string `json:"clusterName"`

	// Image is the ARK server container image used by every ArkServer in this cluster.
	// +required
	Image ImageSpec `json:"image"`

	// SharedStorage holds the ReadWriteMany volume mounted at /ark-shared in every
	// ArkServer Pod. ARK uses it for cluster travel (character transfer between maps).
	// +required
	SharedStorage SharedStorageSpec `json:"sharedStorage"`

	// Game is the source for Game.ini contents shared across all ArkServers.
	// Contains gameplay multipliers, auto-unlocked engrams, etc.
	// +required
	Game IniSource `json:"game"`

	// GameUserSettings is the source for GameUserSettings.ini contents shared across
	// all ArkServers. Contains PvE/PvP rules, structure limits, server rules, etc.
	// +required
	GameUserSettings IniSource `json:"gameUserSettings"`

	// ArkManager configures the arkmanager CLI behavior and the ARK server launch
	// flags it passes through. The Reconciler renders this into arkmanager.cfg.
	// +required
	ArkManager ArkManagerSpec `json:"arkManager"`

	// PlayerLists defines Steam ID lists shared across all ArkServers.
	// +optional
	PlayerLists PlayerListsSpec `json:"playerLists,omitempty"`

	// Passwords references the Secret containing server and admin passwords.
	// The Secret is expected to be created out-of-band (e.g. via `kubectl apply`);
	// it is not managed in Git.
	// +required
	Passwords PasswordsSpec `json:"passwords"`

	// Backup defines the cluster-wide backup schedule and destination.
	// Every ArkServer in this cluster is backed up on this schedule.
	// Omit to disable scheduled backups.
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`
}

// ImageSpec pins the ARK server container image.
//
// Both Tag and Digest are required: Digest is authoritative for the actual pull
// (immutable, supply-chain safe), while Tag is the human-readable version label
// that Renovate updates. The Reconciler always pulls by digest; Tag is stored as
// metadata and surfaced in status/events for operator-friendly diagnostics.
type ImageSpec struct {
	// Repository is the image repository, e.g. "nightdragon1/ark-docker".
	// +kubebuilder:validation:MinLength=1
	// +required
	Repository string `json:"repository"`

	// Tag is the human-readable image tag (e.g. "v1.2.3").
	// Not used for the actual pull — Digest is. Keep Tag in sync with Digest
	// so Renovate can bump both together.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[\w][\w.-]{0,127}$`
	// +required
	Tag string `json:"tag"`

	// Digest pins a specific image digest. Authoritative for image pulls.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	// +required
	Digest string `json:"digest"`

	// PullPolicy controls when the image is pulled.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default=IfNotPresent
	// +optional
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// SharedStorageSpec configures the cluster-travel shared volume.
//
// Exactly one of ExistingClaimName or (StorageClassName, Size, AccessModes)
// should be supplied. The Reconciler creates a PVC only when ExistingClaimName
// is empty.
type SharedStorageSpec struct {
	// ExistingClaimName references a pre-existing PersistentVolumeClaim in the
	// same namespace. When set, the Reconciler does not create a PVC.
	// +optional
	ExistingClaimName string `json:"existingClaimName,omitempty"`

	// StorageClassName is used when the Reconciler creates a new PVC.
	// The class must support ReadWriteMany.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// Size is the requested storage size for a newly created PVC.
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`

	// AccessModes for a newly created PVC. Must include ReadWriteMany for cluster travel.
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// MountPath where the shared volume is exposed inside the ARK container.
	// arkmanager's arkopt_ClusterDirOverride is set to this path.
	// +kubebuilder:default=/ark-shared
	// +optional
	MountPath string `json:"mountPath,omitempty"`
}

// IniSource provides ini file contents either inline or by reference.
// Exactly one of Inline or ConfigMapRef must be set.
type IniSource struct {
	// Inline embeds the ini contents directly in the CR.
	// Convenient for small configs; prefer ConfigMapRef for large files.
	// +optional
	Inline string `json:"inline,omitempty"`

	// ConfigMapRef references a ConfigMap whose data key holds the ini contents.
	// +optional
	ConfigMapRef *IniConfigMapRef `json:"configMapRef,omitempty"`
}

// IniConfigMapRef references a ConfigMap key holding ini contents.
type IniConfigMapRef struct {
	// Name of the ConfigMap in the same namespace as the ArkCluster.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// Key inside the ConfigMap's data map.
	// +kubebuilder:validation:MinLength=1
	// +required
	Key string `json:"key"`
}

// ArkManagerSpec is the structured representation of arkmanager.cfg.
//
// The Reconciler renders this struct into a plain-text arkmanager.cfg and stores
// it in a ConfigMap mounted by every ArkServer Pod.
type ArkManagerSpec struct {
	// WarnMinutes is the lead time for in-game shutdown/update/restart warnings.
	// Maps to arkmanager's arkwarnminutes.
	// +kubebuilder:default=15
	// +kubebuilder:validation:Minimum=0
	// +optional
	WarnMinutes int32 `json:"warnMinutes,omitempty"`

	// BackupPreUpdate runs a save+backup before applying server updates.
	// Maps to arkmanager's arkBackupPreUpdate.
	// +kubebuilder:default=true
	// +optional
	BackupPreUpdate bool `json:"backupPreUpdate,omitempty"`

	// MaxBackupSizeGB caps the total local backup size before arkmanager rotates them.
	// Maps to arkmanager's arkMaxBackupSizeGB.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxBackupSizeGB int32 `json:"maxBackupSizeGB,omitempty"`

	// Messages are the in-game broadcast templates. Each must contain a `%d`
	// placeholder which arkmanager substitutes with the remaining minutes or seconds.
	// +optional
	Messages MessageTemplates `json:"messages,omitempty"`

	// Flags are passed to the ARK server as bare `-flag` arguments
	// (arkflag_* keys in arkmanager.cfg).
	// +optional
	Flags ArkFlags `json:"flags,omitempty"`

	// Options are passed to the ARK server as `-flag=value` arguments
	// (arkopt_* keys in arkmanager.cfg).
	// +optional
	Options ArkOptions `json:"options,omitempty"`

	// BanListURL is the upstream ban list pulled by ARK.
	// +optional
	BanListURL string `json:"banListURL,omitempty"`
}

// MessageTemplates carries the in-game broadcast message strings used by arkmanager.
// All fields contain a single `%d` placeholder.
type MessageTemplates struct {
	// +optional
	WarnUpdateMinutes string `json:"warnUpdateMinutes,omitempty"`
	// +optional
	WarnUpdateSeconds string `json:"warnUpdateSeconds,omitempty"`
	// +optional
	WarnRestartMinutes string `json:"warnRestartMinutes,omitempty"`
	// +optional
	WarnRestartSeconds string `json:"warnRestartSeconds,omitempty"`
	// +optional
	WarnShutdownMinutes string `json:"warnShutdownMinutes,omitempty"`
	// +optional
	WarnShutdownSeconds string `json:"warnShutdownSeconds,omitempty"`
}

// ArkFlags are bare `-flag` arguments passed to the ARK server binary.
// nil means "leave the flag unset" (arkmanager default applies).
type ArkFlags struct {
	// +optional
	Log *bool `json:"log,omitempty"`
	// +optional
	UseAllAvailableCores *bool `json:"useAllAvailableCores,omitempty"`
	// +optional
	Crossplay *bool `json:"crossplay,omitempty"`
	// +optional
	NoTransferFromFiltering *bool `json:"noTransferFromFiltering,omitempty"`
	// +optional
	NoBattlEye *bool `json:"noBattlEye,omitempty"`
}

// ArkOptions are `-flag=value` arguments passed to the ARK server binary.
type ArkOptions struct {
	// ActiveEvent triggers an ARK seasonal in-game event.
	// +kubebuilder:validation:Enum=Easter;Arkaeology;ExtinctionChronicles;WinterWonderland;vday;Summer;FearEvolved;TurkeyTrial;birthday;None
	// +optional
	ActiveEvent ArkEvent `json:"activeEvent,omitempty"`
}

// ArkEvent enumerates ARK's seasonal events.
type ArkEvent string

const (
	ArkEventEaster               ArkEvent = "Easter"
	ArkEventArkaeology           ArkEvent = "Arkaeology"
	ArkEventExtinctionChronicles ArkEvent = "ExtinctionChronicles"
	ArkEventWinterWonderland     ArkEvent = "WinterWonderland"
	ArkEventValentine            ArkEvent = "vday"
	ArkEventSummer               ArkEvent = "Summer"
	ArkEventFearEvolved          ArkEvent = "FearEvolved"
	ArkEventTurkeyTrial          ArkEvent = "TurkeyTrial"
	ArkEventBirthday             ArkEvent = "birthday"
	ArkEventNone                 ArkEvent = "None"
)

// PlayerListsSpec carries the Steam ID lists used by every ArkServer in the cluster.
// Each list is rendered into a separate text file mounted into the Pod.
type PlayerListsSpec struct {
	// AllowedCheaters are Steam IDs allowed to use the in-game console cheats.
	// Rendered as AllowedCheaterSteamIDs.txt.
	// +optional
	AllowedCheaters []string `json:"allowedCheaters,omitempty"`

	// JoinNoCheck are Steam IDs that bypass the join checks (password, etc.).
	// Rendered as PlayersJoinNoCheck.txt.
	// +optional
	JoinNoCheck []string `json:"joinNoCheck,omitempty"`

	// ExclusiveJoinList, when non-empty, restricts joining to listed Steam IDs.
	// Rendered as PlayersExclusiveJoinList.txt.
	// +optional
	ExclusiveJoinList []string `json:"exclusiveJoinList,omitempty"`
}

// PasswordsSpec references the Secret holding the server and admin passwords.
type PasswordsSpec struct {
	// SecretRef points at the Secret in the same namespace. The Secret must
	// contain the keys named by Keys below.
	// +required
	SecretRef PasswordSecretRef `json:"secretRef"`
}

// PasswordSecretRef binds a Secret name to the data keys it exposes.
type PasswordSecretRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// Keys maps Secret data keys to ARK roles.
	// +required
	Keys PasswordKeys `json:"keys"`
}

// PasswordKeys names the Secret data keys for server and admin passwords.
type PasswordKeys struct {
	// ServerPassword is the Secret data key holding the in-game server password.
	// +kubebuilder:default=serverPass
	// +optional
	ServerPassword string `json:"serverPassword,omitempty"`

	// AdminPassword is the Secret data key holding the admin (RCON) password.
	// +kubebuilder:default=adminPass
	// +optional
	AdminPassword string `json:"adminPassword,omitempty"`
}

// BackupSpec configures scheduled backups for every ArkServer in the cluster.
type BackupSpec struct {
	// Schedule is a cron expression in standard 5-field form.
	// Every ArkServer in this cluster is backed up on this schedule.
	// +kubebuilder:validation:MinLength=9
	// +required
	Schedule string `json:"schedule"`

	// Method selects how the backup is taken inside the Pod.
	//   arkmanager: invoke `arkmanager backup` (handles SaveWorld + .ark formatting).
	//   tar:        invoke a raw `tar` of the SavedArks directory (legacy behavior).
	// +kubebuilder:validation:Enum=arkmanager;tar
	// +kubebuilder:default=arkmanager
	// +optional
	Method BackupMethod `json:"method,omitempty"`

	// PreBackup actions executed before the backup is taken.
	// +optional
	PreBackup *PreBackupSpec `json:"preBackup,omitempty"`

	// Retention controls how long uploaded backups are kept.
	// +optional
	Retention *BackupRetention `json:"retention,omitempty"`

	// Destination is the remote object store to upload artifacts to.
	// +required
	Destination BackupDestination `json:"destination"`
}

// BackupMethod selects the backup mechanism executed inside the ARK Pod.
type BackupMethod string

const (
	BackupMethodArkManager BackupMethod = "arkmanager"
	BackupMethodTar        BackupMethod = "tar"
)

// PreBackupSpec describes optional actions performed before the backup runs.
type PreBackupSpec struct {
	// SaveWorld issues an in-game SaveWorld over RCON before the backup begins.
	// Strongly recommended to avoid capturing an in-flight save.
	// +kubebuilder:default=true
	// +optional
	SaveWorld bool `json:"saveWorld,omitempty"`

	// AnnounceMessage is broadcast in-game before backup starts.
	// +optional
	AnnounceMessage string `json:"announceMessage,omitempty"`

	// AnnounceLeadSeconds is the wait between announcement and backup start.
	// +kubebuilder:validation:Minimum=0
	// +optional
	AnnounceLeadSeconds int32 `json:"announceLeadSeconds,omitempty"`
}

// BackupRetention controls retention policy for uploaded backups.
type BackupRetention struct {
	// MaxBackupSizeGB caps the total uploaded backup size before older artifacts
	// are pruned. 0 disables size-based pruning.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxBackupSizeGB int32 `json:"maxBackupSizeGB,omitempty"`

	// KeepLastN keeps the N most recent backups per ArkServer.
	// 0 disables count-based pruning.
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepLastN int32 `json:"keepLastN,omitempty"`
}

// BackupDestination describes where backup artifacts are uploaded.
//
// Only S3 is supported. S3-compatible providers (MinIO, Cloudflare R2, Wasabi,
// Backblaze B2, GCS interop mode, etc.) are reachable via EndpointURL.
type BackupDestination struct {
	// S3 holds the Amazon S3 (or S3-compatible) configuration.
	// +required
	S3 S3Destination `json:"s3"`
}

// S3Destination describes an Amazon S3 (or S3-compatible) bucket.
type S3Destination struct {
	// Bucket name.
	// +kubebuilder:validation:MinLength=1
	// +required
	Bucket string `json:"bucket"`

	// Region for the bucket.
	// +kubebuilder:validation:MinLength=1
	// +required
	Region string `json:"region"`

	// PathPrefix is prepended to every uploaded object key.
	// Final layout: s3://<bucket>/<pathPrefix>/<arkServer>/<timestamp>/...
	// +optional
	PathPrefix string `json:"pathPrefix,omitempty"`

	// EndpointURL overrides the AWS endpoint for S3-compatible providers
	// (MinIO, R2, Wasabi, B2, GCS interop, etc.). Leave empty for AWS S3.
	// +optional
	EndpointURL string `json:"endpointURL,omitempty"`

	// CredentialsSecretRef points at a Secret holding S3 credentials.
	// Expected data keys: "accessKeyID" and "secretAccessKey".
	// +required
	CredentialsSecretRef corev1.LocalObjectReference `json:"credentialsSecretRef"`
}

// ArkClusterStatus is the observed state of an ArkCluster.
type ArkClusterStatus struct {
	// ManagedServers is the number of ArkServers referencing this cluster.
	// +optional
	ManagedServers int32 `json:"managedServers,omitempty"`

	// SharedStorageReady indicates the cluster-travel PVC is bound.
	// +optional
	SharedStorageReady bool `json:"sharedStorageReady,omitempty"`

	// GlobalConfigMapsReady indicates the operator-managed ConfigMaps
	// (Game.ini, GameUserSettings.ini, arkmanager.cfg, player lists) are applied.
	// +optional
	GlobalConfigMapsReady bool `json:"globalConfigMapsReady,omitempty"`

	// ImageDigest is the digest actually pulled by managed Pods.
	// May lag behind spec.image.digest while a rollout is in progress.
	// +optional
	ImageDigest string `json:"imageDigest,omitempty"`

	// LastReconcileTime is when the controller last reconciled this resource.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// Conditions follow standard Kubernetes condition semantics.
	// Known types:
	//   Ready              — overall readiness
	//   ConfigsApplied     — global ConfigMaps reconciled to desired state
	//   SharedStorageBound — cluster-travel PVC is Bound
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=arkc
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterName"
// +kubebuilder:printcolumn:name="Servers",type=integer,JSONPath=".status.managedServers"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ArkCluster is the Schema for the arkclusters API.
type ArkCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ArkCluster
	// +required
	Spec ArkClusterSpec `json:"spec"`

	// status defines the observed state of ArkCluster
	// +optional
	Status ArkClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ArkClusterList contains a list of ArkCluster
type ArkClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ArkCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ArkCluster{}, &ArkClusterList{})
}
