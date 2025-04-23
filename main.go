package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	utils "github.com/mudler/luet/pkg/api/client/utils"
)

var (
	finalRepo = flag.String("repo", os.Getenv("FINAL_REPO"), "Final container registry repository")
	outputDir = flag.String("output", "${PWD}/build", "Output directory for built packages")

	luetVersion = flag.String("luet-version", "0.36.0", "Luet version to use")
	luetArch    = flag.String("luet-arch", "amd64", "Architecture for Luet binary")

	dockerUsername = flag.String("docker-username", os.Getenv("DOCKER_USERNAME"), "Docker username for authentication")
	dockerPassword = flag.String("docker-password", os.Getenv("DOCKER_PASSWORD"), "Docker password for authentication")
	dockerEndpoint = flag.String("docker-endpoint", os.Getenv("DOCKER_ENDPOINT"), "Docker registry endpoint")
)

func main() {
	flag.Parse()

	// Setup Luet
	utils.RunSH("dependencies", fmt.Sprintf("curl -L https://github.com/mudler/luet/releases/download/%s/luet-%s-linux-%s --output luet",
		*luetVersion, *luetVersion, *luetArch))
	utils.RunSH("dependencies", "chmod +x luet")
	utils.RunSH("dependencies", "mv luet /usr/bin/luet && mkdir -p /etc/luet/repos.conf.d/")
	utils.RunSH("dependencies", "curl -L https://raw.githubusercontent.com/mocaccinoOS/repository-index/master/packages/luet.yml --output /etc/luet/repos.conf.d/luet.yml")

	// Login to Docker if credentials are provided
	if *dockerUsername != "" && *dockerPassword != "" {
		out, err := utils.RunSHOUT("login", fmt.Sprintf(
			"echo %s | docker login -u '%s' --password-stdin '%s'",
			*dockerPassword, *dockerUsername, *dockerEndpoint),
		)
		if err != nil {
			fmt.Println(string(out))
			os.Exit(1)
		} else {
			fmt.Printf("Successfully logged in to Docker registry %s as user %s\n", *dockerEndpoint, *dockerUsername)
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

	err := utils.RunSH("build", fmt.Sprintf("sudo luet build --all --destination %s", *outputDir))
	if err != nil {
		return fmt.Errorf("failed to build packages: %w", err)
	}

	// Set permissions on output directory
	err = utils.RunSH("build perms", "chmod -R 777 "+*outputDir)
	if err != nil {
		return fmt.Errorf("failed to set permissions on output directory: %w", err)
	}

	return nil
}

func createRepo() error {
	fmt.Println("Creating repository...")
	finalRepo := strings.ToLower(*finalRepo)

	cmd := fmt.Sprintf(
		"sudo luet create-repo --output %s --packages %s --type docker --push-images --force-push",
		finalRepo,
		*outputDir,
	)

	err := utils.RunSH("create_repo", cmd)
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}

	return nil
}
