package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	"k8s.io/kubernetes/pkg/api"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"

	"github.com/golang/glog"
)

const (
	ARBITRARY_DEFAULT_WORKER_POLL_INTERVAL = 30 * time.Second
	MAX_RETRIES                            = 5
	HAPROXY_NAME                           = "haproxy-sni-name"
	HAPROXY_EXPOSE                         = "haproxy-sni-expose"
	HAPROXY_PATH_NAME                      = "haproxy-pbr-name"
	HAPROXY_PATH_EXPOSE                    = "haproxy-pbr-expose"
)

// All services listen on port 443
const SVC_PORT = 443

type loadbalancer struct {
	// if true write path-based routing config
	// if false write sni routing config
	usePBR bool

	// one ConfigWriter per instance. ConfigWriter has
	// separate lbServers for each service. Each
	// LBServer specifies a SNI. Each LBServer routes traffic
	// to a backend based on the SNI. Each backend specifies
	// a pool of servers to which traffic may be sent
	lbConfigWriter LBConfigWriter

	// maps service names to services
	services map[string]*svc

	// channel passed to each monitor. This channels is used to
	// signal monitors to shutdown when this process receives SIGTERM
	shutdownCh chan struct{}
}

// creates load balancer struct that config for
// services using SNI routing
func newSNILb() *loadbalancer {
	lbConfig := NewLBConfigWriter(HAPROXY_CONFIG_PATH, getSyslogAddr(), make(map[string]Backend), []LBServer{})
	return &loadbalancer{
		usePBR:         false,
		lbConfigWriter: lbConfig,
		services:       make(map[string]*svc),
		shutdownCh:     make(chan struct{}),
	}
}

// creates load balancer struct that config for
// services using path based routing
func newPathBasedLb() *loadbalancer {
	lbConfig := NewPBLBConfigWriter(HAPROXY_CONFIG_PATH, getSyslogAddr(), make(map[string]Backend), []LBServer{})
	return &loadbalancer{
		usePBR:         true,
		lbConfigWriter: lbConfig,
		services:       make(map[string]*svc),
		shutdownCh:     make(chan struct{}),
	}
}

type svc struct {
	// service-specific prefix for this service
	sni string

	// Internal information about containers. ContainerPort
	// and PodIP will be used to construct backends for
	// the loadbalancer to talk to this service
	trackedPods map[string]api.PodStatus
}

// monitors an arbitrary set of pods when it detects
// a change in the monitored pods it rewrites haproxy
// config and invokes haproxy with the -sf option to trigger
// a config reload.
func main() {
	var usePBR = flag.Bool("usePBR", false, "Indicates if path based routing should be used. Default is to use SNI")
	var lb *loadbalancer
	flag.Parse()

	shutdownMainCh := make(chan struct{})
	updateCh := make(chan []api.Pod)

	kube_client, err := client.NewInCluster()
	if err != nil {
		glog.Fatalf("Unable to connect to kubernetes api server: %v", err)
	}

	if *usePBR {
		lb = newPathBasedLb()
	} else {
		lb = newSNILb()
	}

	go lb.monitorPods(ARBITRARY_DEFAULT_WORKER_POLL_INTERVAL, kube_client, updateCh, shutdownMainCh)

	// listen for SIGTERM
	go listenForTermination(shutdownMainCh)
	for {
		select {
		// gets name of updated service from updateCh
		case pods := <-updateCh:
			glog.Info("change in pods detected. updating config")
			// rewrite config + invoke haproxy to reload config
			err := lb.rewriteConfig(pods)
			if err != nil {
				glog.Warning(err)
			}
			// invoke haproxy to reload config
			err = notifyHAProxy()
			if err != nil {
				glog.Warning(err)
			}
		// if listenForTermination detects SIGTERM it will place a
		// value in this channel to indicate process should shutdown
		case <-shutdownMainCh:
			// close lb.shutdownCh will trigger all Asg monitors to exit
			close(lb.shutdownCh)
			return
		}
	}
}

