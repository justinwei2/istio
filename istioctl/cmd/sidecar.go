// Copyright Istio Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/spf13/cobra"

	containerpb "google.golang.org/genproto/googleapis/container/v1"

	"istio.io/api/networking/v1alpha3"
	"istio.io/istio/pkg/config/schema/collections"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	name           string
	network        string
	serviceAccount string
	filename       string
	outputname     string
	ports          []string

	// optional GKE flags
	gkeProject      string
	clusterLocation string
)

const (
	tempPerms = os.FileMode(644)
)

func sidecarCommands() *cobra.Command {
	sidecarCmd := &cobra.Command{
		Use:   "sidecar",
		Short: "Commands to assist in managing sidecar configuration",
	}
	sidecarCmd.AddCommand(createGroupCommand())
	sidecarCmd.AddCommand(generateConfigCommand())
	return sidecarCmd
}

func createGroupCommand() *cobra.Command {
	createGroupCmd := &cobra.Command{
		Use:   "create-group",
		Short: "Creates a WorkloadGroup YAML artifact representing workload instances",
		Long: `Creates a WorkloadGroup YAML artifact representing workload instances for passing to the Kubernetes API server.
The generated artifact can be applied by running kubectl apply -f workloadgroup.yaml.`,
		Example: "create-group --name foo --namespace bar --labels app=foo,bar=baz --ports grpc=3550,http=8080 --network local --serviceAccount sa",
		Args: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("expecting a service name")
			}
			if namespace == "" {
				return fmt.Errorf("expecting a service namespace")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			u := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": collections.IstioNetworkingV1Alpha3Workloadgroups.Resource().APIVersion(),
					"kind":       collections.IstioNetworkingV1Alpha3Workloadgroups.Resource().Kind(),
					"metadata": map[string]interface{}{
						"name":      name,
						"namespace": namespace,
					},
				},
			}
			spec := &v1alpha3.WorkloadGroup{
				Labels:         convertToStringMap(labels),
				Ports:          convertToUnsignedInt32Map(ports),
				Network:        network,
				ServiceAccount: serviceAccount,
			}
			wgYAML, err := generateWorkloadGroupYAML(u, spec)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write([]byte(wgYAML))
			return err
		},
	}
	createGroupCmd.PersistentFlags().StringVar(&name, "name", "", "The name of the workload group")
	createGroupCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "The namespace that the workload instances will belong to")
	createGroupCmd.PersistentFlags().StringSliceVarP(&labels, "labels", "l", nil, "The labels to apply to the workload instances; e.g. -l env=prod,vers=2")
	createGroupCmd.PersistentFlags().StringSliceVarP(&ports, "ports", "p", nil, "The incoming ports that the workload instances will expose")
	createGroupCmd.PersistentFlags().StringVar(&network, "network", "default", "The name of the network for the workload instances")
	createGroupCmd.PersistentFlags().StringVarP(&serviceAccount, "serviceAccount", "s", "default", "The service identity to associate with the workload instances")
	return createGroupCmd
}

func generateWorkloadGroupYAML(u *unstructured.Unstructured, spec *v1alpha3.WorkloadGroup) (string, error) {
	iSpec, err := unstructureIstioType(spec)
	if err != nil {
		return "", err
	}
	u.Object["spec"] = iSpec

	wgYAML, err := yaml.Marshal(u.Object)
	if err != nil {
		return "", err
	}
	return string(wgYAML), nil
}

