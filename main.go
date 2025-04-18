package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	cmdhelpers "github.com/mudler/luet/cmd/helpers"
	"github.com/mudler/luet/pkg/api/client"
	luetClient "github.com/mudler/luet/pkg/api/client"
	utils "github.com/mudler/luet/pkg/api/client/utils"
	"github.com/mudler/luet/pkg/api/core/types"
	"github.com/mudler/luet/pkg/installer"
)

type opData struct {
	FinalRepo string
}

type resultData struct {
	Package luetClient.Package
	Exists  bool
}

var defaultRetries int = 3

// The action can:
// 1: Build packages. Singularly (by specifying CURRENT_PACKAGE), or all of them.
//
//	(TODO:  implement  select  build only missing)
//
// 2: Download metadata for a given tree/repository
// 3: Create repository
var buildPackages = flag.Bool("build", false, "Build missing packages, or specified")
var download = flag.Bool("downloadMeta", false, "Download packages metadata")
var downloadAll = flag.Bool("downloadAllMeta", false, "Download All packages metadata")
var downloadFromList = flag.Bool("downloadFromList", false, "Download All packages metadata by listing all available image tags")

var fromIndex = flag.Bool("fromIndex", false, "Download metadata from index")

// buildx is kept for backwards compatibility. Consumers should install buildx themselves
// (e.g. using docker/setup-buildx-action). Our action auto-detects if buildx is installed.
var buildx = flag.Bool("buildx", false, "Install docker buildx")

var createRepo = flag.Bool("createRepo", false, "create repository")
var onlyMissing = flag.Bool("onlyMissing", false, "Build only missing packages")
var push = flag.Bool("pushCache", false, "Pushing cache images while building")
var pushFinalImages = flag.Bool("pushFinalImages", false, "Pushing final images while building")
var pushFinalImagesRepository = flag.String("pushFinalImagesRepository", "", "Specify a different final repo")

var tree = flag.String("tree", "${PWD}/packages", "create repository")
var platform = flag.String("platform", "", "buildx platform")

var luetVersion = flag.String("luetVersion", "0.20.10", "default Luet version")
var arch = flag.String("luetArch", "amd64", "default Luet arch")
var values = flag.String("values", "", "Values file")

var maxMetadataDownloads = flag.String("maxMetadataDownloads", "5", "How many metadata to download in parallel")

var outputdir = flag.String("output", "${PWD}/build", "output where to store packages")

var skipPackages = flag.String("skipPackages", "", "A space separated list of packages to skip")

var revisionSHA = flag.Bool("revisionSHA", false, "Revision SHA")

//goaction:description Final container registry repository
var finalRepo = os.Getenv("FINAL_REPO")

//goaction:description Current package to build
var currentPackage = os.Getenv("CURRENT_PACKAGE")

//goaction:description Repository Name
var repositoryName = os.Getenv("REPOSITORY_NAME")

//goaction:description Repository Type
var repositoryType = os.Getenv("REPOSITORY_TYPE")

//goaction:description Optional pull cache repository
var pullRepository = os.Getenv("PULL_REPOSITORY")

//goaction:description Docker username to log into
var dockerUsername = os.Getenv("DOCKER_USERNAME")

//goaction:description Docker password to log into
var dockerPassword = os.Getenv("DOCKER_PASSWORD")

//goaction:description Optional docker endpoint, e.g. quay.io
var dockerEndpoint = os.Getenv("DOCKER_ENDPOINT")

