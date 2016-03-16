package main

import (
	"fmt"
	"testing"

	"k8s.io/kubernetes/pkg/api"
)

// add pod to existing service
func appendPod(s *svc, pod api.Pod) *svc {
	s.trackedPods[pod.Status.PodIP] = pod.Status
	return s
}

func lbFromPods(pods *api.PodList) (*loadbalancer, error) {
	var lbServers []LBServer
	var lbServer LBServer
	backends := make(map[string]Backend)
	services := servicesFromPods(pods.Items)

	// create a backend and LBServer for each service being load balanced
	for _, sv := range services {
		servers := []*Server{}
		for _, pod := range sv.trackedPods {
			servers = append(servers, &Server{Host: pod.PodIP, Port: 2048})
		}
		lbServer = getLoadBalancerComponent(sv)
		lbServers = append(lbServers, lbServer)
		backends[sv.sni] = Backend{
			Name:    sv.sni,
			SNI:     fmt.Sprintf("%s.", sv.sni),
			Servers: servers,
		}
	}

	// pass backends map and lbServers to ConfigWriter constructor
	lbConfig := NewLBConfigWriter(HAPROXY_CONFIG_PATH, getSyslogAddr(), backends, lbServers)
	return &loadbalancer{
		lbConfigWriter: lbConfig,
		services:       services,
		shutdownCh:     make(chan struct{}),
	}, nil
}

func servicesFromPods(pods []api.Pod) map[string]*svc {
	services := make(map[string]*svc)
	// create a service for each pod
	for _, pod := range pods {
		if trackedSvc, ok := services[getPodRoute(pod, false)]; ok {
			trackedSvc = appendPod(trackedSvc, pod)
		} else {
			sv := serviceFromPod(pod, false)
			if sv.sni != "" {
				services[sv.sni] = sv
			}
		}
	}
	return services
}

func newObjectMeta(sni string) api.ObjectMeta {
	return api.ObjectMeta{
		Labels: map[string]string{
			HAPROXY_NAME:   sni,
			HAPROXY_EXPOSE: "true",
		},
	}
}

func createPodList() *api.PodList {
	return &api.PodList{
		Items: createPods()[:3],
	}
}

func createPods() []api.Pod {
	return []api.Pod{
		api.Pod{
			ObjectMeta: newObjectMeta("thorSni"),
			Status: api.PodStatus{
				Phase: api.PodRunning,
				PodIP: "thor",
			},
		},
		api.Pod{
			ObjectMeta: newObjectMeta("chiefSni"),
			Status: api.PodStatus{
				Phase: api.PodRunning,
				PodIP: "chief",
			},
		},
		api.Pod{
			ObjectMeta: newObjectMeta("seamusSni"),
			Status: api.PodStatus{
				Phase: api.PodRunning,
				PodIP: "seamus",
			},
		},
		api.Pod{
			ObjectMeta: newObjectMeta("odinSni"),
			Status: api.PodStatus{
				Phase: api.PodRunning,
				PodIP: "odin",
			},
		},
	}
}

func TestGetRunningPods(t *testing.T) {
	pods := []api.Pod{
		api.Pod{
			ObjectMeta: newObjectMeta("odinSni"),
			Status: api.PodStatus{
				Phase: api.PodRunning,
				PodIP: "odin",
			},
		},
	}
	if len(getRunningPods(pods)) != 1 {
		t.Fatal("getRunningPods deleted pod when it shouldn't have")
	}
	pods[0].Status.Phase = api.PodPending
	if len(getRunningPods(pods)) != 0 {
		t.Fatal("getRunningPods did not delete a non-running pod")
	}
	pods[0].Status.Phase = api.PodRunning
	delete(pods[0].ObjectMeta.Labels, HAPROXY_NAME)
	if len(getRunningPods(pods)) != 0 {
		t.Fatal("getRunningPods did not delete a pod that no longer has an sni")
	}
}

func TestCheckForUpdate(t *testing.T) {
	var updated bool
	lb, err := lbFromPods(createPodList())
	if err != nil {
		t.Fatal("lbFromPods failed somehow which means you really done goofed")
	}
	// 3 pods -> 3 pods
	pods := createPods()[:3]
	updated = lb.checkForUpdate(pods)
	if updated {
		t.Fatal("checkForUpdate returned True when no pods changed")
	}
	// 3 pods -> 4 pods (new service)
	updated = lb.checkForUpdate(createPods())
	if !updated {
		t.Fatal("checkForUpdate returned False when one pod was added")
	}
	// 2 pods/2 services removed
	updated = lb.checkForUpdate(createPods()[:2])
	if !updated {
		t.Fatal("checkForUpdate returned False when two pods were removed")
	}
	// replace one pod with empty object meta
	pods[0].ObjectMeta = api.ObjectMeta{}
	updated = lb.checkForUpdate(pods)
	if !updated {
		t.Fatal("checkForUpdate returned False when one pod was removed by deleting the haproxy labels")
	}
	// 3 pods -> 4 pods (one additional pod in existing service)
	pods = createPods()
	pods[len(pods)-1].ObjectMeta = newObjectMeta("SeamusSni")
	pods[len(pods)-1].Status = api.PodStatus{
		PodIP: "127.0.0.1",
	}
	updated = lb.checkForUpdate(pods)
	if !updated {
		t.Fatal("checkForUpdate returned False when one pod was added to an existing service")
	}
}

func TestCheckForUpdateReuseIPs(t *testing.T) {
	lb, err := lbFromPods(createPodList())
	if err != nil {
		t.Fatal("lbFromPods failed somehow which means you really done goofed")
	}
	updated := lb.checkForUpdate(createPods())
	if !updated {
		t.Fatal("checkForUpdate returned False when one pod was added")
	}
	// remove odin pod and assign odin IP to seamus pod
	pods := createPods()[:1]
	pods[len(pods)-1].Status.PodIP = "odin"
	updated = lb.checkForUpdate(pods)
	if !updated {
		t.Fatal("checkForUpdate returned False when two pods were removed")
	}
}

func TestCheckForDeadPods(t *testing.T) {
	lb, err := lbFromPods(createPodList())
	if err != nil {
		t.Fatal("lbFromPods failed somehow which means you really done goofed")
	}
	// remove thor service
	updated := lb.checkForDeadPods(createPods()[1:2])
	if !updated {
		t.Fatal("removeDefunctPods failed to indicate a service changed when it had 0 pods")
	}

	// add 'another pod' pod. then create list that doesnt include
	// that pod and verify an update is detected
	lb.services["chiefSni"].trackedPods["anotherPodSni"] = api.PodStatus{PodIP: "another pod"}
	updated = lb.checkForDeadPods(createPods()[1:2])
	if !updated {
		t.Fatal("removeDefunctPods failed to indicate a service changed when it went from 2 to 1 pods")
	}
}
