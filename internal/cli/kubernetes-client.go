package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubernetesConfig is a convenience function to load a client config from the given kubeconfig file when controller-runtime is not used.
func KubernetesConfig(masterURL string, kubeconfig string) (*rest.Config, error) {
	// build k8s client
	//var cfg *rest.Config
	//var err error
	if masterURL != "" {
		if kubeconfig != "" {
			return clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
		}
		home := os.Getenv("HOME")
		if home != "" {
			kpath := filepath.Join(home, ".kube", "config")
			return clientcmd.BuildConfigFromFlags(masterURL, kpath)
		}
		return nil, fmt.Errorf("no kubeconfig found (no HOME env var set)")
	}
	return rest.InClusterConfig()
}

// KubernetesClientSetOrDie tries to load a client config from the given kubeconfig file.
// If the file is not found, it tries to load the in-cluster config.
func KubernetesClientSetOrDie(masterURL string, kubeconfig string) *kubernetes.Clientset {
	// build k8s client
	cfg, err := KubernetesConfig(masterURL, kubeconfig)
	if err != nil {
		os.Exit(1)
	}
	return kubernetes.NewForConfigOrDie(cfg)
}
