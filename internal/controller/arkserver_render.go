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

package controller

import (
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

// ConfigMap data keys produced into the per-ArkServer rendered-config CM.
const (
	mergedGameIniKey             = "MergedGame.ini"
	mergedGameUserSettingsIniKey = "MergedGameUserSettings.ini"
)

// Default Secret data keys for the passwords Secret (mirrors the
// kubebuilder default on ArkCluster.spec.passwords.secretRef.keys).
const (
	defaultServerPasswordKey = "serverPass"
	defaultAdminPasswordKey  = "adminPass"
)

// Container / volume names used by the ArkServer StatefulSet template.
const (
	arkServerContainerName     = "arkgame"
	arkServerInitContainerName = "ark-prep"

	volumeNameData   = "ark-data"
	volumeNameShared = "ark-shared"
	volumeNameConfig = "ark-config"

	dataMountPath   = "/ark"
	configMountPath = "/ark-config"
)

// busyboxImage pins the image the init container runs. Pinning the init
// image to the operator's release follows the design-doc principle that
// the init script's lifetime is coupled to the operator's.
const busyboxImage = "busybox:1.36"

var defaultArkServerResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("350m"),
		corev1.ResourceMemory: resource.MustParse("7.25Gi"),
	},
	Limits: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("2000m"),
		corev1.ResourceMemory: resource.MustParse("10Gi"),
	},
}

// Default per-map PVC size. ARK install consumes ~15 GiB; remaining ~15 GiB
// headroom covers saves and SteamCMD update workspace for typical play.
var defaultArkServerStorageSize = resource.MustParse("30Gi")

// Default per-map PVC access mode. The StatefulSet runs a single replica so
// single-writer is sufficient.
var defaultArkServerAccessModes = []corev1.PersistentVolumeAccessMode{
	corev1.ReadWriteOnce,
}

// Resource name helpers ----------------------------------------------------

func renderedConfigCMName(s *arkv1.ArkServer) string {
	return s.Name + "-rendered-config"
}

func dataPVCName(s *arkv1.ArkServer) string {
	return s.Name + "-data"
}

func serviceName(s *arkv1.ArkServer) string {
	return s.Name
}

func statefulSetName(s *arkv1.ArkServer) string {
	return s.Name
}

// resolveServerMap picks the actual SERVERMAP value, preferring the enum-typed
// Map field over the free-form MapOverride. Callers should not pass an
// ArkServer with both empty (admission/webhook is expected to enforce that
// invariant; without webhooks a Reconcile-time validation surfaces it).
func resolveServerMap(s *arkv1.ArkServer) string {
	if s.Spec.MapOverride != "" {
		return s.Spec.MapOverride
	}
	return string(s.Spec.Map)
}

// mergeIni concatenates the cluster-wide ini with the per-map override.
// A trailing newline is added between sections so the merged result remains
// valid ini even when the inputs are missing terminal newlines.
func mergeIni(global, override string) string {
	if override == "" {
		return global
	}
	if global == "" {
		return override
	}
	return global + "\n" + override + "\n"
}

// resolveResources merges the user-supplied spec.resources with the operator
// default. An unset field falls back to the default; an empty
// ResourceRequirements deliberately overrides the default (the user said "no
// limits").
func resolveResources(s *arkv1.ArkServer) corev1.ResourceRequirements {
	if s.Spec.Resources != nil {
		return *s.Spec.Resources.DeepCopy()
	}
	return *defaultArkServerResources.DeepCopy()
}

// Builders ----------------------------------------------------------------

// buildRenderedConfigCM produces the single ConfigMap mounted at /ark-config
// by the ArkServer Pod. It carries every file the init container or the ARK
// server expects to find: arkmanager.cfg (copied verbatim from the parent
// render, still containing ${VAR} placeholders for envsubst), the two merged
// ini files (Global + per-map Override), and the three player-list text files.
func buildRenderedConfigCM(server *arkv1.ArkServer, parent *resolvedParent) *corev1.ConfigMap {
	mapName := resolveServerMap(server)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      renderedConfigCMName(server),
			Namespace: server.Namespace,
			Labels:    ServerLabels(server, parent.cluster.Name, mapName, ComponentConfig),
		},
		Data: map[string]string{
			arkManagerCfgKey:             parent.arkManagerCfg,
			mergedGameIniKey:             mergeIni(parent.globalGame, server.Spec.OverrideGame),
			mergedGameUserSettingsIniKey: mergeIni(parent.globalGameUserSettings, server.Spec.OverrideGameUserSettings),
			allowedCheatersKey:           parent.allowedCheaters,
			joinNoCheckKey:               parent.joinNoCheck,
			exclusiveJoinListKey:         parent.exclusiveJoinList,
		},
	}
}

