// # Copyright Meshery Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/layer5io/meshery/mesheryctl/pkg/utils"
	mutils "github.com/layer5io/meshkit/utils"
	"github.com/layer5io/meshkit/utils/store"
	comp "github.com/meshery/schemas/models/v1beta1/component"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

type ComponentSourceParser interface {
	parse() (map[string]map[string][]utils.ComponentCSV, error)
}

type LocalCSVDirParser struct {
	dirPath string
}

type GoogleSheetParser struct {
	spreadsheeetID   string
	spreadsheeetCred string
}

var (
	csvDir                   string
	modelLocation            string
	logFile                  *os.File
	errorLogFile             *os.File
	sheetGID                 int64
	totalAggregateComponents int
	logDirPath               = filepath.Join(mutils.GetHome(), ".meshery", "logs", "registry")
)

// This command is used for retreving the information of components based on the sheet. It updates the components with the actual values of the fetched for sheet.
// Look the utils.ComponentCSV to see the values fetched.
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the registry with latest data.",
	Long:  "Updates the component metadata (SVGs, shapes, styles and other) by referring from a Google Spreadsheet or CSV directory",
	Example: `
// Update models from Meshery Integration Spreadsheet
mesheryctl registry update --spreadsheet-id [id] --spreadsheet-cred [base64 encoded spreadsheet credential] -i [path to the directory containing models].

// Updating models in the meshery/meshery repo
mesheryctl registry update --spreadsheet-id 1DZHnzxYWOlJ69Oguz4LkRVTFM79kC2tuvdwizOJmeMw --spreadsheet-cred $CRED

// Updating models in the meshery/meshery repo based on flag
mesheryctl registry update --spreadsheet-id 1DZHnzxYWOlJ69Oguz4LkRVTFM79kC2tuvdwizOJmeMw --spreadsheet-cred $CRED --model "[model-name]"

// Updating models from a local CSV directory
mesheryctl registry update --csv-dir /path/to/csv-directory

// Updating models from local CSV takes precedence if both CSV directory and Google Sheet are provided
mesheryctl registry update --spreadsheet-id [id] --spreadsheet-cred [base64 encoded spreadsheet credential] --csv-dir [/path/to/csv-directory]
	`,
	PreRunE: func(cmd *cobra.Command, args []string) error {

		if csvDir == "" && (spreadsheeetID == "" || spreadsheeetCred == "") {
			return errors.New("please provide a CSV directory or both spreadsheet-id and spreadsheet-cred")
		}

		err := os.MkdirAll(logDirPath, 0755)
		if err != nil {
			return ErrUpdateRegistry(err, modelLocation)
		}
		utils.Log.SetLevel(logrus.DebugLevel)
		logFilePath := filepath.Join(logDirPath, "registry-update")
		logFile, err = os.Create(logFilePath)
		if err != nil {
			return ErrUpdateRegistry(err, modelLocation)
		}
		utils.Log.Debugf("Logger created and set debug log level at %s", logFilePath)
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := os.Stat(modelLocation)
		if err != nil {
			return fmt.Errorf("invalid --input path '%s': %w", modelLocation, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("--input path '%s' is not a directory", modelLocation)
		}

		utils.Log.Debugf("Input Directory check completed with path  %s", modelLocation)

		var parser ComponentSourceParser

		if csvDir != "" {
			utils.Log.Info("Using local CSV directory: ", csvDir)
			parser = &LocalCSVDirParser{dirPath: csvDir}
		} else {
			utils.Log.Info("Using Google Sheet with ID: ", spreadsheeetID)
			parser = &GoogleSheetParser{spreadsheeetID: spreadsheeetID, spreadsheeetCred: spreadsheeetCred}
		}

		parsedComponents, err := parser.parse()
		if err != nil {
			utils.Log.Error(err)
			return err
		}

		err = InvokeComponentsUpdate(parsedComponents)
		if err != nil {
			utils.Log.Error(err)
			return err
		}
		return nil
	},
}
var (
	ExcludeDirs = []string{"relationships", "policies"}
)

type compUpdateTracker struct {
	totalComps        int
	totalCompsUpdated int
	version           string
}

