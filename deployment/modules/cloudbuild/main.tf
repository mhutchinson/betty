terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "5.14.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

# This will be configured by terragrunt when deploying
terraform {
  backend "gcs" {}
}

resource "google_project_service" "artifact_registry_api" {
  service            = "artifactregistry.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "cloudbuild_api" {
  service            = "cloudbuild.googleapis.com"
  disable_on_destroy = false
}

resource "google_artifact_registry_repository" "sqlog_docker" {
  repository_id = "sqlog-docker-${var.env}"
  location      = var.region
  description   = "docker images for the sqlog"
  format        = "DOCKER"
  depends_on = [
    google_project_service.artifact_registry_api,
  ]
}

locals {
  artifact_repo = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.sqlog_docker.name}"
  docker_image  = "${local.artifact_repo}/sqlog"
}

resource "google_cloudbuild_trigger" "sqlog_docker" {
  name            = "build-sqlog-docker-${var.env}"
  service_account = google_service_account.cloudbuild_service_account.id
  location        = var.region

  github {
    owner = "mhutchinson"
    name  = "betty"
    push {
      branch = "^mysql$"
    }
  }

  build {
    step {
      name = "gcr.io/cloud-builders/docker"
      args = [
        "build",
        "-t", "${local.docker_image}:$SHORT_SHA",
        "-t", "${local.docker_image}:latest",
        "-f", "./Dockerfile",
        "."
      ]
    }
    step {
      name = "gcr.io/cloud-builders/docker"
      args = [
        "push",
        "--all-tags",
        local.docker_image
      ]
    }
    # Deploy container image to Cloud Run
    step {
      name       = "gcr.io/google.com/cloudsdktool/cloud-sdk"
      entrypoint = "gcloud"
      args = [
        "run",
        "deploy",
        var.cloud_run_service,
        "--image",
        "${local.docker_image}:$SHORT_SHA",
        "--region",
        var.region
      ]
    }
    options {
      logging = "CLOUD_LOGGING_ONLY"
    }
  }
  depends_on = [
    google_project_service.cloudbuild_api,
  ]
}

resource "google_service_account" "cloudbuild_service_account" {
  account_id   = "cloudbuild-${var.env}-sa"
  display_name = "Service Account for CloudBuild (${var.env})"
}

resource "google_project_iam_member" "act_as" {
  project = var.project_id
  role    = "roles/iam.serviceAccountUser"
  member  = "serviceAccount:${google_service_account.cloudbuild_service_account.email}"
}

resource "google_project_iam_member" "logs_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.cloudbuild_service_account.email}"
}

resource "google_project_iam_member" "artifact_registry_writer" {
  project = var.project_id
  role    = "roles/artifactregistry.writer"
  member  = "serviceAccount:${google_service_account.cloudbuild_service_account.email}"
}

resource "google_project_iam_member" "cloudrun_deployer" {
  project = var.project_id
  role    = "roles/run.developer"
  member  = "serviceAccount:${google_service_account.cloudbuild_service_account.email}"
}

