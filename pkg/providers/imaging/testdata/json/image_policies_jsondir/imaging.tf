terraform {
  required_providers {
    akamai = {
      source  = "akamai/akamai"
      version = ">= 2.0.0"
    }
  }
  required_version = ">= 0.13"
}

provider "akamai" {
  edgerc         = var.edgerc_path
  config_section = var.config_section
}

resource "akamai_imaging_policy_set" "policyset" {
  name        = "some policy set"
  region      = "EMEA"
  type        = "IMAGE"
  contract_id = "ctr_123"
}

resource "akamai_imaging_policy_image" "policy__auto" {
  policy_id              = ".auto"
  contract_id            = "ctr_123"
  policyset_id           = akamai_imaging_policy_set.policyset.id
  activate_on_production = true
  json                   = file("jsondir/_auto.json")
}

resource "akamai_imaging_policy_image" "policy_test_policy_image" {
  policy_id              = "test_policy_image"
  contract_id            = "ctr_123"
  policyset_id           = akamai_imaging_policy_set.policyset.id
  activate_on_production = true
  json                   = file("jsondir/test_policy_image.json")
}