// Cluster inference from the current kubectl context only works for GKE
func generateConfigCommand() *cobra.Command {
	generateConfigCmd := &cobra.Command{
		Use:   "generate-config",
		Short: "Generates and packs all the required configuration files for deployment",
		Long: `Takes in WorkloadGroup artifact, then generates and packs all the required configuration files for deployment. 
This includes a MeshConfig resource, the cluster.env file, and necessary certificates and security tokens. 
Tries to automatically infer the target cluster, and prompts for flags if the cluster cannot be inferred`,
		Example: "generate-config -f workloadgroup.yaml -o config",
		Args: func(cmd *cobra.Command, args []string) error {
			if filename == "" {
				return fmt.Errorf("expecting a WorkloadGroup artifact file")
			}
			if outputname == "" {
				return fmt.Errorf("expecting an output filename")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			temp, err := ioutil.TempDir("./", "temp")
			if err != nil {
				return err
			}
			// TODO: pack temp into a tarball and then delete folder
			// defer os.RemoveAll(temp)

			var wg map[string]interface{}
			var spec v1alpha3.WorkloadGroup
			if err = readWorkloadGroup(filename, &wg, &spec); err != nil {
				return err
			}

			// consider passed in flags before attempting to infer config
			cluster, err := gkeCluster(gkeProject, clusterLocation, clusterName)
			if err != nil {
				gkeProject, clusterLocation, clusterName, cluster, err = gkeConfig()
				if err != nil {
					return fmt.Errorf("could not infer (project, location, cluster). Try passing in flags instead")
				}
			}
			if err = createClusterEnv(wg, &spec, cluster, temp); err != nil {
				return err
			}
			if err = createHosts(temp); err != nil {
				return err
			}

			fmt.Printf("generated config for (%s, %s, %s) in %s\n", gkeProject, clusterLocation, clusterName, outputname)
			return nil
		},
	}
	generateConfigCmd.PersistentFlags().StringVarP(&filename, "file", "f", "", "filename of the WorkloadGroup artifact")
	generateConfigCmd.PersistentFlags().StringVarP(&outputname, "output", "o", "", "Name of the tarball to be created")

	generateConfigCmd.PersistentFlags().StringVar(&gkeProject, "project", "", "Target project name")
	generateConfigCmd.PersistentFlags().StringVar(&clusterLocation, "location", "", "Target cluster location")
	generateConfigCmd.PersistentFlags().StringVar(&clusterName, "cluster", "", "Target cluster name")
	return generateConfigCmd
}

func readWorkloadGroup(filename string, wg *map[string]interface{}, spec *v1alpha3.WorkloadGroup) error {
	f, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}
	if err = yaml.Unmarshal(f, wg); err != nil {
		return err
	}

	yamlSpec, err := yaml.Marshal((*wg)["spec"])
	if err != nil {
		return err
	}
	jsonSpec, err := yaml.YAMLToJSON(yamlSpec)
	if err != nil {
		return err
	}
	return spec.UnmarshalJSON(jsonSpec)
}

// Write cluster.env into the given directory
func createClusterEnv(wg map[string]interface{}, spec *v1alpha3.WorkloadGroup, cluster *containerpb.Cluster, dir string) error {
	// default attributes
	clusterEnv := map[string]string{"ISTIO_CP_AUTH": "MUTUAL_TLS", "ISTIO_PILOT_PORT": "15012"}
	// service name, namespace, ports, service account, service CIDR
	md := wg["metadata"].(map[string]interface{})
	name, namespace := md["name"], md["namespace"]
	clusterEnv["ISTIO_SERVICE"] = fmt.Sprintf("%s.%s", name, namespace)
	clusterEnv["ISTIO_NAMESPACE"] = namespace.(string)
	for _, v := range spec.Ports {
		ports = append(ports, fmt.Sprint(v))
	}
	clusterEnv["ISTIO_INBOUND_PORTS"] = strings.Join(ports, ",")
	clusterEnv["SERVICE_ACCOUNT"] = spec.ServiceAccount
	clusterEnv["ISTIO_SERVICE_CIDR"] = cluster.ServicesIpv4Cidr

	return ioutil.WriteFile(dir+"/cluster.env", []byte(mapToString(clusterEnv)), tempPerms)
}

// Create the needed hosts addition in the given directory
func createHosts(dir string) error {
	istiod, err := k8sService("istiod", "istio-system")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(dir+"/hosts", []byte(fmt.Sprintf("%s\t%s", istiod.Spec.ClusterIP, "istiod.istio-system.svc")), tempPerms)
}

func mapToString(m map[string]string) string {
	var b strings.Builder
	for k, v := range m {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	return b.String()
}