func getPodRoute(pod api.Pod, usePBR bool) string {
	if usePBR {
		return pod.ObjectMeta.Labels[HAPROXY_PATH_NAME]
	}
	return pod.ObjectMeta.Labels[HAPROXY_NAME]
}

// monitors pods in this k8s instance and triggers an update when
// the pods change
func (lb *loadbalancer) monitorPods(interval time.Duration, kube_client *client.Client, updateCh chan []api.Pod, shutdownCh chan struct{}) {
	// send first update immediately
	podlist, err := kube_client.Pods(api.NamespaceAll).List(labels.Everything(), fields.Everything())
	if err != nil {
		glog.Warning(err)
	}
	updated := lb.checkForUpdate(podlist.Items)
	if updated {
		updateCh <- podlist.Items
	}

	// sit in loop forever checking for updates
	for {
		select {
		// add random duration to interval to help prevent monitors from syncing up
		// which may or may not be important..
		case <-time.After(interval + (time.Duration(rand.Intn(10)) * time.Second)):
			// get up to date pods
			podlist, err := kube_client.Pods(api.NamespaceAll).List(labels.Everything(), fields.Everything())
			if err != nil {
				// probably don't want this routine to die on failed lookup
				glog.Warning(err)
			} else if len(podlist.Items) == 0 {
				glog.Warning("No pods found")
			} else {
				// only update if pods are found and no error is
				// returned from the api call to kubernetes
				updated := lb.checkForUpdate(podlist.Items)
				if updated {
					updateCh <- podlist.Items
				}
			}
		case <-shutdownCh:
			return
		}
	}
}

// check if contents of old and new slices are the same
// returns true if there is a change
func (lb *loadbalancer) checkForUpdate(pods []api.Pod) bool {
	updated := false

	backends := lb.lbConfigWriter.GetBackends()
	// check each pod returned by the api call
	for _, untrackedPod := range pods {
		if untrackedPod.Status.PodIP == "" {
			continue
		}
		// if this service is already monitored check if the untrackedPod is new
		if monitoredService, ok := lb.services[getPodRoute(untrackedPod, lb.usePBR)]; ok {
			// if untrackedPod has an existing sni and a new podIP
			// it is appended to the existing trackedPods
			tracked := false
			servers := backends[monitoredService.sni].Servers
			for _, server := range servers {
				if untrackedPod.Status.PodIP == server.Host {
					tracked = true
				}
			}
			// if untrackedPod is not tracked, append it to the list of tracked pods
			if !tracked {
				updated = true
			}
		} else {
			// if untrackedPod is not already tracked, create a new service entry
			// and set updated to true
			sv := serviceFromPod(untrackedPod, lb.usePBR)
			if sv.sni != "" {
				// update lbConfigWriter with new service
				lb.services[getPodRoute(untrackedPod, lb.usePBR)] = sv
				lbServer := getLoadBalancerComponent(sv)
				lb.lbConfigWriter.AddLbServer(lbServer)
				updated = true
			}
		}
	}

	// check if any services no longer have alive pods
	removed := lb.checkForDeadPods(pods)
	return removed || updated
}

// remove pods being tracked that were not included in
// the result of the most recent API call.
func (lb *loadbalancer) checkForDeadPods(pods []api.Pod) bool {
	updated := false
	for _, svc := range lb.services {
		newTrackedPods := make(map[string]api.PodStatus)
		for _, pod := range pods {
			if trackedPod, ok := svc.trackedPods[pod.Status.PodIP]; ok {
				if svc.sni == getPodRoute(pod, lb.usePBR) {
					newTrackedPods[pod.Status.PodIP] = trackedPod
				}
			}
		}
		// if this service no longer has any pods associated with it
		if len(newTrackedPods) == 0 {
			delete(lb.services, svc.sni)
			updated = true
		} else if !reflect.DeepEqual(svc.trackedPods, newTrackedPods) {
			// if the updated list of pods doesnt match the already tracked pods
			// remove tracked pods that no longer exist
			updated = true
		}
	}
	return updated
}

