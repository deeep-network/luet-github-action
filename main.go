package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	utils "github.com/mudler/luet/pkg/api/client/utils"
)

//goaction:description Final container registry repository
var finalRepo = os.Getenv("FINAL_REPO")

//goaction:description Docker username to log into
var dockerUsername = os.Getenv("DOCKER_USERNAME")

//goaction:description Docker password to log into
var dockerPassword = os.Getenv("DOCKER_PASSWORD")

//goaction:description Optional docker endpoint, e.g. quay.io
var dockerEndpoint = os.Getenv("DOCKER_ENDPOINT")

var outputdir = flag.String("output", "${PWD}/build", "output where to store packages")

func main() {
	flag.Parse()

	finalRepo = strings.ToLower(finalRepo)
	if finalRepo == "" {
		finalRepo = "quay.io/nerdnode/packages"
	}

	// Setup Luet
	utils.RunSH("dependencies", "curl -L https://github.com/mudler/luet/releases/download/0.36.0/luet-0.36.0-linux-amd64 --output luet")
	utils.RunSH("dependencies", "chmod +x luet")
	utils.RunSH("dependencies", "mv luet /usr/bin/luet && mkdir -p /etc/luet/repos.conf.d/")
	utils.RunSH("dependencies", "curl -L https://raw.githubusercontent.com/mocaccinoOS/repository-index/master/packages/luet.yml --output /etc/luet/repos.conf.d/luet.yml")

	// Login to Docker if credentials are provided
	if dockerUsername != "" && dockerPassword != "" {
		out, err := utils.RunSHOUT("login", fmt.Sprintf(
			"echo %s | docker login -u '%s' --password-stdin '%s'",
			dockerPassword, dockerUsername, dockerEndpoint),
		)
		if err != nil {
			fmt.Println(string(out))
			os.Exit(1)
		} else {
			fmt.Printf("Successfully logged in to Docker registry %s as user %s\n", dockerEndpoint, dockerUsername)
		}
	}

	// Build all packages
	err := buildPackages()
	if err != nil {
		fmt.Println("Error during build:", err)
		os.Exit(1)
	}

	// Create repository
	err = createRepo()
	if err != nil {
		fmt.Println("Error during repository creation:", err)
		os.Exit(1)
	}
}

func buildPackages() error {
	fmt.Println("Building all packages...")

	err := utils.RunSH("build", fmt.Sprintf("sudo luet build --all --destination %s", *outputdir))
	if err != nil {
		return fmt.Errorf("failed to build packages: %w", err)
	}

	// Set permissions on output directory
	err = utils.RunSH("build perms", "chmod -R 777 "+*outputdir)
	if err != nil {
		return fmt.Errorf("failed to set permissions on output directory: %w", err)
	}

	return nil
}

func createRepo() error {
	fmt.Println("Creating repository...")

	cmd := fmt.Sprintf(
		"sudo luet create-repo --output %s --packages %s --type docker --push-images --force-push",
		finalRepo,
		*outputdir,
	)

	err := utils.RunSH("create_repo", cmd)
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}

	return nil
}