func main() {
	flag.Parse()

	finalRepo = strings.ToLower(finalRepo)

	// Setup code remains the same
	utils.RunSH("dependencies", "curl -L https://github.com/mudler/luet/releases/download/"+*luetVersion+"/luet-"+*luetVersion+"-linux-"+*arch+" --output luet")
	utils.RunSH("dependencies", "chmod +x luet")

	if *buildx {
		utils.RunSH("dependencies", "curl -L https://github.com/docker/buildx/releases/download/v0.21.1/buildx-v0.21.1.linux-"+*arch+" --output docker-buildx")
		utils.RunSH("dependencies", "chmod a+x docker-buildx")
		utils.RunSH("dependencies", "mkdir -p ~/.docker/cli-plugins")
		utils.RunSH("dependencies", "mv docker-buildx ~/.docker/cli-plugins")
		utils.RunSH("dependencies", "docker buildx install")
		utils.RunSH("dependencies", "docker run --privileged --rm tonistiigi/binfmt --install all")
	}

	utils.RunSH("dependencies", "mv luet /usr/bin/luet && mkdir -p /etc/luet/repos.conf.d/")
	utils.RunSH("dependencies", "curl -L https://raw.githubusercontent.com/mocaccinoOS/repository-index/master/packages/luet.yml --output /etc/luet/repos.conf.d/luet.yml")

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

	var err error
	switch {
	case *buildPackages:
		err = build()
	case *createRepo:
		err = create()
	case *download:
		err = downloadMeta()
	}

	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}

func repositoryPackages(repo string) (client.SearchResult, error) {
	var searchResult client.SearchResult

	fmt.Println("Retrieving remote repository packages")
	tmpdir, err := os.MkdirTemp(os.TempDir(), "ci")
	if err != nil {
		return searchResult, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpdir)

	repoConfig := types.LuetRepository{
		Name:   repositoryName,
		Type:   repositoryType,
		Cached: true,
		Urls:   []string{repo},
	}

	if dockerUsername != "" && dockerPassword != "" {
		repoConfig.Authentication = map[string]string{
			"username": dockerUsername,
			"password": dockerPassword,
		}
	}

	d := installer.NewSystemRepository(repoConfig)

	ctx := types.NewContext()
	ctx.Config.GetSystem().Rootfs = "/"
	ctx.Config.GetSystem().TmpDirBase = tmpdir
	re, err := d.Sync(ctx, false)
	if err != nil {
		return searchResult, fmt.Errorf("failed to sync repository: %w", err)
	}

	for _, p := range re.GetTree().GetDatabase().World() {
		searchResult.Packages = append(searchResult.Packages, client.Package{
			Name:     p.GetName(),
			Category: p.GetCategory(),
			Version:  p.GetVersion(),
		})
	}

	return searchResult, nil
}

func metaWorker(i int, wg *sync.WaitGroup, c <-chan luetClient.Package, o opData, errChan chan<- error) {
	defer wg.Done()

	for p := range c {
		tmpdir, err := os.MkdirTemp(os.TempDir(), "ci")
		if err != nil {
			errChan <- fmt.Errorf("worker %d failed to create temp dir: %w", i, err)
			continue
		}

		unpackdir, err := os.MkdirTemp(os.TempDir(), "ci")
		if err != nil {
			os.RemoveAll(tmpdir)
			errChan <- fmt.Errorf("worker %d failed to create unpack dir: %w", i, err)
			continue
		}

		defer func() {
			os.RemoveAll(tmpdir)
			os.RemoveAll(unpackdir)
		}()

		cmd := []string{
			fmt.Sprintf("TMPDIR=%s XDG_RUNTIME_DIR=%s", tmpdir, tmpdir),
			"luet",
			"util",
			"unpack",
		}

		if dockerUsername != "" && dockerPassword != "" {
			cmd = append(cmd,
				"--auth-username", dockerUsername,
				"--auth-password", dockerPassword)
		}
		cmd = append(cmd, p.ImageMetadata(o.FinalRepo), unpackdir)

		err = utils.RunSH("unpack", strings.Join(cmd, " "))
		if err != nil {
			errChan <- fmt.Errorf("worker %d failed to unpack %s: %w", i, p.String(), err)
			continue
		}

		err = utils.RunSH("move", fmt.Sprintf("mv %s/* %s/", unpackdir, *outputdir))
		if err != nil {
			errChan <- fmt.Errorf("worker %d failed to move files for %s: %w", i, p.String(), err)
			continue
		}
	}
}

func buildWorker(i int, wg *sync.WaitGroup, c <-chan luetClient.Package, o opData, results chan<- resultData, errChan chan<- error) {
	defer wg.Done()

	for p := range c {
		fmt.Println("Checking", p)
		exists, err := func() (bool, error) {
			defer func() {
				if r := recover(); r != nil {
					err := fmt.Errorf("worker %d recovered from panic while checking %s: %v", i, p.String(), r)
					errChan <- err
				}
			}()
			return p.ImageAvailable(o.FinalRepo), nil
		}()

		if err != nil {
			errChan <- err
			continue
		}

		results <- resultData{Package: p, Exists: exists}
	}
}