// rewrites lbconfig struct after a change in pods is detected
// updates then calls LbConfigWriter.WriteConfigFile()
func (lb *loadbalancer) rewriteConfig(pods []api.Pod) error {
	var newBackend Backend
	// reset backends
	lb.lbConfigWriter.SetBackends(make(map[string]Backend))
	// for each service check for pods whose sni matches the service's
	for _, svc := range lb.services {
		servers := []*Server{}
		for _, pod := range pods {
			// if pod matches the route of svc append to server list
			if getPodRoute(pod, lb.usePBR) == svc.sni {
				servers = append(servers, &Server{
					Host: pod.Status.PodIP,
					Port: SVC_PORT,
				})
			}
		}
		if len(servers) > 0 {
			newBackend = Backend{
				Name:    svc.sni,
				SNI:     fmt.Sprintf("%s.", svc.sni),
				Servers: servers,
			}
			lb.lbConfigWriter.SetBackend(svc.sni, newBackend)
		}
	}
	return lb.lbConfigWriter.WriteConfigFile()
}

// invokes haproxy to trigger config reload
func notifyHAProxy() error {
	var cmd *exec.Cmd
	count := 0
	if !validConfig() {
		return fmt.Errorf("Invalid config was written. Not invoking haproxy")
	}
	pidString := getPid()
	if pidString == "" {
		cmd = exec.Command("/usr/sbin/haproxy", "-f", HAPROXY_CONFIG_PATH, "-p", HAPROXY_PID_FILE)
	} else {
		cmd = exec.Command("/usr/sbin/haproxy", "-f", HAPROXY_CONFIG_PATH, "-p", HAPROXY_PID_FILE, "-sf", pidString)
	}
	err := cmd.Run()
	for err != nil {
		count += 1
		if count > MAX_RETRIES {
			break
		}
		time.Sleep(time.Duration(count) * time.Second)
		err = cmd.Run()
	}
	return err
}

func validConfig() bool {
	cmd := exec.Command("/usr/sbin/haproxy", "-f", HAPROXY_CONFIG_PATH, "-c")
	err := cmd.Run()
	if err != nil {
		return false
	}
	return true
}

// reads pidFile. If pidFile is empty retry
// with a linear backoff
func getPid() string {
	glog.Info("getting haproxy pid")
	pidBytes, err := ioutil.ReadFile(HAPROXY_PID_FILE)
	if err != nil {
		glog.Warning("Failed to read pid file")
	}
	return string(pidBytes)
}

func getSyslogAddr() string {
	cmd := exec.Command("ip", "route", "list", "0.0.0.0/0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		glog.Warning("failed to cat route file when getting syslogAddr")
		return ""
	}
	return ipFromRoute(string(out))
}

func ipFromRoute(contents string) string {
	if len(strings.Split(contents, " ")) > 2 {
		return strings.Split(contents, " ")[2]
	}
	return ""
}

// create service for a given pod
func serviceFromPod(pod api.Pod, usePBR bool) *svc {
	return &svc{
		// sni must be service specific
		sni:         getPodRoute(pod, usePBR),
		trackedPods: map[string]api.PodStatus{pod.Status.PodIP: pod.Status},
	}
}

// add pod to existing service
func appendPod(s *svc, pod api.Pod) *svc {
	s.trackedPods[pod.Status.PodIP] = pod.Status
	return s
}

// create haproxy Backend and LBServer struct for a given service
// each service will have its own LBServer listen on a config-specified
// port and a Backend which lists servers in the autoscaling group associated
// with that service
func getLoadBalancerComponent(sv *svc) LBServer {
	return LBServer{
		BackendName: sv.sni,
	}
}

// On SIGTERM signals monitor routines to shutdown
// and triggers process to exit cleanly
func listenForTermination(shutdownCh chan struct{}) {
	signalCh := make(chan os.Signal, 2)
	signal.Notify(signalCh, syscall.SIGTERM, os.Interrupt)
	<-signalCh
	glog.Info("Received signal. Exiting")
	close(shutdownCh)
}
