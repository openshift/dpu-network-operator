package main

import (
	"os"
	v1alpha1 "github.com/openshift/dpu-network-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"k8s.io/klog"
)

func main() {
	klog.Info("starting")

	scheme := runtime.NewScheme()
	err := v1alpha1.AddToScheme(scheme)
	if err != nil {
	    klog.Error(err, "unable to add scheme")
	    os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Namespace: os.Getenv("NAMESPACE", ),
		ClientDisableCacheFor:  []client.Object{&v1alpha1.Dpu{}},
		Scheme: scheme,
	})

	if err != nil {
		klog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	dpuReconciler := &DpuReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("Dpu"),
		Scheme: mgr.GetScheme(),
	}
	if err = dpuReconciler.SetupWithManager(mgr); err != nil {
		klog.Error(err, "unable to create controller", "controller", "Dpu")
		os.Exit(1)
	}

	dpuReconciler.initialReconcile()

	klog.Info("starting manager")
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		klog.Errorf("problem running manager: %v", err)
		os.Exit(1)
	}
}
