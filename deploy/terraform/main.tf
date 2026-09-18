# Roster on Google Cloud: one Cloud Run service, Cloud SQL for Postgres,
# Secret Manager for credentials, Cloud Scheduler to drive the queue, and a
# Pub/Sub topic for inbound events.
#
# The service scales to zero. Nothing runs between requests, so in-process
# workers are switched off and Cloud Scheduler calls two endpoints every
# minute instead: /internal/tick (fire schedules, recover stale jobs) and
# /internal/work (execute queued runs inside the request, while the instance
# has CPU). For steadier throughput set min_instances = 1 and workers > 0.

terraform {
  required_version = ">= 1.6"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 6.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.6"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

locals {
  apis = [
    "run.googleapis.com",
    "sqladmin.googleapis.com",
    "secretmanager.googleapis.com",
    "cloudscheduler.googleapis.com",
    "pubsub.googleapis.com",
    "artifactregistry.googleapis.com",
  ]

  # Provider keys are optional; a secret is created only for those supplied.
  # Whether a key was supplied decides which resources exist, so the map
  # cannot stay marked sensitive (see nonsensitive below). The values only
  # ever flow into secret_data, which the provider itself treats as sensitive.
  provider_keys = { for name, value in {
    ANTHROPIC_API_KEY = var.anthropic_api_key
    OPENAI_API_KEY    = var.openai_api_key
    XAI_API_KEY       = var.xai_api_key
  } : name => value if value != "" }

  secrets = merge(nonsensitive(local.provider_keys), {
    DATABASE_URL     = "postgres://roster:${random_password.db.result}@/roster?host=/cloudsql/${google_sql_database_instance.roster.connection_name}"
    ROSTER_API_TOKEN = random_password.api_token.result
  })
}

resource "google_project_service" "apis" {
  for_each           = toset(local.apis)
  service            = each.value
  disable_on_destroy = false
}

# --- Container registry -----------------------------------------------------

resource "google_artifact_registry_repository" "roster" {
  repository_id = "roster"
  location      = var.region
  format        = "DOCKER"
  depends_on    = [google_project_service.apis]
}

# --- Database ---------------------------------------------------------------

resource "random_password" "db" {
  length  = 32
  special = false
}

resource "google_sql_database_instance" "roster" {
  name                = "roster"
  database_version    = "POSTGRES_17"
  region              = var.region
  deletion_protection = var.deletion_protection

  settings {
    tier    = var.db_tier
    edition = "ENTERPRISE"
    backup_configuration {
      enabled                        = true
      point_in_time_recovery_enabled = true
    }
  }
  depends_on = [google_project_service.apis]
}

resource "google_sql_database" "roster" {
  name     = "roster"
  instance = google_sql_database_instance.roster.name
}

resource "google_sql_user" "roster" {
  name     = "roster"
  instance = google_sql_database_instance.roster.name
  password = random_password.db.result
}

# --- Secrets ----------------------------------------------------------------

resource "random_password" "api_token" {
  length  = 40
  special = false
}

resource "google_secret_manager_secret" "roster" {
  for_each  = local.secrets
  secret_id = "roster-${lower(replace(each.key, "_", "-"))}"
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret_version" "roster" {
  for_each    = local.secrets
  secret      = google_secret_manager_secret.roster[each.key].id
  secret_data = each.value
}

# --- Service ----------------------------------------------------------------

resource "google_service_account" "roster" {
  account_id   = "roster"
  display_name = "Roster"
}

resource "google_project_iam_member" "sql_client" {
  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.roster.email}"
}

resource "google_secret_manager_secret_iam_member" "read" {
  for_each  = local.secrets
  secret_id = google_secret_manager_secret.roster[each.key].id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.roster.email}"
}

resource "google_cloud_run_v2_service" "roster" {
  name                = "roster"
  location            = var.region
  deletion_protection = var.deletion_protection
  ingress             = "INGRESS_TRAFFIC_ALL"

  template {
    service_account = google_service_account.roster.email
    # A run may wait on several model calls; give /internal/work room.
    timeout = "900s"

    scaling {
      min_instance_count = var.min_instances
      max_instance_count = var.max_instances
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [google_sql_database_instance.roster.connection_name]
      }
    }

    containers {
      image = var.image

      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
        # Without an always-on instance, CPU is only allocated during requests.
        cpu_idle = var.min_instances == 0
      }

      env {
        name  = "ROSTER_WORKERS"
        value = tostring(var.workers)
      }
      env {
        name  = "ROSTER_DEMO_SHOP"
        value = tostring(var.demo_shop)
      }

      dynamic "env" {
        for_each = local.secrets
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.roster[env.key].secret_id
              version = "latest"
            }
          }
        }
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }

      startup_probe {
        http_get {
          path = "/healthz"
        }
        period_seconds    = 3
        failure_threshold = 20
      }
    }
  }

  depends_on = [
    google_secret_manager_secret_version.roster,
    google_secret_manager_secret_iam_member.read,
    google_project_iam_member.sql_client,
    google_sql_user.roster,
    google_sql_database.roster,
  ]
}

# Roster does its own authentication with the bearer token, so the service
# accepts unauthenticated requests at the Cloud Run layer. Put IAP or a load
# balancer in front before exposing the dashboard beyond a team.
resource "google_cloud_run_v2_service_iam_member" "public" {
  name     = google_cloud_run_v2_service.roster.name
  location = var.region
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# --- Scheduler: the heartbeat of a scale-to-zero deployment -----------------

resource "google_cloud_scheduler_job" "tick" {
  name             = "roster-tick"
  schedule         = "* * * * *"
  attempt_deadline = "60s"

  http_target {
    http_method = "POST"
    uri         = "${google_cloud_run_v2_service.roster.uri}/internal/tick"
    headers     = { Authorization = "Bearer ${random_password.api_token.result}" }
  }
  depends_on = [google_project_service.apis]
}

resource "google_cloud_scheduler_job" "work" {
  name             = "roster-work"
  schedule         = "* * * * *"
  attempt_deadline = "900s"

  http_target {
    http_method = "POST"
    uri         = "${google_cloud_run_v2_service.roster.uri}/internal/work?max=${var.jobs_per_minute}"
    headers     = { Authorization = "Bearer ${random_password.api_token.result}" }
  }

  retry_config {
    retry_count = 0 # the next minute's call picks up whatever is left
  }
  depends_on = [google_project_service.apis]
}

# --- Events -----------------------------------------------------------------

# Publish a message with an "event" attribute (for example ticket.created)
# and a JSON body; every agent subscribed to that event gets a run.
resource "google_pubsub_topic" "events" {
  name       = "roster-events"
  depends_on = [google_project_service.apis]
}

resource "google_pubsub_subscription" "events" {
  name                 = "roster-events-push"
  topic                = google_pubsub_topic.events.id
  ack_deadline_seconds = 30

  push_config {
    # Push subscriptions cannot set headers, so the token rides in the URL.
    push_endpoint = "${google_cloud_run_v2_service.roster.uri}/events/pubsub?token=${random_password.api_token.result}"
  }

  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "600s"
  }
}
