terraform {
  source = "${get_repo_root()}/deployment/modules/sqlog"
}

locals {
  project_id  = "mhutchinson-tlog-lite"
  region      = "us-central1"
  env         = path_relative_to_include()
}

remote_state {
  backend = "gcs"

  config = {
    project  = local.project_id
    location = local.region
    bucket   = "${local.project_id}-serving-${local.env}-tfstate"
    prefix   = "${path_relative_to_include()}/terraform.tfstate"

    gcs_bucket_labels = {
      name  = "terraform_state_storage"
    }
  }
}
