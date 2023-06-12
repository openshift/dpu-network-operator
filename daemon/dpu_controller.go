package main

import (
	"context"
	"os"

	"github.com/go-logr/logr"
	v1alpha1 "github.com/openshift/dpu-network-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/k8snetworkplumbingwg/sriovnet"
)

type DpuReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
    Manager ctrl.Manager
}

func (r *DpuReconciler) initialReconcile() {
    klog.Info("Scheduling initial reconcile request")
    go func() {
        initialRequest := ctrl.Request{
            NamespacedName: types.NamespacedName{
                Name:      os.Getenv("K8S_NODE"),
                Namespace: os.Getenv("NAMESPACE"),
            },
        }
        r.Reconcile(context.Background(), initialRequest)
    }()
}

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

func getSerialNumberDefaultPort() (string, error) {
	port, err := getDefaultRoutePort()
	if err != nil {
		return "", fmt.Errorf("Error getting default route: %v\n", err)
	}
	pciAddress, err := sriovnet.GetPciFromNetDevice(port)
	return getSerialNumber(pciAddress)
}

func (r *DpuReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    klog.Info("Reconciling")
    found := true

    dpu := &v1alpha1.Dpu{}
    err := r.Get(ctx, req.NamespacedName, dpu)
    if err != nil {
        if errors.IsNotFound(err) {
            klog.Info("Dpu not found, will create it")
            found = false
        } else {
            klog.Errorf("Failed to get Dpu: %v", err)
            return ctrl.Result{Requeue: true}, nil
        }
    }

    dpu.Name = req.Name
    dpu.Namespace = req.Namespace

    if found && dpu.Name != os.Getenv("K8S_NODE") {
        return ctrl.Result{}, nil
    }

    serialNumber, err := getSerialNumberDefaultPort()
    if err != nil {
        klog.Errorf("Failed to get S/N for default port: %v", err)
        return ctrl.Result{}, err
    }
    klog.Infof("Found S/N %v", serialNumber)

    if !found {
        dpu.Spec.Id = serialNumber
        if err := r.Create(ctx, dpu); err != nil {
            klog.Errorf("Failed to create Dpu Cr: %v", err)
            return ctrl.Result{}, err
        }
    } else if dpu.Spec.Id != serialNumber {
        dpu.Spec.Id = serialNumber
        if err := r.Update(ctx, dpu); err != nil {
            klog.Error("Failed to update Dpu Cr: %v", err)
            return ctrl.Result{}, err
        }
        klog.Infof("Updated Dpu with new serial number %v", serialNumber)
    }

    return ctrl.Result{}, nil
}

func (r *DpuReconciler) SetupWithManager(mgr ctrl.Manager) error {
    ret := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Dpu{}).
        Owns(&v1alpha1.Dpu{}).
		Complete(r)
    return ret
}
