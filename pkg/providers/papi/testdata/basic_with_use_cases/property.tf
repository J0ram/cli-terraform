terraform {
  required_providers {
    akamai = {
      source  = "akamai/akamai"
      version = ">= 5.5.0"
    }
  }
  required_version = ">= 0.13"
}

provider "akamai" {
  edgerc         = var.edgerc_path
  config_section = var.config_section
}

data "akamai_property_rules_template" "rules" {
  template_file = abspath("${path.module}/property-snippets/main.json")
}

resource "akamai_edge_hostname" "test-edgesuite-net" {
  contract_id   = var.contract_id
  group_id      = var.group_id
  ip_behavior   = "IPV6_COMPLIANCE"
  edge_hostname = "test.edgesuite.net"
  use_cases = jsonencode([
    {
      "option" : "BACKGROUND",
      "type" : "GLOBAL",
      "useCase" : "Download_Mode"
    }
  ])
}

resource "akamai_property" "test-edgesuite-net" {
  name        = "test.edgesuite.net"
  contract_id = var.contract_id
  group_id    = var.group_id
  product_id  = "prd_HTTP_Content_Del"
  hostnames {
    cname_from             = "test.edgesuite.net"
    cname_to               = akamai_edge_hostname.test-edgesuite-net.edge_hostname
    cert_provisioning_type = "CPS_MANAGED"
  }
  rule_format = "latest"
  rules       = data.akamai_property_rules_template.rules.json
}

resource "akamai_property_activation" "test-edgesuite-net-staging" {
  property_id                    = akamai_property.test-edgesuite-net.id
  contact                        = ["jsmith@akamai.com"]
  version                        = akamai_property.test-edgesuite-net.staging_version
  network                        = "STAGING"
  auto_acknowledge_rule_warnings = false
}

#resource "akamai_property_activation" "test-edgesuite-net-production" {
#  property_id                    = akamai_property.test-edgesuite-net.id
#  contact                        = []
#  version                        = akamai_property.test-edgesuite-net.latest_version
#  network                        = "PRODUCTION"
#  auto_acknowledge_rule_warnings = false
#}