// buildDataPVC returns the per-map PVC mounted at /ark inside the Pod.
// Falls back to operator defaults when spec.storage subfields are unset.
func buildDataPVC(server *arkv1.ArkServer, parent *resolvedParent) *corev1.PersistentVolumeClaim {
	mapName := resolveServerMap(server)

	storageSize := defaultArkServerStorageSize
	accessModes := defaultArkServerAccessModes
	var storageClass *string

	if ss := server.Spec.Storage; ss != nil {
		if ss.Size != nil {
			storageSize = *ss.Size
		}
		if len(ss.AccessModes) > 0 {
			accessModes = ss.AccessModes
		}
		if ss.StorageClassName != nil && *ss.StorageClassName != "" {
			storageClass = ss.StorageClassName
		}
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dataPVCName(server),
			Namespace: server.Namespace,
			Labels:    ServerLabels(server, parent.cluster.Name, mapName, ComponentGameServer),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: accessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}
	if storageClass != nil {
		pvc.Spec.StorageClassName = storageClass
	}
	return pvc
}

// buildService produces the NodePort Service that exposes the four ARK ports.
// The selector uses only stable labels (immutable post-creation) so the
// Service stays correctly targeted across rollouts.
func buildService(server *arkv1.ArkServer, parent *resolvedParent) *corev1.Service {
	mapName := resolveServerMap(server)
	ports := server.Spec.Ports
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName(server),
			Namespace: server.Namespace,
			Labels:    ServerLabels(server, parent.cluster.Name, mapName, ComponentGameServer),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: ServerSelectorLabels(server),
			Ports: []corev1.ServicePort{
				{Name: "client", Port: ports.Client, TargetPort: intstr.FromInt32(ports.Client), NodePort: ports.Client, Protocol: corev1.ProtocolUDP},
				{Name: "game", Port: ports.Game, TargetPort: intstr.FromInt32(ports.Game), NodePort: ports.Game, Protocol: corev1.ProtocolUDP},
				{Name: "query", Port: ports.Query, TargetPort: intstr.FromInt32(ports.Query), NodePort: ports.Query, Protocol: corev1.ProtocolUDP},
				{Name: "rcon", Port: ports.RCON, TargetPort: intstr.FromInt32(ports.RCON), NodePort: ports.RCON, Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// buildStatefulSet produces the StatefulSet that runs the ARK server. PR1
// always sets replicas=1 (Running path); state-machine branches for Stopped
// / Hibernated / Wiped come in later PRs.
func buildStatefulSet(server *arkv1.ArkServer, parent *resolvedParent) *appsv1.StatefulSet {
	replicas := int32(1)
	mapName := resolveServerMap(server)
	labels := ServerLabels(server, parent.cluster.Name, mapName, ComponentGameServer)

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSetName(server),
			Namespace: server.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: serviceName(server),
			Selector: &metav1.LabelSelector{
				MatchLabels: ServerSelectorLabels(server),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						// ARK servers do not readily survive eviction; pin them.
						"cluster-autoscaler.kubernetes.io/safe-to-evict": "false",
					},
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						buildInitContainer(parent),
					},
					Containers: []corev1.Container{
						buildMainContainer(server, parent),
					},
					Volumes: buildPodVolumes(server, parent),
				},
			},
		},
	}
}

