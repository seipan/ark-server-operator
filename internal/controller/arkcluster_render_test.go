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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

func boolPtr(b bool) *bool { return &b }

func newClusterForRender() *arkv1.ArkCluster {
	return &arkv1.ArkCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubic",
			Namespace: "ark",
		},
		Spec: arkv1.ArkClusterSpec{
			ClusterName: "kubicarkcluster",
			SharedStorage: arkv1.SharedStorageSpec{
				MountPath: "/ark-shared",
			},
			Game: arkv1.IniSource{
				Inline: "[/script/shootergame.shootergamemode]\nKillXPMultiplier=8\n",
			},
			GameUserSettings: arkv1.IniSource{
				Inline: "[ServerSettings]\nServerPVE=true\n",
			},
			ArkManager: arkv1.ArkManagerSpec{
				WarnMinutes:     15,
				BackupPreUpdate: true,
				MaxBackupSizeGB: 2,
				BanListURL:      "http://playark.com/banlist.txt",
				Messages: arkv1.MessageTemplates{
					WarnUpdateMinutes: "This ARK server will shutdown for an update in %d minutes",
				},
				Flags: arkv1.ArkFlags{
					Crossplay:            boolPtr(true),
					NoBattlEye:           boolPtr(true),
					UseAllAvailableCores: boolPtr(true),
				},
				Options: arkv1.ArkOptions{
					ActiveEvent: arkv1.ArkEventSummer,
				},
			},
			PlayerLists: arkv1.PlayerListsSpec{
				AllowedCheaters:   []string{"76561198030942091"},
				JoinNoCheck:       []string{"76561198030942091"},
				ExclusiveJoinList: []string{"76561198030942091"},
			},
		},
	}
}

func TestRenderArkManagerCfg(t *testing.T) {
	c := newClusterForRender()
	got := renderArkManagerCfg(c)

	mustContain := []string{
		`arkGameUserSettingsIniFile=/ark/MergedGameUserSettings.ini`,
		`arkGameIniFile=/ark/MergedGame.ini`,
		`arkwarnminutes="15"`,
		`arkBackupPreUpdate="true"`,
		`arkMaxBackupSizeGB="2"`,
		`msgWarnUpdateMinutes="This ARK server will shutdown for an update in %d minutes"`,
		`ark_BanListURL="http://playark.com/banlist.txt"`,
		`arkflag_crossplay=true`,
		`arkflag_NoBattlEye=true`,
		`arkflag_USEALLAVAILABLECORES=true`,
		`arkopt_clusterid=kubicarkcluster`,
		`arkopt_ClusterDirOverride=/ark-shared`,
		`arkopt_ActiveEvent=Summer`,
		`ark_SessionName=${SESSIONNAME}`,
		`ark_RCONPort=${RCONPORT}`,
		`ark_ServerAdminPassword=${ADMINPASSWORD}`,
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("rendered cfg missing %q\n--- output ---\n%s", s, got)
		}
	}

	mustNotContain := []string{
		// Flags left nil must not produce a line.
		`arkflag_log=`,
		`arkflag_NoTransferFromFiltering=`,
	}
	for _, s := range mustNotContain {
		if strings.Contains(got, s) {
			t.Errorf("rendered cfg unexpectedly contains %q\n--- output ---\n%s", s, got)
		}
	}
}

func TestRenderArkManagerCfg_OmitsNoneEvent(t *testing.T) {
	c := newClusterForRender()
	c.Spec.ArkManager.Options.ActiveEvent = arkv1.ArkEventNone

	got := renderArkManagerCfg(c)
	if strings.Contains(got, "arkopt_ActiveEvent=") {
		t.Errorf("expected arkopt_ActiveEvent to be omitted for None event:\n%s", got)
	}
}

func TestRenderArkManagerCfg_DefaultMountPath(t *testing.T) {
	c := newClusterForRender()
	c.Spec.SharedStorage.MountPath = ""

	got := renderArkManagerCfg(c)
	if !strings.Contains(got, "arkopt_ClusterDirOverride=/ark-shared") {
		t.Errorf("expected fallback mount path /ark-shared:\n%s", got)
	}
}

func TestBuildPlayerListsConfigMap_EmptyListsStillProduceKeys(t *testing.T) {
	c := newClusterForRender()
	c.Spec.PlayerLists = arkv1.PlayerListsSpec{}

	cm := buildPlayerListsConfigMap(c)

	for _, key := range []string{allowedCheatersKey, joinNoCheckKey, exclusiveJoinListKey} {
		if _, ok := cm.Data[key]; !ok {
			t.Errorf("player-lists ConfigMap missing data key %q", key)
		}
	}
}

func TestBuildGlobalIni_NilWhenConfigMapRef(t *testing.T) {
	c := newClusterForRender()
	c.Spec.Game.Inline = ""
	c.Spec.Game.ConfigMapRef = &arkv1.IniConfigMapRef{Name: "user-game-ini", Key: "Game.ini"}

	if buildGlobalGameIniConfigMap(c) != nil {
		t.Errorf("expected nil when game.configMapRef is the source")
	}

	c.Spec.GameUserSettings.Inline = ""
	c.Spec.GameUserSettings.ConfigMapRef = &arkv1.IniConfigMapRef{Name: "user-gus", Key: "GameUserSettings.ini"}
	if buildGlobalGameUserSettingsIniConfigMap(c) != nil {
		t.Errorf("expected nil when gameUserSettings.configMapRef is the source")
	}
}
