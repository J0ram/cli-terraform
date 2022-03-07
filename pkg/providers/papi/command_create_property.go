/*
 * Copyright 2018-2020. Akamai Technologies, Inc
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package papi

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/akamai/AkamaiOPEN-edgegrid-golang/edgegrid"
	"github.com/akamai/AkamaiOPEN-edgegrid-golang/papi-v1"
	"github.com/akamai/cli-terraform/pkg/providers/papi/hapi"
	"github.com/akamai/cli-terraform/pkg/tools"
	"github.com/akamai/cli/pkg/terminal"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
)

// EdgeHostname represent data used for single Edge host
type EdgeHostname struct {
	EdgeHostname             string
	EdgeHostnameID           string
	ProductName              string
	ContractID               string
	GroupID                  string
	ID                       string
	IPv6                     string
	EdgeHostnameResourceName string
	SlotNumber               int
	SecurityType             string
}

// Hostname represent data used for single host
type Hostname struct {
	Hostname                 string
	EdgeHostnameResourceName string
}

// TFData represent data used in templates
type TFData struct {
	GroupName            string
	GroupID              string
	ContractID           string
	PropertyResourceName string
	PropertyName         string
	PropertyID           string
	CPCodeID             string
	ProductID            string
	ProductName          string
	RuleFormat           string
	IsSecure             string
	EdgeHostnames        map[string]EdgeHostname
	Hostnames            map[string]Hostname
	Section              string
	Emails               []string
}

// RulesTemplate represent data used for rules
type RulesTemplate struct {
	AccountID       string             `json:"accountId"`
	ContractID      string             `json:"contractId"`
	GroupID         string             `json:"groupId"`
	PropertyID      string             `json:"propertyId"`
	PropertyVersion int                `json:"propertyVersion"`
	Etag            string             `json:"etag"`
	RuleFormat      string             `json:"ruleFormat"`
	Rule            *RuleTemplate      `json:"rules"`
	Errors          []*papi.RuleErrors `json:"errors,omitempty"`
}

// RuleTemplate represent data used for single rule
type RuleTemplate struct {
	Name                string                            `json:"name"`
	Criteria            []*papi.Criteria                  `json:"criteria,omitempty"`
	Behaviors           []*papi.Behavior                  `json:"behaviors,omitempty"`
	Children            []string                          `json:"children,omitempty"`
	Comments            string                            `json:"comments,omitempty"`
	CriteriaLocked      bool                              `json:"criteriaLocked,omitempty"`
	CriteriaMustSatisfy papi.RuleCriteriaMustSatisfyValue `json:"criteriaMustSatisfy,omitempty"`
	UUID                string                            `json:"uuid,omitempty"`
	Variables           []*papi.Variable                  `json:"variables,omitempty"`
	AdvancedOverride    string                            `json:"advancedOverride,omitempty"`

	Options struct {
		IsSecure bool `json:"is_secure,omitempty"`
	} `json:"options,omitempty"`

	CustomOverride *papi.CustomOverride `json:"customOverride,omitempty"`
}

func getEdgegridConfig(c *cli.Context) (edgegrid.Config, error) {
	config, err := edgegrid.Init(c.String("edgerc"), c.String("section"))
	if err != nil {
		return edgegrid.Config{}, cli.NewExitError(err.Error(), 1)
	}

	if len(c.String("accountkey")) > 0 {
		config.AccountKey = c.String("accountkey")
	} else if len(c.String("account-key")) > 0 {
		config.AccountKey = c.String("account-key")
	}

	return config, nil
}

func CmdCreateProperty(c *cli.Context) error {
	term := terminal.Get(c.Context)

	log.SetOutput(ioutil.Discard)
	if c.NArg() == 0 {
		cli.ShowCommandHelp(c, c.Command.Name)
		return cli.Exit(color.RedString("property name is required"), 1)
	}

	if c.IsSet("tfworkpath") {
		tools.TFWorkPath = c.String("tfworkpath")
	}
	tools.TFWorkPath = filepath.FromSlash(tools.TFWorkPath)
	if stat, err := os.Stat(tools.TFWorkPath); err != nil || !stat.IsDir() {
		return cli.NewExitError(color.RedString("Destination work path is not accessible."), 1)
	}
	err := tools.CheckFiles(
		tools.CreateTFFilename("property"),
		tools.CreateTFFilename("versions"),
		tools.CreateTFFilename("variables"),
		filepath.Join(tools.TFWorkPath, "rules.json"),
		filepath.Join(tools.TFWorkPath, "import.sh"))
	if err != nil {
		return cli.Exit(err, 1)
	}

	config, err := getEdgegridConfig(c)
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	config.Debug = false

	papi.Init(config)
	hapi.Init(config)

	var tfData TFData
	tfData.EdgeHostnames = make(map[string]EdgeHostname)
	tfData.Hostnames = make(map[string]Hostname)
	tfData.Emails = make([]string, 0)

	tfData.Section = c.String("section")

	// Get Property
	propertyName := c.Args().First()
	fmt.Println("Configuring Property")
	term.Spinner().Start("Fetching property ")
	property := findProperty(propertyName)
	if property == nil {
		term.Spinner().Fail()
		return cli.Exit(color.RedString("Property not found "), 1)
	}

	tfData.ContractID = property.Contract.ContractID
	tfData.PropertyName = property.PropertyName
	tfData.PropertyID = property.PropertyID
	tfData.PropertyResourceName = strings.Replace(property.PropertyName, ".", "-", -1)
	term.Spinner().OK()

	// Get Property Rules
	term.Spinner().Start("Fetching property rules ")
	rules, err := property.GetRules("")

	if err != nil {
		term.Spinner().Fail()
		return cli.Exit(color.RedString("Property rules not found: ", err), 1)
	}

	tfData.IsSecure = "false"
	if rules.Rule.Options.IsSecure {
		tfData.IsSecure = "true"
	}

	for _, behaviour := range rules.Rule.Behaviors {
		_ = behaviour
		if behaviour.Name == "cpCode" {
			options := behaviour.Options
			value := options["value"].(map[string]interface{})
			tfData.CPCodeID = fmt.Sprintf("%.0f", value["id"].(float64))
		}
	}

	// Get Rule Format
	tfData.RuleFormat = rules.RuleFormat

	// Set up template structure
	var ruletemplate RuleTemplate
	ruletemplate.Name = rules.Rule.Name
	ruletemplate.Criteria = rules.Rule.Criteria
	ruletemplate.Behaviors = rules.Rule.Behaviors
	ruletemplate.Comments = rules.Rule.Comments
	ruletemplate.CriteriaLocked = rules.Rule.CriteriaLocked
	ruletemplate.CriteriaMustSatisfy = rules.Rule.CriteriaMustSatisfy
	ruletemplate.UUID = rules.Rule.UUID
	ruletemplate.Variables = rules.Rule.Variables
	ruletemplate.AdvancedOverride = rules.Rule.AdvancedOverride
	ruletemplate.Children = make([]string, 0)
	ruletemplate.Options = rules.Rule.Options

	var rulestemplate RulesTemplate
	rulestemplate.AccountID = rules.AccountID
	rulestemplate.ContractID = rules.ContractID
	rulestemplate.GroupID = rules.GroupID
	rulestemplate.PropertyID = rules.PropertyID
	rulestemplate.PropertyVersion = rules.PropertyVersion
	rulestemplate.Etag = rules.Etag
	rulestemplate.RuleFormat = rules.RuleFormat
	rulestemplate.Rule = &ruletemplate

	// Save snippets
	snippetspath := filepath.Join(tools.TFWorkPath, "property-snippets")
	os.Mkdir(snippetspath, 0755)

	for _, rule := range rules.Rule.Children {
		jsonBody, err := json.MarshalIndent(rule, "", "  ")
		name := strings.ReplaceAll(rule.Name, " ", "_")
		rulesnamepath := filepath.Join(snippetspath, fmt.Sprintf("%s.json", name))
		err = ioutil.WriteFile(rulesnamepath, jsonBody, 0644)
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Can't write property rule snippets: ", err), 1)
		}
		ruletemplate.Children = append(ruletemplate.Children, fmt.Sprintf("#include:%s.json", name))
	}

	jsonBody, err := json.MarshalIndent(rulestemplate, "", "  ")
	templatepath := filepath.Join(snippetspath, "main.json")
	err = ioutil.WriteFile(templatepath, jsonBody, 0644)
	if err != nil {
		term.Spinner().Fail()

		return cli.Exit(color.RedString("Can't write property rule template: ", err), 1)
	}

	term.Spinner().OK()

	// Get Group
	term.Spinner().Start("Fetching group ")
	group, err := getGroup(property.GroupID)
	if err != nil {
		term.Spinner().Fail()
		return cli.Exit(color.RedString("Group not found: %s", err), 1)
	}

	tfData.GroupName = group.GroupName
	tfData.GroupID = group.GroupID

	term.Spinner().OK()

	// Get Version
	term.Spinner().Start("Fetching property version ")
	version, err := getVersion(property)
	if err != nil {
		term.Spinner().Fail()
		return cli.Exit(color.RedString("Version not found: %s", err), 1)
	}

	tfData.ProductID = version.ProductID

	term.Spinner().OK()

	// Get Product
	term.Spinner().Start("Fetching product name ")
	product, err := getProduct(tfData.ProductID, property.Contract)
	if err != nil {
		term.Spinner().Fail()
		return cli.Exit(color.RedString("Product not found: %s", err), 1)
	}

	tfData.ProductName = product.ProductName

	term.Spinner().OK()

	// Get Hostnames
	term.Spinner().Start("Fetching hostnames ")
	hostnames, err := getHostnames(property, version)

	if err != nil {
		term.Spinner().Fail()
		return cli.Exit(color.RedString("Hostnames not found: %s", err), 1)
	}

	for _, hostname := range hostnames.Hostnames.Items {
		_ = hostname

		if hostname.EdgeHostnameID == "" {
			continue
		}

		// Get slot details
		ehnid := strings.Replace(hostname.EdgeHostnameID, "ehn_", "", 1)

		edgehostname, err := hapi.GetEdgeHostnameById(ehnid)
		if err != nil {
			term.Spinner().Fail()
			return cli.Exit(color.RedString("Edge Hostname not found: %s", err), 1)
		}

		cnameTo := hostname.CnameTo
		cnameFrom := hostname.CnameFrom
		cnameToResource := strings.Replace(cnameTo, ".", "-", -1)

		var edgeHostnameN EdgeHostname
		edgeHostnameN.EdgeHostname = cnameTo
		edgeHostnameN.EdgeHostnameID = hostname.EdgeHostnameID
		edgeHostnameN.EdgeHostnameResourceName = cnameToResource
		edgeHostnameN.ProductName = product.ProductName
		edgeHostnameN.IPv6 = getIPv6(property, hostname.EdgeHostnameID)
		edgeHostnameN.SlotNumber = edgehostname.SlotNumber
		edgeHostnameN.SecurityType = edgehostname.SecurityType
		edgeHostnameN.ContractID = property.Contract.ContractID
		edgeHostnameN.GroupID = group.GroupID
		tfData.EdgeHostnames[cnameToResource] = edgeHostnameN

		var hostnamesN Hostname
		hostnamesN.Hostname = cnameFrom
		hostnamesN.EdgeHostnameResourceName = cnameToResource
		tfData.Hostnames[cnameFrom] = hostnamesN

	}

	term.Spinner().OK()

	// Get contact details
	term.Spinner().Start("Fetching contact details ")
	activations, err := property.GetActivations()
	if err != nil {
		tfData.Emails = append(tfData.Emails, "")
	} else {
		a, err := activations.GetLatestStagingActivation("")
		if err != nil {
			tfData.Emails = append(tfData.Emails, "")
		} else {
			tfData.Emails = a.NotifyEmails
		}
	}

	term.Spinner().OK()

	// Save file
	term.Spinner().Start("Saving TF configurations ")
	err = saveTerraformDefinition(tfData)
	if err != nil {
		term.Spinner().Fail()
		return cli.Exit(color.RedString("Couldn't save tf file: %s", err), 1)
	}

	term.Spinner().OK()
	fmt.Println("Property configuration completed")

	return nil

}

func getHostnames(property *papi.Property, version *papi.Version) (*papi.Hostnames, error) {
	hostnames, err := property.GetHostnames(version, "")
	if err != nil {
		return nil, err
	}
	return hostnames, nil
}

func getIPv6(property *papi.Property, ehn string) string {
	edgeHostnames, err := papi.GetEdgeHostnames(property.Contract, property.Group, "")
	if err != nil {
		return "false"
	}
	for _, edgehostname := range edgeHostnames.EdgeHostnames.Items {
		_ = edgehostname
		if edgehostname.EdgeHostnameID == ehn {
			return edgehostname.IPVersionBehavior
		}
	}
	return ""
}

func getCPCode(property *papi.Property, cpCodeID string) (string, error) {
	cpCode := papi.NewCpCodes(property.Contract, property.Group).NewCpCode()
	cpCode.CpcodeID = cpCodeID
	err := cpCode.GetCpCode()
	if err != nil {
		return "", err
	}
	return cpCode.CpcodeName, nil
}

func findProperty(name string) *papi.Property {
	results, err := papi.Search(papi.SearchByPropertyName, name, "")
	if err != nil {
		return nil
	}

	if err != nil || results == nil {
		return nil
	}

	property := &papi.Property{
		PropertyID: results.Versions.Items[0].PropertyID,
		Group: &papi.Group{
			GroupID: results.Versions.Items[0].GroupID,
		},
		Contract: &papi.Contract{
			ContractID: results.Versions.Items[0].ContractID,
		},
	}

	err = property.GetProperty("")
	if err != nil {
		return nil
	}

	return property
}

func getVersion(property *papi.Property) (*papi.Version, error) {

	versions, err := property.GetVersions("")
	if err != nil {
		return nil, err
	}

	version, err := versions.GetLatestVersion("", "")
	if err != nil {
		return nil, err
	}

	return version, nil
}

func getGroup(groupID string) (*papi.Group, error) {
	groups := papi.NewGroups()
	e := groups.GetGroups("")
	if e != nil {
		return nil, e
	}

	group, e := groups.FindGroup(groupID)
	if e != nil {
		return nil, e
	}

	return group, nil
}

func getProduct(productID string, contract *papi.Contract) (*papi.Product, error) {
	if contract == nil {
		return nil, nil
	}

	products := papi.NewProducts()
	e := products.GetProducts(contract, "")
	if e != nil {
		return nil, e
	}

	product, e := products.FindProduct(productID)
	if e != nil {
		return nil, e
	}

	return product, nil
}

func saveTerraformDefinition(data TFData) error {

	tftemplate, err := template.New("tf").Parse(
		"provider \"akamai\" {\n" +
			" edgerc = \"~/.edgerc\"\n" +
			" config_section = \"{{.Section}}\"\n" +
			"}\n" +
			"\n" +
			"data \"akamai_group\" \"group\" {\n" +
			" group_name = \"{{.GroupName}}\"\n" +
			" contract_id = \"{{.ContractID}}\"\n" +
			"}\n" +
			"\n" +
			"data \"akamai_contract\" \"contract\" {\n" +
			"  group_name = data.akamai_group.group.name\n" +
			"}\n" +
			"\n" +
			"data \"akamai_property_rules_template\" \"rules\" {\n" +
			"  template_file = abspath(\"${path.module}/property-snippets/main.json\")\n" +
			"}\n" +
			"\n" +

			// Edge hostname loop
			"{{range .EdgeHostnames}}" +
			"resource \"akamai_edge_hostname\" \"{{.EdgeHostnameResourceName}}\" {\n" +
			" product_id  = \"prd_{{.ProductName}}\"\n" +
			" contract_id = data.akamai_contract.contract.id\n" +
			" group_id = data.akamai_group.group.id\n" +
			" ip_behavior = \"{{.IPv6}}\"\n" +
			" edge_hostname = \"{{.EdgeHostname}}\"\n" +
			"{{if .SlotNumber}}" +
			" certificate = {{.SlotNumber}}\n" +
			"{{end}}" +
			"}\n" +
			"\n" +
			"{{end}}" +
			"resource \"akamai_property\" \"{{.PropertyResourceName}}\" {\n" +
			" name = \"{{.PropertyName}}\"\n" +
			" contract_id = data.akamai_contract.contract.id\n" +
			" group_id = data.akamai_group.group.id\n" +
			" product_id = \"prd_{{.ProductName}}\"\n" +
			" rule_format = \"{{.RuleFormat}}\"\n" +
			"{{range .Hostnames}}" +
			" hostnames {\n" +
			"  cname_from = \"{{.Hostname}}\"\n" +
			"  cname_to = akamai_edge_hostname.{{.EdgeHostnameResourceName}}.edge_hostname\n" +
			"  cert_provisioning_type = \"CPS_MANAGED\"\n" +
			" }\n" +
			"{{end}}" +
			" rules = data.akamai_property_rules_template.rules.json\n" +
			"}\n" +
			"\n" +
			"resource \"akamai_property_activation\" \"{{.PropertyResourceName}}\" {\n" +
			" property_id = akamai_property.{{.PropertyResourceName}}.id\n" +
			" contact = [\"{{range $index, $element := .Emails}}{{if $index}},{{end}}{{$element}}{{end}}\"]\n" +
			" version = akamai_property.{{.PropertyResourceName}}.latest_version\n" +
			" network = upper(var.env)\n" +
			"}\n")

	if err != nil {
		return err
	}
	propertyconfigfilename := tools.CreateTFFilename("property")
	f, err := os.Create(propertyconfigfilename)
	if err != nil {
		return err
	}

	err = tftemplate.Execute(f, data)
	if err != nil {
		return err
	}
	f.Close()

	// version
	versionsconfigfilename := tools.CreateTFFilename("versions")
	f, err = os.Create(versionsconfigfilename)
	if err != nil {
		return err
	}

	f.WriteString("terraform {\n")
	f.WriteString("required_providers {\n")
	f.WriteString("akamai = {\n")
	f.WriteString("source = \"akamai/akamai\"\n")
	f.WriteString("}\n")
	f.WriteString("}\n")
	f.WriteString("required_version = \">= 0.13\"\n")
	f.WriteString("}\n")
	f.Close()

	// variables
	variablesconfigfilename := tools.CreateTFFilename("variables")
	f, err = os.Create(variablesconfigfilename)
	if err != nil {
		return err
	}

	f.WriteString("variable \"env\" {\n")
	f.WriteString(" default = \"staging\"\n")
	f.WriteString("}\n")
	f.Close()

	// import script
	importfilename := filepath.Join(tools.TFWorkPath, "import.sh")
	f, err = os.Create(importfilename)
	if err != nil {
		return err
	}

	importtemplate, err := template.New("import").Parse(
		"terraform init\n" +
			"{{range .EdgeHostnames}}" +
			"terraform import akamai_edge_hostname.{{.EdgeHostnameResourceName}} {{.EdgeHostnameID}},{{.ContractID}},{{.GroupID}}\n" +
			"{{end}}" +
			"terraform import akamai_property.{{.PropertyResourceName}} {{.PropertyID}},{{.ContractID}},{{.GroupID}}\n")

	err = importtemplate.Execute(f, data)
	if err != nil {
		return err
	}
	f.Close()

	return nil
}
