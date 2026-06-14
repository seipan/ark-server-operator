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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

func newClusterForServer() *arkv1.ArkCluster {
	return &arkv1.ArkCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prod",
			Namespace: "ark",
		},
		Spec: arkv1.ArkClusterSpec{
			ClusterName: "prod-game-cluster",
			Image: arkv1.ImageSpec{
				Repository: "nightdragon1/ark-docker",
				Tag:        "v1",
				Digest:     "sha256:" + strings.Repeat("a", 64),
			},
			SharedStorage: arkv1.SharedStorageSpec{
				MountPath: "/ark-shared",
			},
			Game:             arkv1.IniSource{Inline: "[/script/shootergame.shootergamemode]\nKillXPMultiplier=8\n"},
			GameUserSettings: arkv1.IniSource{Inline: "[ServerSettings]\nServerPVE=true\n"},
			ArkManager:       arkv1.ArkManagerSpec{},
			PlayerLists: arkv1.PlayerListsSpec{
				AllowedCheaters: []string{"76561198030942091"},
			},
			Passwords: arkv1.PasswordsSpec{
				SecretRef: arkv1.PasswordSecretRef{
					Name: "ark-server-secrets",
				},
			},
		},
	}
}

func newServerForRender() *arkv1.ArkServer {
	return &arkv1.ArkServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gen1",
			Namespace: "ark",
		},
		Spec: arkv1.ArkServerSpec{
			ClusterRef:  arkv1.ClusterReference{Name: "prod"},
			Map:         arkv1.MapGenesis,
			SessionName: "Prod - Genesis",
			Ports: arkv1.PortsSpec{
				Client: 31011,
				Game:   31012,
				Query:  31013,
				RCON:   31014,
			},
			OverrideGameUserSettings: "[MessageOfTheDay]\nMessage=Welcome\n",
		},
	}
}

func newResolvedParentForServer(t *testing.T) *resolvedParent {
	t.Helper()
	c := newClusterForServer()
	return &resolvedParent{
		cluster:                c,
		sharedPVCName:          "prod-shared",
		sharedMountPath:        "/ark-shared",
		arkManagerCfg:          renderArkManagerCfg(c),
		globalGame:             c.Spec.Game.Inline,
		globalGameUserSettings: c.Spec.GameUserSettings.Inline,
		allowedCheaters:        "76561198030942091",
	}
}

func TestResolveServerMap_PrefersOverride(t *testing.T) {
	s := newServerForRender()
	s.Spec.MapOverride = "Mordor_P"
	if got := resolveServerMap(s); got != "Mordor_P" {
		t.Errorf("resolveServerMap = %q, want Mordor_P", got)
	}
}

func TestResolveServerMap_FallsBackToEnum(t *testing.T) {
	s := newServerForRender()
	if got := resolveServerMap(s); got != "Genesis" {
		t.Errorf("resolveServerMap = %q, want Genesis", got)
	}
}

func TestResolveReplicas(t *testing.T) {
	cases := []struct {
		state arkv1.ArkServerDesiredState
		want  int32
	}{
		{arkv1.StateRunning, 1},
		{arkv1.StateStopped, 0},
		{arkv1.StateHibernated, 0},
		{arkv1.StateWiped, 1}, // PR3 will replace this with deletion path
		{"", 1},               // empty falls back to Running
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			s := newServerForRender()
			s.Spec.DesiredState = tc.state
			if got := resolveReplicas(s); got != tc.want {
				t.Errorf("resolveReplicas(%q) = %d, want %d", tc.state, got, tc.want)
			}
		})
	}
}

