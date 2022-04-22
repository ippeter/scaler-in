/*
Roadmap:

v1.0
- basic deploy inside Kubernetes
- InClusterConfig
- access API

v1.1
- get list of nodes
- if len == 1, then nothing to do
- otherwise get CPU/Memory capacity for each node

v1.2
- logrus logging
- get uid from metadata for nodes
- collect metrics from pods

v1.3
- check for pods with no requests/limits
- calculate CPU/Memory usage for each node
- map of compact nodes stats: CPU/Mem Available, sum(CPU/Mem Requests), sum(CPU/Mem Limits) in non-system namespaces

v1.4
- get host with max CPU/RAM and min(sum(Requests)) and min(sum(Limits))
- get sum of available resources (Allocatable - sum(R, L))
- final output is pretty

v1.5
- check if pod is controlled by DaemonSet. Otherwise, get stats on this pod
- TIME_TO_WATCH environment variable, and think how to watch for several iterations
- environment variables via ConfigMap

v1.6
- softer check for available Limits
- mark host unschedulable
- check whether the app itself is on the candidate node

v1.7
- evict all user pods
- delete the node from cluster
- delete the VM from cloud


Backlog:
- write node usage to file
- read node usage from file
- KEEP_LARGE_NODES environment variable

*/

// Note: the example only works with the code within the same release/branch.
package main

import (
	"context"
	"os"
	"sort"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
	//"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	policyv1beta1 "k8s.io/api/policy/v1beta1"

	//"k8s.io/client-go/tools/clientcmd"

	// Required for metrics
	v1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	// Debug
	//"reflect"
	//"k8s.io/apimachinery/pkg/api/resource"
	//"k8s.io/metrics/pkg/apis/metrics"
	//
	// Uncomment to load all auth plugins
	// _ "k8s.io/client-go/plugin/pkg/client/auth"
	//
	// Or uncomment to load specific auth plugins
	// _ "k8s.io/client-go/plugin/pkg/client/auth/azure"
	// _ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	// _ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	// _ "k8s.io/client-go/plugin/pkg/client/auth/openstack"
)

type resourceUsage struct {
	CPUUsage    float64
	MemoryUsage int64
}

type resourceRequestsAndLimits struct {
	CPURequest    float64
	MemoryRequest int64
	CPULimit      float64
	MemoryLimit   int64
}

type containerResources struct {
	Usage             resourceUsage
	RequestsAndLimits resourceRequestsAndLimits
}

type podResources struct {
	Namespace             string
	ControlledByDaemonSet bool
	Containers            map[string]containerResources
}

type nodeStats struct {
	Name              string
	CPUCapacity       float64
	MemoryCapacity    int64
	CPUAllocatable    float64
	MemoryAllocatable int64
	Timestamp         time.Time
	NodeUsage         resourceUsage
	Pods              map[string]podResources
}

type compactNodeStats struct {
	Name              string
	UID               string
	CPUAllocatable    float64
	MemoryAllocatable int64
	sumCPURequests    float64
	sumMemoryRequests int64
	sumCPULimits      float64
	sumMemoryLimits   int64
	countsPassed      int8
}

func GetContainerUsageByName(podMetrics []v1beta1.PodMetrics, podName string, containerName string) resourceUsage {
	var ru resourceUsage

	// Find Pod in the list of PodMetrics
	for _, pm := range podMetrics {
		if pm.ObjectMeta.Name == podName {
			// Find Container in the list of Containers
			for _, cm := range pm.Containers {
				if cm.Name == containerName {
					cu := cm.Usage["cpu"]
					ru.CPUUsage = cu.AsApproximateFloat64()
					mu := cm.Usage["memory"]
					ru.MemoryUsage = mu.Value()

					break
				}
			}

			break
		}
	}

	return ru
}

var allNodes map[string]nodeStats
var nodesUIDByName map[string]string

//var prettyNodes []byte
//var prettyPods []byte
var newNode nodeStats
var p, newPod podResources
var c, newContainer containerResources
var allNodesCompactStats []compactNodeStats
var oneNodeCompactStats compactNodeStats

