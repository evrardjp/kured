package cli

import (
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubernetesClientSetOrDie tries to load a client config from the given kubeconfig file.
// If the file is not found, it tries to load the in-cluster config.
func KubernetesClientSetOrDie(masterUrl string, kubeconfig string) *kubernetes.Clientset {
	// build k8s client
	var cfg *rest.Config
	var err error
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags(masterUrl, kubeconfig)
		if err != nil {
			os.Exit(1)
		}
	} else {
		cfg, err = rest.InClusterConfig()
		if err != nil {
			// try default kubeconfig in home for dev
			home := os.Getenv("HOME")
			if home != "" {
				kpath := filepath.Join(home, ".kube", "config")
				cfg, err = clientcmd.BuildConfigFromFlags("", kpath)
				if err != nil {
					os.Exit(1)
				}
			} else {
				os.Exit(1)
			}
		}
	}

	return kubernetes.NewForConfigOrDie(cfg)
}
