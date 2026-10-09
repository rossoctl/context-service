package kube

import (
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Config struct {
	Namespace   string
	UploadImage string
	PodName     string
	RESTConfig  *rest.Config
}

func LoadConfig() (Config, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules,
			&clientcmd.ConfigOverrides{},
		)
		restConfig, err = clientConfig.ClientConfig()
		if err != nil {
			return Config{}, err
		}
	}

	return Config{
		Namespace:   envOr("CS_NAMESPACE", "context-service"),
		UploadImage: os.Getenv("CS_UPLOAD_IMAGE"),
		PodName:     os.Getenv("POD_NAME"),
		RESTConfig:  restConfig,
	}, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
