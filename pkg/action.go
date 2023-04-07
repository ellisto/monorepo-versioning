package pkg

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver"
	"github.com/google/go-github/v50/github"
	"github.com/leodido/go-conventionalcommits"
	"github.com/leodido/go-conventionalcommits/parser"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// VersioningAction contains logic to generate a new version
type VersioningAction struct {
	client         *github.Client
	owner          string
	repository     string
	component      string
	label          string
	branch         string
	revision       string
	initialVersion string
	defaultBranch  string
	parser         conventionalcommits.Machine
}

// NewAction creates a new instance of the GitHub action for a given repository specified in the format
// "owner/repository"
func NewAction(ownerAndRepository string, component string, label string, branch string, revision string, initialVersion string, defaultBranch string, client *github.Client) VersioningAction {
	nameParts := strings.Split(ownerAndRepository, "/")
	owner := nameParts[0]
	repository := nameParts[1]

	return VersioningAction{
		client:         client,
		owner:          owner,
		repository:     repository,
		branch:         branch,
		component:      component,
		label:          label,
		revision:       revision,
		initialVersion: initialVersion,
		defaultBranch:  defaultBranch,
		parser:         parser.NewMachine(conventionalcommits.WithTypes(conventionalcommits.TypesConventional)),
	}
}

// GenerateVersion will generate the next version for a component based on the commits since the previous
// version. If dryRun is true, then the version will not be created on GitHub. The next version number is
// picked based on the Conventional Commits specification. Only commits with a scope matching the component
// name will be considered.
func (a VersioningAction) GenerateVersion(dryRun bool) *semver.Version {
	existingReleases := filterAndSortReleasesForComponent(a.component, a.getAllReleases())
	existingVersion, firstVersionCreated := existingVersionOrNew(a.component, existingReleases, a.initialVersion)

	previousChangeTime := a.getPreviousChangeTime(existingReleases)
	currentChangeTime := a.getCurrentChangeTime()
	// Add 1 millisecond to the current change time so that the current commit is included in the
	// changelog (as when we list commits until a given time, the "until" parameter is exclusive)
	newCommits := a.getNewCommits(previousChangeTime, currentChangeTime.Add(time.Millisecond), a.branch)
	componentConventionalCommits := convertAndFilterCommitsForComponent(a.component, newCommits)

	newVersion := a.newVersion(existingVersion, componentConventionalCommits, firstVersionCreated)
	if newVersion == nil {
		// No new version, nothing else to do
		return nil
	}

	if dryRun {
		// Dry run, don't publish version on GitHub
		return newVersion
	}

	a.createGitHubRelease(newVersion, newCommits)
	return newVersion
}

// createGitHubRelease based on the current revision and generated version
func (a VersioningAction) createGitHubRelease(newVersion *semver.Version, commits []*github.RepositoryCommit) {
	versionName := strings.ToLower(prefixWithComponent(a.component, newVersion.String()))
	var releaseTitle string
	// Prefer a human-readable label if one provided, otherwise use the component name
	if a.label != "" {
		releaseTitle = fmt.Sprintf("%s: %s", cases.Title(language.English).String(a.label), newVersion.String())
	} else {
		releaseTitle = fmt.Sprintf("%s: %s", cases.Title(language.English).String(a.component), newVersion.String())
	}
	isPrerelease := a.branch != a.defaultBranch
	// We can't use auto-generated release notes, as we need to manually filter for changes specific to the
	// given component.
	useGitHubGeneratedReleaseNotes := false
	releaseNotes := a.generateReleaseNotes(commits)

	fmt.Printf("Creating GitHub tag: %s\n", versionName)
	_, _, err := a.client.Repositories.CreateRelease(context.Background(), a.owner, a.repository, &github.RepositoryRelease{
		TagName:              &versionName,
		Name:                 &releaseTitle,
		TargetCommitish:      &a.revision,
		GenerateReleaseNotes: &useGitHubGeneratedReleaseNotes,
		Body:                 &releaseNotes,
		Prerelease:           &isPrerelease,
	})

	if err != nil {
		panic(err)
	}
}

