variable "project_id" {
  description = "Google Cloud project to deploy into."
  type        = string
}

variable "region" {
  description = "Region for every resource."
  type        = string
  default     = "europe-west1"
}

variable "image" {
  description = "Container image to run, for example europe-west1-docker.pkg.dev/PROJECT/roster/roster:v1."
  type        = string
}

variable "min_instances" {
  description = "Instances kept warm. 0 scales to zero and relies on Cloud Scheduler to drive the queue."
  type        = number
  default     = 0
}

variable "max_instances" {
  description = "Upper bound on instances."
  type        = number
  default     = 3
}

variable "workers" {
  description = "Jobs each instance executes at once in the background. Keep 0 unless min_instances is at least 1."
  type        = number
  default     = 0
}

variable "jobs_per_minute" {
  description = "Queued jobs Cloud Scheduler asks the service to execute each minute (1-100)."
  type        = number
  default     = 20
  validation {
    condition     = var.jobs_per_minute >= 1 && var.jobs_per_minute <= 100
    error_message = "jobs_per_minute must be between 1 and 100."
  }
}

variable "db_tier" {
  description = "Cloud SQL machine type."
  type        = string
  default     = "db-f1-micro"
}

variable "demo_shop" {
  description = "Mount the demo store's API at /shop/graphql."
  type        = bool
  default     = false
}

variable "deletion_protection" {
  description = "Guard the database and the service against terraform destroy."
  type        = bool
  default     = true
}

variable "anthropic_api_key" {
  description = "Claude API key. Leave empty to skip."
  type        = string
  default     = ""
  sensitive   = true
}

variable "openai_api_key" {
  description = "OpenAI API key. Leave empty to skip."
  type        = string
  default     = ""
  sensitive   = true
}

variable "xai_api_key" {
  description = "xAI API key. Leave empty to skip."
  type        = string
  default     = ""
  sensitive   = true
}
