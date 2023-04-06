package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ellisto/monorepo-versioning/pkg"
	"github.com/google/go-github/v50/github"
	"golang.org/x/oauth2"
)

func main() {
	outputPath := os.Getenv("GITHUB_OUTPUT")
	token := os.Getenv("INPUT_GITHUB-TOKEN")
	component := os.Getenv("INPUT_COMPONENT")
	isDryRun := isDryRun(os.Getenv("INPUT_DRY-RUN"))
	initialVersion := os.Getenv("INPUT_INITIAL-VERSION")
	defaultBranch := os.Getenv("INPUT_DEFAULT-BRANCH")
	// owner/repository
	ownerAndRepository := os.Getenv("GITHUB_REPOSITORY")
	// Branch or tag
	ref := os.Getenv("GITHUB_REF_NAME")
	revision := os.Getenv("GITHUB_SHA")

	versioning := pkg.NewAction(
		ownerAndRepository,
		component,
		ref,
		revision,
		initialVersion,
		defaultBranch,
		ensureNewGitHubClient(token))

	newVersion := versioning.GenerateVersion(isDryRun)

	if isDryRun {
		fmt.Println("Is dry run? Yes")
	}

	if newVersion == nil {
		fmt.Println("New version generated? No")
	} else {
		fmt.Println("New version generated? Yes")
		fmt.Printf("Is pre-release? %t\n", newVersion.Prerelease() != "")
		fmt.Printf("New version: %s\n", newVersion.String())
	}

	// Only attempt to write to the GitHub output path if it exists
	// This makes it easier to test changes locally when no output file is specified
	if _, err := os.Stat(outputPath); err == nil {
		output, err := os.OpenFile(outputPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			panic(err)
		}

		defer output.Close()

		if newVersion == nil {
			output.WriteString("new_version_created=no\n")
			output.WriteString("version=0.0.0-none\n")
			output.WriteString("prerelease=no\n")
		} else {
			output.WriteString("new_version_created=yes\n")
			output.WriteString(fmt.Sprintf("version=%s", newVersion.String()))
			if newVersion.Prerelease() == "" {
				output.WriteString("prerelease=no\n")
			} else {
				output.WriteString("prerelease=yes\n")
			}
		}
	}
}

func isDryRun(input string) bool {
	return strings.EqualFold(input, "yes") || strings.EqualFold(input, "true")
}

// Create an HTTP client which communicates with the GitHub API using a token.
// This function follows the GitHub Action best practices by sourcing the GitHub
// API address from an environment variable. See:
// https://docs.github.com/en/actions/creating-actions/about-custom-actions#compatibility-with-github-enterprise-server
func ensureNewGitHubClient(token string) *github.Client {
	apiAddress := os.Getenv("GITHUB_API_URL")
	httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	if client, err := github.NewEnterpriseClient(apiAddress, apiAddress, httpClient); err == nil {
		return client
	}

	panic("Could not create GitHub client!")
}