// buildInitContainer creates the minimal init container the BABA design needs:
// the operator pre-merges ini files into /ark-config, so the init script only
// has to symlink the operator-managed config files into the locations the ARK
// binaries hard-code and chmod the cluster-travel volume.
func buildInitContainer(parent *resolvedParent) corev1.Container {
	script := fmt.Sprintf(`set -e
mkdir -p /ark/server/ShooterGame/Saved /ark/server/ShooterGame/Binaries/Linux
ln -sf /ark-config/arkmanager.cfg /ark/arkmanager.cfg
ln -sf /ark-config/MergedGame.ini /ark/MergedGame.ini
ln -sf /ark-config/MergedGameUserSettings.ini /ark/MergedGameUserSettings.ini
ln -sf /ark-config/AllowedCheaterSteamIDs.txt /ark/server/ShooterGame/Saved/AllowedCheaterSteamIDs.txt
ln -sf /ark-config/PlayersJoinNoCheck.txt /ark/server/ShooterGame/Binaries/Linux/PlayersJoinNoCheck.txt
ln -sf /ark-config/PlayersExclusiveJoinList.txt /ark/server/ShooterGame/Binaries/Linux/PlayersExclusiveJoinList.txt
chmod -R 777 %s/ || true
`, parent.sharedMountPath)
	return corev1.Container{
		Name:    arkServerInitContainerName,
		Image:   busyboxImage,
		Command: []string{"sh", "-c"},
		Args:    []string{script},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeNameData, MountPath: dataMountPath},
			{Name: volumeNameShared, MountPath: parent.sharedMountPath},
			{Name: volumeNameConfig, MountPath: configMountPath},
		},
	}
}

// buildMainContainer produces the arkgame container. Per-Pod values that the
// arkmanager.cfg template references via ${VAR} are injected here as env vars
// so bash sourcing of arkmanager.cfg expands them at runtime.
func buildMainContainer(server *arkv1.ArkServer, parent *resolvedParent) corev1.Container {
	mapName := resolveServerMap(server)
	ports := server.Spec.Ports

	serverKey := parent.cluster.Spec.Passwords.SecretRef.Keys.ServerPassword
	if serverKey == "" {
		serverKey = defaultServerPasswordKey
	}
	adminKey := parent.cluster.Spec.Passwords.SecretRef.Keys.AdminPassword
	if adminKey == "" {
		adminKey = defaultAdminPasswordKey
	}

	pullPolicy := parent.cluster.Spec.Image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}

	image := fmt.Sprintf("%s@%s", parent.cluster.Spec.Image.Repository, parent.cluster.Spec.Image.Digest)

	return corev1.Container{
		Name:            arkServerContainerName,
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Env: []corev1.EnvVar{
			{Name: "SERVERMAP", Value: mapName},
			{Name: "SESSIONNAME", Value: server.Spec.SessionName},
			{Name: "STEAMPORT", Value: strconv.Itoa(int(ports.Game))},
			{Name: "SERVERPORT", Value: strconv.Itoa(int(ports.Query))},
			{Name: "RCONPORT", Value: strconv.Itoa(int(ports.RCON))},
			{
				Name: "SERVERPASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: parent.cluster.Spec.Passwords.SecretRef.Name},
						Key:                  serverKey,
					},
				},
			},
			{
				Name: "ADMINPASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: parent.cluster.Spec.Passwords.SecretRef.Name},
						Key:                  adminKey,
					},
				},
			},
		},
		Ports: []corev1.ContainerPort{
			{ContainerPort: ports.Client, Protocol: corev1.ProtocolUDP, Name: "client"},
			{ContainerPort: ports.Game, Protocol: corev1.ProtocolUDP, Name: "game"},
			{ContainerPort: ports.Query, Protocol: corev1.ProtocolUDP, Name: "query"},
			{ContainerPort: ports.RCON, Protocol: corev1.ProtocolTCP, Name: "rcon"},
		},
		Resources: resolveResources(server),
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeNameData, MountPath: dataMountPath},
			{Name: volumeNameShared, MountPath: parent.sharedMountPath},
			{Name: volumeNameConfig, MountPath: configMountPath},
		},
	}
}

func buildPodVolumes(server *arkv1.ArkServer, parent *resolvedParent) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: volumeNameData,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: dataPVCName(server),
				},
			},
		},
		{
			Name: volumeNameShared,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: parent.sharedPVCName,
				},
			},
		},
		{
			Name: volumeNameConfig,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: renderedConfigCMName(server),
					},
				},
			},
		},
	}
}
