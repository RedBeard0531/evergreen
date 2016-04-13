package model

import (
	"fmt"
	"github.com/10gen-labs/slogger/v1"
	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/command"
	"github.com/evergreen-ci/evergreen/model/build"
	"github.com/evergreen-ci/evergreen/model/patch"
	"github.com/evergreen-ci/evergreen/model/version"
	"github.com/evergreen-ci/evergreen/thirdparty"
	"github.com/evergreen-ci/evergreen/util"
	"gopkg.in/mgo.v2/bson"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Given the set of variants and tasks (from both the old and new request formats)
// build a universal set of pairs that can be used to expand the dependency tree.
func VariantTasksToTVPairs(in []patch.VariantTasks) []TVPair {
	out := []TVPair{}
	for _, vt := range in {
		for _, t := range vt.Tasks {
			out = append(out, TVPair{vt.Variant, t})
		}
	}
	return out
}

func TVPairsToVariantTasks(in []TVPair) []patch.VariantTasks {
	vtMap := map[string]patch.VariantTasks{}
	for _, pair := range in {
		vt := vtMap[pair.Variant]
		vt.Variant = pair.Variant
		vt.Tasks = append(vt.Tasks, pair.TaskName)
		vtMap[pair.Variant] = vt
	}
	vts := make([]patch.VariantTasks, len(vtMap))
	for _, vt := range vtMap {
		vts = append(vts, vt)
	}
	return vts
}

// UpdatePatch creates the full set of tasks and variants defined in newPairs
func UpdatePatch(p *patch.Patch, newPairs []TVPair) error {
	oldPairs := p.
	return nil
}

// Given a patch version and a list of task names, creates a new task with
// the given name for each variant, if applicable.
func AddNewTasksForPatch(p *patch.Patch, patchVersion *version.Version, project *Project,
	taskNames []string) error {
	// create new tasks for all of the added patch tasks
	var newTasks []string
	for _, taskName := range taskNames {
		if !util.SliceContains(p.Tasks, taskName) {
			newTasks = append(newTasks, taskName)
		}
	}

	// add tasks to the patch in the db
	if err := p.AddTasks(taskNames); err != nil {
		return err
	}

	// add new tasks to the build, if they exist
	if len(newTasks) > 0 {
		builds, err := build.Find(build.ByIds(patchVersion.BuildIds))
		if err != nil {
			return err
		}

		for _, b := range builds {
			if _, err = AddTasksToBuild(&b, project, patchVersion, newTasks); err != nil {
				return err
			}
		}
	}
	return nil
}

/*
func UpdatePatchConfig(p *patch.Patch, pairs []TVPair) error {

}
*/

// Given the patch version and a list of build variants, creates new builds
// with the patch's tasks.
func AddNewBuildsForPatch(p *patch.Patch, patchVersion *version.Version, project *Project,
	buildVariants []string) (*version.Version, error) {

	// compute a list of the newly added build variants
	var newVariants []string
	for _, variant := range buildVariants {
		if !util.SliceContains(p.BuildVariants, variant) {
			newVariants = append(newVariants, variant)
		}
	}

	// update the patch
	if err := p.AddBuildVariants(buildVariants); err != nil {
		return nil, err
	}

	newBuildIds := make([]string, 0)
	newBuildStatuses := make([]version.BuildStatus, 0)
	tt := NewPatchTaskIdTable(project, patchVersion, p)
	for _, buildVariant := range newVariants {
		evergreen.Logger.Logf(slogger.INFO,
			"Creating build for version %v, buildVariant %v, activated = %v",
			patchVersion.Id, buildVariant, p.Activated)
		buildId, err := CreateBuildFromVersion(project, patchVersion, tt, buildVariant, p.Activated, p.Tasks)
		if err != nil {
			return nil, err
		}
		newBuildIds = append(newBuildIds, buildId)

		newBuildStatuses = append(newBuildStatuses,
			version.BuildStatus{
				BuildVariant: buildVariant,
				BuildId:      buildId,
				Activated:    p.Activated,
			},
		)
		patchVersion.BuildIds = append(patchVersion.BuildIds, buildId)
	}

	err := version.UpdateOne(
		bson.M{version.IdKey: patchVersion.Id},
		bson.M{
			"$push": bson.M{
				version.BuildIdsKey:      bson.M{"$each": newBuildIds},
				version.BuildVariantsKey: bson.M{"$each": newBuildStatuses},
			},
		},
	)
	if err != nil {
		return nil, err
	}

	return patchVersion, nil
}

// IncludePatchDependencies takes a project and a slice of variant/task pairs names
// and returns the expanded set of variant/task pairs to include all the dependencies/requirements
// for the given set of tasks.
// If any dependency is cross-variant, it will include the variant and task for that dependency.
func IncludePatchDependencies(project *Project, tvpairs []TVPair) []TVPair {
	di := &dependencyIncluder{Project: project}
	return di.Include(tvpairs)
}

// MakePatchedConfig takes in the path to a remote configuration a stringified version
// of the current project and returns an unmarshalled version of the project
// with the patch applied
func MakePatchedConfig(p *patch.Patch, remoteConfigPath, projectConfig string) (
	*Project, error) {
	for _, patchPart := range p.Patches {
		// we only need to patch the main project and not any other modules
		if patchPart.ModuleName != "" {
			continue
		}
		// write patch file
		patchFilePath, err := util.WriteToTempFile(patchPart.PatchSet.Patch)
		if err != nil {
			return nil, fmt.Errorf("could not write patch file: %v", err)
		}
		defer os.Remove(patchFilePath)
		// write project configuration
		configFilePath, err := util.WriteToTempFile(projectConfig)
		if err != nil {
			return nil, fmt.Errorf("could not write config file: %v", err)
		}
		defer os.Remove(configFilePath)

		// clean the working directory
		workingDirectory := filepath.Dir(patchFilePath)
		localConfigPath := filepath.Join(
			workingDirectory,
			remoteConfigPath,
		)
		parentDir := strings.Split(
			remoteConfigPath,
			string(os.PathSeparator),
		)[0]
		err = os.RemoveAll(filepath.Join(workingDirectory, parentDir))
		if err != nil {
			return nil, err
		}
		if err = os.MkdirAll(filepath.Dir(localConfigPath), 0755); err != nil {
			return nil, err
		}
		// rename the temporary config file name to the remote config
		// file path if we are patching an existing remote config
		if len(projectConfig) > 0 {
			if err = os.Rename(configFilePath, localConfigPath); err != nil {
				return nil, fmt.Errorf("could not rename file '%v' to '%v': %v",
					configFilePath, localConfigPath, err)
			}
			defer os.Remove(localConfigPath)
		}

		// selectively apply the patch to the config file
		patchCommandStrings := []string{
			fmt.Sprintf("set -o verbose"),
			fmt.Sprintf("set -o errexit"),
			fmt.Sprintf("git apply --whitespace=fix --include=%v < '%v'",
				remoteConfigPath, patchFilePath),
		}

		patchCmd := &command.LocalCommand{
			CmdString:        strings.Join(patchCommandStrings, "\n"),
			WorkingDirectory: workingDirectory,
			Stdout:           evergreen.NewInfoLoggingWriter(&evergreen.Logger),
			Stderr:           evergreen.NewErrorLoggingWriter(&evergreen.Logger),
			ScriptMode:       true,
		}

		if err = patchCmd.Run(); err != nil {
			return nil, fmt.Errorf("could not run patch command: %v", err)
		}
		// read in the patched config file
		data, err := ioutil.ReadFile(localConfigPath)
		if err != nil {
			return nil, fmt.Errorf("could not read patched config file: %v",
				err)
		}
		project := &Project{}
		if err = LoadProjectInto(data, p.Project, project); err != nil {
			return nil, err
		}
		return project, nil
	}
	return nil, fmt.Errorf("no patch on project")
}

// Finalizes a patch:
// Patches a remote project's configuration file if needed.
// Creates a version for this patch and links it.
// Creates builds based on the version.
func FinalizePatch(p *patch.Patch, settings *evergreen.Settings) (*version.Version, error) {
	// unmarshal the project YAML for storage
	project := &Project{}
	err := yaml.Unmarshal([]byte(p.PatchedConfig), project)
	if err != nil {
		return nil, fmt.Errorf(
			"Error marshalling patched project config from repository revision “%v”: %v",
			p.Githash, err)
	}

	projectRef, err := FindOneProjectRef(p.Project)
	if err != nil {
		return nil, err
	}

	gitCommit, err := thirdparty.GetCommitEvent(
		settings.Credentials["github"],
		projectRef.Owner, projectRef.Repo, p.Githash,
	)
	if err != nil {
		return nil, fmt.Errorf("Couldn't fetch commit information: %v", err)
	}
	if gitCommit == nil {
		return nil, fmt.Errorf("Couldn't fetch commit information: git commit doesn't exist?")
	}

	patchVersion := &version.Version{
		Id:            fmt.Sprintf("%v_%v", p.Id.Hex(), 0),
		CreateTime:    time.Now(),
		Identifier:    p.Project,
		Revision:      p.Githash,
		Author:        gitCommit.Commit.Committer.Name,
		AuthorEmail:   gitCommit.Commit.Committer.Email,
		Message:       gitCommit.Commit.Message,
		BuildIds:      []string{},
		BuildVariants: []version.BuildStatus{},
		Config:        string(p.PatchedConfig),
		Status:        evergreen.PatchCreated,
		Requester:     evergreen.PatchVersionRequester,
	}

	tt := NewPatchTaskIdTable(project, patchVersion, p)
	for _, vt := range p.VariantsTasks {
		buildId, err := CreateBuildFromVersion(project, patchVersion, tt, vt.Variant, true, vt.Tasks)
		if err != nil {
			return nil, err
		}
		patchVersion.BuildIds = append(patchVersion.BuildIds, buildId)
		patchVersion.BuildVariants = append(patchVersion.BuildVariants,
			version.BuildStatus{
				BuildVariant: vt.Variant,
				Activated:    true,
				BuildId:      buildId,
			},
		)
	}

	if err = patchVersion.Insert(); err != nil {
		return nil, err
	}
	if err = p.SetActivated(patchVersion.Id); err != nil {
		return nil, err
	}
	return patchVersion, nil
}

func CancelPatch(p *patch.Patch, caller string) error {
	if p.Version != "" {
		if err := SetVersionActivation(p.Version, false, caller); err != nil {
			return err
		}
		return AbortVersion(p.Version)
	} else {
		return patch.Remove(patch.ById(p.Id))
	}
}
