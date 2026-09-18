output "url" {
  description = "Where the dashboard and API are served."
  value       = google_cloud_run_v2_service.roster.uri
}

output "api_token" {
  description = "Bearer token for the API. Enter it under Settings in the dashboard."
  value       = random_password.api_token.result
  sensitive   = true
}

output "events_topic" {
  description = "Publish events here with an \"event\" attribute."
  value       = google_pubsub_topic.events.id
}

output "image_repository" {
  description = "Push the image here before applying."
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.roster.repository_id}"
}