func updateRegistryComponents(
	components map[string]map[string][]utils.ComponentCSV,
	modelToCompUpdateTracker *store.GenerticThreadSafeStore[[]compUpdateTracker],
) error {
	var modelLocPath string
	if filepath.IsAbs(modelLocation) {
		// If user gave an absolute path, don't prepend pwd
		modelLocPath = modelLocation
	} else {
		// If user gave a relative path, join with pwd
		pwd, _ := os.Getwd()
		modelLocPath = filepath.Join(pwd, modelLocation)
	}
	utils.Log.Debugf("looking for models at %v", modelLocPath)

	// var wg sync.WaitGroup
	for registrant, model := range components {
		if registrant == "" {
			continue
		}

		// Iterate all models
		for modelNameFromCSV, comps := range model {
			modelPath := filepath.Join(modelLocPath, modelNameFromCSV)
			info, err := os.Stat(modelPath)
			if err != nil || !info.IsDir() {
				utils.Log.Warnf("No models found corresponding to %s", modelNameFromCSV)
				continue
			}
			utils.Log.Debugf("Found matching model from CSV at %s", modelPath)

			availableComponentsPerModelPerVersion := 0
			utils.Log.Debugf("Processing files inside model path %s", modelPath)
			utils.Log.Info("Starting to update components of model ", modelNameFromCSV)

			modelContents, err := os.ReadDir(modelPath)
			if err != nil {
				err = ErrUpdateModel(err, modelNameFromCSV)
				utils.Log.Error(err)
				continue
			}

			// Iterate over all content inside model
			// Comps, relationships, policies
			compUpdateArray := []compUpdateTracker{}
			for _, content := range modelContents {
				totalCompsUpdatedPerModelPerVersion := 0
				if !content.IsDir() {
					continue
				}
				if utils.Contains(content.Name(), ExcludeDirs) != -1 {
					continue
				}

				versionPath := filepath.Join(modelPath, content.Name(), "v1.0.0") // remove the hard coded definition version, add just for testing
				utils.Log.Infof("Looking for components in %s", versionPath)

				entries, _ := os.ReadDir(versionPath)
				availableComponentsPerModelPerVersion += len(entries)

				utils.Log.Info("Updating component of model ", modelNameFromCSV, " with version: ", content.Name())
				for _, c := range comps {
					compPath := filepath.Join(versionPath, "components", fmt.Sprintf("%s.json", c.Component))
					componentByte, err := os.ReadFile(compPath)
					if err != nil {
						utils.Log.Error(ErrUpdateComponent(err, modelNameFromCSV, c.Component))
						continue
					}
					componentDef := comp.ComponentDefinition{}
					err = json.Unmarshal(componentByte, &componentDef)
					if err != nil {
						utils.Log.Error(ErrUpdateComponent(err, modelNameFromCSV, c.Component))
						continue
					}

					err = c.UpdateCompDefinition(&componentDef)
					if err != nil {
						utils.Log.Error(ErrUpdateComponent(err, modelNameFromCSV, c.Component))
						continue
					}
					tmpFilePath := filepath.Join(versionPath, "components", "tmp_model.json")

					// Ensure the temporary file is removed regardless of what happens
					defer func() {
						_ = os.Remove(tmpFilePath)
					}()

					if _, err := os.Stat(compPath); err == nil {
						existingData, err := os.ReadFile(compPath)
						if err != nil {
							utils.Log.Error(err)
							continue
						}

						err = mutils.WriteJSONToFile[comp.ComponentDefinition](tmpFilePath, componentDef)
						if err != nil {
							utils.Log.Error(err)
							continue
						}

						newData, err := os.ReadFile(tmpFilePath)
						if err != nil {
							utils.Log.Error(err)
							continue
						}

						if bytes.Equal(existingData, newData) {
							utils.Log.Info("No changes detected for ", componentDef.Component.Kind)
							continue
						} else {
							err = mutils.WriteJSONToFile[comp.ComponentDefinition](compPath, componentDef)
							if err != nil {
								utils.Log.Error(err)
								continue
							}
							totalCompsUpdatedPerModelPerVersion++

						}
					}
				}

				compUpdateArray = append(compUpdateArray, compUpdateTracker{
					totalComps:        availableComponentsPerModelPerVersion,
					totalCompsUpdated: totalCompsUpdatedPerModelPerVersion,
					version:           content.Name(),
				})

			}
			modelToCompUpdateTracker.Set(modelNameFromCSV, compUpdateArray)
			utils.Log.Info("\n")
		}

	}
	return nil
}

func logModelUpdateSummary(modelToCompUpdateTracker *store.GenerticThreadSafeStore[[]compUpdateTracker]) {
	values := modelToCompUpdateTracker.GetAllPairs()
	for key, val := range values {
		for _, value := range val {
			utils.Log.Info(fmt.Sprintf("For model %s-%s, updated %d out of %d components.", key, value.version, value.totalCompsUpdated, value.totalComps))
			totalAggregateComponents += value.totalCompsUpdated
		}
	}

	utils.Log.Info(fmt.Sprintf("For %d models updated %d components", len(values), totalAggregateComponents))
}

