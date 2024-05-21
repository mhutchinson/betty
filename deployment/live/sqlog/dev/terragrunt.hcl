include "root" {
  path   = find_in_parent_folders()
  expose = true
}

inputs = merge(
  include.root.locals,
  {
    sqlog_docker_image = "us-central1-docker.pkg.dev/${include.root.locals.project_id}/sqlog-docker-dev/sqlog:latest"
  }
)