func TestBuildStatefulSet_StoppedHasZeroReplicas(t *testing.T) {
	s := newServerForRender()
	p := newResolvedParentForServer(t)

	s.Spec.DesiredState = arkv1.StateStopped
	if got := *buildStatefulSet(s, p).Spec.Replicas; got != 0 {
		t.Errorf("Stopped replicas = %d, want 0", got)
	}

	s.Spec.DesiredState = arkv1.StateHibernated
	if got := *buildStatefulSet(s, p).Spec.Replicas; got != 0 {
		t.Errorf("Hibernated replicas = %d, want 0", got)
	}

	s.Spec.DesiredState = arkv1.StateRunning
	if got := *buildStatefulSet(s, p).Spec.Replicas; got != 1 {
		t.Errorf("Running replicas = %d, want 1", got)
	}
}

func TestMergeIni(t *testing.T) {
	cases := []struct {
		name           string
		global, delta  string
		wantSubstrings []string
	}{
		{
			name:           "both set",
			global:         "[A]\nx=1\n",
			delta:          "[B]\ny=2\n",
			wantSubstrings: []string{"x=1", "y=2"},
		},
		{
			name:           "no override",
			global:         "[A]\nx=1\n",
			delta:          "",
			wantSubstrings: []string{"x=1"},
		},
		{
			name:           "no global",
			global:         "",
			delta:          "[B]\ny=2\n",
			wantSubstrings: []string{"y=2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeIni(tc.global, tc.delta)
			for _, s := range tc.wantSubstrings {
				if !strings.Contains(got, s) {
					t.Errorf("mergeIni missing %q\n--- output ---\n%s", s, got)
				}
			}
		})
	}
}

func TestBuildRenderedConfigCM_AllKeys(t *testing.T) {
	s := newServerForRender()
	p := newResolvedParentForServer(t)

	cm := buildRenderedConfigCM(s, p)

	if cm.Name != "gen1-rendered-config" {
		t.Errorf("CM name = %q", cm.Name)
	}
	wantKeys := []string{
		arkManagerCfgKey,
		mergedGameIniKey,
		mergedGameUserSettingsIniKey,
		allowedCheatersKey,
		joinNoCheckKey,
		exclusiveJoinListKey,
	}
	for _, k := range wantKeys {
		if _, ok := cm.Data[k]; !ok {
			t.Errorf("rendered-config CM missing key %q", k)
		}
	}
	if !strings.Contains(cm.Data[mergedGameUserSettingsIniKey], "Message=Welcome") {
		t.Errorf("override GUS not included in merged: %q", cm.Data[mergedGameUserSettingsIniKey])
	}
	if !strings.Contains(cm.Data[mergedGameUserSettingsIniKey], "ServerPVE=true") {
		t.Errorf("global GUS not included in merged: %q", cm.Data[mergedGameUserSettingsIniKey])
	}
	if cm.Labels[LabelComponent] != ComponentConfig {
		t.Errorf("component label = %q, want %q", cm.Labels[LabelComponent], ComponentConfig)
	}
	if cm.Labels[LabelMap] != "Genesis" {
		t.Errorf("map label = %q, want Genesis", cm.Labels[LabelMap])
	}
}

func TestBuildDataPVC_Defaults(t *testing.T) {
	s := newServerForRender()
	p := newResolvedParentForServer(t)

	pvc := buildDataPVC(s, p)

	if pvc.Name != "gen1-data" {
		t.Errorf("PVC name = %q", pvc.Name)
	}
	got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	want := resource.MustParse("30Gi")
	if got.Cmp(want) != 0 {
		t.Errorf("PVC size = %s, want 30Gi", got.String())
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("PVC access modes = %v, want [ReadWriteOnce]", pvc.Spec.AccessModes)
	}
	if pvc.Spec.StorageClassName != nil {
		t.Errorf("PVC storageClassName = %v, want nil (cluster default)", *pvc.Spec.StorageClassName)
	}
}

func TestBuildDataPVC_OverridesApplied(t *testing.T) {
	s := newServerForRender()
	custom := resource.MustParse("100Gi")
	sc := "fast-ssd"
	s.Spec.Storage = &arkv1.ArkServerStorageSpec{
		Size:             &custom,
		StorageClassName: &sc,
		AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
	}
	p := newResolvedParentForServer(t)

	pvc := buildDataPVC(s, p)
	got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if got.Cmp(custom) != 0 {
		t.Errorf("PVC size override not applied: got %s", got.String())
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast-ssd" {
		t.Errorf("PVC storageClassName not applied: %v", pvc.Spec.StorageClassName)
	}
	if pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("PVC access modes not applied: %v", pvc.Spec.AccessModes)
	}
}