// getAllReleases for the given repository
func (a VersioningAction) getAllReleases() (existingReleases []*github.RepositoryRelease) {
	allReleasesListed := false
	page := 1

	for !allReleasesListed {
		releases, _, err := a.client.Repositories.ListReleases(context.Background(), a.owner, a.repository, &github.ListOptions{
			PerPage: 100,
			Page:    page,
		})

		if err != nil {
			panic(err)
		}

		existingReleases = append(existingReleases, releases...)
		allReleasesListed = len(releases) == 0
		page++
	}

	return existingReleases
}

// getNewCommits since a given commit-like reference. If sinceComitish is empty, gets all commits
func (a VersioningAction) getNewCommits(since *time.Time, until time.Time, branch string) (existingCommits []*github.RepositoryCommit) {
	if since == nil {
		startOfEpoch := time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
		since = &startOfEpoch
	} else {
		// Add a second from the last change time so there's no overlap
		exclusiveSince := since.Add(time.Second)
		since = &exclusiveSince
	}

	fmt.Printf("Looking for commits from %s, until %s\n", since.String(), until.String())

	page := 1
	allCommitsListed := false
	for !allCommitsListed {
		commits, _, err := a.client.Repositories.ListCommits(context.Background(), a.owner, a.repository, &github.CommitsListOptions{
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: 100,
			},
			Since: *since,
			Until: until,
			SHA:   branch,
		})

		if err != nil {
			panic(err)
		}

		existingCommits = append(existingCommits, commits...)
		allCommitsListed = len(commits) == 0
		page++
	}

	return existingCommits
}

func (a VersioningAction) getCurrentChangeTime() time.Time {
	commit, _, err := a.client.Git.GetCommit(context.Background(), a.owner, a.repository, a.revision)
	if err != nil {
		panic(err)
	}

	return commit.GetCommitter().Date.Time

}

func (a VersioningAction) getPreviousChangeTime(existingReleases []*github.RepositoryRelease) *time.Time {
	if len(existingReleases) == 0 {
		return nil
	}

	// Releases are ordered descending by publish date
	latestRelease := existingReleases[0]
	fmt.Printf("Using %s as latest release for change time comparison...\n", latestRelease.GetName())
	targetRevision := latestRelease.GetTargetCommitish()

	commit, _, err := a.client.Git.GetCommit(context.Background(), a.owner, a.repository, targetRevision)
	if err != nil {
		panic(err)
	}

	commitTime := commit.GetCommitter().Date.Time
	return &commitTime
}

// newVersion based on the current version and commits since this version
func (a VersioningAction) newVersion(currentVersion *semver.Version, newCommits []*conventionalcommits.ConventionalCommit, firstVersionCreated bool) *semver.Version {
	// If the version was just created (ie: it's 1.0.0 and was generated because no existing version is present)
	// then just return the created version. Otherwise we'll immediately bump to 1.0.1/1.1.0/2.0.0 based on
	// any commits in the repository.
	if firstVersionCreated {
		// We aren't generating a version on the default branch, so this
		// should be a prerelease version
		if a.branch != a.defaultBranch {
			fmt.Printf("Current branch (%s) is not the default branch (%s), this version will be a pre-release\n", a.branch, a.defaultBranch)
			prereleaseVersion, err := currentVersion.SetPrerelease(a.revision[:7])
			if err != nil {
				panic(err)
			}

			currentVersion = &prereleaseVersion
		}

		fmt.Printf("No existing version found for component, will generate %s\n", currentVersion.String())

		return currentVersion
	}

	// Major version bump
	breakingChangesFound := false
	// Minor version bump
	featureChangesFound := false
	// Patch version bump
	fixChangesFound := false
	// Any other commit types are currently ignored and will not generate a new version

	for _, commit := range newCommits {
		if commit.IsBreakingChange() {
			breakingChangesFound = true
			// Breaking changes always mean a major version bump so we can bail out here
			// without examining any other commits
			break
		}

		if commit.IsFeat() {
			featureChangesFound = true
		}

		if commit.IsFix() {
			fixChangesFound = true
		}
	}

	var nextVersion semver.Version
	if breakingChangesFound {
		nextVersion = currentVersion.IncMajor()
	} else if featureChangesFound {
		nextVersion = currentVersion.IncMinor()
	} else if fixChangesFound {
		nextVersion = currentVersion.IncPatch()
	}

	// No changes, so no new version
	if nextVersion == (semver.Version{}) {
		return nil
	}

	// We aren't generating a version on the default branch, so this
	// should be a prerelease version
	if a.branch != a.defaultBranch {
		fmt.Printf("Current branch (%s) is not the default branch (%s), this version will be a pre-release\n", a.branch, a.defaultBranch)

		var err error
		nextVersion, err = nextVersion.SetPrerelease(a.revision[:7])
		if err != nil {
			panic(err)
		}
	}

	// No relevant changes found, don't generate a new version number
	return &nextVersion
}

