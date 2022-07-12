// Copyright 2020. Akamai Technologies, Inc
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

package dns

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	dns "github.com/akamai/AkamaiOPEN-edgegrid-golang/v2/pkg/configdns"
	"github.com/akamai/cli-terraform/pkg/edgegrid"
	"github.com/akamai/cli-terraform/pkg/tools"
	"github.com/akamai/cli/pkg/terminal"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
)

// Types contains list of Name Types to organize types by name
type Types []string

// Import List Struct
type zoneImportListStruct struct {
	Zone       string
	Recordsets map[string]Types // zone recordsets grouped by name
}

// tfWorkPath is a target directory for generated terraform resources
var tfWorkPath = "./"
var createImportList = false
var createConfig = false

var recordNames []string
var importScript = false

type fetchConfigStruct struct {
	ConfigOnly bool
	ModSegment bool
	NamesOnly  bool
}

var fetchConfig = fetchConfigStruct{ConfigOnly: false, ModSegment: false, NamesOnly: false}

var zoneName string
var contractid string

var fullZoneImportList *zoneImportListStruct
var fullZoneConfigMap map[string]Types

// work defs
var moduleFolder = "modules"
var modulePath = ""

// text for root module construction
var zoneTFfileHandle *os.File
var zonetfConfig = ""