//var evictionPolicy eviction.Eviction
var nodeUID, candidateName, candidateUID, myNodeName, podName string
var availableCPURequests, availableCPULimits, maxCPULimits float64
var k, keyOfMax, timeToSleep int
var found, maxLimitsSet bool

type ByCPUAllocatable []compactNodeStats

func (cns ByCPUAllocatable) Len() int      { return len(cns) }
func (cns ByCPUAllocatable) Swap(i, j int) { cns[i], cns[j] = cns[j], cns[i] }
func (cns ByCPUAllocatable) Less(i, j int) bool {
	return cns[i].CPUAllocatable < cns[j].CPUAllocatable
}

var tmpCompactStats ByCPUAllocatable

func main() {

	if len(os.Getenv("TIME_TO_SLEEP")) > 0 {
		timeToSleep, _ = strconv.Atoi(os.Getenv("TIME_TO_SLEEP"))
	} else {
		timeToSleep = 600
	}

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		log.WithFields(log.Fields{
			"when": "InClusterConfig",
		}).Error(err.Error())
		os.Exit(1)
	}
	log.Info("InClusterConfig successful")

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.WithFields(log.Fields{
			"when": "Create clientset",
		}).Error(err.Error())
		os.Exit(1)
	}
	log.Info("Create clientset successful")

	log.Info("Starting the loop...")

	for {
		//
		// Get nodes
		//
		nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			log.WithFields(log.Fields{
				"when": "Get list of nodes",
			}).Error(err.Error())
		}
		nodesCount := len(nodes.Items)

		//
		// DEBUG TODO: Change to 1. 0 is for local development only!
		//
		if nodesCount > 0 {
			log.Infof("There are %d nodes in the cluster", nodesCount)

			//
			// Step 1: get capacity details of each node
			//
			allNodes = make(map[string]nodeStats)
			nodesUIDByName = make(map[string]string)

			for _, n := range nodes.Items {
				log.Infof("Node detected: %s", n.ObjectMeta.Name)

				nodeCPUCapacity := n.Status.Capacity["cpu"]
				nodeMemoryCapacity := n.Status.Capacity["memory"]
				nodeCPUAllocatable := n.Status.Allocatable["cpu"]
				nodeMemoryAllocatable := n.Status.Allocatable["memory"]

				newNode.Name = n.ObjectMeta.Name
				newNode.CPUCapacity = nodeCPUCapacity.AsApproximateFloat64()
				newNode.MemoryCapacity = nodeMemoryCapacity.Value()
				newNode.CPUAllocatable = nodeCPUAllocatable.AsApproximateFloat64()
				newNode.MemoryAllocatable = nodeMemoryAllocatable.Value()
				newNode.Pods = make(map[string]podResources)

				allNodes[string(n.ObjectMeta.UID)] = newNode
				nodesUIDByName[n.ObjectMeta.Name] = string(n.ObjectMeta.UID)
			}

			//
			// Step 2: get pods and containers, fetch Requests and Limits. Get pods usage
			//
			// access the API to list pods
			pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				log.WithFields(log.Fields{
					"when": "Get list of pods",
				}).Error(err.Error())
			}

			// Get metrics of pods
			clientset_metricsv, err := metricsv.NewForConfig(config)
			if err != nil {
				log.WithFields(log.Fields{
					"when": "Create clientset for metrics",
				}).Error(err.Error())
				os.Exit(1)
			}
			log.Info("Create clientset for metrics successful")

			podMetricsList, err := clientset_metricsv.MetricsV1beta1().PodMetricses("").List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				log.WithFields(log.Fields{
					"when": "Get pods metrics",
				}).Error(err.Error())
				os.Exit(1)
			}

			// Loop through pods to get limits and requests of each container in each pod
			for _, p := range pods.Items {
				newPod.Namespace = p.ObjectMeta.Namespace
				newPod.Containers = make(map[string]containerResources)
				newPod.ControlledByDaemonSet = false

				// Check whether pod is controlled by DaemonSet
				if len(p.ObjectMeta.OwnerReferences) > 0 {
					for _, o := range p.ObjectMeta.OwnerReferences {
						if o.Kind == "DaemonSet" {
							log.Infof("Pod %s is controlled by DaemonSet", p.ObjectMeta.Name)
							newPod.ControlledByDaemonSet = true
						}
					}
				}

				// Process pod containers
				for _, c := range p.Spec.Containers {
					// Handle Requests and Limits
					if len(c.Resources.Requests) > 0 {
						containerCPURequest := c.Resources.Requests["cpu"]
						containerMemoryRequest := c.Resources.Requests["memory"]

						newContainer.RequestsAndLimits.CPURequest = containerCPURequest.AsApproximateFloat64()
						newContainer.RequestsAndLimits.MemoryRequest = containerMemoryRequest.Value()
					} else {
						log.Warnf("Container %s belonging to pod %s doesn't have Requests defined!", c.Name, p.ObjectMeta.Name)

						newContainer.RequestsAndLimits.CPURequest = 0
						newContainer.RequestsAndLimits.MemoryRequest = 0
					}

					if len(c.Resources.Limits) > 0 {
						containerCPULimit := c.Resources.Limits["cpu"]
						containerMemoryLimit := c.Resources.Limits["memory"]

						newContainer.RequestsAndLimits.CPULimit = containerCPULimit.AsApproximateFloat64()
						newContainer.RequestsAndLimits.MemoryLimit = containerMemoryLimit.Value()
					} else {
						log.Warnf("Container %s belonging to pod %s doesn't have Limits defined!", c.Name, p.ObjectMeta.Name)

						newContainer.RequestsAndLimits.CPULimit = 0
						newContainer.RequestsAndLimits.MemoryLimit = 0
					}

					// Handle usage
					newContainer.Usage = GetContainerUsageByName(podMetricsList.Items, p.ObjectMeta.Name, c.Name)
					//log.Infof("Container usage: %+v", newContainer.Usage)

					// Update Container details
					newPod.Containers[c.Name] = newContainer
				}

				// Get UID by node name
				nodeUID = nodesUIDByName[p.Spec.NodeName]
				allNodes[nodeUID].Pods[p.ObjectMeta.Name] = newPod
			}

			//
			// Step 3: get nodes usage
			//

			// Get metrics of nodes
			nodeMetricsList, err := clientset_metricsv.MetricsV1beta1().NodeMetricses().List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				log.WithFields(log.Fields{
					"when": "Get nodes metrics",
				}).Error(err.Error())
			}

			for _, nm := range nodeMetricsList.Items {
				t := nm.Timestamp

				if thisNode, ok := allNodes[nm.ObjectMeta.Name]; ok {
					cu := nm.Usage["cpu"]
					mu := nm.Usage["memory"]

					thisNode.Timestamp, _ = time.Parse(time.RFC3339, t.Format(time.RFC3339))
					thisNode.NodeUsage.CPUUsage = cu.AsApproximateFloat64()
					thisNode.NodeUsage.MemoryUsage = mu.Value()

					// Get UID by node name
					nodeUID = nodesUIDByName[nm.ObjectMeta.Name]
					allNodes[nodeUID] = thisNode
				}
			}

			//
			// Calculate CPU/Memory usage for each node and make compactNodesUsage
			//
			allNodesCompactStats = make([]compactNodeStats, 0, len(allNodes))

			for uid, ns := range allNodes {
				log.Infof("Node UID: %s", uid)
				log.Infof("Node %s: CPU Used: %f, CPU Allocatable: %f, CPU Usage: %.2f", ns.Name, ns.NodeUsage.CPUUsage, ns.CPUAllocatable, ns.NodeUsage.CPUUsage/ns.CPUAllocatable)
				log.Infof("Node %s: Memory Used: %d, Memory Allocatable: %d, Memory Usage: %.2f", ns.Name, ns.NodeUsage.MemoryUsage, ns.MemoryAllocatable, float64(ns.NodeUsage.MemoryUsage)/float64(ns.MemoryAllocatable))

				oneNodeCompactStats.Name = ns.Name
				oneNodeCompactStats.UID = uid
				oneNodeCompactStats.countsPassed = 0
				oneNodeCompactStats.CPUAllocatable = ns.CPUAllocatable
				oneNodeCompactStats.MemoryAllocatable = ns.MemoryAllocatable

				oneNodeCompactStats.sumCPURequests = 0
				oneNodeCompactStats.sumMemoryRequests = 0
				oneNodeCompactStats.sumCPULimits = 0
				oneNodeCompactStats.sumMemoryLimits = 0

				for _, p := range ns.Pods {
					if !p.ControlledByDaemonSet {
						for _, c := range p.Containers {
							oneNodeCompactStats.sumCPURequests = oneNodeCompactStats.sumCPURequests + c.RequestsAndLimits.CPURequest
							oneNodeCompactStats.sumMemoryRequests = oneNodeCompactStats.sumMemoryRequests + c.RequestsAndLimits.MemoryRequest
							oneNodeCompactStats.sumCPULimits = oneNodeCompactStats.sumCPULimits + c.RequestsAndLimits.CPULimit
							oneNodeCompactStats.sumMemoryLimits = oneNodeCompactStats.sumMemoryLimits + c.RequestsAndLimits.MemoryLimit
						}
					}
				}

				allNodesCompactStats = append(allNodesCompactStats, oneNodeCompactStats)
			}

			// DEBUG output, just to make sure we have all required info on all nodes
			for _, cns := range allNodesCompactStats {
				log.Infof("Node UID: %s, Name: %s, CPU Allocatable: %.2f, Total CPU Requests: %.2f, Total CPU Limits: %.2f", cns.UID, cns.Name, cns.CPUAllocatable, cns.sumCPURequests, cns.sumCPULimits)
			}

			// Copy allNodesCompactStats into temp slice
			tmpCompactStats := make(ByCPUAllocatable, len(allNodesCompactStats))
			copy(tmpCompactStats, allNodesCompactStats)
			// DEBUG
			log.Infof("Length of tmpCompactStats: %d", len(tmpCompactStats))

			// Sort by CPUAllocatable
			sort.Sort(tmpCompactStats)

			// DEBUG output, just to make sure we have all required info on all nodes
			log.Infof("Length of the list sorted by CPUAllocatable: %d", len(tmpCompactStats))
			for _, cns := range tmpCompactStats {
				log.Infof("Node UID: %s, Name: %s, CPU Allocatable: %.2f, Total CPU Requests: %.2f, Total CPU Limits: %.2f", cns.UID, cns.Name, cns.CPUAllocatable, cns.sumCPURequests, cns.sumCPULimits)
			}

			// Loop through temp slice until we find a scale-in candidate
			found = false
			maxLimitsSet = false

			for {
				// Find node with max CPUAllocatable
				keyOfMax = len(tmpCompactStats) - 1
				oneNodeCompactStats = tmpCompactStats[keyOfMax]

				// Caclulate available CPU resources on other
				availableCPURequests = 0.0
				availableCPULimits = 0.0

				for _, v := range allNodesCompactStats {
					if v.UID != oneNodeCompactStats.UID {
						availableCPURequests = availableCPURequests + v.CPUAllocatable - v.sumCPURequests
						availableCPULimits = availableCPULimits + v.CPUAllocatable - v.sumCPULimits
					}
				}

				//
				// DEBUG Output on each node
				//
				log.Infof("Candidate: node %s", oneNodeCompactStats.Name)
				log.Infof("It has SUM CPU requests: %.2f, while available are: %.2f", oneNodeCompactStats.sumCPURequests, availableCPURequests)
				log.Infof("It has SUM CPU limits: %.2f, while available are: %.2f", oneNodeCompactStats.sumCPULimits, availableCPULimits)

				// Check if node with max CPUAllocatable fits into other nodes
				if oneNodeCompactStats.sumCPURequests <= availableCPURequests {
					found = true

					if !maxLimitsSet {
						maxLimitsSet = true
						maxCPULimits = availableCPULimits - oneNodeCompactStats.sumCPULimits

						// TODO: Maybe we will need more details on this node: need to see how to taint the node
						candidateName = oneNodeCompactStats.Name
						candidateUID = oneNodeCompactStats.UID
					} else {
						if availableCPULimits-oneNodeCompactStats.sumCPULimits > maxCPULimits {
							maxCPULimits = availableCPULimits - oneNodeCompactStats.sumCPULimits

							// TODO: Maybe we will need more details on this node: need to see how to taint the node
							candidateName = oneNodeCompactStats.Name
							candidateUID = oneNodeCompactStats.UID
						}
					}
				}

				// Remove last element from temp slice
				tmpCompactStats = tmpCompactStats[:len(tmpCompactStats)-1]

				// Check if temp slice is empty. If it is, we didn't find any scale-in options :(
				if len(tmpCompactStats) == 0 {
					break
				}
			}

			if found {
				// DEBUG
				log.Infof("Host %s is a candidate for scale-in! Situation with CPU Limits: %.2f. Cordoning it now...", candidateName, maxCPULimits)

				// Cordon the node
				_, err := clientset.CoreV1().Nodes().Patch(context.TODO(), candidateName, types.StrategicMergePatchType, []byte("{\"spec\":{\"unschedulable\":true}}"), metav1.PatchOptions{})
				if err != nil {
					log.WithFields(log.Fields{
						"when": "Cordon the node",
					}).Error(err.Error())
				}

				log.Infof("Host %s conrdoned successfully.", candidateName)

				// Check whether THIS app itself is on the candidate node
				if len(os.Getenv("MY_NODE_NAME")) > 0 {
					myNodeName = os.Getenv("MY_NODE_NAME")
				} else {
					log.Error("Environment variable MY_NODE_NAME not found!")
					os.Exit(1)
				}

				if myNodeName == candidateName {
					log.Warn("The app is running on the same host, which is the candidate for removal!")
					// This will terminate the app, and it will be restarted on another node
					os.Exit(1)
				} else {
					log.Infof("The app is running on the host %s", myNodeName)

					for podName, p := range allNodes[candidateUID].Pods {
						if p.ControlledByDaemonSet {
							log.Infof("Pod %s in namespace %s is controlled by DaemonSet. It will not be evicted.", podName, p.Namespace)
						} else {
							log.Infof("Pod for eviction: %s, in namespace: %s", podName, p.Namespace)

							evictionPolicy := policyv1beta1.Eviction{metav1.TypeMeta{APIVersion: "policy/v1beta1", Kind: "Eviction"}, metav1.ObjectMeta{Name: "quux", Namespace: "default"}, &metav1.DeleteOptions{}}

							err := clientset.CoreV1().Pods("").EvictV1beta1(context.TODO(), &evictionPolicy)
							if err != nil {
								log.WithFields(log.Fields{
									"when": "Evict pod",
								}).Error(err.Error())
							} else {
								log.Infof("Pod %s evicted successfully  ", podName)
							}
						}
					}
				}

			} else {
				log.Info("No host suitable for scale-in found :(")
			}

		} else {
			log.Warn("There is only 1 node in the cluster, nothing to do")
		}

		/*
			// get pods in all the namespaces by omitting namespace
			// Or specify namespace to get pods in particular namespace
			pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				panic(err.Error())
			}
			fmt.Printf("There are %d pods in the cluster\n", len(pods.Items))

			// Examples for error handling:
			// - Use helper functions e.g. errors.IsNotFound()
			// - And/or cast to StatusError and use its properties like e.g. ErrStatus.Message
			_, err = clientset.CoreV1().Pods("default").Get(context.TODO(), "example-xxxxx", metav1.GetOptions{})
			if errors.IsNotFound(err) {
				fmt.Printf("Pod example-xxxxx not found in default namespace\n")
			} else if statusError, isStatus := err.(*errors.StatusError); isStatus {
				fmt.Printf("Error getting pod %v\n", statusError.ErrStatus.Message)
			} else if err != nil {
				panic(err.Error())
			} else {
				fmt.Printf("Found example-xxxxx pod in default namespace\n")
			}
		*/

		time.Sleep(time.Duration(timeToSleep) * time.Second)
	}
}
