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
	arkv1 "github.com/seipan/ark-server-operator/api/v1"
)

const (
	LabelName      = "app.kubernetes.io/name"
	LabelInstance  = "app.kubernetes.io/instance"
	LabelPartOf    = "app.kubernetes.io/part-of"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelComponent = "app.kubernetes.io/component"
)

const (
	LabelClusterRef = "ark.yadon3141.com/cluster"
	LabelServerRef  = "ark.yadon3141.com/server"
	LabelMap        = "ark.yadon3141.com/map"
)

const (
	PartOfValue    = "ark"
	ManagedByValue = "ark-server-operator"

	AppNameArkCluster = "ark-cluster"
	AppNameArkServer  = "ark-server"
	AppNameArkBackup  = "ark-backup"
)

const (
	ComponentConfig        = "config"
	ComponentSharedStorage = "shared-storage"
	ComponentGameServer    = "game-server"
	ComponentBackup        = "backup"
)

// ClusterLabels returns the label set stamped on every resource owned by an
// ArkCluster. The component argument differentiates resources of different
// roles (e.g. ConfigMaps are "config", the shared PVC is "shared-storage").
func ClusterLabels(c *arkv1.ArkCluster, component string) map[string]string {
	return map[string]string{
		LabelName:       AppNameArkCluster,
		LabelInstance:   c.Name,
		LabelPartOf:     PartOfValue,
		LabelManagedBy:  ManagedByValue,
		LabelComponent:  component,
		LabelClusterRef: c.Name,
	}
}