// CmdCreateZone is an entrypoint to create-zone command
func CmdCreateZone(c *cli.Context) error {
	ctx := c.Context
	log.SetOutput(ioutil.Discard)
	if c.NArg() != 1 {
		cli.ShowCommandHelp(c, c.Command.Name)
		return cli.Exit(color.RedString("zone is required"), 1)
	}

	sess := edgegrid.GetSession(ctx)
	configdns := dns.Client(sess)

	// uppercase characters cause issues with TF and the generated config
	zoneName = strings.ToLower(c.Args().Get(0))

	if c.IsSet("tfworkpath") {
		tfWorkPath = c.String("tfworkpath")
	}
	tfWorkPath = filepath.FromSlash(tfWorkPath)
	if c.IsSet("resources") {
		createImportList = true
	}
	if c.IsSet("createconfig") {
		createConfig = true
	}
	if c.IsSet("configonly") {
		fetchConfig.ConfigOnly = true
	}
	if c.IsSet("namesonly") {
		fetchConfig.NamesOnly = true
	}
	if c.IsSet("recordname") {
		recordNames = c.StringSlice("recordname")
	}
	if c.IsSet("segmentconfig") {
		fetchConfig.ModSegment = true
	}
	if c.IsSet("importscript") {
		importScript = true
	}

	term := terminal.Get(ctx)
	fmt.Println("Configuring Zone")
	zoneObject, err := configdns.GetZone(ctx, zoneName)
	if err != nil {
		term.Spinner().Fail()
		fmt.Println("Error: " + err.Error())
		return cli.Exit(color.RedString("Zone retrieval failed"), 1)
	}
	contractid = zoneObject.ContractID // grab for use later
	// normalize zone name for zone resource name
	resourceZoneName := normalizeResourceName(zoneName)
	if createImportList {

		term.Spinner().Start("Inventorying zone and recordsets ")
		recordsets := make(map[string]Types)
		// Retrieve all zone names
		if len(recordNames) == 0 {
			recordsetNames, err := configdns.GetZoneNames(ctx, zoneName)
			if err != nil {
				term.Spinner().Fail()
				fmt.Println("Error: " + err.Error())
				return cli.Exit(color.RedString("Zone Name retrieval failed"), 1)
			}
			recordNames = recordsetNames.Names
		}
		for _, zname := range recordNames {
			if fetchConfig.NamesOnly {
				recordsets[zname] = make([]string, 0, 0)
			} else {
				nameTypesResp, err := configdns.GetZoneNameTypes(ctx, zname, zoneName)
				if err != nil {
					term.Spinner().Fail()
					fmt.Println("Error: " + err.Error())
					return cli.Exit(color.RedString("Zone Name types retrieval failed"), 1)
				}
				recordsets[zname] = nameTypesResp.Types
			}
		}
		term.Spinner().OK()
		term.Spinner().Start("Creating Zone Resources list file ")
		// pathname and exists?
		if stat, err := os.Stat(tfWorkPath); err == nil && stat.IsDir() {
			importListFilename := createImportListFilename(resourceZoneName)
			if _, err := os.Stat(importListFilename); err == nil {
				term.Spinner().Fail()
				return cli.Exit(color.RedString("Resource list file exists. Remove to continue."), 1)
			}
			fullZoneImportList = &zoneImportListStruct{}
			fullZoneImportList.Zone = zoneName
			fullZoneImportList.Recordsets = recordsets
			json, err := json.MarshalIndent(fullZoneImportList, "", "  ")
			if err != nil {
				term.Spinner().Fail()
				return cli.Exit(color.RedString("Unable to generate json formatted zone resource list"), 1)
			}
			f, err := os.Create(importListFilename)
			if err != nil {
				term.Spinner().Fail()
				return cli.Exit(color.RedString("Unable to create zone resources file"), 1)
			}
			defer f.Close()
			_, err = f.WriteString(string(json))
			if err != nil {
				term.Spinner().Fail()
				return cli.Exit(color.RedString("Unable to write zone resources file"), 1)
			}
			f.Sync()
		} else {
			// Path doesnt exist. Bail
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Destination work path is not accessible."), 1)
		}
		term.Spinner().OK()
	}

	if createConfig {
		// Read in resources list
		zoneImportList, err := retrieveZoneImportList(resourceZoneName)
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Failed to read json zone resources file"), 1)
		}
		// if segmenting recordsets by name, make sure module folder exists
		if fetchConfig.ModSegment {
			modulePath = filepath.Join(tfWorkPath, moduleFolder)
			if !createDirectory(modulePath) {
				term.Spinner().Fail()
				return cli.Exit(color.RedString("Failed to create modules folder."), 1)
			}
		}
		term.Spinner().Start("Creating zone configuration file ")
		// see if configuration file already exists and exclude any resources already represented.
		var configImportList *zoneImportListStruct
		var zoneTypeMap map[string]map[string]bool
		zoneTFfileHandle, zonetfConfig, configImportList, zoneTypeMap, err = reconcileZoneResourceTargets(zoneImportList, resourceZoneName)
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Failed to open/create zone config file."), 1)
		}
		defer zoneTFfileHandle.Close()
		fileUtils := fileUtilsProcessor{}

		// build tf file if none
		if len(zonetfConfig) > 0 {
			if strings.Contains(zonetfConfig, "module") && strings.Contains(zonetfConfig, "zonename") {
				if !fetchConfig.ModSegment {
					// already have a top level zone config and its modularized!
					term.Spinner().Fail()
					return cli.Exit(color.RedString("Failed. Existing zone config is modularized"), 1)
				}
			} else if fetchConfig.ModSegment {
				// already have a top level zone config and its not mudularized!
				term.Spinner().Fail()
				return cli.Exit(color.RedString("Failed. Existing zone config is not modularized"), 1)
			}
		} else {
			// if tf pre existed, zone has to exist by definition
			zonetfConfig, err = processZone(ctx, zoneObject, resourceZoneName, fetchConfig.ModSegment, fileUtils)
			if err != nil {
				term.Spinner().Fail()
				fmt.Println(err.Error())
				return cli.Exit(color.RedString("Failed. Couldn't initialize zone config"), 1)
			}
		}
		err = fileUtils.appendRootModuleTF(zonetfConfig)
		if err != nil {
			term.Spinner().Fail()
			fmt.Println(err.Error())
			return cli.Exit(color.RedString("Failed. Couldn't write to zone config"), 1)
		}

		// process Recordsets.
		fullZoneConfigMap, err = processRecordsets(ctx, configdns, configImportList.Zone, resourceZoneName, zoneTypeMap, fetchConfig, fileUtils)
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Failed to process recordsets."), 1)
		}
		// Save config map for import script generation
		resourceConfigFilename := createResourceConfigFilename(resourceZoneName)
		json, err := json.MarshalIndent(&fullZoneConfigMap, "", "  ")
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Unable to generate json formatted zone config"), 1)
		}
		f, err := os.Create(resourceConfigFilename)
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Unable to create resource config file"), 1)
		}
		defer f.Close()
		_, err = f.WriteString(string(json))
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Unable to write zone resource config file"), 1)
		}
		f.Sync()

		// Need create dnsvars.tf dependency
		dnsvarsFilename := filepath.Join(tfWorkPath, "dnsvars.tf")
		// see if exists already.
		//if _, err := os.Stat(dnsvarsFilename); err != nil {
		dnsvarsHandle, err := os.Create(dnsvarsFilename)
		//}
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Unable to create gtmvars config file"), 1)
		}
		defer dnsvarsHandle.Close()
		_, err = dnsvarsHandle.WriteString(fmt.Sprintf(useTemplate(nil, "dnsvars.tmpl", true), contractid))
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Unable to write gtmvars config file"), 1)
		}
		dnsvarsHandle.Sync()
		term.Spinner().OK()
	}

	if importScript {
		term.Spinner().Start("Creating zone import script file")
		fullZoneConfigMap, _ = retrieveZoneResourceConfig(resourceZoneName)
		importScriptFilename := filepath.Join(tfWorkPath, resourceZoneName+"_resource_import.script")
		if _, err := os.Stat(importScriptFilename); err == nil {
			// File exists. Bail
			term.Spinner().OK()
		}
		scriptContent, err := buildZoneImportScript(zoneName, fullZoneConfigMap, resourceZoneName)

		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Import script content generation failed"), 1)
		}
		f, err := os.Create(importScriptFilename)
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Unable to create import script file"), 1)
		}
		defer f.Close()
		_, err = f.WriteString(scriptContent)
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Unable to write import script file"), 1)
		}
		f.Sync()
		term.Spinner().OK()
	}

	fmt.Println("Zone configuration completed")

	return nil
}

