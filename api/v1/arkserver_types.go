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

// ArkServerSpec defines the desired state of one ARK map server.
//
// One ArkServer maps 1:1 to an ARK game map running as a StatefulSet
// (replicas=1). It references a parent ArkCluster (same namespace) that
// supplies the shared configuration: cluster ID, image, shared storage,
// ini base, player lists, passwords, and backup destination.
type ArkServerSpec struct {
	// ClusterRef points at the ArkCluster (in the same namespace) that owns
	// the shared configuration used by this server.
	// +required
	ClusterRef ClusterReference `json:"clusterRef"`

	// Map is the ARK map this server runs. One of the operator's known map
	// enum values. For maps that the operator does not enumerate (newer
	// DLCs), leave this empty and set MapOverride instead.
	// Exactly one of Map or MapOverride must be set; webhook enforcement is
	// planned. The renderer maps the chosen value to the SERVERMAP env var.
	// +optional
	Map ArkMap `json:"map,omitempty"`

	// MapOverride accepts an arbitrary SERVERMAP string for ARK maps that
	// are not yet in the Map enum. Mutually exclusive with Map.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	MapOverride string `json:"mapOverride,omitempty"`

	// SessionName is the in-game server name visible to players.
	// Special characters should go in OverrideGameUserSettings instead, per
	// the arkmanager.cfg comment in the original KubicArk config.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +required
	SessionName string `json:"sessionName"`

	// DesiredState drives the per-server state machine.
	//   Running     — StatefulSet has 1 replica, PVC retained.
	//   Stopped     — StatefulSet scaled to 0, PVC retained (manual stop).
	//   Hibernated  — Same effect as Stopped; reserved for future
	//                 auto-hibernation (Phase 3) when no players are online.
	//   Wiped       — StatefulSet deleted and PVC removed via a finalizer
	//                 that takes a final backup before allowing removal.
	// +kubebuilder:validation:Enum=Running;Stopped;Hibernated;Wiped
	// +kubebuilder:default=Running
	// +optional
	DesiredState ArkServerDesiredState `json:"desiredState,omitempty"`

	// Ports defines the four NodePort numbers exposed by the server Service.
	// Phase 1 requires the operator to specify all four; a Phase 2 defaulting
	// webhook will allow omitting Ports and assigning a contiguous block
	// automatically.
	// +required
	Ports PortsSpec `json:"ports"`

	// Resources overrides the operator-default container resource requirements.
	// Operator defaults are pinned in Go constants and applied when this field
	// is omitted (requests cpu=350m, memory=7.25Gi; limits cpu=2000m, memory=10Gi
	// matching the legacy KubicArk numbers).
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Storage configures the per-map PVC that holds the ARK server install
	// and the SavedArks/* save files. Omit to use operator defaults.
	// +optional
	Storage *ArkServerStorageSpec `json:"storage,omitempty"`

	// OverrideGame is the per-map delta appended to Game.ini at Pod startup.
	// Concatenated with the cluster's Game.ini source by the init step.
	// +optional
	OverrideGame string `json:"overrideGame,omitempty"`

	// OverrideGameUserSettings is the per-map delta appended to
	// GameUserSettings.ini at Pod startup. Typical content is the
	// [MessageOfTheDay] block.
	// +optional
	OverrideGameUserSettings string `json:"overrideGameUserSettings,omitempty"`
}

// ClusterReference points at an ArkCluster in the same namespace.
type ClusterReference struct {
	// Name of the parent ArkCluster.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`
}

// ArkMap enumerates the ARK maps the operator knows about. Use MapOverride
// for any map outside this list (e.g. newly released DLC).
// +kubebuilder:validation:Enum=TheIsland;TheCenter;ScorchedEarth_P;Ragnarok;Aberration_P;Extinction;Valguero_P;Genesis;Gen2;CrystalIsles;LostIsland;Fjordur
type ArkMap string

const (
	MapTheIsland     ArkMap = "TheIsland"
	MapTheCenter     ArkMap = "TheCenter"
	MapScorchedEarth ArkMap = "ScorchedEarth_P"
	MapRagnarok      ArkMap = "Ragnarok"
	MapAberration    ArkMap = "Aberration_P"
	MapExtinction    ArkMap = "Extinction"
	MapValguero      ArkMap = "Valguero_P"
	MapGenesis       ArkMap = "Genesis"
	MapGenesis2      ArkMap = "Gen2"
	MapCrystalIsles  ArkMap = "CrystalIsles"
	MapLostIsland    ArkMap = "LostIsland"
	MapFjordur       ArkMap = "Fjordur"
)

// ArkServerDesiredState is the input to the per-server state machine.
type ArkServerDesiredState string

const (
	StateRunning    ArkServerDesiredState = "Running"
	StateStopped    ArkServerDesiredState = "Stopped"
	StateHibernated ArkServerDesiredState = "Hibernated"
	StateWiped      ArkServerDesiredState = "Wiped"
)

