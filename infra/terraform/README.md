# Terraform — Phase 7

Infrastructure-as-code for the relayer. Modules in `modules/`, environment compositions in `environments/{dev,prod}`.

## Layout

```
infra/terraform/
├── modules/
│   ├── vpc/      # multi-AZ VPC, public + private subnets, IGW, NAT
│   ├── alb/      # internet-facing ALB, target group, HTTP/HTTPS listeners
│   ├── ecs/      # Fargate cluster, task def, service, IAM, log group
│   └── redis/    # ElastiCache Redis replication group, SG locked to ECS SG
└── environments/
    ├── dev/      # 1 task, 1 NAT, single-node Redis, HTTP-only by default
    └── prod/     # 2 tasks across AZs, NAT/AZ, Multi-AZ Redis, HTTPS required
```

## Bootstrap (one-time per AWS account)

Remote state lives in S3 with DynamoDB locking. Create them once, manually or
with a small bootstrap stack:

```sh
aws s3api create-bucket --bucket relayer-tfstate-<acct> --region us-east-1
aws s3api put-bucket-versioning --bucket relayer-tfstate-<acct> \
    --versioning-configuration Status=Enabled
aws dynamodb create-table --table-name relayer-tfstate-lock \
    --billing-mode PAY_PER_REQUEST \
    --attribute-definitions AttributeName=LockID,AttributeType=S \
    --key-schema AttributeName=LockID,KeyType=HASH
```

## Initialize an environment

```sh
cd environments/dev
cp terraform.tfvars.example terraform.tfvars   # fill in image, etc.

terraform init \
  -backend-config="bucket=relayer-tfstate-<acct>" \
  -backend-config="key=dev/terraform.tfstate" \
  -backend-config="region=us-east-1" \
  -backend-config="dynamodb_table=relayer-tfstate-lock" \
  -backend-config="encrypt=true"

terraform plan
terraform apply
```

Same flow for `environments/prod`, with `key=prod/terraform.tfstate`.

## Notes

- Phase 7 builds *infra*. Phase 8 wires GitHub Actions → ECR push → ECS deploy.
  Until Phase 8 lands, supply a placeholder `image` and accept that the service
  will fail health checks; that's expected.
- ECS service has `lifecycle.ignore_changes = [task_definition, desired_count]`
  so Phase 8's deploy can update the task def without Terraform fighting it.
- `transit_encryption_enabled = true` (prod Redis) means clients must use TLS.
  When wiring the relayer, set the Redis URL scheme to `rediss://` accordingly.
