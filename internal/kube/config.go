package kube

import (
	"errors"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Config struct {
	Namespace             string
	SandboxImage          string
	SandboxServiceAccount string
	RESTConfig            *rest.Config
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

	image := os.Getenv("CS_SANDBOX_IMAGE")
	if image == "" {
		return Config{}, errors.New("CS_SANDBOX_IMAGE is required")
	}
	return Config{
		Namespace:             envOr("CS_NAMESPACE", "serverless-harness"),
		SandboxImage:          image,
		SandboxServiceAccount: os.Getenv("CS_SANDBOX_SERVICE_ACCOUNT"),
		RESTConfig:            restConfig,
	}, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