func TestBuildService_PortMapping(t *testing.T) {
	s := newServerForRender()
	p := newResolvedParentForServer(t)

	svc := buildService(s, p)

	if svc.Spec.Type != corev1.ServiceTypeNodePort {
		t.Errorf("Service type = %v", svc.Spec.Type)
	}
	if len(svc.Spec.Ports) != 4 {
		t.Fatalf("Service ports = %d, want 4", len(svc.Spec.Ports))
	}
	want := map[string]struct {
		port     int32
		protocol corev1.Protocol
	}{
		"client": {31011, corev1.ProtocolUDP},
		"game":   {31012, corev1.ProtocolUDP},
		"query":  {31013, corev1.ProtocolUDP},
		"rcon":   {31014, corev1.ProtocolTCP},
	}
	for _, p := range svc.Spec.Ports {
		w, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected Service port name %q", p.Name)
			continue
		}
		if p.NodePort != w.port || p.Port != w.port {
			t.Errorf("port %s: got NodePort=%d Port=%d, want %d", p.Name, p.NodePort, p.Port, w.port)
		}
		if p.Protocol != w.protocol {
			t.Errorf("port %s: protocol %s, want %s", p.Name, p.Protocol, w.protocol)
		}
	}
}

func TestBuildStatefulSet_BasicShape(t *testing.T) {
	s := newServerForRender()
	p := newResolvedParentForServer(t)

	sts := buildStatefulSet(s, p)

	if *sts.Spec.Replicas != 1 {
		t.Errorf("replicas = %d, want 1", *sts.Spec.Replicas)
	}
	if sts.Spec.ServiceName != "gen1" {
		t.Errorf("serviceName = %q, want gen1", sts.Spec.ServiceName)
	}
	if len(sts.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(sts.Spec.Template.Spec.InitContainers))
	}
	if len(sts.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 main container, got %d", len(sts.Spec.Template.Spec.Containers))
	}
	main := sts.Spec.Template.Spec.Containers[0]
	if main.Image != "nightdragon1/ark-docker@sha256:"+strings.Repeat("a", 64) {
		t.Errorf("image = %q", main.Image)
	}
	envByName := map[string]string{}
	for _, e := range main.Env {
		if e.Value != "" {
			envByName[e.Name] = e.Value
		}
	}
	if envByName["SERVERMAP"] != "Genesis" {
		t.Errorf("SERVERMAP env = %q", envByName["SERVERMAP"])
	}
	if envByName["STEAMPORT"] != "31012" {
		t.Errorf("STEAMPORT env = %q", envByName["STEAMPORT"])
	}
	if envByName["SERVERPORT"] != "31013" {
		t.Errorf("SERVERPORT env = %q", envByName["SERVERPORT"])
	}
	if envByName["RCONPORT"] != "31014" {
		t.Errorf("RCONPORT env = %q", envByName["RCONPORT"])
	}

	// Init container script references all symlink targets.
	init := sts.Spec.Template.Spec.InitContainers[0]
	if len(init.Args) != 1 {
		t.Fatalf("init args length = %d", len(init.Args))
	}
	script := init.Args[0]
	for _, expected := range []string{
		"arkmanager.cfg",
		"MergedGame.ini",
		"MergedGameUserSettings.ini",
		"AllowedCheaterSteamIDs.txt",
		"PlayersJoinNoCheck.txt",
		"PlayersExclusiveJoinList.txt",
		"chmod -R 777 /ark-shared",
	} {
		if !strings.Contains(script, expected) {
			t.Errorf("init script missing %q\n--- script ---\n%s", expected, script)
		}
	}
}
