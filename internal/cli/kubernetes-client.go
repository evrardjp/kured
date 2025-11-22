package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func KubernetesConfig(masterUrl string, kubeconfig string) (*rest.Config, error) {
	// build k8s client
	//var cfg *rest.Config
	//var err error
	if masterUrl != "" {
		if kubeconfig != "" {
			return clientcmd.BuildConfigFromFlags(masterUrl, kubeconfig)
		}
		home := os.Getenv("HOME")
		if home != "" {
			kpath := filepath.Join(home, ".kube", "config")
			return clientcmd.BuildConfigFromFlags(masterUrl, kpath)
		}
		return nil, fmt.Errorf("no kubeconfig found (no HOME env var set)")
	}
	return rest.InClusterConfig()
}

// KubernetesClientSetOrDie tries to load a client config from the given kubeconfig file.
// If the file is not found, it tries to load the in-cluster config.
func KubernetesClientSetOrDie(masterUrl string, kubeconfig string) *kubernetes.Clientset {
	// build k8s client
	cfg, err := KubernetesConfig(masterUrl, kubeconfig)
	if err != nil {
		os.Exit(1)
	}
	return kubernetes.NewForConfigOrDie(cfg)
}