func create() error {
	cmd := []string{
		"luet",
		"create-repo",
		"--name", repositoryName,
		"--packages", *outputdir,
		"--tree", *tree,
	}

	if *push {
		cmd = append(cmd,
			"--output", finalRepo,
			"--push-images",
			"--type", "docker")
	} else {
		cmd = append(cmd,
			"--output", *outputdir,
			"--type", "http")
	}

	if *revisionSHA {
		out, err := utils.RunSHOUT("date", "date +%Y%m%d%H%M")
		if err != nil {
			return fmt.Errorf("failed to get date: %w", err)
		}
		date := strings.TrimSpace(string(out))

		githubSHA := os.Getenv("GITHUB_SHA")
		shortSHA := githubSHA
		if len(githubSHA) >= 8 {
			shortSHA = githubSHA[:8]
		}

		snapshotID := date + "-git" + shortSHA
		cmd = append(cmd, "--snapshot-id", snapshotID)

		err = utils.RunSH("exportOutput", fmt.Sprintf("echo \"LUET_PUSHED_REPO=%s-git%s\" >> \"$GITHUB_OUTPUT\"", date, shortSHA))
		if err != nil {
			return fmt.Errorf("failed to export output: %w", err)
		}
	}

	// Execute the command
	err := utils.RunSH("create_repo", strings.Join(cmd, " "))
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}

	return nil
}

func build() error {
	packs, err := luetClient.TreePackages(*tree)
	if err != nil {
		return fmt.Errorf("failed to get tree packages: %w", err)
	}

	if *fromIndex {
		currentPackages, err := repositoryPackages(finalRepo)
		if err != nil {
			return fmt.Errorf("failed to get repository packages: %w", err)
		}

		missingPackages := []client.Package{}
		skipP := []client.Package{}

		for _, f := range strings.Fields(*skipPackages) {
			pack, err := cmdhelpers.ParsePackageStr(f)
			if err == nil {
				skipP = append(skipP, client.Package{Name: pack.Name, Category: pack.Category})
			}
		}

		for _, p := range packs.Packages {
			if !client.Packages(currentPackages.Packages).Exist(p) ||
				len(skipP) != 0 && !client.Packages(skipP).Exist(client.Package{Name: p.Name, Category: p.Category}) {
				missingPackages = append(missingPackages, p)
			}
		}

		fmt.Println("Missing packages: " + fmt.Sprint(len(missingPackages)))
		for _, m := range missingPackages {
			fmt.Println("-", m.String())
		}

		var buildErrors int
		for _, p := range missingPackages {
			err := buildPackage(p.String())
			if err != nil {
				buildErrors++
				fmt.Printf("Error building package %s: %v\n", p.String(), err)
			}
		}

		if buildErrors > 0 {
			fmt.Printf("Encountered %d errors during package builds\n", buildErrors)
			if buildErrors == len(missingPackages) {
				return fmt.Errorf("all package builds failed")
			}
		}

		return nil
	}

	var buildCount int
	var buildErrors int
	for _, p := range packs.Packages {
		if ((*onlyMissing && !p.ImageAvailable(finalRepo)) || !*onlyMissing) &&
			(currentPackage != "" && p.EqualSV(currentPackage) || currentPackage == "") {
			buildCount++
			err := buildPackage(p.String())
			if err != nil {
				buildErrors++
				fmt.Printf("Error building package %s: %v\n", p.String(), err)
			}
		}
	}

	if buildErrors > 0 {
		fmt.Printf("Encountered %d errors during package builds\n", buildErrors)
		if buildErrors == buildCount {
			return fmt.Errorf("all package builds failed")
		}
	}

	err = utils.RunSH("build perms", "chmod -R 777 "+*outputdir)
	if err != nil {
		return fmt.Errorf("failed to set permissions on output directory: %w", err)
	}

	return nil
}

