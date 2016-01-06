package main

import (
	"testing"

	"k8s.io/kubernetes/pkg/api"
)

func lbFromPods(pods *api.PodList) (*loadbalancer, error) {
	var lbServers []LBServer
	var lbServer LBServer
	backends := make(map[string]Backend)
	services := servicesFromPods(pods.Items)

	// create a backend and LBServer for each service being load balanced
	for _, sv := range services {
		backends[sv.sni], lbServer = getLoadBalancerComponent(sv)
		lbServers = append(lbServers, lbServer)
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
		Items: createPods()[:2],
	}
}

func createPods() []api.Pod {
	return []api.Pod{
		api.Pod{
			ObjectMeta: newObjectMeta("thorSni"),
			Status: api.PodStatus{
				PodIP: "thor",
			},
		},
		api.Pod{
			ObjectMeta: newObjectMeta("chiefSni"),
			Status: api.PodStatus{
				PodIP: "chief",
			},
		},
		api.Pod{
			ObjectMeta: newObjectMeta("seamusSni"),
			Status: api.PodStatus{
				PodIP: "seamus",
			},
		},
		api.Pod{
			ObjectMeta: newObjectMeta("odinSni"),
			Status: api.PodStatus{
				PodIP: "odin",
			},
		},
	}
}

func TestCheckForUpdate(t *testing.T) {
	lb, err := lbFromPods(createPodList())
	if err != nil {
		t.Fatal("lbFromPods failed somehow which means you really done goofed")
	}
	updated, updatedPods := lb.checkForUpdate(createPods())
	if !updated {
		t.Fatal("checkForUpdate returned False when one pod was added")
	}
	if _, ok := updatedPods["odinSni"]; !ok {
		t.Fatal("checkForUpdate marked the wrong pods for update ", updatedPods)
	}
	updated, updatedPods = lb.checkForUpdate(createPods()[:1])
	if !updated {
		t.Fatal("checkForUpdate returned False when two pods were removed")
	}
	if _, ok := updatedPods["seamusSni"]; !ok {
		t.Fatal("checkForUpdate marked the wrong pods for update")
	}
}

func TestCheckForUpdateReuseIPs(t *testing.T) {
	lb, err := lbFromPods(createPodList())
	if err != nil {
		t.Fatal("lbFromPods failed somehow which means you really done goofed")
	}
	updated, updatedPods := lb.checkForUpdate(createPods())
	if !updated {
		t.Fatal("checkForUpdate returned False when one pod was added")
	}
	if _, ok := updatedPods["odinSni"]; !ok {
		t.Fatal("checkForUpdate marked the wrong pods for update ", updatedPods)
	}
	// remove odin pod and assign odin IP to seamus pod
	pods := createPods()[:1]
	pods[len(pods)-1].Status.PodIP = "odin"
	updated, updatedPods = lb.checkForUpdate(pods)
	if !updated {
		t.Fatal("checkForUpdate returned False when two pods were removed")
	}
	if _, ok := updatedPods["seamusSni"]; !ok {
		t.Fatal("checkForUpdate marked the wrong pods for update")
	}
}

func TestRemoveDefunctPods(t *testing.T) {
	lb, err := lbFromPods(createPodList())
	if err != nil {
		t.Fatal("lbFromPods failed somehow which means you really done goofed")
	}
	// remove thor service
	updated, updatedPods := lb.removeDefunctPods(createPods()[1:2], make(map[string]struct{}))
	if !updated {
		t.Fatal("removeDefunctPods failed to indicate a service changed when it had 0 pods")
	}
	if _, ok := updatedPods["thorSni"]; !ok {
		t.Fatal("removedDefunctPods marked the wrong pods for updated")
	}

	// add 'another pod' pod. then create list that doesnt include
	// that pod and verify an update is detected
	lb.services["chiefSni"].trackedPods["anotherPodSni"] = api.PodStatus{PodIP: "another pod"}
	updated, updatedPods = lb.removeDefunctPods(createPods()[1:2], make(map[string]struct{}))
	if !updated {
		t.Fatal("removeDefunctPods failed to indicate a service changed when it went from 2 to 1 pods")
	}
	if _, ok := updatedPods["chiefSni"]; !ok {
		t.Fatal("removedDefunctPods marked the wrong pods for updated")
	}
	if len(lb.services["chiefSni"].trackedPods) != 1 {
		t.Fatal("unused pod was not removed from chiefSni service")
	}
}