func (parser *LocalCSVDirParser) parse() (map[string]map[string][]utils.ComponentCSV, error) {
	localComps := make(map[string]map[string][]utils.ComponentCSV)
	var allErrors []error

	entries, err := os.ReadDir(parser.dirPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) == ".csv" {
			csvPath := filepath.Join(parser.dirPath, entry.Name())
			helper, err := utils.NewComponentCSVHelper("", "Components", 0, csvPath)
			if err != nil {
				allErrors = append(allErrors, err)
				continue
			}
			parseErr := helper.ParseComponentsSheet(modelName)
			utils.Log.Info("Parsed rows: ", len(helper.Components), " registrants from CSV path=", csvPath)

			if parseErr != nil {
				allErrors = append(allErrors, parseErr)
				continue
			}
			// Merge
			for registrant, modelMap := range helper.Components {
				utils.Log.Info("  Registrant: ", registrant, ", models = ", len(modelMap))
				if _, ok := localComps[registrant]; !ok {
					localComps[registrant] = make(map[string][]utils.ComponentCSV)
				}
				for model, comps := range modelMap {
					utils.Log.Info("    model ", model, " => # of comps: ", len(comps))
					localComps[registrant][model] = append(localComps[registrant][model], comps...)
				}
			}
		}
	}
	if len(allErrors) > 0 {
		return nil, errors.Join(allErrors...)
	}
	return localComps, nil
}

func (parser *GoogleSheetParser) parse() (map[string]map[string][]utils.ComponentCSV, error) {
	srv, err := mutils.NewSheetSRV(parser.spreadsheeetCred)
	if err != nil {
		return nil, err
	}

	resp, err := srv.Spreadsheets.Get(parser.spreadsheeetID).Fields().Do()
	if err != nil || resp.HTTPStatusCode != 200 {
		return nil, fmt.Errorf("failed to get google sheet: %w", err)
	}

	gid := GetSheetIDFromTitle(resp, "Components")
	url := GoogleSpreadSheetURL + parser.spreadsheeetID
	helper, err := utils.NewComponentCSVHelper(url, "Components", gid, componentCSVFilePath)
	if err != nil {
		return nil, err
	}

	err = helper.ParseComponentsSheet(modelName)
	if err != nil {
		return nil, ErrUpdateRegistry(err, modelLocation)
	}

	utils.Log.Info("Total Registrants: ", len(helper.Components))
	return helper.Components, nil
}

func InvokeComponentsUpdate(comps map[string]map[string][]utils.ComponentCSV) error {
	utils.Log.UpdateLogOutput(logFile)
	defer func() {
		_ = logFile.Close()
		utils.Log.UpdateLogOutput(os.Stdout)
		utils.Log.Info(fmt.Sprintf("Updated %d models and %d components", totalAggregateModel, totalAggregateComponents))
		utils.Log.Info("refer ", logDirPath, " for detailed registry update logs")
		totalAggregateModel = 0
		totalAggregateComponents = 0
	}()

	modelToCompUpdateTracker := store.NewGenericThreadSafeStore[[]compUpdateTracker]()

	err := updateRegistryComponents(comps, modelToCompUpdateTracker)
	if err != nil {
		return err
	}

	logModelUpdateSummary(modelToCompUpdateTracker)
	return nil
}

func init() {
	updateCmd.PersistentFlags().StringVarP(&modelLocation, "input", "i", "../server/meshmodel", "relative or absolute input path to the models directory")
	_ = updateCmd.MarkPersistentFlagRequired("path")

	updateCmd.PersistentFlags().StringVar(&spreadsheeetID, "spreadsheet-id", "", "spreadsheet it for the integration spreadsheet")
	updateCmd.PersistentFlags().StringVar(&spreadsheeetCred, "spreadsheet-cred", "", "base64 encoded credential to download the spreadsheet")
	updateCmd.PersistentFlags().StringVarP(&modelName, "model", "m", "", "specific model name to be generated")
	updateCmd.PersistentFlags().StringVar(&csvDir, "csv-dir", "", "Path to directory containing local CSV files for model and component updates")

	updateCmd.MarkFlagsRequiredTogether("spreadsheet-id", "spreadsheet-cred")

}