func buildPackage(s string) error {
	fmt.Println("Building", s)

	cmd := []string{
		"luet",
		"build",
		"--only-target-package",
		"--pull",
		"--from-repositories",
		"--live-output",
	}

	if pullRepository != "" {
		cmd = append(cmd, "--pull-repository", pullRepository)
	}

	if *push {
		cmd = append(cmd, "--push")
	}

	err := utils.RunSH("check-for-buildx", "docker buildx inspect")
	if err == nil { // buildx is available
		cmd = append(cmd, "--backend-args", "--load")
	}

	if *platform != "" {
		cmd = append(cmd, "--backend-args", "--platform")
		cmd = append(cmd, "--backend-args", *platform)
	}

	if *values != "" {
		cmd = append(cmd, "--values", *values)
	}

	if *pushFinalImages {
		cmd = append(cmd, "--push-final-images")
	}

	if *pushFinalImagesRepository != "" {
		cmd = append(cmd, "--push-final-images-repository", *pushFinalImagesRepository)
	}

	if finalRepo != "" {
		cmd = append(cmd, "--image-repository", finalRepo)
	}
	if pullRepository != "" {
		cmd = append(cmd, "--pull-repository", pullRepository)
	}
	if *tree != "" {
		cmd = append(cmd, "--tree", *tree)
	}
	cmd = append(cmd, s)

	err = utils.RunSH("build", strings.Join(cmd, " "))
	if err != nil {
		return fmt.Errorf("failed to build package %s: %w", s, err)
	}

	return nil
}

func retryList(image string, t int) ([]string, error) {
	tags, err := crane.ListTags(image)
	if err != nil {
		if t <= 0 {
			return tags, err
		}
		fmt.Printf("failed listing tags for '%s', retrying..\n", image)
		time.Sleep(time.Duration(defaultRetries-t+1) * time.Second)
		return retryList(image, t-1)
	}

	return tags, nil
}

func imageTags(tag string) ([]string, error) {
	return retryList(tag, defaultRetries)
}

func retryDownload(img, dest string, t int, errChan chan<- error) {
	err := make(chan error, 1)
	go downloadImg(img, dest, err)

	downloadErr := <-err
	if downloadErr != nil {
		if t <= 0 {
			errChan <- fmt.Errorf("failed downloading '%s' after %d retries: %w", img, defaultRetries, downloadErr)
			return
		}
		fmt.Printf("failed downloading '%s', retrying..\n", img)
		time.Sleep(time.Duration(defaultRetries-t+1) * time.Second)
		retryDownload(img, dest, t-1, errChan)
		return
	}
	errChan <- nil
}

func downloadImg(img, dst string, errChan chan<- error) {
	tmpdir, err := os.MkdirTemp(os.TempDir(), "ci")
	if err != nil {
		errChan <- fmt.Errorf("failed to create temp dir for %s: %w", img, err)
		return
	}

	unpackdir, err := os.MkdirTemp(os.TempDir(), "ci")
	if err != nil {
		os.RemoveAll(tmpdir)
		errChan <- fmt.Errorf("failed to create unpack dir for %s: %w", img, err)
		return
	}

	defer func() {
		os.RemoveAll(tmpdir)
		os.RemoveAll(unpackdir)
	}()

	cmd := []string{
		fmt.Sprintf("TMPDIR=%s XDG_RUNTIME_DIR=%s", tmpdir, tmpdir),
		"luet",
		"util",
		"unpack",
	}

	if dockerUsername != "" && dockerPassword != "" {
		cmd = append(cmd,
			"--auth-username", dockerUsername,
			"--auth-password", dockerPassword)
	}
	cmd = append(cmd, img, unpackdir)

	err = utils.RunSH("unpack", strings.Join(cmd, " "))
	if err != nil {
		errChan <- fmt.Errorf("failed to unpack %s: %w", img, err)
		return
	}

	err = utils.RunSH("move", fmt.Sprintf("mv %s/* %s/", unpackdir, dst))
	if err != nil {
		errChan <- fmt.Errorf("failed to move files from %s: %w", img, err)
		return
	}
	errChan <- nil
}

func downloadImage(img, dst string, errChan chan<- error) {
	retryDownload(img, dst, defaultRetries, errChan)
}

