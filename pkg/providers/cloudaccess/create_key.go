// Package cloudaccess contains code for exporting CloudAccess key
package cloudaccess

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/v8/pkg/cloudaccess"
	"github.com/akamai/cli-terraform/pkg/edgegrid"
	"github.com/akamai/cli-terraform/pkg/templates"
	"github.com/akamai/cli-terraform/pkg/tools"
	"github.com/akamai/cli/pkg/terminal"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
)

type (
	// TFCloudAccessData represents the data used in CloudAccess
	TFCloudAccessData struct {
		Key     TFCloudAccessKey
		Section string
	}

	// TFCloudAccessKey represents the data used for export CloudAccess key
	TFCloudAccessKey struct {
		KeyResourceName      string
		AccessKeyName        string
		AuthenticationMethod string
		GroupID              int64
		ContractID           string
		AccessKeyUID         int64
		CredentialA          *Credential
		CredentialB          *Credential
		NetworkConfiguration *NetworkConfiguration
	}

	// Credential represents CLoudAccess credential
	Credential struct {
		CloudAccessKeyID string
	}

	// NetworkConfiguration represents CLoudAccess network configuration
	NetworkConfiguration struct {
		AdditionalCDN   *string
		SecurityNetwork string
	}
)

var (
	//go:embed templates/*
	templateFiles embed.FS

	additionalFunctions = tools.DecorateWithMultilineHandlingFunctions(map[string]any{})

	// ErrFetchingKey is returned when key could not be fetched
	ErrFetchingKey = errors.New("problem with fetching key")
	// ErrListingKeyVersions is returned when key versions could not be listed
	ErrListingKeyVersions = errors.New("problem with listing key versions")
	// ErrSavingFiles is returned when an issue with processing templates occurs
	ErrSavingFiles = errors.New("saving terraform project files")
	// ErrNoGroup is returned when key does not have group and contract assigned
	ErrNoGroup = errors.New("access key has no defined group or contract")
)

// CmdCreateCloudAccess is an entrypoint to export-cloudaccess command
func CmdCreateCloudAccess(c *cli.Context) error {
	ctx := c.Context
	sess := edgegrid.GetSession(ctx)
	client := cloudaccess.Client(sess)

	// tfWorkPath is a target directory for generated terraform resources
	var tfWorkPath = "./"
	if c.IsSet("tfworkpath") {
		tfWorkPath = c.String("tfworkpath")
	}
	cloudAccessPath := filepath.Join(tfWorkPath, "cloudaccess.tf")
	variablesPath := filepath.Join(tfWorkPath, "variables.tf")
	importPath := filepath.Join(tfWorkPath, "import.sh")
	if err := tools.CheckFiles(cloudAccessPath, variablesPath, importPath); err != nil {
		return cli.Exit(color.RedString(err.Error()), 1)
	}

	templateToFile := map[string]string{
		"cloudaccess.tmpl": cloudAccessPath,
		"variables.tmpl":   variablesPath,
		"imports.tmpl":     importPath,
	}

	processor := templates.FSTemplateProcessor{
		TemplatesFS:     templateFiles,
		TemplateTargets: templateToFile,
		AdditionalFuncs: additionalFunctions,
	}

	keyUID, err := strconv.ParseInt(c.Args().Get(0), 10, 64)
	if err != nil {
		return cli.Exit(color.RedString(err.Error()), 1)
	}
	section := edgegrid.GetEdgercSection(c)
	if err = createCloudAccess(ctx, keyUID, section, client, processor); err != nil {
		return cli.Exit(color.RedString(fmt.Sprintf("Error exporting cloudaccess: %s", err)), 1)
	}
	return nil
}

func createCloudAccess(ctx context.Context, accessKeyUID int64, section string, client cloudaccess.CloudAccess, templateProcessor templates.TemplateProcessor) error {
	term := terminal.Get(ctx)
	term.Spinner().Start("Fetching cloudaccess key " + strconv.Itoa(int(accessKeyUID)))
	key, err := client.GetAccessKey(ctx, cloudaccess.AccessKeyRequest{
		AccessKeyUID: accessKeyUID,
	})
	if err != nil {
		term.Spinner().Fail()
		return fmt.Errorf("%w: %s", ErrFetchingKey, err)
	}

	if len(key.Groups) == 0 {
		return ErrNoGroup
	}

	versions, err := client.ListAccessKeyVersions(ctx, cloudaccess.ListAccessKeyVersionsRequest{
		AccessKeyUID: accessKeyUID,
	})
	if err != nil {
		term.Spinner().Fail()
		return fmt.Errorf("%w: %s", ErrListingKeyVersions, err)
	}
	tfCloudAccessData := populateCloudAccessData(section, key, versions.AccessKeyVersions)

	term.Spinner().Start("Saving TF configurations ")
	if err = templateProcessor.ProcessTemplates(tfCloudAccessData); err != nil {
		term.Spinner().Fail()
		return fmt.Errorf("%w: %s", ErrSavingFiles, err)
	}

	term.Spinner().OK()
	term.Printf("Terraform configuration for cloudaccess key '%s' was saved successfully\n", tfCloudAccessData.Key.AccessKeyName)

	return nil
}

func populateCloudAccessData(section string, key *cloudaccess.GetAccessKeyResponse, versions []cloudaccess.AccessKeyVersion) TFCloudAccessData {
	var netConf *NetworkConfiguration
	if key.NetworkConfiguration != nil {
		netConf = &NetworkConfiguration{
			SecurityNetwork: string(key.NetworkConfiguration.SecurityNetwork),
		}
		if key.NetworkConfiguration.AdditionalCDN != nil {
			netConf.AdditionalCDN = tools.StringPtr(string(*key.NetworkConfiguration.AdditionalCDN))
		}
	}

	var contractID string
	var groupID int64
	if len(key.Groups) > 0 {
		groupID = key.Groups[0].GroupID
		if len(key.Groups[0].ContractIDs) > 0 {
			contractID = key.Groups[0].ContractIDs[0]
		}
	}

	tfCloudAccessData := TFCloudAccessData{
		Section: section,
		Key: TFCloudAccessKey{
			KeyResourceName:      strings.ReplaceAll(key.AccessKeyName, "-", "_"),
			AccessKeyName:        key.AccessKeyName,
			AuthenticationMethod: key.AuthenticationMethod,
			GroupID:              groupID,
			ContractID:           contractID,
			AccessKeyUID:         key.AccessKeyUID,
			NetworkConfiguration: netConf,
		},
	}

	versionNum := len(versions)
	switch versionNum {
	case 0:
		return tfCloudAccessData
	case 1:
		tfCloudAccessData.Key.CredentialA = &Credential{
			CloudAccessKeyID: *versions[0].CloudAccessKeyID,
		}
	default:
		slices.SortFunc(versions, func(a, b cloudaccess.AccessKeyVersion) int {
			return cmp.Compare(a.Version, b.Version)
		})
		// first version from the response from API is assigned to `credentials_b`, second version to `credentials_a`
		tfCloudAccessData.Key.CredentialA = &Credential{
			CloudAccessKeyID: *versions[1].CloudAccessKeyID,
		}
		tfCloudAccessData.Key.CredentialB = &Credential{
			CloudAccessKeyID: *versions[0].CloudAccessKeyID,
		}
	}

	return tfCloudAccessData
}
