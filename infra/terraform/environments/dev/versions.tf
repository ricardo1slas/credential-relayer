terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.42"
    }
  }

  # Remote state backend. Configure via `terraform init -backend-config=...`
  # before first apply. Example:
  #   terraform init \
  #     -backend-config="bucket=relayer-tfstate-<acct>" \
  #     -backend-config="key=dev/terraform.tfstate" \
  #     -backend-config="region=us-east-1" \
  #     -backend-config="dynamodb_table=relayer-tfstate-lock"
  backend "s3" {}
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "private-relayer"
      Environment = "dev"
      ManagedBy   = "terraform"
    }
  }
}