// PortsSpec carries the four NodePort numbers the ARK Service exposes.
//
// All four ports must fall within the standard NodePort range (30000-32767)
// because the Service is type=NodePort — LoadBalancers are intentionally
// avoided per the project README (UDP support is patchy).
//
// Renamed from the original KubicArk env names for clarity:
//
//	KubicArk env    ArkServer field   arkmanager.cfg key
//	-----------     ---------------   ---------------------
//	(client)        Client            (UDP client connection)
//	STEAMPORT       Game              ark_Port
//	SERVERPORT      Query             ark_QueryPort
//	RCONPORT        RCON              ark_RCONPort
type PortsSpec struct {
	// Client is the UDP port for game client connections.
	// +kubebuilder:validation:Minimum=30000
	// +kubebuilder:validation:Maximum=32767
	// +required
	Client int32 `json:"client"`

	// Game is the UDP port for raw ARK server traffic.
	// Renders as ark_Port in arkmanager.cfg.
	// +kubebuilder:validation:Minimum=30000
	// +kubebuilder:validation:Maximum=32767
	// +required
	Game int32 `json:"game"`

	// Query is the UDP port for Steam server-browser queries.
	// Renders as ark_QueryPort in arkmanager.cfg.
	// +kubebuilder:validation:Minimum=30000
	// +kubebuilder:validation:Maximum=32767
	// +required
	Query int32 `json:"query"`

	// RCON is the TCP port for remote admin console.
	// Renders as ark_RCONPort in arkmanager.cfg. Service must NOT expose
	// this externally beyond the cluster network.
	// +kubebuilder:validation:Minimum=30000
	// +kubebuilder:validation:Maximum=32767
	// +required
	RCON int32 `json:"rcon"`
}

// ArkServerStorageSpec configures the per-map PVC mounted at /ark in the Pod.
//
// The PVC holds:
//   - the ARK server installation (~15 GiB)
//   - save files in SavedArks/* (a few hundred MB to several GiB on
//     long-running servers)
//   - the SteamCMD update working area
//
// Operator default Size is 30Gi, which fits a normal-play scenario. Heavily
// modded or multi-year servers should bump this (KubicArk's original choice
// was 100Gi).
type ArkServerStorageSpec struct {
	// Size of the per-map PVC.
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`

	// StorageClassName for the per-map PVC. If empty, the cluster's default
	// StorageClass is used.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// AccessModes for the per-map PVC. Defaults to [ReadWriteOnce].
	// Multi-node access is not required; a single Pod (the StatefulSet
	// replica) mounts this volume.
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
}

// ArkServerPhase summarizes the lifecycle stage as a single human-readable token.
//
// It complements (but does not replace) Conditions: Phase is meant for quick
// kubectl-get-style visibility ("what is this server doing right now?"), while
// Conditions carry the precise per-axis state for tooling and alerts.
// +kubebuilder:validation:Enum=Pending;Provisioning;Running;Stopped;Wiping;Failed
type ArkServerPhase string

const (
	// PhasePending is the immediately-after-creation state where no
	// subresource has been observed yet.
	PhasePending ArkServerPhase = "Pending"
	// PhaseProvisioning means the StatefulSet/PVC/Service exist but the Pod
	// is not yet Running.
	PhaseProvisioning ArkServerPhase = "Provisioning"
	// PhaseRunning means the Pod is Running and the PVC is Bound.
	PhaseRunning ArkServerPhase = "Running"
	// PhaseStopped means the StatefulSet is intentionally scaled to 0 because
	// DesiredState is Stopped or Hibernated.
	PhaseStopped ArkServerPhase = "Stopped"
	// PhaseWiping means DesiredState=Wiped is being processed: the finalizer
	// is taking a last backup before the PVC is allowed to be deleted.
	PhaseWiping ArkServerPhase = "Wiping"
	// PhaseFailed means the reconciler has been unable to converge on the
	// desired state and the operator should look at the conditions / events.
	PhaseFailed ArkServerPhase = "Failed"
)

// ArkServerStatus is the observed state of an ArkServer.
type ArkServerStatus struct {
	// Phase is the high-level lifecycle stage.
	// +optional
	Phase ArkServerPhase `json:"phase,omitempty"`

	// ClusterName is the resolved name of the parent ArkCluster (cached from
	// spec.clusterRef.name for easy printcolumn display).
	// +optional
	ClusterName string `json:"clusterName,omitempty"`

	// PodName is the name of the (single) StatefulSet pod, conventionally
	// "<arkserver.name>-0".
	// +optional
	PodName string `json:"podName,omitempty"`

	// StatefulSetReady is true when the StatefulSet's single replica is in
	// Running state with all containers ready.
	// +optional
	StatefulSetReady bool `json:"statefulSetReady,omitempty"`

	// PVCReady is true when the per-map PVC is in Bound phase.
	// +optional
	PVCReady bool `json:"pvcReady,omitempty"`

	// LastReconcileTime is the timestamp of the most recent reconcile pass.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// Conditions follow standard Kubernetes condition semantics.
	// Known types (Phase 1):
	//   Ready            — overall readiness (Pod up, PVC bound, cluster ref valid)
	//   ClusterRefValid  — spec.clusterRef points at an existing ArkCluster
	//   StatefulSetReady — STS replica is in Running with all containers ready
	//   PVCReady         — per-map PVC is in Bound phase
	// Known types (Phase 2+):
	//   GameReady        — RCON probe succeeds; the in-game server accepts players
	//   BackupHealthy    — most recent ArkBackup succeeded within expected window
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=arks
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Map",type=string,JSONPath=".spec.map"
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=".spec.desiredState"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ArkServer is the Schema for the arkservers API.
type ArkServer struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ArkServer
	// +required
	Spec ArkServerSpec `json:"spec"`

	// status defines the observed state of ArkServer
	// +optional
	Status ArkServerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ArkServerList contains a list of ArkServer
type ArkServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ArkServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ArkServer{}, &ArkServerList{})
}
