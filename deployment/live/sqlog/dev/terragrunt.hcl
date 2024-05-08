include "root" {
  path   = find_in_parent_folders()
  expose = true
}

inputs = merge(
  include.root.locals,
  {
    sqlog_docker_image = "us-central1-docker.pkg.dev/mhutchinson-tlog-lite/sqlog-docker-dev/sqlog:latest"
  }
)

