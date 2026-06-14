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
	"strings"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	arkManagerCfgKey     = "arkmanager.cfg"
	allowedCheatersKey   = "AllowedCheaterSteamIDs.txt"
	joinNoCheckKey       = "PlayersJoinNoCheck.txt"
	exclusiveJoinListKey = "PlayersExclusiveJoinList.txt"
)

const defaultSharedMountPath = "/ark-shared"

func SharedStoragePVCName(c *arkv1.ArkCluster) string {
	return c.Name + "-shared"
}

// buildSharedStoragePVC returns the desired PVC for cluster-travel storage.
// Returns nil when spec.sharedStorage.existingClaimName is set — in that case
// the operator does not own the PVC and only validates its existence at apply time.
func buildSharedStoragePVC(c *arkv1.ArkCluster) *corev1.PersistentVolumeClaim {
	ss := c.Spec.SharedStorage
	if ss.ExistingClaimName != "" {
		return nil
	}
	spec := corev1.PersistentVolumeClaimSpec{
		AccessModes: ss.AccessModes,
	}
	if ss.StorageClassName != "" {
		spec.StorageClassName = &ss.StorageClassName
	}
	if ss.Size != nil {
		spec.Resources = corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: *ss.Size,
			},
		}
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SharedStoragePVCName(c),
			Namespace: c.Namespace,
			Labels:    ClusterLabels(c, ComponentSharedStorage),
		},
		Spec: spec,
	}
}

// renderArkManagerCfg produces the plain-text arkmanager.cfg.
//
// Per-Pod values (session name, ports, passwords) are emitted as ${VAR}
// placeholders so envsubst at Pod start can substitute the per-ArkServer values.
func renderArkManagerCfg(c *arkv1.ArkCluster) string {
	var b strings.Builder
	s := c.Spec.ArkManager

	b.WriteString("arkGameUserSettingsIniFile=/ark/MergedGameUserSettings.ini\n")
	b.WriteString("arkGameIniFile=/ark/MergedGame.ini\n")

	fmt.Fprintf(&b, "arkwarnminutes=%q\n", fmt.Sprintf("%d", s.WarnMinutes))
	fmt.Fprintf(&b, "arkBackupPreUpdate=%q\n", boolStr(s.BackupPreUpdate))
	fmt.Fprintf(&b, "arkMaxBackupSizeGB=%q\n", fmt.Sprintf("%d", s.MaxBackupSizeGB))

	writeMsg(&b, "msgWarnUpdateMinutes", s.Messages.WarnUpdateMinutes)
	writeMsg(&b, "msgWarnUpdateSeconds", s.Messages.WarnUpdateSeconds)
	writeMsg(&b, "msgWarnRestartMinutes", s.Messages.WarnRestartMinutes)
	writeMsg(&b, "msgWarnRestartSeconds", s.Messages.WarnRestartSeconds)
	writeMsg(&b, "msgWarnShutdownMinutes", s.Messages.WarnShutdownMinutes)
	writeMsg(&b, "msgWarnShutdownSeconds", s.Messages.WarnShutdownSeconds)

	b.WriteString("serverMap=${SERVERMAP}\n")
	b.WriteString("ark_SessionName=${SESSIONNAME}\n")
	b.WriteString("ark_Port=${STEAMPORT}\n")
	b.WriteString("ark_QueryPort=${SERVERPORT}\n")
	b.WriteString("ark_RCONEnabled=\"True\"\n")
	b.WriteString("ark_RCONPort=${RCONPORT}\n")
	b.WriteString("ark_ServerPassword=${SERVERPASSWORD}\n")
	b.WriteString("ark_ServerAdminPassword=${ADMINPASSWORD}\n")
	b.WriteString("ark_MaxPlayers=${MAX_PLAYERS}\n")

	if s.BanListURL != "" {
		fmt.Fprintf(&b, "ark_BanListURL=%q\n", s.BanListURL)
	}

	writeFlag(&b, "arkflag_log", s.Flags.Log)
	writeFlag(&b, "arkflag_USEALLAVAILABLECORES", s.Flags.UseAllAvailableCores)
	writeFlag(&b, "arkflag_crossplay", s.Flags.Crossplay)
	writeFlag(&b, "arkflag_NoTransferFromFiltering", s.Flags.NoTransferFromFiltering)
	writeFlag(&b, "arkflag_NoBattlEye", s.Flags.NoBattlEye)

	fmt.Fprintf(&b, "arkopt_clusterid=%s\n", c.Spec.ClusterName)
	mount := c.Spec.SharedStorage.MountPath
	if mount == "" {
		mount = defaultSharedMountPath
	}
	fmt.Fprintf(&b, "arkopt_ClusterDirOverride=%s\n", mount)
	if s.Options.ActiveEvent != "" && s.Options.ActiveEvent != arkv1.ArkEventNone {
		fmt.Fprintf(&b, "arkopt_ActiveEvent=%s\n", s.Options.ActiveEvent)
	}
	return b.String()
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func writeMsg(b *strings.Builder, key, val string) {
	if val == "" {
		return
	}
	fmt.Fprintf(b, "%s=%q\n", key, val)
}

func writeFlag(b *strings.Builder, key string, v *bool) {
	if v == nil {
		return
	}
	fmt.Fprintf(b, "%s=%s\n", key, boolStr(*v))
}
