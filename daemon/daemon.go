package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
    "context"

	"github.com/k8snetworkplumbingwg/sriovnet"
	"k8s.io/klog"
	"k8s.io/client-go/rest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1alpha1 "github.com/openshift/dpu-network-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	scheme   = runtime.NewScheme()
)

func getSerialNumber(pciAddress string) (string, error) {
	cmd := exec.Command("lspci", "-vvv", "-s", pciAddress)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	serialNumberRegex := regexp.MustCompile(`Serial number:\s+(.*)`)
	match := serialNumberRegex.FindStringSubmatch(string(output))
	if len(match) != 2 {
		return "", fmt.Errorf("serial number not found for PCI address: %s", pciAddress)
	}

	return strings.TrimSpace(match[1]), nil
}

func getDefaultRoutePort() (string, error) {
	cmd := exec.Command("nmcli", "--get-values", "GENERAL.DEVICES", "conn", "show", "ovs-if-phys0")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return string(output), nil
}

func createClient() (client.Client, error) {
	// Get the in-cluster configuration.
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	
	// Create a new controller-runtime client from the configuration.
	controllerClient, err := client.New(config, client.Options{})
	if err != nil {
		return nil, err
	}
	
	return controllerClient, nil
}

func getSerialNumberDefaultPort() (string, error) {
	port, err := getDefaultRoutePort()
	if err != nil {
		return "", fmt.Errorf("Error getting default route: %v\n", err)
	}
	pciAddress, err := sriovnet.GetPciFromNetDevice(port)
	return getSerialNumber(pciAddress)
}

func main() {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})
	if err != nil {
		klog.Errorf("unable to start manager: %v", err)
		os.Exit(1)
	}

	err = v1alpha1.AddToScheme(mgr.GetScheme())
	if err != nil {
		klog.Errorf("unable to add scheme: %v", err)
		os.Exit(1)
	}


	serialNumber, err := getSerialNumberDefaultPort()
	if err != nil {
		klog.Errorf("Failed to get S/N for default port: %v\n", err)
	}
	
	klog.Errorf("Found S/N: %v\n", serialNumber)
	
	apiVersion := "dpu.openshift.io/v1alpha1"
	kind := "Dpu"
	
	dpu := &v1alpha1.Dpu{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiVersion,
			Kind:       kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      os.Getenv("K8S_NODE"),
			Namespace: os.Getenv("NAMESPACE"),
		},
		Spec: v1alpha1.DpuSpec{
		    Id: serialNumber,
		},
	}
	
	err = mgr.GetClient().Create(context.TODO(), dpu)
	if err != nil {
		klog.Errorf("Failed to create/update Dpu CR: %v", err)
	}
	
	select {}
}