// Utility method to create full resource config file path
func createResourceConfigFilename(resourceName string) string {

	return filepath.Join(tfWorkPath, resourceName+"_zoneconfig.json")

}

// util func. create named module path
func createNamedModulePath(modName string) string {

	fpath := filepath.Join(tfWorkPath, moduleFolder, normalizeResourceName(modName))
	if fpath[0:1] != "./" && fpath[0:2] != "../" {
		fpath = filepath.FromSlash("./" + fpath)
	}

	return fpath
}

//Utility func
func createDirectory(dirName string) bool {

	stat, err := os.Stat(dirName)
	if err == nil && stat.IsDir() {
		return true
	}
	if os.IsNotExist(err) {
		errDir := os.MkdirAll(dirName, 0755)
		if errDir != nil {
			panic(err)
		}
		return true
	}
	if stat.Mode().IsRegular() {
		return false
	}

	return false
}

func buildZoneImportScript(zone string, zoneConfigMap map[string]Types, resourceName string) (string, error) {
	data := ImportData{
		Zone:          zone,
		ZoneConfigMap: zoneConfigMap,
		ResourceName:  resourceName,
	}
	return useTemplate(&data, "import-script.tmpl", true), nil
}

// remove any resources already present in existing zone tf configuration
func reconcileZoneResourceTargets(zoneImportList *zoneImportListStruct, zoneName string) (*os.File, string, *zoneImportListStruct, map[string]map[string]bool, error) {

	zoneTypeMap := make(map[string]map[string]bool)
	// populate zoneTypeMap

	tfFilename := tools.CreateTFFilename(zoneName, tfWorkPath)
	var tfHandle *os.File
	tfHandle, err := os.OpenFile(tfFilename, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil && err != io.EOF {
		fmt.Println(err.Error())
		return nil, "", zoneImportList, zoneTypeMap, err
	}
	tfInfo, err := os.Stat(tfFilename)
	if err != nil {
		fmt.Println(err.Error())
		return nil, "", zoneImportList, zoneTypeMap, err
	}
	tfScratch := make([]byte, tfInfo.Size())
	charsRead, err := tfHandle.Read(tfScratch)
	if err != nil && err != io.EOF {
		fmt.Println(err.Error())
		return nil, "", zoneImportList, zoneTypeMap, err
	}
	_, err = tfHandle.Seek(0, 0)
	if err != nil {
		fmt.Println(err.Error())
		return nil, "", zoneImportList, zoneTypeMap, err
	}
	tfConfig := ""
	if charsRead > 0 {
		tfConfig = fmt.Sprintf("%s", tfScratch[0:charsRead-1])
	}
	// need walk thru each resource type
	for zname, typeList := range zoneImportList.Recordsets {
		typeMap := make(map[string]bool)
		revisedTypeList := make([]string, 0, len(typeList))
		for _, ntype := range typeList {
			normalName := createUniqueRecordsetName(zoneName, zname, ntype)
			if !strings.Contains(tfConfig, "\""+normalName+"\"") {
				typeMap[ntype] = true
				revisedTypeList = append(revisedTypeList, ntype)
			} else {
				fmt.Println("Recordset resource " + normalName + " found in existing tf file")
			}
		}
		zoneImportList.Recordsets[zname] = revisedTypeList
		zoneTypeMap[zname] = typeMap
	}

	return tfHandle, tfConfig, zoneImportList, zoneTypeMap, err

}

func retrieveZoneImportList(rscName string) (*zoneImportListStruct, error) {

	// check if createImportList set. If so, already have ....
	if createImportList {
		return fullZoneImportList, nil
	}
	if fetchConfig.ConfigOnly {
		fullZoneImportList := &zoneImportListStruct{Zone: zoneName}
		fullZoneImportList.Recordsets = make(map[string]Types)
		return fullZoneImportList, nil
	}
	importListFilename := createImportListFilename(rscName)
	if _, err := os.Stat(importListFilename); err != nil {
		return nil, err
	}
	importData, err := ioutil.ReadFile(importListFilename)
	if err != nil {
		return nil, err
	}
	importList := &zoneImportListStruct{}
	err = json.Unmarshal(importData, importList)
	if err != nil {
		return nil, err
	}

	return importList, nil

}

func retrieveZoneResourceConfig(rscName string) (map[string]Types, error) {

	configList := make(map[string]Types)
	// check if createConfig set. If so, already have ....
	if createConfig {
		return fullZoneConfigMap, nil
	}
	resourceConfigFilename := createResourceConfigFilename(rscName)
	if _, err := os.Stat(resourceConfigFilename); err != nil {
		return configList, err
	}
	configData, err := ioutil.ReadFile(resourceConfigFilename)
	if err != nil {
		return configList, err
	}
	err = json.Unmarshal(configData, &configList)
	if err != nil {
		return configList, err
	}

	return configList, nil

}