func downloadMeta() error {
	var packs luetClient.SearchResult
	var err error

	// Get tree packages
	packs, err = luetClient.TreePackages(*tree)
	if err != nil {
		return fmt.Errorf("failed to get tree packages: %w", err)
	}

	if *downloadAll {
		if *fromIndex {
			packs, err = repositoryPackages(finalRepo)
			if err != nil {
				fmt.Printf("Error retrieving repository packages: %v\n", err)
				// Continue with empty packs rather than returning error
				packs = luetClient.SearchResult{}
			}
		}

		if *downloadFromList {
			tags, err := imageTags(finalRepo)
			if err != nil {
				return fmt.Errorf("failed to list tags: %w", err)
			}

			var metadata []string
			for _, t := range tags {
				if strings.HasSuffix(t, ".metadata.yaml") {
					metadata = append(metadata, t)
				}
			}

			var wg sync.WaitGroup
			value, err := strconv.Atoi(*maxMetadataDownloads)
			if err != nil {
				value = 5
			}

			semaphore := make(chan struct{}, value)
			metaErrors := make(chan error, len(metadata))

			// Start download goroutines
			for _, m := range metadata {
				meta := m
				semaphore <- struct{}{}
				wg.Add(1)
				go func(m string) {
					defer func() {
						<-semaphore
						wg.Done()
					}()

					img := fmt.Sprintf("%s:%s", finalRepo, m)
					fmt.Println("Downloading start", img)
					downloadImage(img, *outputdir, metaErrors)
					fmt.Println("Downloading finished", img)
				}(meta)
			}

			// Wait for all downloads to complete
			wg.Wait()
			close(metaErrors)

			// Collect and handle errors
			var errCount int
			for err := range metaErrors {
				if err != nil {
					errCount++
					fmt.Printf("Error downloading metadata: %v\n", err)
				}
			}

			if errCount > 0 {
				fmt.Printf("Encountered %d errors during metadata downloads\n", errCount)
				if errCount == len(metadata) {
					return fmt.Errorf("all metadata downloads failed")
				}
			}

			// Return early since we're done with downloads
			return nil
		}
	} else {
		rpacks, err := luetClient.TreePackages(*tree)
		if err != nil {
			return fmt.Errorf("failed to get tree packages: %w", err)
		}

		missingPackages := luetClient.SearchResult{}

		currentPackages, err := repositoryPackages(finalRepo)
		if err != nil {
			fmt.Printf("Error retrieving repository packages: %v\n", err)
			currentPackages = client.SearchResult{}
		}

		skipP := []client.Package{}
		for _, f := range strings.Fields(*skipPackages) {
			pack, err := cmdhelpers.ParsePackageStr(f)
			if err == nil {
				skipP = append(skipP, client.Package{Name: pack.Name, Category: pack.Category})
			}
		}

		for _, p := range rpacks.Packages {
			if !client.Packages(currentPackages.Packages).Exist(p) ||
				len(skipP) != 0 && !client.Packages(skipP).Exist(client.Package{Name: p.Name, Category: p.Category}) {
				missingPackages.Packages = append(missingPackages.Packages, p)
			}
		}

		packs = missingPackages
	}

	// Process packages with workers
	all := make(chan luetClient.Package)
	wg := new(sync.WaitGroup)

	workers := 1                                     // Using 1 worker as in the original code
	errChan := make(chan error, len(packs.Packages)) // Buffer for all possible errors

	// Start workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go metaWorker(i, wg, all, opData{FinalRepo: finalRepo}, errChan)
	}

	// Send packages to workers
	for _, p := range packs.Packages {
		all <- p
	}
	close(all)

	// Start a goroutine to close the error channel when all workers finish
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// Collect and handle errors
	var errCount int
	for err := range errChan {
		if err != nil {
			errCount++
			fmt.Printf("Error processing package: %v\n", err)
		}
	}

	// Decide how to handle errors
	if errCount > 0 {
		fmt.Printf("Encountered %d errors during metadata processing\n", errCount)
		if errCount == len(packs.Packages) {
			return fmt.Errorf("all packages failed to process")
		}
	}

	return nil
}