// generateReleaseNotes based on the commits since the last version
func (a VersioningAction) generateReleaseNotes(commits []*github.RepositoryCommit) string {
	releaseNotesTemplate := `
> Below is the changelog for this version. Changes are categorised by the type of change (breaking change, new feature, or bugfix). If there isn't a heading for a type of change, there were no relevant changes.
{breaking}
{features}
{fixes}
{contributors}
`

	breakingChangesStr := strings.Builder{}
	breakingChangesStr.WriteString("### :hammer: Breaking Changes\n")
	breakingChangesStr.WriteString("_Breaking changes indicate that an existing behaviour or feature no longer works as before. Pay close attention to any listed breaking changes, and make sure they are acknowledged or mitigated before deploying this version._\n")
	breakingChangesInitialLength := breakingChangesStr.Len()

	featuresStr := strings.Builder{}
	featuresStr.WriteString("### :bulb: Features\n")
	featuresStr.WriteString("_Feature changes contain some new functionality. Existing behaviour should not be affected._\n")
	featuresInitialLength := featuresStr.Len()

	fixesStr := strings.Builder{}
	fixesStr.WriteString("### :construction_worker: Fixes\n")
	fixesStr.WriteString("_Fixes some unintended behaviour from a previous version. You should familiarise yourself with these changes to understand any problems you may have experienced in previous versions._\n")

	fixesInitialLength := fixesStr.Len()

	contributorsStr := strings.Builder{}
	contributorsStr.WriteString("### :heart_eyes: Contributors\n")
	contributorsStr.WriteString("_These people contributed to this version of the component - thank you! Note: GitHub's auto-generated contributor list may also include contributors to other components._\n")
	contributorsInitialLength := contributorsStr.Len()
	contributors := make(map[string]bool)

	for _, commit := range commits {
		parsedMessage, err := a.parser.Parse([]byte(commit.GetCommit().GetMessage()))
		if err != nil {
			continue
		}

		conventionalCommit, ok := parsedMessage.(*conventionalcommits.ConventionalCommit)
		if !ok {
			continue
		}

		if conventionalCommit.Scope == nil {
			continue
		}

		if conventionalCommit.Scope != nil && !strings.EqualFold(*conventionalCommit.Scope, a.component) {
			continue
		}

		if conventionalCommit.IsBreakingChange() {
			breakingChangesStr.WriteString(formatCommitChangelogEntry(commit, conventionalCommit))
		}

		if conventionalCommit.IsFeat() {
			featuresStr.WriteString(formatCommitChangelogEntry(commit, conventionalCommit))
		}

		if conventionalCommit.IsFix() {
			fixesStr.WriteString(formatCommitChangelogEntry(commit, conventionalCommit))
		}

		if _, ok := contributors[commit.GetAuthor().GetLogin()]; !ok {
			contributors[commit.GetAuthor().GetLogin()] = true
			contributorsStr.WriteString(fmt.Sprintf("* @%s\n", commit.GetAuthor().GetLogin()))
		}
	}

	if breakingChangesStr.Len() == breakingChangesInitialLength {
		releaseNotesTemplate = strings.Replace(releaseNotesTemplate, "{breaking}", "", 1)
	} else {
		releaseNotesTemplate = strings.Replace(releaseNotesTemplate, "{breaking}", breakingChangesStr.String(), 1)
	}

	if featuresStr.Len() == featuresInitialLength {
		releaseNotesTemplate = strings.Replace(releaseNotesTemplate, "{features}", "", 1)
	} else {
		releaseNotesTemplate = strings.Replace(releaseNotesTemplate, "{features}", featuresStr.String(), 1)
	}

	if fixesStr.Len() == fixesInitialLength {
		releaseNotesTemplate = strings.Replace(releaseNotesTemplate, "{fixes}", "", 1)
	} else {
		releaseNotesTemplate = strings.Replace(releaseNotesTemplate, "{fixes}", fixesStr.String(), 1)
	}

	if contributorsStr.Len() == contributorsInitialLength {
		releaseNotesTemplate = strings.Replace(releaseNotesTemplate, "{contributors}", "", 1)
	} else {
		releaseNotesTemplate = strings.Replace(releaseNotesTemplate, "{contributors}", contributorsStr.String(), 1)
	}

	return releaseNotesTemplate
}

