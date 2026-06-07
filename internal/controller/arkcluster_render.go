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

// ConfigMap data keys rendered by the ArkCluster reconciler.
const (
	arkManagerCfgKey             = "arkmanager.cfg"
	globalGameIniKey             = "GlobalGame.ini"
	globalGameUserSettingsIniKey = "GlobalGameUserSettings.ini"
	allowedCheatersKey           = "AllowedCheaterSteamIDs.txt"
	joinNoCheckKey               = "PlayersJoinNoCheck.txt"
	exclusiveJoinListKey         = "PlayersExclusiveJoinList.txt"
)

// Default value mirrored from the kubebuilder default on
// ArkClusterSpec.SharedStorage.MountPath so the renderer also works on Cluster
// objects constructed in tests without the API server applying defaults.
const defaultSharedMountPath = "/ark-shared"

func arkManagerCfgConfigMapName(c *arkv1.ArkCluster) string {
	return c.Name + "-arkmanager-cfg"
}

func globalGameIniConfigMapName(c *arkv1.ArkCluster) string {
	return c.Name + "-game-ini"
}

func globalGameUserSettingsConfigMapName(c *arkv1.ArkCluster) string {
	return c.Name + "-game-user-settings-ini"
}

func playerListsConfigMapName(c *arkv1.ArkCluster) string {
	return c.Name + "-player-lists"
}

// buildArkManagerCfgConfigMap renders the arkmanager.cfg ConfigMap.
func buildArkManagerCfgConfigMap(c *arkv1.ArkCluster) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      arkManagerCfgConfigMapName(c),
			Namespace: c.Namespace,
			Labels:    managedLabels(c),
		},
		Data: map[string]string{
			arkManagerCfgKey: renderArkManagerCfg(c),
		},
	}
}

// buildGlobalGameIniConfigMap renders the GlobalGame.ini ConfigMap from
// spec.game.inline. Returns nil when spec.game.configMapRef is the source —
// in that case the user-provided ConfigMap is referenced directly downstream.
func buildGlobalGameIniConfigMap(c *arkv1.ArkCluster) *corev1.ConfigMap {
	if c.Spec.Game.Inline == "" {
		return nil
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      globalGameIniConfigMapName(c),
			Namespace: c.Namespace,
			Labels:    managedLabels(c),
		},
		Data: map[string]string{
			globalGameIniKey: c.Spec.Game.Inline,
		},
	}
}

// buildGlobalGameUserSettingsIniConfigMap renders the GlobalGameUserSettings.ini
// ConfigMap from spec.gameUserSettings.inline. Returns nil when configMapRef is used.
func buildGlobalGameUserSettingsIniConfigMap(c *arkv1.ArkCluster) *corev1.ConfigMap {
	if c.Spec.GameUserSettings.Inline == "" {
		return nil
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      globalGameUserSettingsConfigMapName(c),
			Namespace: c.Namespace,
			Labels:    managedLabels(c),
		},
		Data: map[string]string{
			globalGameUserSettingsIniKey: c.Spec.GameUserSettings.Inline,
		},
	}
}

// buildPlayerListsConfigMap renders the three player-list text files into a
// single ConfigMap. Empty lists produce empty data values so consumers can
// rely on the keys existing.
func buildPlayerListsConfigMap(c *arkv1.ArkCluster) *corev1.ConfigMap {
	pl := c.Spec.PlayerLists
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      playerListsConfigMapName(c),
			Namespace: c.Namespace,
			Labels:    managedLabels(c),
		},
		Data: map[string]string{
			allowedCheatersKey:   strings.Join(pl.AllowedCheaters, "\n"),
			joinNoCheckKey:       strings.Join(pl.JoinNoCheck, "\n"),
			exclusiveJoinListKey: strings.Join(pl.ExclusiveJoinList, "\n"),
		},
	}
}

// renderArkManagerCfg produces the plain-text arkmanager.cfg.
//
// Per-Pod values (session name, ports, passwords) are emitted as ${VAR}
// placeholders so envsubst at Pod start can substitute the per-ArkServer values.
func renderArkManagerCfg(c *arkv1.ArkCluster) string {
	var b strings.Builder
	s := c.Spec.ArkManager

	// Tell arkmanager where the operator-merged ini files land inside the Pod.
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

	// Per-ArkServer values are envsubst-resolved at Pod start.
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

// managedLabels are stamped on every ConfigMap the reconciler creates so
// operators can grep with kubectl -l.
func managedLabels(c *arkv1.ArkCluster) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "ark-server-operator",
		"app.kubernetes.io/part-of":    c.Name,
	}
}