// formatCommitChangelogEntry formats a given commit as a changelog entry
func formatCommitChangelogEntry(commit *github.RepositoryCommit, conventionalCommit *conventionalcommits.ConventionalCommit) string {
	if commit.GetSHA() != "" {
		// Shorten SHA to 7 characters to match how GitHub usually displays it
		return fmt.Sprintf("* [`%s`](%s) %s (@%s)\n", commit.GetSHA()[:7], commit.GetHTMLURL(), conventionalCommit.Description, commit.GetAuthor().GetLogin())
	} else {
		return fmt.Sprintf("* [%s](%s) (@%s)\n", commit.GetHTMLURL(), conventionalCommit.Description, commit.GetAuthor().GetLogin())
	}
}

// existingVersionOrNew gets the existing version for a component, or generates a version 1.0.0.
func existingVersionOrNew(component string, existingReleases []*github.RepositoryRelease, initialVersion string) (version *semver.Version, firstVersion bool) {
	if len(existingReleases) == 0 {
		fmt.Println("No existing releases for component, will use initial version")
		return semver.MustParse(initialVersion), true
	}

	latestRelease := existingReleases[0] // existingReleases is sorted in descending order of publish date
	fmt.Printf("Using %s as latest release for version comparison...\n", latestRelease.GetName())
	// Releases are named "ComponentName-SemanticVersion", strip the prefix to just get the latest version
	latestReleaseVersion := strings.TrimPrefix(latestRelease.GetTagName(), getComponentPrefix(component))
	return semver.MustParse(latestReleaseVersion), false
}

// convertAndFilterCommitsForComponent, parsing the conventional commit message, and then filtering for commits
// scoped to the provided component. If a commit does not match the Conventional Commits specification, it is
// ignored.
func convertAndFilterCommitsForComponent(component string, commits []*github.RepositoryCommit) []*conventionalcommits.ConventionalCommit {
	var matchingCommits []*conventionalcommits.ConventionalCommit
	for _, commit := range commits {
		// Parse conventional commit message
		// m := conventionalcommits.WithTypes(conventionalcommits.TypesConventional)
		parser := parser.NewMachine(conventionalcommits.WithTypes(conventionalcommits.TypesConventional))
		parsedMessage, err := parser.Parse([]byte(commit.GetCommit().GetMessage()))

		if err != nil {
			continue
		}

		conventionalCommit, ok := parsedMessage.(*conventionalcommits.ConventionalCommit)
		if !ok {
			continue
		}

		if conventionalCommit.Scope == nil {
			continue
		}

		if strings.EqualFold(*conventionalCommit.Scope, component) {
			matchingCommits = append(matchingCommits, conventionalCommit)
		}
	}

	return matchingCommits
}

// Filter all the repository releases to only the releases for the provided component, and then
// sort them by release publish date.
func filterAndSortReleasesForComponent(component string, releases []*github.RepositoryRelease) []*github.RepositoryRelease {
	var matchingReleases []*github.RepositoryRelease
	for _, release := range releases {
		if strings.HasPrefix(strings.ToLower(release.GetTagName()), getComponentPrefix(component)) {
			matchingReleases = append(matchingReleases, release)
		}
	}

	sort.Slice(matchingReleases, func(i, j int) bool {
		// Use > instead of < in the less function so that we end up sorting in descending (greatest first)
		// order, instead of ascending (smallest first) order
		return matchingReleases[i].PublishedAt.Unix() > matchingReleases[j].PublishedAt.Unix()
	})

	return matchingReleases
}

// prefixWithComponent defines the logic to convert a component name into a tag prefix
func prefixWithComponent(component string, str string) string {
	return fmt.Sprintf("%s%s", getComponentPrefix(component), str)
}

func getComponentPrefix(component string) string {
	// Append a hyphen to the component name
	return fmt.Sprintf("%s-", strings.ToLower(component))
}
